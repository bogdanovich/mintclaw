package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

type JobRuntime struct {
	store       *JobStore
	managers    map[string]*DirectJobManager
	descriptors map[string]nodes.CommandDescriptor
}

func NewJobRuntime(
	stateDir string,
	profiles JobProfiles,
	systemExec SystemExecPolicy,
) (*JobRuntime, error) {
	retention, err := enabledJobProfileLimits(profiles)
	if err != nil {
		return nil, err
	}
	descriptorProfiles, err := jobProfileDescriptorsForPolicy(profiles, systemExec)
	if err != nil {
		return nil, fmt.Errorf("project node job profiles: %w", err)
	}
	descriptors, err := nodes.JobCommandDescriptors(descriptorProfiles)
	if err != nil {
		return nil, fmt.Errorf("build node job descriptors: %w", err)
	}
	store, err := NewJobStore(JobStorePath(stateDir), JobStoreLimits{Retention: retention})
	if err != nil {
		return nil, err
	}
	runtime := &JobRuntime{
		store: store, managers: make(map[string]*DirectJobManager, len(descriptorProfiles)),
		descriptors: make(map[string]nodes.CommandDescriptor, len(descriptors)),
	}
	for _, descriptor := range descriptors {
		runtime.descriptors[descriptor.Name] = descriptor
	}
	for _, descriptor := range descriptorProfiles {
		profile := profiles[descriptor.Alias]
		manager, managerErr := NewDirectJobManager(
			store,
			systemExec,
			descriptor.Alias,
			descriptor.Revision,
			profile.limits,
		)
		if managerErr != nil {
			store.Close()
			return nil, fmt.Errorf("configure node job profile %q: %w", descriptor.Alias, managerErr)
		}
		runtime.managers[descriptor.Alias] = manager
	}
	return runtime, nil
}

func (runtime *JobRuntime) Descriptors() []nodes.CommandDescriptor {
	if runtime == nil {
		return nil
	}
	commands := []string{
		nodes.JobCommandStart,
		nodes.JobCommandStatus,
		nodes.JobCommandLogs,
		nodes.JobCommandArtifacts,
		nodes.JobCommandCancel,
	}
	result := make([]nodes.CommandDescriptor, 0, len(commands))
	for _, command := range commands {
		descriptor, found := runtime.descriptors[command]
		if found {
			result = append(result, cloneJobCommandDescriptor(descriptor))
		}
	}
	return result
}

func (runtime *JobRuntime) handlers(policy nodes.LocalCommandPolicy) ([]commandHandler, error) {
	descriptors := runtime.Descriptors()
	if len(descriptors) != 5 {
		return nil, errors.New("node job runtime has incomplete descriptors")
	}
	handlers := make([]commandHandler, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !slices.Contains(policy.AllowedCommands, descriptor.Name) ||
			modelRiskRank(descriptor.Risk) > modelRiskRank(policy.MaximumRisk) {
			continue
		}
		contract := cloneModelContract(descriptor.ModelContract)
		contract.TimeoutSecondsMax = min(contract.TimeoutSecondsMax, policy.MaxTimeoutSeconds)
		contract.OutputBytesMax = min(contract.OutputBytesMax, policy.MaxOutputBytes)
		descriptor.ModelContract = contract
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
		handlers = append(handlers, &jobCommandHandler{runtime: runtime, descriptorValue: descriptor})
	}
	return handlers, nil
}

func (runtime *JobRuntime) manager(profile string) (*DirectJobManager, error) {
	if runtime == nil || profile == "" {
		return nil, ErrCommandUnavailable
	}
	manager := runtime.managers[profile]
	if manager == nil {
		return nil, ErrCommandUnavailable
	}
	return manager, nil
}

func (runtime *JobRuntime) OpenArtifact(
	owner JobOwner,
	profile string,
	jobID string,
	artifactRef string,
) (*os.File, JobArtifactRecord, error) {
	manager, err := runtime.manager(profile)
	if err != nil {
		return nil, JobArtifactRecord{}, err
	}
	return manager.OpenArtifact(owner, jobID, artifactRef)
}

func (runtime *JobRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	aliases := make([]string, 0, len(runtime.managers))
	for alias := range runtime.managers {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		if err := runtime.managers[alias].Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown node job profile %q: %w", alias, err)
		}
	}
	runtime.store.Close()
	return nil
}

type jobCommandHandler struct {
	runtime         *JobRuntime
	descriptorValue nodes.CommandDescriptor
}

func (handler *jobCommandHandler) descriptor() nodes.CommandDescriptor {
	return cloneJobCommandDescriptor(handler.descriptorValue)
}

func (handler *jobCommandHandler) authorize(plan nodes.ExecutionPlan) error {
	manager, err := handler.runtime.manager(plan.JobProfile)
	if err != nil {
		return err
	}
	owner := jobOwnerFromPlan(plan)
	switch plan.Command {
	case nodes.JobCommandStart:
		if outputErr := ensureJobOutputFits(
			handler.descriptorValue,
			jobStartOutput{
				JobID: "job_0123456789abcdef0123456789abcdef", State: string(JobFailedBeforeLaunch),
				CreatedAt: 1 << 62, StartedAt: 1 << 62, TimeoutAt: 1 << 62,
				CancelGuarantee: string(JobCancelDirectProcess),
			},
			plan.OutputLimitBytes,
		); outputErr != nil {
			return outputErr
		}
		prepared, prepareErr := manager.prepare(plan)
		if prepared.root != nil {
			_ = prepared.root.close()
		}
		err = prepareErr
	case nodes.JobCommandStatus, nodes.JobCommandArtifacts, nodes.JobCommandCancel:
		input, inputErr := decodeJobIDInput(plan.Input)
		if inputErr != nil {
			return inputErr
		}
		_, err = manager.Status(owner, input.JobID)
		if err == nil && plan.Command == nodes.JobCommandCancel {
			err = ensureJobOutputFits(
				handler.descriptorValue,
				jobCancelOutput{
					JobID: input.JobID, State: string(JobFailedBeforeLaunch),
					Disposition:       "already_terminal",
					CancelRequestedAt: 1 << 62, CancellationSignal: true,
					FailureCode: strings.Repeat("X", nodes.MaxIDLength),
				},
				plan.OutputLimitBytes,
			)
		}
	case nodes.JobCommandLogs:
		input, inputErr := decodeJobLogsInput(plan.Input)
		if inputErr != nil {
			return inputErr
		}
		_, err = manager.Status(owner, input.JobID)
	default:
		return ErrCommandUnavailable
	}
	return err
}

func (handler *jobCommandHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, newCommandFailure("EXECUTION_CANCELED", "node job command canceled", err)
	}
	manager, err := handler.runtime.manager(invocation.Plan.JobProfile)
	if err != nil {
		return nil, newCommandFailure("COMMAND_DENIED", "node job profile unavailable", err)
	}
	owner := jobOwnerFromPlan(invocation.Plan)
	switch invocation.Plan.Command {
	case nodes.JobCommandStart:
		record, startErr := manager.Start(invocation.Plan)
		if startErr != nil {
			return nil, jobCommandFailure(startErr)
		}
		return jobStartOutputFromRecord(record), nil
	case nodes.JobCommandStatus:
		input, inputErr := decodeJobIDInput(invocation.Input)
		if inputErr != nil {
			return nil, jobCommandFailure(inputErr)
		}
		record, statusErr := manager.Status(owner, input.JobID)
		if statusErr != nil {
			return nil, jobCommandFailure(statusErr)
		}
		return jobStatusOutputFromRecord(record), nil
	case nodes.JobCommandLogs:
		input, inputErr := decodeJobLogsInput(invocation.Input)
		if inputErr != nil {
			return nil, jobCommandFailure(inputErr)
		}
		chunk, readErr := manager.ReadLog(
			owner,
			input.JobID,
			input.Stream == "stderr",
			input.Cursor,
			input.LimitBytes+utf8.UTFMax-1,
		)
		if readErr != nil {
			return nil, jobCommandFailure(readErr)
		}
		return fitJobLogOutput(input, chunk, invocation.OutputLimitBytes)
	case nodes.JobCommandArtifacts:
		input, inputErr := decodeJobIDInput(invocation.Input)
		if inputErr != nil {
			return nil, jobCommandFailure(inputErr)
		}
		record, statusErr := manager.Status(owner, input.JobID)
		if statusErr != nil {
			return nil, jobCommandFailure(statusErr)
		}
		artifacts, artifactsErr := manager.Artifacts(owner, input.JobID)
		if artifactsErr != nil {
			return nil, jobCommandFailure(artifactsErr)
		}
		return jobArtifactsOutputFromRecord(record, artifacts), nil
	case nodes.JobCommandCancel:
		input, inputErr := decodeJobIDInput(invocation.Input)
		if inputErr != nil {
			return nil, jobCommandFailure(inputErr)
		}
		record, cancelErr := manager.Cancel(owner, input.JobID)
		if cancelErr != nil {
			return nil, jobCommandFailure(cancelErr)
		}
		return jobCancelOutputFromRecord(record), nil
	default:
		return nil, ErrCommandUnavailable
	}
}

type jobIDInput struct {
	JobID string `json:"job_id"`
}

type jobLogsInput struct {
	JobID      string `json:"job_id"`
	Stream     string `json:"stream"`
	Cursor     int64  `json:"cursor"`
	LimitBytes int    `json:"limit_bytes"`
}

func decodeJobIDInput(raw json.RawMessage) (jobIDInput, error) {
	var input jobIDInput
	if err := decodeStrictJSON(raw, &input); err != nil || !validJobID(input.JobID) {
		return jobIDInput{}, errors.New("invalid node job identifier")
	}
	return input, nil
}

func decodeJobLogsInput(raw json.RawMessage) (jobLogsInput, error) {
	var input jobLogsInput
	if err := decodeStrictJSON(raw, &input); err != nil || !validJobID(input.JobID) ||
		(input.Stream != "stdout" && input.Stream != "stderr") ||
		input.Cursor < 0 || input.Cursor > nodes.MaxJobLogBytes ||
		input.LimitBytes < 1 || input.LimitBytes > nodes.MaxJobLogChunkBytes {
		return jobLogsInput{}, errors.New("invalid node job log request")
	}
	return input, nil
}

func validJobID(value string) bool {
	return len(value) == 36 && strings.HasPrefix(value, "job_") && nodes.ID(value).Validate() == nil
}

func jobOwnerFromPlan(plan nodes.ExecutionPlan) JobOwner {
	return JobOwner{AgentID: plan.AgentID, SessionID: plan.SessionID, ActorID: plan.ActorID}
}

func jobCommandFailure(err error) error {
	switch {
	case errors.Is(err, ErrJobNotFound):
		return newCommandFailure("JOB_NOT_FOUND", "node job not found", err)
	case errors.Is(err, ErrJobBusy):
		return newCommandFailure("JOB_BUSY", "node job concurrency limit reached", err)
	case errors.Is(err, ErrJobStoreFull):
		return newCommandFailure("JOB_STORE_FULL", "node job store is full", err)
	case errors.Is(err, ErrJobConflict):
		return newCommandFailure("JOB_CONFLICT", "node job conflicts with durable state", err)
	case errors.Is(err, ErrCommandUnavailable), errors.Is(err, nodes.ErrCommandDenied):
		return newCommandFailure("COMMAND_DENIED", "node job command denied", err)
	default:
		return newCommandFailure("JOB_OPERATION_FAILED", "node job operation failed", err)
	}
}

type jobStartOutput struct {
	JobID           string `json:"job_id"`
	State           string `json:"state"`
	CreatedAt       int64  `json:"created_at"`
	StartedAt       int64  `json:"started_at"`
	TimeoutAt       int64  `json:"timeout_at"`
	CancelGuarantee string `json:"cancel_guarantee"`
}

func jobStartOutputFromRecord(record JobRecord) jobStartOutput {
	return jobStartOutput{
		JobID: record.JobID, State: string(record.State), CreatedAt: jobUnix(record.CreatedAt),
		StartedAt: jobUnix(record.StartedAt), TimeoutAt: jobUnix(record.TimeoutAt),
		CancelGuarantee: string(record.CancelGuarantee),
	}
}

type jobStatusOutput struct {
	JobID              string `json:"job_id"`
	State              string `json:"state"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
	LaunchAttemptedAt  int64  `json:"launch_attempted_at"`
	StartedAt          int64  `json:"started_at"`
	TimeoutAt          int64  `json:"timeout_at"`
	CompletedAt        int64  `json:"completed_at"`
	ExitCode           int    `json:"exit_code"`
	ExitCodeKnown      bool   `json:"exit_code_known"`
	Signal             string `json:"signal"`
	FailureCode        string `json:"failure_code"`
	CancelRequestedAt  int64  `json:"cancel_requested_at"`
	CancellationSignal bool   `json:"cancellation_signal"`
	StdoutBytes        int64  `json:"stdout_bytes"`
	StderrBytes        int64  `json:"stderr_bytes"`
	StdoutTruncated    bool   `json:"stdout_truncated"`
	StderrTruncated    bool   `json:"stderr_truncated"`
	ArtifactCount      int    `json:"artifact_count"`
}

func jobStatusOutputFromRecord(record JobRecord) jobStatusOutput {
	exitCode := 0
	if record.ExitCode != nil {
		exitCode = *record.ExitCode
	}
	return jobStatusOutput{
		JobID: record.JobID, State: string(record.State), CreatedAt: jobUnix(record.CreatedAt),
		UpdatedAt: jobUnix(record.UpdatedAt), LaunchAttemptedAt: jobUnix(record.LaunchAttemptedAt),
		StartedAt: jobUnix(record.StartedAt), TimeoutAt: jobUnix(record.TimeoutAt),
		CompletedAt: jobUnix(record.CompletedAt), ExitCode: exitCode, ExitCodeKnown: record.ExitCode != nil,
		Signal: record.Signal, FailureCode: record.FailureCode,
		CancelRequestedAt: jobUnix(record.CancelRequestedAt), CancellationSignal: record.CancellationSignal,
		StdoutBytes: record.Stdout.Bytes, StderrBytes: record.Stderr.Bytes,
		StdoutTruncated: record.Stdout.Truncated, StderrTruncated: record.Stderr.Truncated,
		ArtifactCount: len(record.Artifacts),
	}
}

type jobLogOutput struct {
	JobID      string `json:"job_id"`
	Stream     string `json:"stream"`
	Data       string `json:"data"`
	NextCursor int64  `json:"next_cursor"`
	Available  int64  `json:"available_bytes"`
	Truncated  bool   `json:"truncated"`
	State      string `json:"state"`
}

func fitJobLogOutput(input jobLogsInput, chunk JobLogChunk, limit int) (jobLogOutput, error) {
	pageEnd := jobLogPageEnd(chunk.Data, input.LimitBytes, chunk.State.terminal())
	boundaries := jobLogRuneBoundaries(chunk.Data[:pageEnd])
	fit := func(length int) (jobLogOutput, int) {
		output := jobLogOutput{
			JobID: input.JobID, Stream: input.Stream, Data: safeJobLogString(chunk.Data[:length]),
			NextCursor: input.Cursor + int64(length), Available: chunk.Available,
			Truncated: chunk.Truncated, State: string(chunk.State),
		}
		encoded, _ := json.Marshal(output)
		return output, len(encoded)
	}
	low, high := 0, len(boundaries)-1
	for low < high {
		middle := low + (high-low+1)/2
		_, size := fit(boundaries[middle])
		if size <= limit {
			low = middle
		} else {
			high = middle - 1
		}
	}
	output, size := fit(boundaries[low])
	if size > limit {
		return jobLogOutput{}, newCommandFailure(
			"OUTPUT_LIMIT_TOO_SMALL",
			"node job log output limit is too small",
			errors.New("node job log envelope exceeds output limit"),
		)
	}
	if pageEnd > 0 && boundaries[low] == 0 {
		return jobLogOutput{}, newCommandFailure(
			"OUTPUT_LIMIT_TOO_SMALL",
			"node job log output limit is too small",
			errors.New("node job log output cannot fit one complete rune"),
		)
	}
	return output, nil
}

func jobLogPageEnd(data []byte, requested int, terminal bool) int {
	if requested <= 0 || len(data) == 0 {
		return 0
	}
	previous := 0
	for offset := 0; offset < len(data); {
		if !terminal && !utf8.FullRune(data[offset:]) {
			return previous
		}
		_, size := utf8.DecodeRune(data[offset:])
		if size <= 0 {
			size = 1
		}
		next := offset + size
		if next > requested {
			if previous == 0 {
				return next
			}
			return previous
		}
		previous = next
		offset = next
		if next == requested {
			return next
		}
	}
	return previous
}

func jobLogRuneBoundaries(data []byte) []int {
	boundaries := []int{0}
	for offset := 0; offset < len(data); {
		_, size := utf8.DecodeRune(data[offset:])
		if size <= 0 {
			size = 1
		}
		offset += size
		boundaries = append(boundaries, offset)
	}
	return boundaries
}

func safeJobLogString(data []byte) string {
	var result strings.Builder
	result.Grow(len(data))
	for len(data) > 0 {
		runeValue, size := utf8.DecodeRune(data)
		if runeValue == utf8.RuneError && size == 1 {
			result.WriteRune(utf8.RuneError)
			data = data[1:]
			continue
		}
		result.Write(data[:size])
		data = data[size:]
	}
	return result.String()
}

type jobArtifactOutput struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	ArtifactRef string `json:"artifact_ref"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	FailureCode string `json:"failure_code"`
}

type jobArtifactsOutput struct {
	JobID     string              `json:"job_id"`
	State     string              `json:"state"`
	Artifacts []jobArtifactOutput `json:"artifacts"`
}

func jobArtifactsOutputFromRecord(record JobRecord, artifacts []JobArtifactRecord) jobArtifactsOutput {
	output := jobArtifactsOutput{
		JobID: record.JobID, State: string(record.State), Artifacts: make([]jobArtifactOutput, 0, len(artifacts)),
	}
	for _, artifact := range artifacts {
		output.Artifacts = append(output.Artifacts, jobArtifactOutput{
			Name: artifact.Name, State: string(artifact.State), ArtifactRef: artifact.ArtifactRef,
			Size: artifact.Size, SHA256: artifact.SHA256, FailureCode: artifact.FailureCode,
		})
	}
	return output
}

type jobCancelOutput struct {
	JobID              string `json:"job_id"`
	State              string `json:"state"`
	Disposition        string `json:"disposition"`
	CancelRequestedAt  int64  `json:"cancel_requested_at"`
	CancellationSignal bool   `json:"cancellation_signal"`
	FailureCode        string `json:"failure_code"`
}

func jobCancelOutputFromRecord(record JobRecord) jobCancelOutput {
	disposition := "cancel_requested"
	switch record.State {
	case JobCanceled:
		disposition = "canceled"
	case JobCancelUnknown:
		disposition = "cancel_unknown"
	default:
		if record.State.terminal() && record.CancelRequestedAt == 0 {
			disposition = "already_terminal"
		}
	}
	return jobCancelOutput{
		JobID: record.JobID, State: string(record.State), Disposition: disposition,
		CancelRequestedAt:  jobUnix(record.CancelRequestedAt),
		CancellationSignal: record.CancellationSignal, FailureCode: record.FailureCode,
	}
}

func jobUnix(nanoseconds int64) int64 {
	if nanoseconds <= 0 {
		return 0
	}
	return time.Unix(0, nanoseconds).Unix()
}

func ensureJobOutputFits(
	descriptor nodes.CommandDescriptor,
	value any,
	limit int,
) error {
	raw, err := json.Marshal(value)
	if err == nil {
		_, err = nodes.ValidateInvocationOutputForProtocol(nodes.ProtocolV2, descriptor, raw, limit)
	}
	if err != nil {
		return newCommandFailure(
			"OUTPUT_LIMIT_TOO_SMALL",
			"node job output limit is too small",
			err,
		)
	}
	return nil
}

func cloneJobCommandDescriptor(descriptor nodes.CommandDescriptor) nodes.CommandDescriptor {
	return cloneCatalog(nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{descriptor}}).Commands[0]
}
