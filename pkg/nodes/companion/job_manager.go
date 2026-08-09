package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	JobCommandStart = nodes.JobCommandStart
	maxJobLogRead   = 256 * 1024
)

type directJobInput struct {
	Argv           []string                 `json:"argv"`
	CWD            string                   `json:"cwd"`
	TimeoutSeconds float64                  `json:"timeout_seconds"`
	Env            map[string]string        `json:"env"`
	Artifacts      []JobArtifactDeclaration `json:"artifacts"`
}

type preparedDirectJob struct {
	command   preparedSystemExec
	artifacts []JobArtifactDeclaration
	timeout   time.Duration
	root      *fileRoot
}

type boundedJobLog struct {
	mu        sync.Mutex
	file      *os.File
	limit     int64
	written   int64
	truncated bool
}

func (log *boundedJobLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil {
		return 0, errors.New("node job log is closed")
	}
	accepted := min(int64(len(data)), log.limit-log.written)
	if accepted > 0 {
		written, err := log.file.Write(data[:accepted])
		log.written += int64(written)
		if err != nil {
			return written, err
		}
		if int64(written) != accepted {
			return written, io.ErrShortWrite
		}
	}
	if accepted < int64(len(data)) {
		log.truncated = true
	}
	return len(data), nil
}

func (log *boundedJobLog) close() JobLogRecord {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file != nil {
		_ = log.file.Sync()
		_ = log.file.Close()
		log.file = nil
	}
	return JobLogRecord{Bytes: log.written, Truncated: log.truncated}
}

func (log *boundedJobLog) record() JobLogRecord {
	log.mu.Lock()
	defer log.mu.Unlock()
	return JobLogRecord{Bytes: log.written, Truncated: log.truncated}
}

type activeDirectJob struct {
	launchMu  sync.Mutex
	command   *exec.Cmd
	stdout    *boundedJobLog
	stderr    *boundedJobLog
	root      *fileRoot
	artifacts []JobArtifactDeclaration
	timeoutAt int64
	cancel    chan struct{}
	done      chan struct{}
	once      sync.Once
}

type jobProcessDrainResult struct {
	signalSent         bool
	signaledLiveLeader bool
	hadDescendants     bool
	observationUnknown bool
}

type DirectJobManager struct {
	store           *JobStore
	policy          SystemExecPolicy
	profileAlias    string
	profileRevision string
	limits          DirectJobLimits

	mu     sync.Mutex
	active map[string]*activeDirectJob
	closed bool
	starts sync.WaitGroup
}

func NewDirectJobManager(
	store *JobStore,
	policy SystemExecPolicy,
	profileAlias string,
	profileRevision string,
	limits DirectJobLimits,
) (*DirectJobManager, error) {
	if store == nil {
		return nil, errors.New("node job store is required")
	}
	if supportErr := jobProcessSupported(); supportErr != nil {
		return nil, supportErr
	}
	if err := (nodes.Alias(profileAlias)).Validate(); err != nil {
		return nil, errors.New("node job profile alias is invalid")
	}
	cloned, cloneErr := cloneReadySystemExecPolicy(policy)
	if cloneErr != nil {
		return nil, cloneErr
	}
	if validateErr := nodes.ID(profileRevision).Validate(); validateErr != nil {
		return nil, errors.New("node job profile revision is invalid")
	}
	limits, limitsErr := normalizeDirectJobLimits(limits)
	if limitsErr != nil {
		return nil, limitsErr
	}
	return &DirectJobManager{
		store:           store,
		policy:          cloned,
		profileAlias:    profileAlias,
		profileRevision: profileRevision,
		limits:          limits,
		active:          make(map[string]*activeDirectJob),
	}, nil
}

// Start persists the launch boundary and detaches process ownership from the
// caller's context. Invocation/turn cancellation after a running result does
// not become job cancellation.
func (manager *DirectJobManager) Start(plan nodes.ExecutionPlan) (JobRecord, error) {
	if err := plan.Validate(); err != nil {
		return JobRecord{}, err
	}
	if plan.Command != JobCommandStart || plan.JobProfile != manager.profileAlias {
		return JobRecord{}, ErrCommandUnavailable
	}
	prepared, prepareErr := manager.prepare(plan)
	if prepareErr != nil {
		return JobRecord{}, prepareErr
	}
	defer func() {
		if prepared.root != nil {
			_ = prepared.root.close()
		}
	}()
	jobID, idErr := newJobID()
	if idErr != nil {
		return JobRecord{}, idErr
	}
	now := time.Now().UnixNano()
	record := JobRecord{
		JobID:               jobID,
		StartInvocationID:   plan.InvocationID,
		StartIdempotencyKey: plan.IdempotencyKey,
		PlanHash:            plan.PlanHash,
		ProfileAlias:        manager.profileAlias,
		ProfileRevision:     manager.profileRevision,
		RetentionSeconds:    int(manager.limits.Retention / time.Second),
		Owner: JobOwner{
			AgentID: plan.AgentID, SessionID: plan.SessionID, ActorID: plan.ActorID,
		},
		State:           JobAccepted,
		CancelGuarantee: manager.limits.CancelGuarantee,
		CreatedAt:       now,
		UpdatedAt:       now,
		Artifacts:       pendingJobArtifacts(prepared.artifacts),
	}
	existing, found, existingErr := manager.store.Existing(record)
	if existingErr != nil {
		return JobRecord{}, existingErr
	}
	if found {
		if existing.State != JobAccepted {
			return existing, nil
		}
		record = existing
	} else {
		accepted, _, acceptErr := manager.store.Accept(record)
		if acceptErr != nil {
			return JobRecord{}, acceptErr
		}
		record = accepted
	}
	if reserveErr := manager.reserve(record.JobID); reserveErr != nil {
		if errors.Is(reserveErr, ErrJobConflict) {
			current, currentFound, lookupErr := manager.store.Lookup(record.JobID)
			if lookupErr != nil {
				return JobRecord{}, lookupErr
			}
			if currentFound {
				return current, nil
			}
		}
		failed, failErr := manager.store.MarkFailedBeforeLaunch(record.JobID, "CONCURRENCY_LIMIT")
		if failErr != nil {
			return JobRecord{}, errors.Join(reserveErr, failErr)
		}
		return failed, nil
	}
	defer manager.starts.Done()
	payloadReservation := manager.limits.StdoutBytes + manager.limits.StderrBytes +
		manager.limits.ArtifactsTotal
	if payloadErr := manager.store.ReservePayload(record.JobID, payloadReservation); payloadErr != nil {
		manager.release(record.JobID)
		failed, failErr := manager.store.MarkFailedBeforeLaunch(record.JobID, "STORE_CAPACITY")
		if failErr != nil {
			return JobRecord{}, errors.Join(payloadErr, failErr)
		}
		return failed, nil
	}
	active, activeErr := manager.prepareActive(record.JobID, &prepared)
	if activeErr != nil {
		_ = manager.store.ReleasePayload(record.JobID)
		manager.release(record.JobID)
		failed, failErr := manager.store.MarkFailedBeforeLaunch(record.JobID, "LOG_PREPARE_FAILED")
		if failErr != nil {
			return JobRecord{}, errors.Join(activeErr, failErr)
		}
		return failed, nil
	}
	manager.setActive(record.JobID, active)
	active.launchMu.Lock()
	if _, launchErr := manager.store.MarkLaunchAttempted(record.JobID); launchErr != nil {
		active.launchMu.Unlock()
		manager.abortBeforeStart(record.JobID, active)
		current, currentFound, lookupErr := manager.store.Lookup(record.JobID)
		if lookupErr != nil {
			return JobRecord{}, errors.Join(launchErr, lookupErr)
		}
		if currentFound && current.State.terminal() {
			return current, nil
		}
		return JobRecord{}, launchErr
	}
	if startErr := active.command.Start(); startErr != nil {
		stdout := active.stdout.close()
		stderr := active.stderr.close()
		_ = active.root.close()
		_ = manager.store.ReleasePayload(record.JobID)
		manager.release(record.JobID)
		failed, failErr := manager.store.MarkStartFailed(record.JobID, "START_FAILED")
		active.launchMu.Unlock()
		if failErr != nil {
			return JobRecord{}, errors.Join(startErr, failErr)
		}
		failed.Stdout = stdout
		failed.Stderr = stderr
		return failed, nil
	}
	running, err := manager.store.MarkRunning(record.JobID, prepared.timeout)
	if err != nil {
		drainJobProcessGroup(active.command, true)
		_ = active.command.Wait()
		active.stdout.close()
		active.stderr.close()
		_ = active.root.close()
		_ = manager.store.ReleasePayload(record.JobID)
		manager.release(record.JobID)
		if _, unknownErr := manager.store.MarkUnknown(record.JobID, "RUNNING_STATE_UNCERTAIN"); unknownErr != nil {
			active.launchMu.Unlock()
			return JobRecord{}, errors.Join(err, unknownErr)
		}
		active.launchMu.Unlock()
		return JobRecord{}, fmt.Errorf("persist running node job: %w", err)
	}
	active.timeoutAt = running.TimeoutAt
	go manager.wait(record.JobID, active)
	active.launchMu.Unlock()
	return running, nil
}

func (manager *DirectJobManager) prepare(plan nodes.ExecutionPlan) (preparedDirectJob, error) {
	var input directJobInput
	if err := decodeStrictJSON(plan.Input, &input); err != nil {
		return preparedDirectJob{}, errors.New("invalid job.start input")
	}
	if len(input.Artifacts) > manager.limits.ArtifactCount {
		return preparedDirectJob{}, errors.New("node job artifact count exceeds policy")
	}
	names := make(map[string]struct{}, len(input.Artifacts))
	for _, artifact := range input.Artifacts {
		if err := artifact.validate(); err != nil {
			return preparedDirectJob{}, err
		}
		if _, duplicate := names[artifact.Name]; duplicate {
			return preparedDirectJob{}, errors.New("duplicate node job artifact name")
		}
		names[artifact.Name] = struct{}{}
	}
	base, err := json.Marshal(systemExecInput{
		Argv:           input.Argv,
		CWD:            input.CWD,
		TimeoutSeconds: input.TimeoutSeconds,
		Env:            input.Env,
	})
	if err != nil {
		return preparedDirectJob{}, err
	}
	command, err := newSystemExecHandler(manager.policy).prepare(
		base,
		int(manager.limits.Timeout/time.Second),
	)
	if err != nil {
		return preparedDirectJob{}, err
	}
	timeout := time.Duration(command.timeoutSeconds) * time.Second
	if timeout > manager.limits.Timeout {
		return preparedDirectJob{}, errors.New("node job timeout exceeds profile")
	}
	var root *fileRoot
	if len(input.Artifacts) > 0 {
		root, err = openFileRoot(command.cwd)
		if err != nil {
			return preparedDirectJob{}, fmt.Errorf("anchor node job working scope: %w", err)
		}
	}
	return preparedDirectJob{
		command: command, artifacts: append([]JobArtifactDeclaration(nil), input.Artifacts...),
		timeout: timeout, root: root,
	}, nil
}

func (manager *DirectJobManager) prepareActive(
	jobID string,
	prepared *preparedDirectJob,
) (*activeDirectJob, error) {
	if prepared == nil {
		return nil, errors.New("prepared node job is required")
	}
	stdoutName := jobLogFileName(jobID, false)
	stderrName := jobLogFileName(jobID, true)
	_ = manager.store.RemoveFile(stdoutName)
	_ = manager.store.RemoveFile(stderrName)
	stdoutFile, err := manager.store.CreateFile(stdoutName)
	if err != nil {
		return nil, err
	}
	stderrFile, err := manager.store.CreateFile(stderrName)
	if err != nil {
		_ = stdoutFile.Close()
		_ = manager.store.RemoveFile(stdoutName)
		return nil, err
	}
	stdout := &boundedJobLog{file: stdoutFile, limit: manager.limits.StdoutBytes}
	stderr := &boundedJobLog{file: stderrFile, limit: manager.limits.StderrBytes}
	command := exec.Command(prepared.command.executable, prepared.command.args...)
	command.Dir = prepared.command.cwd
	command.Env = prepared.command.env
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second
	prepareJobProcess(command)
	active := &activeDirectJob{
		command: command, stdout: stdout, stderr: stderr, root: prepared.root,
		artifacts: prepared.artifacts,
		cancel:    make(chan struct{}), done: make(chan struct{}),
	}
	prepared.root = nil
	return active, nil
}

func (manager *DirectJobManager) wait(jobID string, active *activeDirectJob) {
	timer := time.NewTimer(jobTimeoutDelay(active.timeoutAt, time.Now()))
	defer timer.Stop()
	observer := time.NewTicker(jobProcessObservationInterval)
	defer observer.Stop()
	reason := ""
	for reason == "" {
		select {
		case <-active.cancel:
			reason = "cancel"
		case <-timer.C:
			reason = "timeout"
		case <-observer.C:
			exited, err := jobProcessLeaderExited(active.command.Process.Pid)
			if err == nil && exited {
				reason = "completed"
			}
		}
	}
	drain := drainJobProcessGroup(active.command, reason != "completed")
	waitErr := active.command.Wait()
	stdout := active.stdout.close()
	stderr := active.stderr.close()
	artifacts := manager.snapshotArtifacts(active)
	_ = active.root.close()
	completion := jobCompletionForProcess(
		active.command,
		waitErr,
		reason,
		drain,
	)
	completion.Stdout = stdout
	completion.Stderr = stderr
	completion.Artifacts = artifacts
	if _, err := manager.store.Complete(jobID, completion); err != nil {
		_, _ = manager.store.MarkUnknown(jobID, "COMPLETION_PERSIST_FAILED")
	}
	_ = manager.store.ReleasePayload(jobID)
	manager.release(jobID)
	close(active.done)
}

func jobTimeoutDelay(timeoutAt int64, now time.Time) time.Duration {
	return max(time.Unix(0, timeoutAt).Sub(now), 0)
}

func jobCompletionForProcess(
	command *exec.Cmd,
	waitErr error,
	reason string,
	drain jobProcessDrainResult,
) JobCompletion {
	completion := JobCompletion{}
	var processState *os.ProcessState
	if command != nil && command.ProcessState != nil {
		processState = command.ProcessState
		exitCode := processState.ExitCode()
		completion.ExitCode = &exitCode
		completion.Signal = jobProcessSignal(processState)
	}
	terminationProven := !drain.observationUnknown &&
		drain.signalSent &&
		drain.signaledLiveLeader &&
		jobProcessKilled(processState)
	completion.CancellationSignal = drain.signalSent
	if drain.observationUnknown {
		if reason == "cancel" {
			completion.State = JobCancelUnknown
			completion.FailureCode = "CANCEL_OUTCOME_UNKNOWN"
			return completion
		}
		completion.State = JobUnknown
		completion.FailureCode = "PROCESS_OBSERVATION_FAILED"
		return completion
	}
	switch reason {
	case "timeout":
		if terminationProven {
			completion.State = JobTimedOut
			completion.FailureCode = "TIMEOUT"
			return completion
		}
	case "cancel":
		if terminationProven {
			completion.State = JobCanceled
			completion.FailureCode = "CANCELED"
			return completion
		}
	case "completed":
		// Fall through to the observed natural process result.
	default:
		completion.State = JobUnknown
		completion.FailureCode = "PROCESS_OUTCOME_UNKNOWN"
		return completion
	}
	if drain.hadDescendants {
		completion.State = JobFailed
		completion.FailureCode = "PROCESS_GROUP_OUTLIVED_LEADER"
		return completion
	}
	if waitErr == nil {
		completion.State = JobSucceeded
		return completion
	}
	completion.State = JobFailed
	completion.FailureCode = "PROCESS_FAILED"
	return completion
}

func (manager *DirectJobManager) snapshotArtifacts(active *activeDirectJob) []JobArtifactRecord {
	results := make([]JobArtifactRecord, 0, len(active.artifacts))
	remaining := manager.limits.ArtifactsTotal
	for _, declaration := range active.artifacts {
		result := JobArtifactRecord{Name: declaration.Name, State: JobArtifactFailed}
		if active.root == nil {
			result.FailureCode = "WORKING_SCOPE_UNAVAILABLE"
			results = append(results, result)
			continue
		}
		limit := min(manager.limits.ArtifactBytes, remaining)
		if limit <= 0 {
			result.FailureCode = "ARTIFACT_TOTAL_LIMIT"
			results = append(results, result)
			continue
		}
		sourcePath := filepath.Join(active.root.path, declaration.Path)
		source, err := active.root.openRegular(sourcePath, limit, false)
		if errors.Is(err, ErrFileNotFound) {
			result.State = JobArtifactMissing
			result.FailureCode = "NOT_FOUND"
			results = append(results, result)
			continue
		}
		if err != nil || source.identity.Links != 1 {
			result.FailureCode = "SOURCE_DENIED"
			if source != nil {
				_ = source.file.Close()
			}
			results = append(results, result)
			continue
		}
		result = manager.snapshotArtifact(declaration.Name, source, limit)
		_ = source.file.Close()
		if result.State == JobArtifactAvailable {
			remaining -= result.Size
		}
		results = append(results, result)
	}
	return results
}

func (manager *DirectJobManager) snapshotArtifact(
	name string,
	source *resolvedFile,
	limit int64,
) JobArtifactRecord {
	result := JobArtifactRecord{Name: name, State: JobArtifactFailed}
	ref, err := newJobArtifactRef()
	if err != nil {
		result.FailureCode = "STORE_FAILED"
		return result
	}
	fileName := ref + ".artifact"
	destination, err := manager.store.CreateFile(fileName)
	if err != nil {
		result.FailureCode = "STORE_FAILED"
		return result
	}
	committed := false
	defer func() {
		if destination != nil {
			_ = destination.Close()
		}
		if !committed {
			_ = manager.store.RemoveFile(fileName)
		}
	}()
	hasher := sha256.New()
	written, overflow, err := copyBoundedJobArtifact(
		io.MultiWriter(destination, hasher),
		source.file,
		limit,
	)
	if err != nil || destination.Sync() != nil {
		result.FailureCode = "SNAPSHOT_FAILED"
		return result
	}
	if overflow || written != source.info.Size() {
		result.FailureCode = "SOURCE_CHANGED"
		return result
	}
	after, err := source.file.Stat()
	if err != nil || !sameOpenedFile(source, after) {
		result.FailureCode = "SOURCE_CHANGED"
		return result
	}
	identity, err := identityFromInfo(after)
	if err != nil || identity.Links != 1 {
		result.FailureCode = "SOURCE_CHANGED"
		return result
	}
	if err := destination.Close(); err != nil {
		result.FailureCode = "SNAPSHOT_FAILED"
		return result
	}
	destination = nil
	if err := manager.store.SyncFiles(); err != nil {
		result.FailureCode = "SNAPSHOT_FAILED"
		return result
	}
	committed = true
	return JobArtifactRecord{
		Name: name, State: JobArtifactAvailable, ArtifactRef: ref, FileName: fileName,
		Size: written, SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}
}

func copyBoundedJobArtifact(destination io.Writer, source io.Reader, limit int64) (int64, bool, error) {
	limited := &io.LimitedReader{R: source, N: limit}
	written, err := io.Copy(destination, limited)
	if err != nil {
		return written, false, err
	}
	var probe [1]byte
	read, probeErr := source.Read(probe[:])
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return written, false, probeErr
	}
	return written, read > 0, nil
}

func (manager *DirectJobManager) Cancel(owner JobOwner, jobID string) (JobRecord, error) {
	record, found, err := manager.store.Lookup(jobID)
	if err != nil {
		return JobRecord{}, err
	}
	if !found {
		return JobRecord{}, ErrJobNotFound
	}
	if !manager.owns(record, owner) {
		return JobRecord{}, ErrJobNotFound
	}
	manager.mu.Lock()
	active := manager.active[jobID]
	manager.mu.Unlock()
	if active != nil {
		active.launchMu.Lock()
		defer active.launchMu.Unlock()
	}
	record, err = manager.store.RequestCancellation(jobID)
	if err != nil || record.State.terminal() {
		return record, err
	}
	if active == nil {
		return manager.store.MarkUnknown(jobID, "ACTIVE_PROCESS_UNAVAILABLE")
	}
	active.once.Do(func() { close(active.cancel) })
	return record, nil
}

func (manager *DirectJobManager) Status(owner JobOwner, jobID string) (JobRecord, error) {
	record, found, err := manager.store.Lookup(jobID)
	if err != nil {
		return JobRecord{}, err
	}
	if !found || !manager.owns(record, owner) {
		return JobRecord{}, ErrJobNotFound
	}
	return record, nil
}

type JobLogChunk struct {
	Data      []byte
	Next      int64
	Available int64
	Truncated bool
	State     JobState
}

func (manager *DirectJobManager) ReadLog(
	owner JobOwner,
	jobID string,
	stderr bool,
	cursor int64,
	limit int,
) (JobLogChunk, error) {
	if cursor < 0 || limit <= 0 || limit > maxJobLogRead {
		return JobLogChunk{}, errors.New("node job log request exceeds bounds")
	}
	record, found, lookupErr := manager.store.Lookup(jobID)
	if lookupErr != nil {
		return JobLogChunk{}, lookupErr
	}
	if !found || !manager.owns(record, owner) {
		return JobLogChunk{}, ErrJobNotFound
	}
	file, info, err := manager.store.OpenFile(jobLogFileName(jobID, stderr))
	if err != nil {
		return JobLogChunk{}, err
	}
	defer func() { _ = file.Close() }()
	if cursor > info.Size() {
		return JobLogChunk{}, errors.New("node job log cursor is beyond retained data")
	}
	data := make([]byte, min(int64(limit), info.Size()-cursor))
	read, err := file.ReadAt(data, cursor)
	if err != nil && !errors.Is(err, io.EOF) {
		return JobLogChunk{}, err
	}
	logRecord := record.Stdout
	if stderr {
		logRecord = record.Stderr
	}
	manager.mu.Lock()
	active := manager.active[jobID]
	manager.mu.Unlock()
	if active != nil {
		if stderr {
			logRecord = active.stderr.record()
		} else {
			logRecord = active.stdout.record()
		}
	}
	return JobLogChunk{
		Data: data[:read], Next: cursor + int64(read), Available: info.Size(),
		Truncated: logRecord.Truncated, State: record.State,
	}, nil
}

func (manager *DirectJobManager) Artifacts(owner JobOwner, jobID string) ([]JobArtifactRecord, error) {
	record, found, err := manager.store.Lookup(jobID)
	if err != nil {
		return nil, err
	}
	if !found || !manager.owns(record, owner) {
		return nil, ErrJobNotFound
	}
	return cloneJobArtifacts(record.Artifacts), nil
}

func (manager *DirectJobManager) OpenArtifact(
	owner JobOwner,
	jobID string,
	artifactRef string,
) (*os.File, JobArtifactRecord, error) {
	record, found, lookupErr := manager.store.Lookup(jobID)
	if lookupErr != nil {
		return nil, JobArtifactRecord{}, lookupErr
	}
	if !found || !manager.owns(record, owner) {
		return nil, JobArtifactRecord{}, ErrJobNotFound
	}
	for _, artifact := range record.Artifacts {
		if artifact.ArtifactRef != artifactRef || artifact.State != JobArtifactAvailable {
			continue
		}
		file, info, err := manager.store.OpenFile(artifact.FileName)
		if err != nil {
			return nil, JobArtifactRecord{}, err
		}
		identity, identityErr := identityFromInfo(info)
		if info.Size() != artifact.Size || identityErr != nil || identity.Links != 1 {
			_ = file.Close()
			return nil, JobArtifactRecord{}, ErrJobConflict
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, file); err != nil ||
			hex.EncodeToString(hasher.Sum(nil)) != artifact.SHA256 {
			_ = file.Close()
			return nil, JobArtifactRecord{}, ErrJobConflict
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, JobArtifactRecord{}, err
		}
		return file, artifact, nil
	}
	return nil, JobArtifactRecord{}, ErrJobNotFound
}

func (manager *DirectJobManager) Shutdown(ctx context.Context) error {
	manager.mu.Lock()
	manager.closed = true
	manager.mu.Unlock()
	manager.starts.Wait()
	manager.mu.Lock()
	type ownedActive struct {
		jobID string
		job   *activeDirectJob
	}
	active := make([]ownedActive, 0, len(manager.active))
	for jobID, job := range manager.active {
		if job != nil {
			active = append(active, ownedActive{jobID: jobID, job: job})
		}
	}
	manager.mu.Unlock()
	for _, item := range active {
		record, found, lookupErr := manager.store.Lookup(item.jobID)
		if lookupErr != nil {
			return lookupErr
		}
		if found {
			_, _ = manager.Cancel(record.Owner, item.jobID)
		}
		select {
		case <-item.job.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return nil
}

func (manager *DirectJobManager) owns(record JobRecord, owner JobOwner) bool {
	return record.Owner == owner &&
		record.ProfileAlias == manager.profileAlias &&
		record.ProfileRevision == manager.profileRevision
}

func (manager *DirectJobManager) reserve(jobID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return errors.New("node job manager is closed")
	}
	if _, duplicate := manager.active[jobID]; duplicate {
		return ErrJobConflict
	}
	if len(manager.active) >= manager.limits.ConcurrentJobs {
		return ErrJobBusy
	}
	manager.starts.Add(1)
	manager.active[jobID] = nil
	return nil
}

func (manager *DirectJobManager) setActive(jobID string, active *activeDirectJob) {
	manager.mu.Lock()
	manager.active[jobID] = active
	manager.mu.Unlock()
}

func (manager *DirectJobManager) release(jobID string) {
	manager.mu.Lock()
	delete(manager.active, jobID)
	manager.mu.Unlock()
}

func (manager *DirectJobManager) abortBeforeStart(jobID string, active *activeDirectJob) {
	active.stdout.close()
	active.stderr.close()
	_ = active.root.close()
	_ = manager.store.ReleasePayload(jobID)
	manager.release(jobID)
}

func pendingJobArtifacts(declarations []JobArtifactDeclaration) []JobArtifactRecord {
	artifacts := make([]JobArtifactRecord, 0, len(declarations))
	for _, declaration := range declarations {
		artifacts = append(artifacts, JobArtifactRecord{
			Name: declaration.Name, State: JobArtifactPending,
		})
	}
	return artifacts
}
