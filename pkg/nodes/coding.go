package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

const (
	CodingCommandScopes     = "coding.scopes.v5"
	CodingCommandTaskStart  = "coding.task.start.v5"
	CodingCommandTaskStatus = "coding.task.status.v5"
	CodingCommandTaskSteer  = "coding.task.steer.v5"
	CodingCommandTaskCancel = "coding.task.cancel.v5"

	MaxCodingScopes        = 64
	MaxCodingTaskTextBytes = 128 << 10
	// JSON can encode one accepted byte as a six-byte Unicode escape. The
	// fixed allowance covers either transport-only content wrapper.
	MaxCodingEphemeralInputBytes = MaxCodingTaskTextBytes*6 + 256
	MinCodingTaskOutputBytes     = 64 << 10
	MaxCodingQuestionOptions     = 32
	MaxCodingQuestionTextBytes   = 8 << 10
)

var ErrInvalidCodingCommand = errors.New("invalid coding command")

type CodingTaskStartInput struct {
	TaskID             string              `json:"task_id"`
	TaskGenerationID   string              `json:"task_generation_id"`
	ScopeAlias         string              `json:"scope_alias"`
	ScopeRevision      string              `json:"scope_revision"`
	Profile            codingtask.TaskMode `json:"profile"`
	RequestDigest      string              `json:"request_digest"`
	ObjectiveBytes     int                 `json:"objective_bytes"`
	DoneCriteriaBytes  int                 `json:"done_criteria_bytes"`
	TurnIdempotencyKey string              `json:"turn_idempotency_key"`
}

type CodingTaskStartEphemeralInput struct {
	Objective    string `json:"objective"`
	DoneCriteria string `json:"done_criteria"`
}

func NewCodingTaskStartInputs(
	taskID string,
	taskGenerationID string,
	scopeAlias string,
	scopeRevision string,
	profile codingtask.TaskMode,
	objective string,
	doneCriteria string,
	turnIdempotencyKey string,
) (CodingTaskStartInput, CodingTaskStartEphemeralInput, error) {
	ephemeral := CodingTaskStartEphemeralInput{Objective: objective, DoneCriteria: doneCriteria}
	request := codingtask.NewStartRequest(
		taskID,
		taskGenerationID,
		scopeAlias,
		scopeRevision,
		profile,
		objective,
		doneCriteria,
		turnIdempotencyKey,
	)
	input := CodingTaskStartInput{
		TaskID: taskID, TaskGenerationID: taskGenerationID,
		ScopeAlias: scopeAlias, ScopeRevision: scopeRevision, Profile: profile,
		RequestDigest: request.RequestDigest, ObjectiveBytes: len(objective),
		DoneCriteriaBytes: len(doneCriteria), TurnIdempotencyKey: turnIdempotencyKey,
	}
	if _, err := input.Bind(ephemeral); err != nil {
		return CodingTaskStartInput{}, CodingTaskStartEphemeralInput{}, err
	}
	return input, ephemeral, nil
}

func (input CodingTaskStartInput) Validate() error {
	if !codingtask.ValidIdentifier(input.TaskID) || !codingtask.ValidIdentifier(input.TaskGenerationID) ||
		!codingtask.ValidAlias(input.ScopeAlias) || !codingtask.ValidRevision(input.ScopeRevision) ||
		!input.Profile.AdmittedInV5() || !validSHA256Digest(input.RequestDigest) ||
		!codingtask.ValidIdentifier(input.TurnIdempotencyKey) || input.ObjectiveBytes < 1 ||
		input.ObjectiveBytes > MaxCodingTaskTextBytes || input.DoneCriteriaBytes < 0 ||
		input.DoneCriteriaBytes > MaxCodingTaskTextBytes ||
		codingTaskPromptBytes(input.ObjectiveBytes, input.DoneCriteriaBytes) > MaxCodingTaskTextBytes {
		return fmt.Errorf("%w: malformed start authority", ErrInvalidCodingCommand)
	}
	return nil
}

func (input CodingTaskStartInput) Bind(
	ephemeral CodingTaskStartEphemeralInput,
) (codingtask.StartRequest, error) {
	if err := input.Validate(); err != nil || len(ephemeral.Objective) != input.ObjectiveBytes ||
		len(ephemeral.DoneCriteria) != input.DoneCriteriaBytes {
		return codingtask.StartRequest{}, fmt.Errorf(
			"%w: start content does not match durable authority",
			ErrInvalidCodingCommand,
		)
	}
	request := codingtask.NewStartRequest(
		input.TaskID,
		input.TaskGenerationID,
		input.ScopeAlias,
		input.ScopeRevision,
		input.Profile,
		ephemeral.Objective,
		ephemeral.DoneCriteria,
		input.TurnIdempotencyKey,
	)
	if request.Validate() != nil || request.RequestDigest != input.RequestDigest ||
		len(request.Prompt()) > MaxCodingTaskTextBytes {
		return codingtask.StartRequest{}, fmt.Errorf("%w: start content digest mismatch", ErrInvalidCodingCommand)
	}
	return request, nil
}

type CodingTaskIdentityInput struct {
	TaskID           string `json:"task_id"`
	TaskGenerationID string `json:"task_generation_id"`
}

func (input CodingTaskIdentityInput) Validate() error {
	if !codingtask.ValidIdentifier(input.TaskID) || !codingtask.ValidIdentifier(input.TaskGenerationID) {
		return fmt.Errorf("%w: malformed task identity", ErrInvalidCodingCommand)
	}
	return nil
}

type CodingQuestionAnswer struct {
	QuestionID       string `json:"question_id"`
	QuestionRevision uint64 `json:"question_revision"`
	AnswerID         string `json:"answer_id"`
}

func (answer CodingQuestionAnswer) Validate() error {
	if !codingtask.ValidIdentifier(answer.QuestionID) || answer.QuestionRevision == 0 ||
		!codingtask.ValidIdentifier(answer.AnswerID) {
		return fmt.Errorf("%w: malformed question answer", ErrInvalidCodingCommand)
	}
	return nil
}

type CodingTaskSteerInput struct {
	TaskID             string                `json:"task_id"`
	TaskGenerationID   string                `json:"task_generation_id"`
	WorkerGenerationID string                `json:"worker_generation_id"`
	TurnIdempotencyKey string                `json:"turn_idempotency_key"`
	TextDigest         string                `json:"text_digest"`
	TextBytes          int                   `json:"text_bytes"`
	QuestionAnswer     *CodingQuestionAnswer `json:"question_answer,omitempty"`
}

type CodingTaskSteerEphemeralInput struct {
	Text string `json:"text"`
}

func NewCodingTaskSteerInputs(
	taskID string,
	taskGenerationID string,
	workerGenerationID string,
	turnIdempotencyKey string,
	text string,
	answer *CodingQuestionAnswer,
) (CodingTaskSteerInput, CodingTaskSteerEphemeralInput, error) {
	var clonedAnswer *CodingQuestionAnswer
	if answer != nil {
		value := *answer
		clonedAnswer = &value
	}
	input := CodingTaskSteerInput{
		TaskID: taskID, TaskGenerationID: taskGenerationID,
		WorkerGenerationID: workerGenerationID, TurnIdempotencyKey: turnIdempotencyKey,
		TextDigest: CodingTaskTextDigest(text), TextBytes: len(text), QuestionAnswer: clonedAnswer,
	}
	ephemeral := CodingTaskSteerEphemeralInput{Text: text}
	if _, err := input.Bind(ephemeral); err != nil {
		return CodingTaskSteerInput{}, CodingTaskSteerEphemeralInput{}, err
	}
	return input, ephemeral, nil
}

func (input CodingTaskSteerInput) Validate() error {
	if err := (CodingTaskIdentityInput{
		TaskID: input.TaskID, TaskGenerationID: input.TaskGenerationID,
	}).Validate(); err != nil {
		return err
	}
	if !codingtask.ValidIdentifier(input.WorkerGenerationID) ||
		!codingtask.ValidIdentifier(input.TurnIdempotencyKey) ||
		!validSHA256Digest(input.TextDigest) || input.TextBytes < 1 ||
		input.TextBytes > MaxCodingTaskTextBytes {
		return fmt.Errorf("%w: malformed steer authority", ErrInvalidCodingCommand)
	}
	if input.QuestionAnswer != nil {
		if err := input.QuestionAnswer.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (input CodingTaskSteerInput) Bind(ephemeral CodingTaskSteerEphemeralInput) (string, error) {
	if err := input.Validate(); err != nil || len(ephemeral.Text) != input.TextBytes ||
		CodingTaskTextDigest(ephemeral.Text) != input.TextDigest {
		return "", fmt.Errorf("%w: steer content does not match durable authority", ErrInvalidCodingCommand)
	}
	request := codingtask.NewResumeRequest(
		input.TaskID,
		input.TaskGenerationID,
		input.WorkerGenerationID,
		ephemeral.Text,
		input.TurnIdempotencyKey,
	)
	if request.Validate() != nil || len(ephemeral.Text) > MaxCodingTaskTextBytes {
		return "", fmt.Errorf("%w: invalid steer content", ErrInvalidCodingCommand)
	}
	return ephemeral.Text, nil
}

type CodingTaskCancelInput struct {
	TaskID               string `json:"task_id"`
	TaskGenerationID     string `json:"task_generation_id"`
	WorkerGenerationID   string `json:"worker_generation_id"`
	CancelIdempotencyKey string `json:"cancel_idempotency_key"`
}

func (input CodingTaskCancelInput) Validate() error {
	if err := (CodingTaskIdentityInput{
		TaskID: input.TaskID, TaskGenerationID: input.TaskGenerationID,
	}).Validate(); err != nil {
		return err
	}
	if !codingtask.ValidIdentifier(input.WorkerGenerationID) ||
		!codingtask.ValidIdentifier(input.CancelIdempotencyKey) {
		return fmt.Errorf("%w: malformed cancel authority", ErrInvalidCodingCommand)
	}
	return nil
}

type CodingScopeResult struct {
	Alias           string                `json:"alias"`
	Revision        string                `json:"revision"`
	Kind            codingscope.Kind      `json:"kind"`
	AllowedProfiles []codingtask.TaskMode `json:"allowed_profiles"`
	Available       bool                  `json:"available"`
	Busy            bool                  `json:"busy"`
}

type CodingScopesResult struct {
	Scopes []CodingScopeResult `json:"scopes"`
}

type CodingQuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type CodingQuestionResult struct {
	QuestionID string                 `json:"question_id"`
	Revision   uint64                 `json:"revision"`
	Prompt     string                 `json:"prompt"`
	Options    []CodingQuestionOption `json:"options"`
}

type CodingTaskResult struct {
	TaskID             string                     `json:"task_id"`
	TaskGenerationID   string                     `json:"task_generation_id"`
	ScopeAlias         string                     `json:"scope_alias"`
	ScopeRevision      string                     `json:"scope_revision"`
	Profile            codingtask.TaskMode        `json:"profile"`
	ThreadID           string                     `json:"thread_id"`
	ThreadOpenMode     codingtask.ThreadOpenMode  `json:"thread_open_mode"`
	WorkerGenerationID string                     `json:"worker_generation_id"`
	ResumeSequence     uint64                     `json:"resume_sequence"`
	State              codingtask.State           `json:"state"`
	Revision           uint64                     `json:"revision"`
	Activity           codingtask.Activity        `json:"activity"`
	WorktreeID         string                     `json:"worktree_id"`
	Branch             string                     `json:"branch"`
	HandoffID          string                     `json:"handoff_id"`
	FailureCode        string                     `json:"failure_code"`
	AcceptedAt         int64                      `json:"accepted_at"`
	UpdatedAt          int64                      `json:"updated_at"`
	RetainUntil        int64                      `json:"retain_until"`
	Question           *CodingQuestionResult      `json:"question,omitempty"`
	QuestionTruncated  bool                       `json:"question_truncated"`
	TerminalReport     *codingtask.TerminalReport `json:"terminal_report,omitempty"`
}

func IsCodingCommand(name string) bool {
	switch name {
	case CodingCommandScopes, CodingCommandTaskStart, CodingCommandTaskStatus,
		CodingCommandTaskSteer, CodingCommandTaskCancel:
		return true
	default:
		return false
	}
}

func CodingCommandDescriptors() ([]CommandDescriptor, error) {
	commands := []string{
		CodingCommandScopes,
		CodingCommandTaskStart,
		CodingCommandTaskStatus,
		CodingCommandTaskSteer,
		CodingCommandTaskCancel,
	}
	descriptors := make([]CommandDescriptor, 0, len(commands))
	for _, command := range commands {
		risk := RiskRead
		if command == CodingCommandTaskStart || command == CodingCommandTaskSteer ||
			command == CodingCommandTaskCancel {
			risk = RiskWrite
		}
		descriptor := CommandDescriptor{
			Name: command, InputSchema: CodingCommandInputSchema(command),
			OutputSchema: CodingCommandOutputSchema(command), Risk: risk,
		}
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func (descriptor CommandDescriptor) validateCodingCommand() error {
	if !IsCodingCommand(descriptor.Name) {
		return nil
	}
	wantRisk := RiskRead
	if descriptor.Name == CodingCommandTaskStart || descriptor.Name == CodingCommandTaskSteer ||
		descriptor.Name == CodingCommandTaskCancel {
		wantRisk = RiskWrite
	}
	if descriptor.Risk != wantRisk || descriptor.ModelContract != nil || descriptor.SupportsCancel ||
		descriptor.SupportsProgress || len(descriptor.FileProfiles) != 0 ||
		len(descriptor.ServiceProfiles) != 0 || len(descriptor.BrowserProfiles) != 0 ||
		len(descriptor.UpdateProfiles) != 0 || len(descriptor.JobProfiles) != 0 {
		return fmt.Errorf("%w: malformed internal coding command behavior", ErrInvalidCapability)
	}
	wantInput, err := canonicalJSON(CodingCommandInputSchema(descriptor.Name))
	if err != nil {
		return err
	}
	actualInput, err := canonicalJSON(descriptor.InputSchema)
	if err != nil || !jsonBytesEqual(actualInput, wantInput) {
		return fmt.Errorf("%w: coding command input schema is not canonical", ErrInvalidCapability)
	}
	wantOutput, err := canonicalJSON(CodingCommandOutputSchema(descriptor.Name))
	if err != nil {
		return err
	}
	actualOutput, err := canonicalJSON(descriptor.OutputSchema)
	if err != nil || !jsonBytesEqual(actualOutput, wantOutput) {
		return fmt.Errorf("%w: coding command output schema is not canonical", ErrInvalidCapability)
	}
	return nil
}

func CodingCommandInputSchema(command string) json.RawMessage {
	identity := map[string]any{
		"task_id":            codingIdentifierSchema(),
		"task_generation_id": codingIdentifierSchema(),
	}
	switch command {
	case CodingCommandScopes:
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{},
		})
	case CodingCommandTaskStart:
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{
				"task_id", "task_generation_id", "scope_alias", "scope_revision", "profile",
				"request_digest", "objective_bytes", "done_criteria_bytes", "turn_idempotency_key",
			},
			"properties": map[string]any{
				"task_id": codingIdentifierSchema(), "task_generation_id": codingIdentifierSchema(),
				"scope_alias": codingAliasSchema(), "scope_revision": codingIdentifierSchema(),
				"profile": codingProfileSchema(), "request_digest": codingDigestSchema(),
				"objective_bytes": map[string]any{
					"type": "integer", "minimum": 1, "maximum": MaxCodingTaskTextBytes,
				},
				"done_criteria_bytes": map[string]any{
					"type": "integer", "minimum": 0, "maximum": MaxCodingTaskTextBytes,
				},
				"turn_idempotency_key": codingIdentifierSchema(),
			},
		})
	case CodingCommandTaskStatus:
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"task_id", "task_generation_id"}, "properties": identity,
		})
	case CodingCommandTaskSteer:
		properties := map[string]any{
			"task_id": codingIdentifierSchema(), "task_generation_id": codingIdentifierSchema(),
			"worker_generation_id": codingIdentifierSchema(),
			"turn_idempotency_key": codingIdentifierSchema(), "text_digest": codingDigestSchema(),
			"text_bytes": map[string]any{
				"type": "integer", "minimum": 1, "maximum": MaxCodingTaskTextBytes,
			},
			"question_answer": codingQuestionAnswerSchema(),
		}
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{
				"task_id", "task_generation_id", "worker_generation_id", "turn_idempotency_key",
				"text_digest", "text_bytes",
			},
			"properties": properties,
		})
	case CodingCommandTaskCancel:
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{
				"task_id", "task_generation_id", "worker_generation_id", "cancel_idempotency_key",
			},
			"properties": map[string]any{
				"task_id": codingIdentifierSchema(), "task_generation_id": codingIdentifierSchema(),
				"worker_generation_id":   codingIdentifierSchema(),
				"cancel_idempotency_key": codingIdentifierSchema(),
			},
		})
	default:
		return json.RawMessage("false")
	}
}

func CodingCommandOutputSchema(command string) json.RawMessage {
	if command == CodingCommandScopes {
		return mustCodingSchema(map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"scopes"},
			"properties": map[string]any{
				"scopes": map[string]any{
					"type": "array", "maxItems": MaxCodingScopes,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{
							"alias", "revision", "kind", "allowed_profiles", "available", "busy",
						},
						"properties": map[string]any{
							"alias": codingAliasSchema(), "revision": codingIdentifierSchema(),
							"kind": map[string]any{
								"type": "string", "enum": []string{"git_project", "machine"},
							},
							"allowed_profiles": map[string]any{
								"type": "array", "minItems": 1, "maxItems": 3,
								"uniqueItems": true, "items": codingProfileSchema(),
							},
							"available": map[string]any{"type": "boolean"},
							"busy":      map[string]any{"type": "boolean"},
						},
					},
				},
			},
		})
	}
	if command != CodingCommandTaskStart && command != CodingCommandTaskStatus &&
		command != CodingCommandTaskSteer && command != CodingCommandTaskCancel {
		return json.RawMessage("false")
	}
	return mustCodingSchema(codingTaskResultSchema())
}

func codingTaskResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"task_id", "task_generation_id", "scope_alias", "scope_revision", "profile", "thread_id",
			"thread_open_mode", "worker_generation_id", "resume_sequence", "state", "revision", "activity",
			"worktree_id", "branch", "handoff_id", "failure_code", "accepted_at", "updated_at",
			"retain_until", "question_truncated",
		},
		"properties": map[string]any{
			"task_id": codingIdentifierSchema(), "task_generation_id": codingIdentifierSchema(),
			"scope_alias": codingAliasSchema(), "scope_revision": codingIdentifierSchema(),
			"profile": codingProfileSchema(),
			"thread_id": map[string]any{
				"type": "string", "minLength": 36, "maxLength": 36,
				"pattern": `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
			},
			"thread_open_mode":     map[string]any{"type": "string", "enum": []string{"new", "resume"}},
			"worker_generation_id": codingIdentifierSchema(),
			"resume_sequence":      map[string]any{"type": "integer", "minimum": 0},
			"state": map[string]any{"type": "string", "enum": []string{
				"accepted", "preparing", "running", "waiting_for_input", "idle", "completed", "failed",
				"canceled", "uncertain",
			}},
			"revision": map[string]any{"type": "integer", "minimum": 1},
			"activity": map[string]any{"type": "string", "enum": []string{
				"", "idle", "running", "interrupting", "compacting", "reviewing", "waiting_for_input", "failed",
			}},
			"worktree_id": map[string]any{"type": "string", "maxLength": codingtask.MaxIDBytes},
			"branch":      map[string]any{"type": "string", "maxLength": codingtask.MaxBranchBytes},
			"handoff_id":  map[string]any{"type": "string", "maxLength": 64},
			"failure_code": map[string]any{
				"type": "string", "maxLength": codingtask.MaxFailureCodeBytes,
			},
			"accepted_at": map[string]any{"type": "integer", "minimum": 1},
			"updated_at":  map[string]any{"type": "integer", "minimum": 1},
			"retain_until": map[string]any{
				"type": "integer", "minimum": 0,
			},
			"question":           codingQuestionResultSchema(),
			"question_truncated": map[string]any{"type": "boolean"},
			"terminal_report":    codingTerminalReportSchema(),
		},
	}
}

func codingTerminalReportSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "maxLength": codingtask.MaxTerminalSummaryBytes},
			"changed_paths": map[string]any{
				"type": "array", "maxItems": codingtask.MaxTerminalPaths,
				"uniqueItems": true,
				"items":       map[string]any{"type": "string", "maxLength": codingtask.MaxPathBytes},
			},
			"validations": map[string]any{
				"type": "array", "maxItems": codingtask.MaxTerminalValidations,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"kind", "status"},
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []string{"command"}},
						"status": map[string]any{
							"type": "string", "enum": []string{"succeeded", "failed", "canceled", "timed_out"},
						},
					},
				},
			},
			"external_effects": map[string]any{
				"type": "array", "maxItems": codingtask.MaxTerminalEffects,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"kind", "outcome", "reference"},
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []string{
							"commit", "push", "pull_request", "repository", "release", "deployment",
							"package", "process", "service",
						}},
						"outcome": map[string]any{
							"type": "string", "enum": []string{"verified", "failed", "uncertain"},
						},
						"reference": map[string]any{
							"type": "string", "minLength": 1,
							"maxLength": codingtask.MaxEffectReferenceBytes,
						},
					},
				},
			},
			"privilege": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"backend", "profile", "profile_revision", "usage", "commands", "outcome"},
				"properties": map[string]any{
					"backend": map[string]any{
						"type": "string", "enum": []string{"authority-broker"},
					},
					"profile": map[string]any{
						"type": "string", "minLength": 1, "maxLength": codingtask.MaxRevisionBytes,
					},
					"profile_revision": map[string]any{
						"type": "string", "minLength": 1, "maxLength": codingtask.MaxRevisionBytes,
					},
					"usage": map[string]any{
						"type": "string", "enum": []string{
							codingtask.PrivilegeUsageUnused,
							codingtask.PrivilegeUsageObserved,
							codingtask.PrivilegeUsageUncertain,
						},
					},
					"commands": map[string]any{
						"type": "integer", "minimum": 0, "maximum": codingtask.MaxTerminalValidations,
					},
					"outcome": map[string]any{
						"type": "string", "enum": []string{
							codingtask.PrivilegeOutcomeNone,
							codingtask.PrivilegeOutcomeSucceeded,
							codingtask.PrivilegeOutcomeFailed,
							codingtask.PrivilegeOutcomeCanceled,
							codingtask.PrivilegeOutcomeTimedOut,
							codingtask.PrivilegeOutcomeMixed,
							codingtask.PrivilegeOutcomeUncertain,
						},
					},
				},
			},
			"commit":        map[string]any{"type": "string", "maxLength": codingtask.MaxRevisionBytes},
			"cleanup_state": map[string]any{"type": "string", "maxLength": codingtask.MaxRevisionBytes},
			"rollback_state": map[string]any{
				"type": "string", "enum": []string{codingtask.RollbackNotApplicable, codingtask.RollbackUnavailable},
			},
			"unresolved":            map[string]any{"type": "string", "maxLength": codingtask.MaxFailureMessageBytes},
			"paths_truncated":       map[string]any{"type": "boolean"},
			"validations_truncated": map[string]any{"type": "boolean"},
			"effects_truncated":     map[string]any{"type": "boolean"},
			"summary_truncated":     map[string]any{"type": "boolean"},
		},
	}
}

func codingQuestionAnswerSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"question_id", "question_revision", "answer_id"},
		"properties": map[string]any{
			"question_id": codingIdentifierSchema(),
			"question_revision": map[string]any{
				"type": "integer", "minimum": 1,
			},
			"answer_id": codingIdentifierSchema(),
		},
	}
}

func codingQuestionResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"question_id", "revision", "prompt", "options"},
		"properties": map[string]any{
			"question_id": codingIdentifierSchema(),
			"revision":    map[string]any{"type": "integer", "minimum": 1},
			"prompt": map[string]any{
				"type": "string", "minLength": 1, "maxLength": MaxCodingQuestionTextBytes,
			},
			"options": map[string]any{
				"type": "array", "maxItems": MaxCodingQuestionOptions,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"id", "label", "description"},
					"properties": map[string]any{
						"id": codingIdentifierSchema(),
						"label": map[string]any{
							"type": "string", "minLength": 1, "maxLength": codingtask.MaxQuestionLabelBytes,
						},
						"description": map[string]any{
							"type": "string", "maxLength": MaxCodingQuestionTextBytes,
						},
					},
				},
			},
		},
	}
}

func codingIdentifierSchema() map[string]any {
	return map[string]any{
		"type": "string", "minLength": 1, "maxLength": codingtask.MaxIDBytes,
		"pattern": `^[A-Za-z0-9][A-Za-z0-9._:-]*$`,
	}
}

func codingAliasSchema() map[string]any {
	return map[string]any{
		"type": "string", "minLength": 1, "maxLength": codingtask.MaxAliasBytes,
		"pattern": `^[a-z][a-z0-9_-]*$`,
	}
}

func codingDigestSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": `^[0-9a-f]{64}$`}
}

func codingProfileSchema() map[string]any {
	return map[string]any{
		"type": "string", "enum": []string{
			"investigate", "mutate", "project-yolo", "machine-yolo", "machine-yolo-root",
		},
	}
}

func mustCodingSchema(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("false")
	}
	return encoded
}

func jsonBytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func CodingTaskTextDigest(text string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("mintclaw:coding-task-text:v1\x00"))
	_, _ = fmt.Fprintf(digest, "%d:", len(text))
	_, _ = digest.Write([]byte(text))
	return hex.EncodeToString(digest.Sum(nil))
}

func codingTaskPromptBytes(objectiveBytes, doneCriteriaBytes int) int {
	if doneCriteriaBytes == 0 {
		return objectiveBytes
	}
	return objectiveBytes + len("\n\nDone criteria:\n") + doneCriteriaBytes
}
