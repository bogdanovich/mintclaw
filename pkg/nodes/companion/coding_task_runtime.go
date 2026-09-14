package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	maxCodingQuestionOutputPromptBytes = 4 << 10
	maxCodingQuestionOutputLabelBytes  = 96
)

type codingCommandHandler struct {
	host            *CodingTaskHost
	command         string
	descriptorValue nodes.CommandDescriptor
}

func newCodingCommandHandlers(
	host *CodingTaskHost,
	policy nodes.LocalCommandPolicy,
) ([]commandHandler, error) {
	if host == nil || host.catalog == nil || host.ledger == nil {
		return nil, errors.New("node coding task host is required")
	}
	if len(host.catalog.List()) == 0 {
		return nil, nil
	}
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		return nil, err
	}
	handlers := make([]commandHandler, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !slices.Contains(policy.AllowedCommands, descriptor.Name) ||
			modelRiskRank(descriptor.Risk) > modelRiskRank(policy.MaximumRisk) ||
			policy.MaxOutputBytes < nodes.MinCodingTaskOutputBytes {
			continue
		}
		handlers = append(handlers, &codingCommandHandler{
			host: host, command: descriptor.Name, descriptorValue: descriptor,
		})
	}
	return handlers, nil
}

func (handler *codingCommandHandler) descriptor() nodes.CommandDescriptor {
	return cloneCatalog(nodes.CapabilityCatalog{
		Commands: []nodes.CommandDescriptor{handler.descriptorValue},
	}).Commands[0]
}

func (handler *codingCommandHandler) authorize(plan nodes.ExecutionPlan) error {
	if plan.OutputLimitBytes < nodes.MinCodingTaskOutputBytes {
		return fmt.Errorf("%w: coding output limit is too small", nodes.ErrCommandDenied)
	}
	switch handler.command {
	case nodes.CodingCommandProjects:
		if err := decodeCodingProjectsInput(plan.Input); err != nil {
			return nodes.ErrCommandDenied
		}
	case nodes.CodingCommandTaskStart:
		input, err := decodeCodingStartInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		if _, err = handler.host.catalog.resolve(
			context.Background(),
			input.ProjectAlias,
			input.ProjectRevision,
			input.Mode,
		); err != nil {
			return fmt.Errorf("%w: coding project authority unavailable", nodes.ErrCommandDenied)
		}
	case nodes.CodingCommandTaskStatus:
		input, err := decodeCodingIdentityInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		if _, err = handler.ownedTask(plan, input.TaskID, input.TaskGenerationID); err != nil {
			return nodes.ErrCommandDenied
		}
	case nodes.CodingCommandTaskSteer:
		input, err := decodeCodingSteerInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		record, err := handler.ownedTask(plan, input.TaskID, input.TaskGenerationID)
		if err != nil || record.WorkerGenerationID != input.WorkerGenerationID ||
			!codingAnswerMatches(record, input.QuestionAnswer) {
			return nodes.ErrCommandDenied
		}
	case nodes.CodingCommandTaskCancel:
		input, err := decodeCodingCancelInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		record, err := handler.ownedTask(plan, input.TaskID, input.TaskGenerationID)
		if err != nil || record.WorkerGenerationID != input.WorkerGenerationID {
			return nodes.ErrCommandDenied
		}
	default:
		return ErrCommandUnavailable
	}
	return nil
}

func (handler *codingCommandHandler) authorizeEphemeral(
	plan nodes.ExecutionPlan,
	ephemeralInput json.RawMessage,
) error {
	switch handler.command {
	case nodes.CodingCommandTaskStart:
		input, err := decodeCodingStartInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		ephemeral, err := decodeCodingStartEphemeralInput(ephemeralInput)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		if _, err = input.Bind(ephemeral); err != nil {
			return nodes.ErrCommandDenied
		}
	case nodes.CodingCommandTaskSteer:
		input, err := decodeCodingSteerInput(plan.Input)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		ephemeral, err := decodeCodingSteerEphemeralInput(ephemeralInput)
		if err != nil {
			return nodes.ErrCommandDenied
		}
		if _, err = input.Bind(ephemeral); err != nil {
			return nodes.ErrCommandDenied
		}
	default:
		if len(ephemeralInput) != 0 {
			return nodes.ErrCommandDenied
		}
	}
	return nil
}

func (handler *codingCommandHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, newCommandFailure("EXECUTION_CANCELED", "coding command canceled", err)
	}
	switch handler.command {
	case nodes.CodingCommandProjects:
		if err := decodeCodingProjectsInput(invocation.Input); err != nil {
			return nil, codingCommandFailure(err)
		}
		result := handler.projectResult()
		if err := ensureCodingOutputFits(handler.descriptorValue, result, invocation.OutputLimitBytes); err != nil {
			return nil, err
		}
		return result, nil
	case nodes.CodingCommandTaskStart:
		input, err := decodeCodingStartInput(invocation.Input)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		ephemeral, err := decodeCodingStartEphemeralInput(invocation.EphemeralInput)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		request, err := input.Bind(ephemeral)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		record, _, startErr := handler.host.Start(ctx, invocation.Plan.InvocationID, request)
		if startErr != nil {
			retained, statusErr := handler.host.Status(input.TaskID, input.TaskGenerationID)
			if statusErr != nil || retained.InvocationID != invocation.Plan.InvocationID {
				return nil, codingCommandFailure(startErr)
			}
			record = retained
		}
		return fitCodingTaskResult(handler.descriptorValue, record, invocation.OutputLimitBytes)
	case nodes.CodingCommandTaskStatus:
		input, err := decodeCodingIdentityInput(invocation.Input)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		record, err := handler.ownedTask(invocation.Plan, input.TaskID, input.TaskGenerationID)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		return fitCodingTaskResult(handler.descriptorValue, record, invocation.OutputLimitBytes)
	case nodes.CodingCommandTaskSteer:
		input, err := decodeCodingSteerInput(invocation.Input)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		ephemeral, err := decodeCodingSteerEphemeralInput(invocation.EphemeralInput)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		text, err := input.Bind(ephemeral)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		if _, err = handler.ownedTask(invocation.Plan, input.TaskID, input.TaskGenerationID); err != nil {
			return nil, codingCommandFailure(err)
		}
		var answer *worker.QuestionAnswerRef
		if input.QuestionAnswer != nil {
			answer = &worker.QuestionAnswerRef{
				QuestionID:       input.QuestionAnswer.QuestionID,
				QuestionRevision: input.QuestionAnswer.QuestionRevision,
				AnswerID:         input.QuestionAnswer.AnswerID,
			}
		}
		record, err := handler.host.Steer(
			ctx,
			input.TaskID,
			input.TaskGenerationID,
			input.WorkerGenerationID,
			input.TurnIdempotencyKey,
			text,
			answer,
		)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		return fitCodingTaskResult(handler.descriptorValue, record, invocation.OutputLimitBytes)
	case nodes.CodingCommandTaskCancel:
		input, err := decodeCodingCancelInput(invocation.Input)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		if _, err = handler.ownedTask(invocation.Plan, input.TaskID, input.TaskGenerationID); err != nil {
			return nil, codingCommandFailure(err)
		}
		record, err := handler.host.Cancel(
			ctx,
			input.TaskID,
			input.TaskGenerationID,
			input.WorkerGenerationID,
			input.CancelIdempotencyKey,
		)
		if err != nil {
			return nil, codingCommandFailure(err)
		}
		return fitCodingTaskResult(handler.descriptorValue, record, invocation.OutputLimitBytes)
	default:
		return nil, ErrCommandUnavailable
	}
}

func (handler *codingCommandHandler) ownedTask(
	plan nodes.ExecutionPlan,
	taskID string,
	generationID string,
) (codingtask.Record, error) {
	record, err := handler.host.Status(taskID, generationID)
	if err != nil {
		return codingtask.Record{}, err
	}
	invocation, found, err := handler.host.ledger.Lookup(record.InvocationID)
	if err != nil || !found || invocation.OwnerDigest == "" {
		return codingtask.Record{}, nodes.ErrCommandDenied
	}
	digest, err := nodes.InvocationOwnerDigest(plan.AgentID, plan.SessionID, plan.ActorID)
	if err != nil || digest != invocation.OwnerDigest {
		return codingtask.Record{}, nodes.ErrCommandDenied
	}
	return record, nil
}

func (handler *codingCommandHandler) projectResult() nodes.CodingProjectsResult {
	descriptors := handler.host.catalog.List()
	result := nodes.CodingProjectsResult{Projects: make([]nodes.CodingProjectResult, 0, len(descriptors))}
	handler.host.mu.Lock()
	closed := handler.host.closed
	active := make(map[string]int, len(handler.host.projectActive))
	for alias, count := range handler.host.projectActive {
		active[alias] = count
	}
	handler.host.mu.Unlock()
	for _, descriptor := range descriptors {
		busy := active[descriptor.Alias] >= descriptor.MaxConcurrentTasks
		result.Projects = append(result.Projects, nodes.CodingProjectResult{
			Alias: descriptor.Alias, Revision: descriptor.Revision,
			AllowedModes: append([]codingtask.TaskMode(nil), descriptor.AllowedModes...),
			Available:    !closed && !busy, Busy: busy,
		})
	}
	return result
}

func decodeCodingProjectsInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictJSON(raw, &input)
}

func decodeCodingStartInput(raw json.RawMessage) (nodes.CodingTaskStartInput, error) {
	var input nodes.CodingTaskStartInput
	if err := decodeStrictJSON(raw, &input); err != nil || input.Validate() != nil {
		return nodes.CodingTaskStartInput{}, nodes.ErrInvalidCodingCommand
	}
	return input, nil
}

func decodeCodingStartEphemeralInput(
	raw json.RawMessage,
) (nodes.CodingTaskStartEphemeralInput, error) {
	var wire struct {
		Objective    *string `json:"objective"`
		DoneCriteria *string `json:"done_criteria"`
	}
	if len(raw) == 0 || len(raw) > nodes.MaxCodingEphemeralInputBytes ||
		decodeStrictJSON(raw, &wire) != nil || wire.Objective == nil || wire.DoneCriteria == nil {
		return nodes.CodingTaskStartEphemeralInput{}, nodes.ErrInvalidCodingCommand
	}
	return nodes.CodingTaskStartEphemeralInput{
		Objective: *wire.Objective, DoneCriteria: *wire.DoneCriteria,
	}, nil
}

func decodeCodingIdentityInput(raw json.RawMessage) (nodes.CodingTaskIdentityInput, error) {
	var input nodes.CodingTaskIdentityInput
	if err := decodeStrictJSON(raw, &input); err != nil || input.Validate() != nil {
		return nodes.CodingTaskIdentityInput{}, nodes.ErrInvalidCodingCommand
	}
	return input, nil
}

func decodeCodingSteerInput(raw json.RawMessage) (nodes.CodingTaskSteerInput, error) {
	var input nodes.CodingTaskSteerInput
	if err := decodeStrictJSON(raw, &input); err != nil || input.Validate() != nil {
		return nodes.CodingTaskSteerInput{}, nodes.ErrInvalidCodingCommand
	}
	return input, nil
}

func decodeCodingSteerEphemeralInput(
	raw json.RawMessage,
) (nodes.CodingTaskSteerEphemeralInput, error) {
	var wire struct {
		Text *string `json:"text"`
	}
	if len(raw) == 0 || len(raw) > nodes.MaxCodingEphemeralInputBytes ||
		decodeStrictJSON(raw, &wire) != nil || wire.Text == nil {
		return nodes.CodingTaskSteerEphemeralInput{}, nodes.ErrInvalidCodingCommand
	}
	return nodes.CodingTaskSteerEphemeralInput{Text: *wire.Text}, nil
}

func decodeCodingCancelInput(raw json.RawMessage) (nodes.CodingTaskCancelInput, error) {
	var input nodes.CodingTaskCancelInput
	if err := decodeStrictJSON(raw, &input); err != nil || input.Validate() != nil {
		return nodes.CodingTaskCancelInput{}, nodes.ErrInvalidCodingCommand
	}
	return input, nil
}

func codingAnswerMatches(record codingtask.Record, answer *nodes.CodingQuestionAnswer) bool {
	if answer == nil {
		return true
	}
	if record.State != codingtask.StateWaitingInput || record.Question == nil ||
		record.Question.QuestionID != answer.QuestionID || record.Question.Revision != answer.QuestionRevision {
		return false
	}
	for _, option := range record.Question.Options {
		if option.ID == answer.AnswerID {
			return true
		}
	}
	return false
}

func codingTaskResult(record codingtask.Record, compactQuestion bool) nodes.CodingTaskResult {
	result := nodes.CodingTaskResult{
		TaskID: record.TaskID, TaskGenerationID: record.TaskGenerationID,
		ProjectAlias: record.ProjectAlias, ProjectRevision: record.ProjectRevision, Mode: record.Mode,
		ThreadID: record.ThreadID, ThreadOpenMode: record.ThreadOpenMode,
		WorkerGenerationID: record.WorkerGenerationID, ResumeSequence: record.ResumeSequence,
		State: record.State, Revision: record.Revision, Activity: record.Activity,
		WorktreeID: record.WorktreeID, Branch: record.Branch, HandoffID: record.HandoffID,
		AcceptedAt: record.AcceptedAt, UpdatedAt: record.UpdatedAt, RetainUntil: record.RetainUntil,
	}
	if record.Failure != nil {
		result.FailureCode = record.Failure.Code
	}
	if record.Question != nil {
		prompt := record.Question.Prompt
		if compactQuestion {
			prompt, _ = boundedCodingQuestionText(prompt, maxCodingQuestionOutputPromptBytes)
		}
		result.Question = &nodes.CodingQuestionResult{
			QuestionID: record.Question.QuestionID, Revision: record.Question.Revision,
			Prompt:  prompt,
			Options: make([]nodes.CodingQuestionOption, 0, len(record.Question.Options)),
		}
		for _, option := range record.Question.Options {
			label := option.Label
			description := option.Description
			if compactQuestion {
				label, _ = boundedCodingQuestionText(label, maxCodingQuestionOutputLabelBytes)
				description = ""
			}
			result.Question.Options = append(result.Question.Options, nodes.CodingQuestionOption{
				ID: option.ID, Label: label, Description: description,
			})
		}
		result.QuestionTruncated = compactQuestion
	}
	return result
}

func boundedCodingQuestionText(value string, maximum int) (string, bool) {
	if len(value) <= maximum {
		return value, false
	}
	const marker = "…"
	limit := maximum - len(marker)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + marker, true
}

func fitCodingTaskResult(
	descriptor nodes.CommandDescriptor,
	record codingtask.Record,
	limit int,
) (nodes.CodingTaskResult, error) {
	if record.Validate() != nil {
		return nodes.CodingTaskResult{}, newCommandFailure(
			"TASK_STATE_INVALID",
			"coding task state is unavailable",
			codingtask.ErrInvalidRecord,
		)
	}
	result := codingTaskResult(record, false)
	if ensureCodingOutputFits(descriptor, result, limit) == nil {
		return result, nil
	}
	if record.Question != nil {
		result = codingTaskResult(record, true)
		if ensureCodingOutputFits(descriptor, result, limit) == nil {
			return result, nil
		}
	}
	return nodes.CodingTaskResult{}, newCommandFailure(
		"OUTPUT_LIMIT_TOO_SMALL",
		"coding task output limit is too small",
		errors.New("coding task status exceeds output limit"),
	)
}

func ensureCodingOutputFits(descriptor nodes.CommandDescriptor, value any, limit int) error {
	raw, err := json.Marshal(value)
	if err == nil {
		_, err = nodes.ValidateInvocationOutputForProtocol(nodes.ProtocolV2, descriptor, raw, limit)
	}
	if err != nil {
		return newCommandFailure(
			"OUTPUT_LIMIT_TOO_SMALL",
			"coding command output limit is too small",
			err,
		)
	}
	return nil
}

func codingCommandFailure(err error) error {
	switch {
	case errors.Is(err, nodes.ErrInvalidCodingCommand), errors.Is(err, codingtask.ErrInvalidRequest),
		errors.Is(err, nodes.ErrCommandDenied):
		return newCommandFailure("COMMAND_DENIED", "coding command denied", err)
	case errors.Is(err, ErrCodingProjectNotFound):
		return newCommandFailure("PROJECT_NOT_FOUND", "coding project was not found", err)
	case errors.Is(err, ErrCodingProjectStale), errors.Is(err, ErrCodingProjectChanged):
		return newCommandFailure("PROJECT_STALE", "coding project authority is stale", err)
	case errors.Is(err, ErrCodingModeDenied):
		return newCommandFailure("MODE_DENIED", "coding task mode is denied", err)
	case errors.Is(err, ErrCodingTaskNotFound):
		return newCommandFailure("TASK_NOT_FOUND", "coding task was not found", err)
	case errors.Is(err, ErrCodingTaskBusy):
		return newCommandFailure("PROJECT_BUSY", "coding project is busy", err)
	case errors.Is(err, ErrCodingTaskConflict):
		return newCommandFailure("TASK_CONFLICT", "coding task conflicts with durable state", err)
	case errors.Is(err, ErrCodingTaskNotIdle):
		return newCommandFailure("TASK_NOT_RESUMABLE", "coding task is not resumable", err)
	case errors.Is(err, ErrCodingTaskNotRunning):
		return newCommandFailure("TASK_NOT_RUNNING", "coding task worker is not running", err)
	case errors.Is(err, ErrCodingTaskHostClosed):
		return newCommandFailure("HOST_UNAVAILABLE", "coding task host is unavailable", err)
	case errors.Is(err, ErrCodingTaskUnsettled), errors.Is(err, worker.ErrControlStreamUncertain):
		return fmt.Errorf("%w: coding task outcome is uncertain", ErrInvocationOutcomeUnknown)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return newCommandFailure("COMMAND_TIMEOUT", "coding command did not complete", err)
	default:
		return newCommandFailure("CODING_OPERATION_FAILED", "coding command failed", err)
	}
}
