// Package task defines the bounded node-local identity and lifecycle
// projection for one channel-originated native coding task. It does not own a
// store: the accepted Node Companion invocation ledger persists Record.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
)

const (
	SchemaVersion           = 1
	MaxAliasBytes           = 64
	MaxRevisionBytes        = 128
	MaxStatusBytes          = 4 << 10
	MaxFailureCodeBytes     = 64
	MaxFailureMessageBytes  = 512
	MaxBranchBytes          = 512
	MaxRetainDuration       = 30 * 24 * time.Hour
	MaxIDBytes              = 128
	MaxBuildIDBytes         = 256
	MaxModelIDBytes         = 256
	MaxPromptBytes          = 1 << 20
	MaxQuestionOptions      = 32
	MaxQuestionTextBytes    = 8 << 10
	MaxQuestionLabelBytes   = 255
	MaxPathBytes            = 32 << 10
	MaxTerminalSummaryBytes = 16 << 10
	MaxTerminalReportBytes  = 48 << 10
	MaxTerminalPaths        = 256
	MaxTerminalValidations  = 64
)

var (
	ErrInvalidRequest = errors.New("invalid coding task request")
	ErrInvalidRecord  = errors.New("invalid coding task record")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	aliasPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	buildIDPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	failurePattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

type TaskMode string

const (
	TaskModeInvestigate TaskMode = "investigate"
	TaskModeMutate      TaskMode = "mutate"
)

func (mode TaskMode) Valid() bool {
	return mode == TaskModeInvestigate || mode == TaskModeMutate
}

type ThreadOpenMode string

const (
	ThreadOpenNew    ThreadOpenMode = "new"
	ThreadOpenResume ThreadOpenMode = "resume"
)

func (mode ThreadOpenMode) Valid() bool {
	return mode == ThreadOpenNew || mode == ThreadOpenResume
}

type Activity string

const (
	ActivityIdle         Activity = "idle"
	ActivityRunning      Activity = "running"
	ActivityInterrupting Activity = "interrupting"
	ActivityCompacting   Activity = "compacting"
	ActivityReviewing    Activity = "reviewing"
	ActivityWaitingInput Activity = "waiting_for_input"
	ActivityFailed       Activity = "failed"
)

type QuestionStatus string

const (
	QuestionWaiting  QuestionStatus = "waiting"
	QuestionAnswered QuestionStatus = "answered"
	QuestionCanceled QuestionStatus = "canceled"
)

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type QuestionState struct {
	QuestionID string           `json:"question_id"`
	Revision   uint64           `json:"revision"`
	Status     QuestionStatus   `json:"status"`
	Prompt     string           `json:"prompt"`
	Options    []QuestionOption `json:"options,omitempty"`
}

func (question QuestionState) Validate() error {
	if !ValidIdentifier(question.QuestionID) || question.Revision == 0 ||
		(question.Status != QuestionWaiting && question.Status != QuestionAnswered &&
			question.Status != QuestionCanceled) ||
		!validText(question.Prompt, MaxQuestionTextBytes, true) ||
		len(question.Options) > MaxQuestionOptions {
		return fmt.Errorf("%w: malformed coding question state", ErrInvalidRecord)
	}
	seen := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		if !ValidIdentifier(option.ID) || !validText(option.Label, MaxQuestionLabelBytes, true) ||
			!validText(option.Description, MaxQuestionTextBytes, false) {
			return fmt.Errorf("%w: malformed coding question option", ErrInvalidRecord)
		}
		if _, duplicate := seen[option.ID]; duplicate {
			return fmt.Errorf("%w: duplicate coding question option", ErrInvalidRecord)
		}
		seen[option.ID] = struct{}{}
	}
	return nil
}

// State is a projection of worker and repository evidence. It is not the
// CodingThread catalog status and does not replace transcript authority.
type State string

const (
	StateAccepted     State = "accepted"
	StatePreparing    State = "preparing"
	StateRunning      State = "running"
	StateWaitingInput State = "waiting_for_input"
	StateIdle         State = "idle"
	StateCompleted    State = "completed"
	StateFailed       State = "failed"
	StateCanceled     State = "canceled"
	StateUncertain    State = "uncertain"
)

func (state State) Valid() bool {
	switch state {
	case StateAccepted, StatePreparing, StateRunning, StateWaitingInput,
		StateIdle, StateCompleted, StateFailed, StateCanceled, StateUncertain:
		return true
	default:
		return false
	}
}

func (state State) Terminal() bool {
	return state == StateCompleted || state == StateFailed || state == StateCanceled || state == StateUncertain
}

func (state State) CanTransitionTo(next State) bool {
	if state == next {
		return !state.Terminal()
	}
	switch state {
	case StateAccepted:
		return next == StatePreparing || next == StateFailed || next == StateCanceled || next == StateUncertain
	case StatePreparing:
		return next == StateRunning || next == StateFailed || next == StateCanceled || next == StateUncertain
	case StateRunning:
		return next == StateWaitingInput || next == StateIdle || next.Terminal()
	case StateWaitingInput:
		return next == StateRunning || next == StateIdle || next.Terminal()
	case StateIdle:
		return next == StateRunning || next.Terminal()
	default:
		return false
	}
}

// Failure is a stable, redacted task-host failure. It must not contain paths,
// provider payloads, prompts, or repository content.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationOutcome is deliberately structural. Command text, arguments,
// logs, environment, and provider payloads never enter the durable report.
type ValidationOutcome struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// TerminalReport is the bounded channel-safe projection produced node-locally
// from worker presentation and worktree evidence. It is not a transcript and
// contains no repository roots, full diffs, command text, or reasoning.
type TerminalReport struct {
	Summary              string              `json:"summary,omitempty"`
	ChangedPaths         []string            `json:"changed_paths,omitempty"`
	Validations          []ValidationOutcome `json:"validations,omitempty"`
	Commit               string              `json:"commit,omitempty"`
	CleanupState         string              `json:"cleanup_state,omitempty"`
	Unresolved           string              `json:"unresolved,omitempty"`
	PathsTruncated       bool                `json:"paths_truncated,omitempty"`
	ValidationsTruncated bool                `json:"validations_truncated,omitempty"`
	SummaryTruncated     bool                `json:"summary_truncated,omitempty"`
}

func (report TerminalReport) Validate() error {
	if !validText(report.Summary, MaxTerminalSummaryBytes, false) ||
		len(report.ChangedPaths) > MaxTerminalPaths ||
		len(report.Validations) > MaxTerminalValidations ||
		!validStructuralText(report.Commit, MaxRevisionBytes, false) ||
		!validStructuralText(report.CleanupState, MaxRevisionBytes, false) ||
		!validStructuralText(report.Unresolved, MaxFailureMessageBytes, false) {
		return fmt.Errorf("%w: malformed terminal report", ErrInvalidRecord)
	}
	seen := make(map[string]struct{}, len(report.ChangedPaths))
	for _, path := range report.ChangedPaths {
		if path == "" || len(path) > MaxPathBytes || !filepath.IsLocal(path) ||
			path != filepath.Clean(path) || strings.ContainsAny(path, "\r\n\t") {
			return fmt.Errorf("%w: terminal report contains invalid relative path", ErrInvalidRecord)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("%w: terminal report contains duplicate path", ErrInvalidRecord)
		}
		seen[path] = struct{}{}
	}
	for _, validation := range report.Validations {
		if validation.Kind != "command" ||
			(validation.Status != "succeeded" && validation.Status != "failed" &&
				validation.Status != "canceled" && validation.Status != "timed_out") {
			return fmt.Errorf("%w: terminal report contains invalid validation", ErrInvalidRecord)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > MaxTerminalReportBytes {
		return fmt.Errorf("%w: terminal report exceeds its encoded byte budget", ErrInvalidRecord)
	}
	return nil
}

func (failure Failure) Validate() error {
	if len(failure.Code) > MaxFailureCodeBytes || !failurePattern.MatchString(failure.Code) ||
		!validStructuralText(failure.Message, MaxFailureMessageBytes, true) {
		return fmt.Errorf("%w: malformed failure", ErrInvalidRecord)
	}
	return nil
}

// StartRequest contains the bounded content accepted from the gateway. Only
// its digest and immutable identities are retained after worker admission.
type StartRequest struct {
	TaskID             string   `json:"task_id"`
	TaskGenerationID   string   `json:"task_generation_id"`
	ProjectAlias       string   `json:"project_alias"`
	ProjectRevision    string   `json:"project_revision"`
	Mode               TaskMode `json:"mode"`
	Objective          string   `json:"objective"`
	DoneCriteria       string   `json:"done_criteria,omitempty"`
	RequestDigest      string   `json:"request_digest"`
	TurnIdempotencyKey string   `json:"turn_idempotency_key"`
}

// ResumeRequest is one explicit successor turn for an idle retained task. The
// text is used only to start that turn; durable task state retains its digest
// and idempotency identity, never the text itself.
type ResumeRequest struct {
	TaskID                     string `json:"task_id"`
	TaskGenerationID           string `json:"task_generation_id"`
	PreviousWorkerGenerationID string `json:"previous_worker_generation_id"`
	Text                       string `json:"text"`
	RequestDigest              string `json:"request_digest"`
	TurnIdempotencyKey         string `json:"turn_idempotency_key"`
}

func NewResumeRequest(
	taskID string,
	taskGenerationID string,
	previousWorkerGeneration string,
	text string,
	turnIdempotencyKey string,
) ResumeRequest {
	request := ResumeRequest{
		TaskID: taskID, TaskGenerationID: taskGenerationID,
		PreviousWorkerGenerationID: previousWorkerGeneration,
		Text:                       text, TurnIdempotencyKey: turnIdempotencyKey,
	}
	request.RequestDigest = resumeRequestDigest(request)
	return request
}

func (request ResumeRequest) Validate() error {
	if !ValidIdentifier(request.TaskID) || !ValidIdentifier(request.TaskGenerationID) ||
		!ValidIdentifier(request.PreviousWorkerGenerationID) || !ValidIdentifier(request.TurnIdempotencyKey) {
		return fmt.Errorf("%w: malformed resume identity or idempotency key", ErrInvalidRequest)
	}
	if err := validatePrompt(request.Text); err != nil {
		return fmt.Errorf("%w: resume text: %w", ErrInvalidRequest, err)
	}
	if !digestPattern.MatchString(request.RequestDigest) || request.RequestDigest != resumeRequestDigest(request) {
		return fmt.Errorf("%w: resume request digest mismatch", ErrInvalidRequest)
	}
	return nil
}

func NewStartRequest(
	taskID string,
	taskGenerationID string,
	projectAlias string,
	projectRevision string,
	mode TaskMode,
	objective string,
	doneCriteria string,
	turnIdempotencyKey string,
) StartRequest {
	request := StartRequest{
		TaskID: taskID, TaskGenerationID: taskGenerationID,
		ProjectAlias: projectAlias, ProjectRevision: projectRevision, Mode: mode,
		Objective: objective, DoneCriteria: doneCriteria, TurnIdempotencyKey: turnIdempotencyKey,
	}
	request.RequestDigest = requestDigest(request)
	return request
}

func (request StartRequest) Validate() error {
	if !ValidIdentifier(request.TaskID) || !ValidIdentifier(request.TaskGenerationID) ||
		!ValidAlias(request.ProjectAlias) || !ValidRevision(request.ProjectRevision) ||
		!request.Mode.Valid() || !ValidIdentifier(request.TurnIdempotencyKey) {
		return fmt.Errorf("%w: malformed identity, project, mode, or idempotency key", ErrInvalidRequest)
	}
	if err := validatePrompt(request.Objective); err != nil {
		return fmt.Errorf("%w: objective: %w", ErrInvalidRequest, err)
	}
	if request.DoneCriteria != "" {
		if err := validatePrompt(request.DoneCriteria); err != nil {
			return fmt.Errorf("%w: done criteria: %w", ErrInvalidRequest, err)
		}
	}
	if err := validatePrompt(request.Prompt()); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if !digestPattern.MatchString(request.RequestDigest) || request.RequestDigest != requestDigest(request) {
		return fmt.Errorf("%w: request digest mismatch", ErrInvalidRequest)
	}
	return nil
}

func (request StartRequest) Prompt() string {
	if request.DoneCriteria == "" {
		return request.Objective
	}
	return request.Objective + "\n\nDone criteria:\n" + request.DoneCriteria
}

func requestDigest(request StartRequest) string {
	digest := sha256.New()
	for _, value := range []string{
		request.TaskID,
		request.TaskGenerationID,
		request.ProjectAlias,
		request.ProjectRevision,
		string(request.Mode),
		request.Objective,
		request.DoneCriteria,
		request.TurnIdempotencyKey,
	} {
		_, _ = fmt.Fprintf(digest, "%d:%s\n", len(value), value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func resumeRequestDigest(request ResumeRequest) string {
	digest := sha256.New()
	for _, value := range []string{
		request.TaskID,
		request.TaskGenerationID,
		request.PreviousWorkerGenerationID,
		request.Text,
		request.TurnIdempotencyKey,
	} {
		_, _ = fmt.Fprintf(digest, "%d:%s\n", len(value), value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// Record is the durable bounded projection embedded in the accepted start
// invocation. Objective text is deliberately absent to prevent restart replay.
type Record struct {
	SchemaVersion         int                     `json:"schema_version"`
	InvocationID          string                  `json:"invocation_id"`
	RequestDigest         string                  `json:"request_digest"`
	TaskID                string                  `json:"task_id"`
	TaskGenerationID      string                  `json:"task_generation_id"`
	ProjectAlias          string                  `json:"project_alias"`
	ProjectRevision       string                  `json:"project_revision"`
	Mode                  TaskMode                `json:"mode"`
	ThreadID              string                  `json:"thread_id"`
	ThreadOpenMode        ThreadOpenMode          `json:"thread_open_mode"`
	WorkerGenerationID    string                  `json:"worker_generation_id"`
	ResumeSequence        uint64                  `json:"resume_sequence,omitempty"`
	ResumeRequestDigest   string                  `json:"resume_request_digest,omitempty"`
	ResumeIdempotencyKey  string                  `json:"resume_idempotency_key,omitempty"`
	Project               project.ProjectIdentity `json:"project"`
	ExecutionRoot         string                  `json:"execution_root,omitempty"`
	ExecutionRootIdentity string                  `json:"execution_root_identity,omitempty"`
	WorktreeID            string                  `json:"worktree_id,omitempty"`
	ProviderProfile       string                  `json:"provider_profile"`
	Model                 string                  `json:"model"`
	Provider              string                  `json:"provider"`
	ExpectedWorkerBuildID string                  `json:"expected_worker_build_id"`
	State                 State                   `json:"state"`
	Revision              uint64                  `json:"revision"`
	Activity              Activity                `json:"activity,omitempty"`
	Status                string                  `json:"status,omitempty"`
	Question              *QuestionState          `json:"question,omitempty"`
	HandoffID             string                  `json:"handoff_id,omitempty"`
	Branch                string                  `json:"branch,omitempty"`
	Failure               *Failure                `json:"failure,omitempty"`
	TerminalReport        *TerminalReport         `json:"terminal_report,omitempty"`
	AcceptedAt            int64                   `json:"accepted_at"`
	UpdatedAt             int64                   `json:"updated_at"`
	RetainUntil           int64                   `json:"retain_until,omitempty"`
}

// Binding is the lightweight immutable worker authority retained by the node
// host. The native worker adapter converts it to the private worker protocol;
// this package does not import the in-process agent runtime.
type Binding struct {
	TaskID                string                  `json:"task_id"`
	TaskGenerationID      string                  `json:"task_generation_id"`
	WorkerGenerationID    string                  `json:"worker_generation_id"`
	ThreadID              string                  `json:"thread_id"`
	ThreadOpenMode        ThreadOpenMode          `json:"thread_open_mode"`
	Project               project.ProjectIdentity `json:"project"`
	ExecutionRoot         string                  `json:"execution_root"`
	ExecutionRootIdentity string                  `json:"execution_root_identity"`
	Mode                  TaskMode                `json:"mode"`
	ProviderProfile       string                  `json:"provider_profile"`
	Model                 string                  `json:"model"`
	Provider              string                  `json:"provider"`
	ExpectedWorkerBuildID string                  `json:"expected_worker_build_id"`
}

func (binding Binding) Validate() error {
	if !ValidIdentifier(binding.TaskID) || !ValidIdentifier(binding.TaskGenerationID) ||
		!ValidIdentifier(binding.WorkerGenerationID) || !validUUID(binding.ThreadID) ||
		!binding.ThreadOpenMode.Valid() || binding.Project.Validate() != nil || !binding.Mode.Valid() ||
		!ValidIdentifier(binding.ProviderProfile) ||
		!validStructuralText(binding.Model, MaxModelIDBytes, true) ||
		!ValidIdentifier(binding.Provider) ||
		!buildIDPattern.MatchString(binding.ExpectedWorkerBuildID) {
		return fmt.Errorf("%w: malformed worker binding", ErrInvalidRecord)
	}
	if !validPath(binding.ExecutionRoot) ||
		!validPath(binding.Project.ProjectRoot) ||
		binding.ExecutionRootIdentity != ExecutionRootIdentity(binding.ExecutionRoot) {
		return fmt.Errorf("%w: malformed worker execution root", ErrInvalidRecord)
	}
	if binding.Mode == TaskModeInvestigate && binding.ExecutionRoot != binding.Project.ProjectRoot {
		return fmt.Errorf("%w: investigation escaped its source project", ErrInvalidRecord)
	}
	if binding.Mode == TaskModeMutate &&
		!validMutationExecutionRoot(binding.Project.ProjectRoot, binding.ExecutionRoot) {
		return fmt.Errorf("%w: mutation execution root is not isolated", ErrInvalidRecord)
	}
	return nil
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || !ValidIdentifier(record.InvocationID) ||
		!digestPattern.MatchString(record.RequestDigest) || !ValidIdentifier(record.TaskID) ||
		!ValidIdentifier(record.TaskGenerationID) || !ValidAlias(record.ProjectAlias) ||
		!ValidRevision(record.ProjectRevision) || !record.Mode.Valid() ||
		!validUUID(record.ThreadID) || !record.ThreadOpenMode.Valid() ||
		!ValidIdentifier(record.WorkerGenerationID) || record.Project.Validate() != nil ||
		!ValidIdentifier(record.ProviderProfile) ||
		!validStructuralText(record.Model, MaxModelIDBytes, true) ||
		!ValidIdentifier(record.Provider) ||
		!buildIDPattern.MatchString(record.ExpectedWorkerBuildID) ||
		!record.State.Valid() || record.Revision == 0 || record.AcceptedAt <= 0 ||
		record.UpdatedAt < record.AcceptedAt {
		return fmt.Errorf("%w: malformed identity, authority, lifecycle, or timestamp", ErrInvalidRecord)
	}
	if !validText(record.Status, MaxStatusBytes, false) ||
		!validStructuralText(record.HandoffID, MaxRevisionBytes, false) ||
		(record.Branch != "" && !ValidBranch(record.Branch)) ||
		(record.HandoffID != "" && !digestPattern.MatchString(record.HandoffID)) {
		return fmt.Errorf("%w: malformed bounded projection", ErrInvalidRecord)
	}
	if err := record.validateExecution(); err != nil {
		return err
	}
	if !validActivity(record.Activity) || !validStateActivity(record.State, record.Activity) {
		return fmt.Errorf("%w: malformed worker activity", ErrInvalidRecord)
	}
	if record.ResumeSequence == 0 {
		if record.ThreadOpenMode != ThreadOpenNew || record.ResumeRequestDigest != "" ||
			record.ResumeIdempotencyKey != "" {
			return fmt.Errorf("%w: malformed initial worker generation", ErrInvalidRecord)
		}
	} else if record.ThreadOpenMode != ThreadOpenResume ||
		!digestPattern.MatchString(record.ResumeRequestDigest) ||
		!ValidIdentifier(record.ResumeIdempotencyKey) {
		return fmt.Errorf("%w: malformed successor worker generation", ErrInvalidRecord)
	}
	if record.Question != nil {
		if record.State != StateWaitingInput || record.Question.Status != QuestionWaiting ||
			record.Question.Validate() != nil {
			return fmt.Errorf("%w: question is not a valid waiting projection", ErrInvalidRecord)
		}
	}
	if record.State == StateWaitingInput &&
		(record.Question == nil || record.Activity != ActivityWaitingInput) {
		return fmt.Errorf("%w: waiting state lacks a matching question", ErrInvalidRecord)
	}
	if record.State.Terminal() {
		if record.RetainUntil < record.UpdatedAt {
			return fmt.Errorf("%w: terminal task lacks a retention boundary", ErrInvalidRecord)
		}
		if record.RetainUntil-record.UpdatedAt > int64(MaxRetainDuration) {
			return fmt.Errorf("%w: terminal task retention exceeds its bound", ErrInvalidRecord)
		}
	} else if record.RetainUntil != 0 || record.Failure != nil {
		return fmt.Errorf("%w: nonterminal task contains terminal metadata", ErrInvalidRecord)
	}
	if record.State == StateFailed || record.State == StateUncertain {
		if record.Failure == nil || record.Failure.Validate() != nil {
			return fmt.Errorf("%w: failure state lacks a valid failure", ErrInvalidRecord)
		}
	} else if record.Failure != nil {
		return fmt.Errorf("%w: non-failure state contains failure", ErrInvalidRecord)
	}
	if record.TerminalReport != nil {
		if !record.State.Terminal() || record.TerminalReport.Validate() != nil {
			return fmt.Errorf("%w: terminal report does not match lifecycle", ErrInvalidRecord)
		}
	}
	return nil
}

func (record Record) validateExecution() error {
	switch record.Mode {
	case TaskModeInvestigate:
		if !validPath(record.Project.ProjectRoot) || !validPath(record.ExecutionRoot) ||
			record.WorktreeID != "" || record.ExecutionRoot != record.Project.ProjectRoot ||
			record.ExecutionRootIdentity != ExecutionRootIdentity(record.ExecutionRoot) ||
			record.HandoffID != "" || record.Branch != "" {
			return fmt.Errorf("%w: investigation escaped its source project", ErrInvalidRecord)
		}
	case TaskModeMutate:
		if record.WorktreeID != WorktreeIDForThread(record.ThreadID) {
			return fmt.Errorf("%w: mutation worktree does not match its thread", ErrInvalidRecord)
		}
		if record.ExecutionRoot == "" {
			if record.State != StateAccepted && record.State != StatePreparing && record.State != StateFailed &&
				record.State != StateCanceled && record.State != StateUncertain || record.ExecutionRootIdentity != "" ||
				record.HandoffID != "" || record.Branch != "" {
				return fmt.Errorf("%w: unprepared mutation contains execution evidence", ErrInvalidRecord)
			}
			return nil
		}
		if !validMutationExecutionRoot(record.Project.ProjectRoot, record.ExecutionRoot) ||
			record.ExecutionRootIdentity != ExecutionRootIdentity(record.ExecutionRoot) {
			return fmt.Errorf("%w: mutation execution root is not isolated", ErrInvalidRecord)
		}
		if record.HandoffID != "" && !record.State.Terminal() && record.State != StateIdle {
			return fmt.Errorf("%w: live mutation contains terminal handoff evidence", ErrInvalidRecord)
		}
		if record.State == StateCompleted && (record.HandoffID == "" || record.Branch == "") {
			return fmt.Errorf("%w: completed mutation lacks handoff evidence", ErrInvalidRecord)
		}
	default:
		return fmt.Errorf("%w: unsupported mode", ErrInvalidRecord)
	}
	return nil
}

func (record Record) WorkerBinding() (Binding, error) {
	if err := record.Validate(); err != nil {
		return Binding{}, err
	}
	if record.ExecutionRoot == "" {
		return Binding{}, fmt.Errorf("%w: execution root is not prepared", ErrInvalidRecord)
	}
	binding := Binding{
		TaskID: record.TaskID, TaskGenerationID: record.TaskGenerationID,
		WorkerGenerationID: record.WorkerGenerationID, ThreadID: record.ThreadID,
		ThreadOpenMode: record.ThreadOpenMode, Project: record.Project,
		ExecutionRoot: record.ExecutionRoot, ExecutionRootIdentity: record.ExecutionRootIdentity,
		Mode: record.Mode, ProviderProfile: record.ProviderProfile, Model: record.Model,
		Provider: record.Provider, ExpectedWorkerBuildID: record.ExpectedWorkerBuildID,
	}
	if err := binding.Validate(); err != nil {
		return Binding{}, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	return binding, nil
}

func (record Record) MatchesRequest(request StartRequest) bool {
	return request.Validate() == nil && record.TaskID == request.TaskID &&
		record.TaskGenerationID == request.TaskGenerationID &&
		record.ProjectAlias == request.ProjectAlias && record.ProjectRevision == request.ProjectRevision &&
		record.Mode == request.Mode && record.RequestDigest == request.RequestDigest
}

func (record Record) MatchesResumeRequest(request ResumeRequest) bool {
	return request.Validate() == nil && record.ResumeSequence > 0 &&
		record.TaskID == request.TaskID && record.TaskGenerationID == request.TaskGenerationID &&
		record.ResumeRequestDigest == request.RequestDigest &&
		record.ResumeIdempotencyKey == request.TurnIdempotencyKey
}

func (record Record) SameIdentity(other Record) bool {
	return record.SchemaVersion == other.SchemaVersion && record.InvocationID == other.InvocationID &&
		record.RequestDigest == other.RequestDigest && record.TaskID == other.TaskID &&
		record.TaskGenerationID == other.TaskGenerationID && record.ProjectAlias == other.ProjectAlias &&
		record.ProjectRevision == other.ProjectRevision && record.Mode == other.Mode &&
		record.ThreadID == other.ThreadID && record.ThreadOpenMode == other.ThreadOpenMode &&
		record.WorkerGenerationID == other.WorkerGenerationID &&
		record.ResumeSequence == other.ResumeSequence &&
		record.ResumeRequestDigest == other.ResumeRequestDigest &&
		record.ResumeIdempotencyKey == other.ResumeIdempotencyKey && record.Project == other.Project &&
		record.WorktreeID == other.WorktreeID && record.ProviderProfile == other.ProviderProfile &&
		record.Model == other.Model && record.Provider == other.Provider &&
		record.ExpectedWorkerBuildID == other.ExpectedWorkerBuildID && record.AcceptedAt == other.AcceptedAt
}

func (record Record) Clone() Record {
	cloned := record
	if record.Question != nil {
		question := *record.Question
		question.Options = append([]QuestionOption(nil), record.Question.Options...)
		cloned.Question = &question
	}
	if record.Failure != nil {
		failure := *record.Failure
		cloned.Failure = &failure
	}
	if record.TerminalReport != nil {
		report := *record.TerminalReport
		report.ChangedPaths = append([]string(nil), record.TerminalReport.ChangedPaths...)
		report.Validations = append([]ValidationOutcome(nil), record.TerminalReport.Validations...)
		cloned.TerminalReport = &report
	}
	return cloned
}

func ValidAlias(value string) bool {
	return len(value) <= MaxAliasBytes && aliasPattern.MatchString(value)
}

func ValidIdentifier(value string) bool {
	return len(value) <= MaxIDBytes && identifierPattern.MatchString(value)
}

func ValidRevision(value string) bool {
	return len(value) <= MaxRevisionBytes && identifierPattern.MatchString(value)
}

// ValidBranch implements the branch-name subset accepted by
// `git check-ref-format --branch`, with case-insensitive .lock rejection for
// portability across supported filesystems.
func ValidBranch(value string) bool {
	if value == "" || len(value) > MaxBranchBytes || !utf8.ValidString(value) ||
		value != strings.TrimSpace(value) || strings.HasPrefix(value, "-") || value == "HEAD" ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f || strings.ContainsRune("~^:?*[\\", character) {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(strings.ToLower(component), ".lock") {
			return false
		}
	}
	return true
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validatePrompt(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("coding task prompt is required")
	}
	if !utf8.ValidString(content) || len(content) > MaxPromptBytes || containsControl(content) {
		return fmt.Errorf("coding task prompt must be valid UTF-8 within %d bytes", MaxPromptBytes)
	}
	return nil
}

func validPath(value string) bool {
	return value != "" && len(value) <= MaxPathBytes && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\r\n\t") &&
		!containsControl(value) && supportedPathNamespace(value)
}

func supportedPathNamespace(value string) bool {
	slashed := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(slashed, "//?/") || strings.HasPrefix(slashed, "//./") ||
		strings.HasPrefix(slashed, "/??/") || strings.HasPrefix(slashed, "//??/") {
		return false
	}
	for _, component := range strings.Split(slashed, "/") {
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return false
		}
	}
	return true
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validMutationExecutionRoot(sourceRoot string, executionRoot string) bool {
	return validPath(sourceRoot) && validPath(executionRoot) &&
		!pathWithin(sourceRoot, executionRoot) && !pathWithin(executionRoot, sourceRoot)
}

func ExecutionRootIdentity(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(digest[:])
}

func WorktreeIDForThread(threadID string) string {
	return "wt-" + strings.ReplaceAll(strings.ToLower(threadID), "-", "")
}

func validActivity(activity Activity) bool {
	switch activity {
	case "", ActivityIdle, ActivityRunning, ActivityInterrupting,
		ActivityCompacting, ActivityReviewing, ActivityWaitingInput, ActivityFailed:
		return true
	default:
		return false
	}
}

func validStateActivity(state State, activity Activity) bool {
	switch state {
	case StateAccepted, StatePreparing:
		return activity == ""
	case StateRunning:
		return activity == ActivityRunning || activity == ActivityInterrupting ||
			activity == ActivityCompacting || activity == ActivityReviewing
	case StateWaitingInput:
		return activity == ActivityWaitingInput
	case StateIdle:
		return activity == ActivityIdle
	case StateCompleted:
		return activity == "" || activity == ActivityIdle
	case StateFailed:
		return activity == "" || activity == ActivityFailed
	case StateCanceled:
		return activity == "" || activity == ActivityIdle || activity == ActivityInterrupting
	case StateUncertain:
		return activity == "" || activity == ActivityFailed
	default:
		return false
	}
}

func validText(value string, maximum int, required bool) bool {
	if len(value) > maximum || !utf8.ValidString(value) || containsControl(value) {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func validStructuralText(value string, maximum int, required bool) bool {
	return validText(value, maximum, required) && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\r\n\t")
}

func containsControl(value string) bool {
	for _, character := range value {
		if character == '\n' || character == '\r' || character == '\t' {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
