package companion

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const jobStoreVersion = 1

type jobStoreDocument struct {
	Version int                  `json:"version"`
	Records map[string]JobRecord `json:"records"`
}

type JobStore struct {
	root        string
	indexPath   string
	maxRecords  int
	maxBytes    int
	maxPayload  int64
	retention   time.Duration
	now         func() time.Time
	writeFile   func(string, []byte, os.FileMode) error
	removeFile  func(string) error
	directory   *jobStoreDirectory
	releaseLock func()

	mu          sync.Mutex
	records     map[string]JobRecord
	invocations map[string]string
	idempotency map[string]string
	payloadUsed int64
	reserved    map[string]int64
	pending     map[string]struct{}
}

func JobStorePath(stateDir string) string {
	return filepath.Join(stateDir, "jobs")
}

func NewJobStore(
	root string,
	limits JobStoreLimits,
) (*JobStore, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(filepath.Separator) {
		return nil, errors.New("node job store path is required")
	}
	limits, limitsErr := normalizeJobStoreLimits(limits)
	if limitsErr != nil {
		return nil, limitsErr
	}
	if mkdirErr := os.MkdirAll(root, 0o700); mkdirErr != nil {
		return nil, fmt.Errorf("create node job store: %w", mkdirErr)
	}
	directory, openErr := openJobStoreDirectory(root)
	if openErr != nil {
		return nil, fmt.Errorf("open node job store: %w", openErr)
	}
	indexPath := filepath.Join(root, "index.json")
	release, lockErr := acquireInvocationLedgerLock(indexPath + ".lock")
	if lockErr != nil {
		_ = directory.close()
		return nil, lockErr
	}
	store := &JobStore{
		root:        root,
		indexPath:   indexPath,
		maxRecords:  limits.Records,
		maxBytes:    limits.IndexBytes,
		maxPayload:  limits.PayloadBytes,
		retention:   limits.Retention,
		now:         time.Now,
		writeFile:   fileutil.WriteFileAtomic,
		removeFile:  directory.removeRegular,
		directory:   directory,
		releaseLock: release,
		records:     make(map[string]JobRecord),
		invocations: make(map[string]string),
		idempotency: make(map[string]string),
		reserved:    make(map[string]int64),
		pending:     make(map[string]struct{}),
	}
	if err := store.load(); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.reconcileUnfinished(); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.pruneExpired(); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.removeOrphanFiles(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (store *JobStore) Close() {
	if store == nil {
		return
	}
	store.mu.Lock()
	directory := store.directory
	store.directory = nil
	release := store.releaseLock
	store.releaseLock = nil
	store.mu.Unlock()
	if directory != nil {
		_ = directory.close()
	}
	if release != nil {
		release()
	}
}

func (store *JobStore) Accept(record JobRecord) (JobRecord, bool, error) {
	if err := record.validate(); err != nil {
		return JobRecord{}, false, err
	}
	if time.Duration(record.RetentionSeconds)*time.Second > store.retention {
		return JobRecord{}, false, ErrJobConflict
	}
	if record.State != JobAccepted {
		return JobRecord{}, false, ErrJobConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found, err := store.existingLocked(record); found || err != nil {
		return existing, found, err
	}
	previous := cloneJobRecords(store.records)
	store.pruneExpiredLocked(store.now(), record.JobID)
	if len(store.records) >= store.maxRecords {
		store.records = previous
		store.rebuildIndexesLocked()
		store.pending = make(map[string]struct{})
		return JobRecord{}, false, ErrJobStoreFull
	}
	store.records[record.JobID] = cloneJobRecord(record)
	store.invocations[record.StartInvocationID] = record.JobID
	store.idempotency[record.StartIdempotencyKey] = record.JobID
	if err := store.persistLocked(record.JobID); err != nil {
		store.rollbackLocked(previous, err)
		return JobRecord{}, false, fmt.Errorf("persist accepted node job: %w", err)
	}
	return cloneJobRecord(record), false, nil
}

func (store *JobStore) Existing(record JobRecord) (JobRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.existingLocked(record)
}

func (store *JobStore) existingLocked(record JobRecord) (JobRecord, bool, error) {
	if id, found := store.invocations[record.StartInvocationID]; found {
		existing := store.records[id]
		if !sameJobStartBinding(existing, record) {
			return JobRecord{}, false, ErrJobConflict
		}
		return cloneJobRecord(existing), true, nil
	}
	if id, found := store.idempotency[record.StartIdempotencyKey]; found {
		existing := store.records[id]
		if !sameJobStartBinding(existing, record) {
			return JobRecord{}, false, ErrJobConflict
		}
		return cloneJobRecord(existing), true, nil
	}
	return JobRecord{}, false, nil
}

func (store *JobStore) Lookup(jobID string) (JobRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.pruneExpiredAndPersistLocked(); err != nil {
		return JobRecord{}, false, fmt.Errorf("prune expired node jobs: %w", err)
	}
	record, found := store.records[jobID]
	return cloneJobRecord(record), found, nil
}

func (store *JobStore) Records() []JobRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	records := make([]JobRecord, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, cloneJobRecord(record))
	}
	slices.SortFunc(records, func(a, b JobRecord) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.JobID, b.JobID)
	})
	return records
}

func (store *JobStore) transition(
	jobID string,
	update func(*JobRecord, time.Time) error,
) (JobRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[jobID]
	if !found {
		return JobRecord{}, ErrJobNotFound
	}
	previousRecord := cloneJobRecord(record)
	previous := cloneJobRecords(store.records)
	now := store.now()
	if err := update(&record, now); err != nil {
		return JobRecord{}, err
	}
	record.UpdatedAt = now.UnixNano()
	if record.State.terminal() && record.CompletedAt == 0 {
		record.CompletedAt = record.UpdatedAt
	}
	if err := validateJobTransition(previousRecord, record); err != nil {
		return JobRecord{}, err
	}
	if err := record.validate(); err != nil {
		return JobRecord{}, err
	}
	store.records[jobID] = record
	if err := store.persistLocked(jobID); err != nil {
		store.rollbackLocked(previous, err)
		return JobRecord{}, fmt.Errorf("persist node job transition: %w", err)
	}
	return cloneJobRecord(record), nil
}

func (store *JobStore) MarkLaunchAttempted(jobID string) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, now time.Time) error {
		if record.State != JobAccepted {
			return ErrJobConflict
		}
		record.State = JobLaunchAttempted
		record.LaunchAttemptedAt = now.UnixNano()
		return nil
	})
}

func (store *JobStore) MarkRunning(jobID string, timeout time.Duration) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, now time.Time) error {
		if record.State != JobLaunchAttempted || timeout < time.Second || timeout > MaxJobTimeout {
			return ErrJobConflict
		}
		record.State = JobRunning
		record.StartedAt = now.UnixNano()
		record.TimeoutAt = now.Add(timeout).UnixNano()
		return nil
	})
}

func (store *JobStore) MarkFailedBeforeLaunch(jobID, code string) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, _ time.Time) error {
		if record.State != JobAccepted || code == "" {
			return ErrJobConflict
		}
		record.State = JobFailedBeforeLaunch
		record.FailureCode = code
		return nil
	})
}

func (store *JobStore) MarkStartFailed(jobID, code string) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, _ time.Time) error {
		if record.State != JobLaunchAttempted || code == "" {
			return ErrJobConflict
		}
		record.State = JobFailed
		record.FailureCode = code
		return nil
	})
}

func (store *JobStore) MarkUnknown(jobID, code string) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, _ time.Time) error {
		if record.State != JobLaunchAttempted && record.State != JobRunning &&
			record.State != JobCancelRequested || code == "" {
			return ErrJobConflict
		}
		record.State = JobUnknown
		record.FailureCode = code
		return nil
	})
}

func (store *JobStore) RequestCancellation(jobID string) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, now time.Time) error {
		if record.State.terminal() || record.State == JobCancelRequested {
			return nil
		}
		if record.State == JobAccepted {
			record.State = JobCanceled
			record.CancelRequestedAt = now.UnixNano()
			return nil
		}
		if record.State != JobLaunchAttempted && record.State != JobRunning {
			return ErrJobConflict
		}
		record.State = JobCancelRequested
		record.CancelRequestedAt = now.UnixNano()
		return nil
	})
}

type JobCompletion struct {
	State              JobState
	ExitCode           *int
	Signal             string
	FailureCode        string
	CancellationSignal bool
	Stdout             JobLogRecord
	Stderr             JobLogRecord
	Artifacts          []JobArtifactRecord
}

func (store *JobStore) Complete(jobID string, completion JobCompletion) (JobRecord, error) {
	return store.transition(jobID, func(record *JobRecord, _ time.Time) error {
		if record.State.terminal() {
			return nil
		}
		if !completion.State.terminal() || completion.State == JobFailedBeforeLaunch ||
			completion.State == JobUnknown ||
			(record.State != JobRunning && record.State != JobCancelRequested &&
				record.State != JobLaunchAttempted) {
			return ErrJobConflict
		}
		record.State = completion.State
		record.ExitCode = cloneInt(completion.ExitCode)
		record.Signal = completion.Signal
		record.FailureCode = completion.FailureCode
		record.CancellationSignal = completion.CancellationSignal
		record.Stdout = completion.Stdout
		record.Stderr = completion.Stderr
		record.Artifacts = cloneJobArtifacts(completion.Artifacts)
		return nil
	})
}

func (store *JobStore) CreateFile(name string) (*os.File, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.directory == nil {
		return nil, errors.New("node job store is closed")
	}
	return store.directory.createRegularExclusive(name, 0o600)
}

func (store *JobStore) OpenFile(name string) (*os.File, os.FileInfo, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.directory == nil {
		return nil, nil, errors.New("node job store is closed")
	}
	return store.directory.openRegular(name)
}

func (store *JobStore) RemoveFile(name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.directory == nil {
		return errors.New("node job store is closed")
	}
	return store.directory.removeRegular(name)
}

func (store *JobStore) SyncFiles() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.directory == nil {
		return errors.New("node job store is closed")
	}
	return store.directory.sync()
}

func (store *JobStore) ReservePayload(jobID string, bytes int64) error {
	if bytes <= 0 || bytes > store.maxPayload {
		return ErrJobStoreFull
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found := store.reserved[jobID]; found {
		if existing != bytes {
			return ErrJobConflict
		}
		return nil
	}
	reserved := int64(0)
	for _, value := range store.reserved {
		reserved += value
	}
	if store.payloadUsed+reserved+bytes > store.maxPayload {
		return ErrJobStoreFull
	}
	store.reserved[jobID] = bytes
	return nil
}

func (store *JobStore) ReleasePayload(jobID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.reserved, jobID)
	used, err := store.payloadBytesLocked()
	if err != nil {
		return err
	}
	store.payloadUsed = used
	return nil
}

func (store *JobStore) reconcileUnfinished() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previous := cloneJobRecords(store.records)
	changed := false
	now := store.now().UnixNano()
	for id, record := range store.records {
		switch record.State {
		case JobLaunchAttempted, JobRunning, JobCancelRequested:
			if stdout, found, err := store.retainedLogLocked(id, false); err != nil {
				return err
			} else if found {
				record.Stdout.Bytes = stdout
			}
			if stderr, found, err := store.retainedLogLocked(id, true); err != nil {
				return err
			} else if found {
				record.Stderr.Bytes = stderr
			}
			record.State = JobUnknown
			record.FailureCode = "COMPANION_RESTART"
			record.UpdatedAt = now
			record.CompletedAt = now
			store.records[id] = record
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := store.persistLocked(""); err != nil {
		store.rollbackLocked(previous, err)
		return fmt.Errorf("persist reconciled node jobs: %w", err)
	}
	return nil
}

func (store *JobStore) retainedLogLocked(jobID string, stderr bool) (int64, bool, error) {
	file, info, err := store.directory.openRegular(jobLogFileName(jobID, stderr))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	_ = file.Close()
	if info.Size() < 0 || info.Size() > MaxJobLogBytes {
		return 0, false, errors.New("retained node job log exceeds hard bound")
	}
	return info.Size(), true, nil
}

func (store *JobStore) load() error {
	file, err := os.Open(store.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open node job index: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, int64(store.maxBytes)+1))
	decoder.DisallowUnknownFields()
	var document jobStoreDocument
	if decodeErr := decoder.Decode(&document); decodeErr != nil {
		return fmt.Errorf("decode node job index: %w", decodeErr)
	}
	if eofErr := ensureConfigEOF(decoder); eofErr != nil {
		return fmt.Errorf("decode node job index: %w", eofErr)
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat node job index: %w", err)
	}
	if info.Size() > int64(store.maxBytes) || document.Version != jobStoreVersion ||
		document.Records == nil || len(document.Records) > store.maxRecords {
		return errors.New("invalid node job index document")
	}
	for id, record := range document.Records {
		if id != record.JobID {
			return errors.New("node job index key does not match record")
		}
		if err := record.validate(); err != nil {
			return fmt.Errorf("validate node job record: %w", err)
		}
	}
	store.records = cloneJobRecords(document.Records)
	store.rebuildIndexesLocked()
	if len(store.invocations) != len(store.records) || len(store.idempotency) != len(store.records) {
		return errors.New("node job index contains duplicate start identity")
	}
	return nil
}

func (store *JobStore) persistLocked(protectedID string) error {
	for {
		data, err := json.Marshal(jobStoreDocument{Version: jobStoreVersion, Records: store.records})
		if err != nil {
			return fmt.Errorf("encode node job index: %w", err)
		}
		if len(data) <= store.maxBytes {
			if err := store.writeFile(store.indexPath, append(data, '\n'), 0o600); err != nil {
				return err
			}
			if err := store.flushPendingLocked(); err != nil {
				return &fileutil.CommittedWriteError{Err: err}
			}
			return nil
		}
		if !store.pruneOldestExpiredLocked(store.now(), protectedID) {
			return ErrJobStoreFull
		}
	}
}

func (store *JobStore) pruneExpiredLocked(now time.Time, protectedID string) {
	for store.pruneOldestExpiredLocked(now, protectedID) {
	}
}

func (store *JobStore) pruneExpired() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, err := store.pruneExpiredAndPersistLocked()
	return err
}

func (store *JobStore) pruneExpiredAndPersistLocked() (bool, error) {
	now := store.now()
	if !store.hasExpiredLocked(now, "") {
		if err := store.flushPendingLocked(); err != nil {
			return false, err
		}
		return false, nil
	}
	previous := cloneJobRecords(store.records)
	store.pruneExpiredLocked(now, "")
	if err := store.persistLocked(""); err != nil {
		store.rollbackLocked(previous, err)
		return true, err
	}
	return true, nil
}

func (store *JobStore) hasExpiredLocked(now time.Time, protectedID string) bool {
	for id, record := range store.records {
		retention := min(time.Duration(record.RetentionSeconds)*time.Second, store.retention)
		if id != protectedID && record.State.terminal() &&
			!now.Before(time.Unix(0, record.CompletedAt).Add(retention)) {
			return true
		}
	}
	return false
}

func (store *JobStore) pruneOldestExpiredLocked(now time.Time, protectedID string) bool {
	oldestID := ""
	var oldestAt int64
	for id, record := range store.records {
		retention := min(time.Duration(record.RetentionSeconds)*time.Second, store.retention)
		if id == protectedID || !record.State.terminal() ||
			now.Before(time.Unix(0, record.CompletedAt).Add(retention)) {
			continue
		}
		if oldestID == "" || record.CompletedAt < oldestAt ||
			record.CompletedAt == oldestAt && id < oldestID {
			oldestID = id
			oldestAt = record.CompletedAt
		}
	}
	if oldestID == "" {
		return false
	}
	record := store.records[oldestID]
	store.queueRecordFilesLocked(record)
	delete(store.records, oldestID)
	delete(store.invocations, record.StartInvocationID)
	delete(store.idempotency, record.StartIdempotencyKey)
	return true
}

func (store *JobStore) removeOrphanFiles() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.directory == nil {
		return errors.New("node job store is closed")
	}
	referenced := map[string]struct{}{"index.json": {}, "index.json.lock": {}}
	for _, record := range store.records {
		referenced[jobLogFileName(record.JobID, false)] = struct{}{}
		referenced[jobLogFileName(record.JobID, true)] = struct{}{}
		for _, artifact := range record.Artifacts {
			if artifact.FileName != "" {
				referenced[artifact.FileName] = struct{}{}
			}
		}
	}
	names, err := store.directory.listNames()
	if err != nil {
		return err
	}
	used := int64(0)
	for _, name := range names {
		if _, keep := referenced[name]; keep {
			if name != "index.json" && name != "index.json.lock" {
				file, info, openErr := store.directory.openRegular(name)
				if openErr != nil {
					return openErr
				}
				_ = file.Close()
				used += info.Size()
			}
			continue
		}
		if err := store.removeFile(name); err != nil {
			return fmt.Errorf("remove orphaned node job file %q: %w", name, err)
		}
	}
	store.payloadUsed = used
	return nil
}

func (store *JobStore) queueRecordFilesLocked(record JobRecord) {
	store.pending[jobLogFileName(record.JobID, false)] = struct{}{}
	store.pending[jobLogFileName(record.JobID, true)] = struct{}{}
	for _, artifact := range record.Artifacts {
		if artifact.FileName != "" {
			store.pending[artifact.FileName] = struct{}{}
		}
	}
}

func (store *JobStore) flushPendingLocked() error {
	for name := range store.pending {
		file, info, err := store.directory.openRegular(name)
		if errors.Is(err, os.ErrNotExist) {
			delete(store.pending, name)
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect expired node job file %q: %w", name, err)
		}
		_ = file.Close()
		if err := store.removeFile(name); err != nil {
			return fmt.Errorf("remove expired node job file %q: %w", name, err)
		}
		store.payloadUsed -= info.Size()
		if store.payloadUsed < 0 {
			store.payloadUsed = 0
		}
		delete(store.pending, name)
	}
	return nil
}

func (store *JobStore) payloadBytesLocked() (int64, error) {
	names, err := store.directory.listNames()
	if err != nil {
		return 0, err
	}
	used := int64(0)
	for _, name := range names {
		if name == "index.json" || name == "index.json.lock" {
			continue
		}
		file, info, err := store.directory.openRegular(name)
		if err != nil {
			return 0, err
		}
		_ = file.Close()
		used += info.Size()
	}
	return used, nil
}

func validateJobTransition(previous, next JobRecord) error {
	if !sameJobStartBinding(previous, next) || previous.JobID != next.JobID ||
		previous.CreatedAt != next.CreatedAt || previous.CancelGuarantee != next.CancelGuarantee ||
		next.UpdatedAt < previous.UpdatedAt || next.StartedAt < previous.StartedAt ||
		next.LaunchAttemptedAt < previous.LaunchAttemptedAt ||
		next.CancelRequestedAt < previous.CancelRequestedAt ||
		next.Stdout.Bytes < previous.Stdout.Bytes || next.Stderr.Bytes < previous.Stderr.Bytes {
		return ErrJobConflict
	}
	var allowed bool
	switch previous.State {
	case JobAccepted:
		allowed = next.State == JobLaunchAttempted || next.State == JobFailedBeforeLaunch ||
			next.State == JobCanceled
	case JobLaunchAttempted:
		allowed = next.State == JobRunning || next.State == JobFailed ||
			next.State == JobCancelRequested || next.State == JobUnknown
	case JobRunning:
		allowed = next.State == JobSucceeded || next.State == JobFailed ||
			next.State == JobCancelRequested || next.State == JobTimedOut ||
			next.State == JobUnknown
	case JobCancelRequested:
		allowed = next.State == JobCanceled || next.State == JobCancelUnknown ||
			next.State == JobUnknown || next.State == JobSucceeded || next.State == JobFailed ||
			next.State == JobTimedOut
	default:
		allowed = previous.State == next.State && previous.State.terminal()
	}
	if !allowed {
		return ErrJobConflict
	}
	return nil
}

func sameJobStartBinding(left, right JobRecord) bool {
	return left.StartInvocationID == right.StartInvocationID &&
		left.StartIdempotencyKey == right.StartIdempotencyKey &&
		left.PlanHash == right.PlanHash && left.ProfileAlias == right.ProfileAlias &&
		left.ProfileRevision == right.ProfileRevision &&
		left.RetentionSeconds == right.RetentionSeconds &&
		left.Owner == right.Owner
}

func newJobID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(value[:]), nil
}

func newJobArtifactRef() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "jobart_" + hex.EncodeToString(value[:]), nil
}

func jobLogFileName(jobID string, stderr bool) string {
	stream := "stdout"
	if stderr {
		stream = "stderr"
	}
	return jobID + "." + stream + ".log"
}

func cloneJobRecord(record JobRecord) JobRecord {
	record.ExitCode = cloneInt(record.ExitCode)
	record.Artifacts = cloneJobArtifacts(record.Artifacts)
	return record
}

func cloneJobRecords(records map[string]JobRecord) map[string]JobRecord {
	cloned := make(map[string]JobRecord, len(records))
	for id, record := range records {
		cloned[id] = cloneJobRecord(record)
	}
	return cloned
}

func cloneJobArtifacts(artifacts []JobArtifactRecord) []JobArtifactRecord {
	return append([]JobArtifactRecord(nil), artifacts...)
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (store *JobStore) rollbackLocked(previous map[string]JobRecord, err error) {
	if fileutil.IsCommittedWriteError(err) {
		return
	}
	store.records = previous
	store.pending = make(map[string]struct{})
	store.rebuildIndexesLocked()
}

func (store *JobStore) rebuildIndexesLocked() {
	store.invocations = make(map[string]string, len(store.records))
	store.idempotency = make(map[string]string, len(store.records))
	for id, record := range store.records {
		store.invocations[record.StartInvocationID] = id
		store.idempotency[record.StartIdempotencyKey] = id
	}
}
