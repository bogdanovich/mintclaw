package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	remoteCodingPollRunning = 10 * time.Second
	remoteCodingPollWaiting = 15 * time.Second
	remoteCodingPollOffline = 30 * time.Second
	remoteCodingQuestionTTL = 24 * time.Hour
)

type RemoteCodingInvoker interface {
	Invoke(
		context.Context,
		tools.CodingInvocationAuthority,
		string,
		string,
		any,
		any,
	) (json.RawMessage, error)
}

type RemoteCodingInvokerFactory func(*config.Config) (RemoteCodingInvoker, error)

type remoteCodingRuntime struct {
	loop    *AgentLoop
	factory RemoteCodingInvokerFactory

	mu      sync.Mutex
	started bool
	ctx     context.Context
	active  map[string]context.CancelFunc
	locks   sync.Map
}

// ConfigureRemoteCodingTaskRuntime installs the process-wide coordinator used
// by agent-scoped coding_task tools and restart reconciliation.
func (al *AgentLoop) ConfigureRemoteCodingTaskRuntime(factory RemoteCodingInvokerFactory) error {
	if al == nil || factory == nil {
		return errors.New("remote coding runtime requires an agent loop and invocation factory")
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.remoteCoding != nil {
		return nil
	}
	al.remoteCoding = &remoteCodingRuntime{
		loop: al, factory: factory, active: make(map[string]context.CancelFunc),
	}
	return nil
}

// NewRemoteCodingTaskTool returns the owner-scoped model surface. A nil tool
// keeps deny-by-default configurations out of model discovery unless the
// agent still owns active work that must remain inspectable and cancelable.
func (al *AgentLoop) NewRemoteCodingTaskTool(
	cfg *config.Config,
	agentID string,
) (toolshared.Tool, error) {
	if al == nil || al.remoteCoding == nil || cfg == nil ||
		(!cfg.HasRemoteCodingProjectForAgent(agentID) && !al.hasActiveRemoteCodingTask(agentID)) {
		return nil, nil
	}
	return &remoteCodingTool{runtime: al.remoteCoding, agentID: strings.TrimSpace(agentID)}, nil
}

func (al *AgentLoop) hasActiveRemoteCodingTask(agentID string) bool {
	if al == nil || al.GetRegistry() == nil {
		return false
	}
	agent, found := al.GetRegistry().GetAgent(strings.TrimSpace(agentID))
	if !found || agent == nil {
		return false
	}
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if tasks == nil {
		return false
	}
	for _, record := range tasks.ListActive() {
		if record.Runtime == taskregistry.RuntimeCoding && record.AgentID == strings.TrimSpace(agentID) {
			return true
		}
	}
	return false
}

type remoteCodingTool struct {
	runtime *remoteCodingRuntime
	agentID string
}

func (*remoteCodingTool) Name() string { return "coding_task" }

func (*remoteCodingTool) Description() string {
	return "Start, inspect, steer, or cancel one durable coding task on an operator-approved paired project. " +
		"Use investigate for read-only root-cause analysis and mutate for an isolated-worktree fix. " +
		"The tool accepts only configured project aliases; it never accepts paths, repositories, commands, " +
		"credentials, executables, or cleanup policy."
}

func (*remoteCodingTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "enum": []string{"start", "status", "steer", "cancel"},
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "Durable task ID returned by start; required for status, steer, and cancel.",
			},
			"project": map[string]any{
				"type": "string", "description": "Configured remote coding alias; required for start.",
			},
			"mode": map[string]any{
				"type": "string", "enum": []string{"investigate", "mutate"},
			},
			"objective": map[string]any{
				"type": "string", "description": "Bounded coding objective; required for start.",
			},
			"done_criteria": map[string]any{
				"type": "string", "description": "Optional bounded completion criteria for start.",
			},
			"text": map[string]any{
				"type": "string", "description": "Bounded additional guidance; required for steer.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (*remoteCodingTool) DurableArguments(args map[string]any) (map[string]any, error) {
	projected := make(map[string]any, 4)
	for _, key := range []string{"action", "task_id", "project", "mode"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			projected[key] = strings.TrimSpace(value)
		}
	}
	return projected, nil
}

func (*remoteCodingTool) ProtectedDurableResult(map[string]any) bool { return false }

func (*remoteCodingTool) ProtectedDurableArguments(args map[string]any) bool {
	for _, key := range []string{"objective", "done_criteria", "text"} {
		if value, ok := args[key].(string); ok && value != "" {
			return true
		}
	}
	return false
}

func (tool *remoteCodingTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if tool == nil || tool.runtime == nil {
		return toolshared.ErrorResult("coding task runtime is unavailable")
	}
	switch strings.ToLower(strings.TrimSpace(stringArgumentValue(args, "action"))) {
	case "start":
		return tool.runtime.startTask(ctx, tool.agentID, args)
	case "status":
		return tool.runtime.statusTask(ctx, tool.agentID, args)
	case "steer":
		return tool.runtime.steerTask(ctx, tool.agentID, args, nil)
	case "cancel":
		return tool.runtime.cancelTask(ctx, tool.agentID, args)
	default:
		return toolshared.ErrorResult("action must be start, status, steer, or cancel")
	}
}

func (*remoteCodingTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (runtime *remoteCodingRuntime) start(ctx context.Context) {
	if runtime == nil || runtime.loop == nil {
		return
	}
	runtime.mu.Lock()
	if runtime.started {
		runtime.mu.Unlock()
		return
	}
	runtime.started = true
	runtime.ctx = ctx
	runtime.mu.Unlock()
	registry := runtime.loop.GetRegistry()
	if registry == nil {
		return
	}
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		tasks := runtime.loop.taskRegistryForWorkspace(agent.Workspace)
		if tasks == nil {
			continue
		}
		for _, record := range tasks.ListActive() {
			if record.Runtime == taskregistry.RuntimeCoding {
				runtime.monitor(agent.Workspace, record.TaskID)
			}
		}
		for _, record := range tasks.ListPendingTerminalDelivery() {
			if record.Runtime == taskregistry.RuntimeCoding {
				runtime.monitor(agent.Workspace, record.TaskID)
			}
		}
	}
}

func (runtime *remoteCodingRuntime) monitor(workspace, taskID string) {
	if runtime == nil {
		return
	}
	key := normalizeRuntimeWorkspace(workspace) + "\x00" + strings.TrimSpace(taskID)
	runtime.mu.Lock()
	if _, exists := runtime.active[key]; exists {
		runtime.mu.Unlock()
		return
	}
	parent := runtime.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	runtime.active[key] = cancel
	runtime.mu.Unlock()
	go func() {
		defer func() {
			runtime.mu.Lock()
			delete(runtime.active, key)
			runtime.mu.Unlock()
			cancel()
		}()
		runtime.monitorTask(ctx, workspace, taskID)
	}()
}

func (runtime *remoteCodingRuntime) startTask(
	ctx context.Context,
	agentID string,
	args map[string]any,
) *toolshared.ToolResult {
	identity, err := remoteCodingIdentityFromContext(ctx, agentID)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	alias := strings.TrimSpace(stringArgumentValue(args, "project"))
	mode := codingtask.TaskMode(strings.TrimSpace(stringArgumentValue(args, "mode")))
	objective := strings.TrimSpace(stringArgumentValue(args, "objective"))
	doneCriteria := strings.TrimSpace(stringArgumentValue(args, "done_criteria"))
	if !validRemoteCodingPrompt(objective, true, taskregistry.MaxCodingObjectiveBytes) ||
		!validRemoteCodingPrompt(doneCriteria, false, taskregistry.MaxCodingDoneCriteriaBytes) {
		return toolshared.ErrorResult(
			"objective must be bounded non-empty UTF-8 text and done_criteria must be bounded UTF-8 text",
		)
	}
	combinedBytes := len(objective)
	if doneCriteria != "" {
		combinedBytes += len("\n\nDone criteria:\n") + len(doneCriteria)
	}
	if combinedBytes > nodes.MaxCodingTaskTextBytes {
		return toolshared.ErrorResult("objective and done_criteria exceed the combined coding task limit")
	}
	cfg := runtime.loop.GetConfig()
	project, allowed := cfg.RemoteCodingProjectFor(
		alias,
		identity.AgentID,
		identity.Channel,
		identity.SenderID,
		mode,
	)
	if !allowed {
		return toolshared.ErrorResult("remote coding project or requester grant is unavailable")
	}
	taskID := remoteCodingStartTaskID(identity)
	placeholder := sha256.Sum256([]byte(taskID + "\x00" + alias + "\x00" + objective))
	projection := &taskregistry.CodingProjection{
		SchemaVersion: taskregistry.CodingProjectionSchemaV1,
		Alias:         alias, Target: project.Target, Project: project.Project,
		Revision: project.Revision, Mode: mode,
		RequestDigest: hex.EncodeToString(placeholder[:]), DoneCriteria: doneCriteria,
		RouteSessionKey: identity.RouteSessionKey, SessionKey: identity.SessionKey,
		ActorID: identity.ActorID, SenderID: identity.SenderID,
		AccountID: identity.Inbound.Account, ChatType: identity.Inbound.ChatType,
		SpaceID: identity.Inbound.SpaceID, SpaceType: identity.Inbound.SpaceType,
		OriginMessageID: identity.Inbound.MessageID,
	}
	tasks := runtime.loop.taskRegistryForWorkspace(identity.Workspace)
	if tasks == nil {
		return toolshared.ErrorResult("coding task registry is unavailable")
	}
	record := taskregistry.Record{
		TaskID: taskID, Runtime: taskregistry.RuntimeCoding, TaskKind: "coding_task",
		RequesterSessionKey: identity.RouteSessionKey,
		OwnerKey:            remoteCodingOwnerKey(identity.AgentID, identity.RouteSessionKey, identity.ActorID),
		Channel:             identity.Channel, ChatID: identity.ChatID, TopicID: identity.TopicID,
		AgentID: identity.AgentID, Label: alias, Task: objective,
		Status: taskregistry.StatusQueued, DeliveryStatus: taskregistry.DeliveryPending,
		NotifyPolicy: taskregistry.NotifyDoneOnly,
		DeliveryMode: string(toolshared.AsyncDeliveryUserOnly), Coding: projection,
	}
	if createErr := tasks.Create(record); createErr != nil &&
		!errors.Is(createErr, taskregistry.ErrTaskAlreadyExists) {
		return toolshared.ErrorResult("persist coding task before dispatch: " + createErr.Error())
	}
	record, found := tasks.Get(taskID)
	if !found {
		return toolshared.ErrorResult("coding task disappeared after durable creation")
	}
	if !remoteCodingStartMatches(record, identity, alias, project, mode, objective, doneCriteria) {
		return remoteCodingTaskError(
			taskID,
			"coding task start identity conflicts with a retained request; inspect the existing task",
		)
	}
	startInput, _, err := nodes.NewCodingTaskStartInputs(
		record.TaskID,
		record.GenerationID,
		project.Project,
		project.Revision,
		mode,
		objective,
		doneCriteria,
		"start-"+record.GenerationID,
	)
	if err != nil {
		_ = tasks.Fail(taskID, taskregistry.StatusFailed, "coding task authority is invalid")
		return remoteCodingTaskError(taskID, "coding task authority is invalid")
	}
	if err := tasks.Update(taskID, func(current *taskregistry.Record) {
		if current.GenerationID == record.GenerationID && current.Coding != nil {
			current.Coding.RequestDigest = startInput.RequestDigest
		}
	}); err != nil {
		return remoteCodingTaskError(taskID, "persist coding task digest: "+err.Error())
	}
	runtime.monitor(identity.Workspace, taskID)
	return remoteCodingTaskResult(tasks, taskID, false)
}

func (runtime *remoteCodingRuntime) dispatchStart(
	ctx context.Context,
	workspace string,
	tasks *taskregistry.Registry,
	record taskregistry.Record,
) error {
	lock := runtime.taskOperationLock(workspace, record.TaskID)
	lock.Lock()
	defer lock.Unlock()
	current, found := tasks.Get(record.TaskID)
	if !found || current.GenerationID != record.GenerationID ||
		(current.Status != taskregistry.StatusQueued && current.Status != taskregistry.StatusRunning) {
		return nil
	}
	record = current
	if record.Coding == nil {
		return errors.New("coding task projection is unavailable")
	}
	input, ephemeral, err := nodes.NewCodingTaskStartInputs(
		record.TaskID,
		record.GenerationID,
		record.Coding.Project,
		record.Coding.Revision,
		record.Coding.Mode,
		record.Task,
		record.Coding.DoneCriteria,
		"start-"+record.GenerationID,
	)
	if err != nil || input.RequestDigest != record.Coding.RequestDigest {
		return runtime.settleGatewayFailure(
			ctx,
			workspace,
			tasks,
			record,
			"coding task durable start authority is invalid",
		)
	}
	invoker, err := runtime.invoker()
	if err != nil {
		return err
	}
	raw, err := invoker.Invoke(
		ctx,
		remoteCodingInvocationAuthorityForWorkspace(record, record.Coding, workspace, "start"),
		record.Coding.Target,
		nodes.CodingCommandTaskStart,
		input,
		ephemeral,
	)
	if err != nil {
		if errors.Is(err, tools.ErrCodingNodeUnavailable) ||
			errors.Is(err, tools.ErrCodingInvocationUncertain) {
			return err
		}
		return runtime.settleGatewayFailure(
			ctx,
			workspace,
			tasks,
			record,
			safeRemoteCodingError(err),
		)
	}
	result, err := decodeRemoteCodingResult(raw, record)
	if err != nil {
		return errors.Join(tools.ErrCodingInvocationUncertain, err)
	}
	return runtime.projectResult(workspace, tasks, record, result)
}

func (runtime *remoteCodingRuntime) statusTask(
	ctx context.Context,
	agentID string,
	args map[string]any,
) *toolshared.ToolResult {
	record, identity, tasks, err := runtime.ownedTask(ctx, agentID, stringArgumentValue(args, "task_id"))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	if record.Status != taskregistry.StatusQueued && record.Status != taskregistry.StatusRunning {
		return remoteCodingTaskResult(tasks, record.TaskID, false)
	}
	if record.Coding.ThreadID == "" {
		return remoteCodingTaskResult(tasks, record.TaskID, false)
	}
	result, err := runtime.queryStatus(ctx, record, identity.Workspace)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, safeRemoteCodingError(err))
	}
	if err := runtime.projectResult(identity.Workspace, tasks, record, result); err != nil {
		return remoteCodingTaskError(record.TaskID, "persist coding task status: "+err.Error())
	}
	return remoteCodingTaskResult(tasks, record.TaskID, false)
}

func (runtime *remoteCodingRuntime) steerTask(
	ctx context.Context,
	agentID string,
	args map[string]any,
	answer *nodes.CodingQuestionAnswer,
) *toolshared.ToolResult {
	record, identity, tasks, err := runtime.ownedTask(ctx, agentID, stringArgumentValue(args, "task_id"))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	if grantErr := runtime.requireCurrentGrant(record, identity); grantErr != nil {
		return remoteCodingTaskError(record.TaskID, grantErr.Error())
	}
	text := strings.TrimSpace(stringArgumentValue(args, "text"))
	if !validRemoteCodingPrompt(text, true, nodes.MaxCodingTaskTextBytes) || record.Coding == nil ||
		record.Coding.WorkerGenerationID == "" {
		return remoteCodingTaskError(record.TaskID, "coding task is not ready for steering")
	}
	idempotencyKey := remoteCodingIdempotencyKey(
		"steer",
		record.GenerationID,
		record.Coding.WorkerGenerationID,
		toolshared.ToolCallID(ctx),
	)
	if answer != nil {
		idempotencyKey = remoteCodingIdempotencyKey(
			"answer",
			record.GenerationID,
			answer.QuestionID,
			fmt.Sprint(answer.QuestionRevision),
			answer.AnswerID,
		)
	}
	input, ephemeral, err := nodes.NewCodingTaskSteerInputs(
		record.TaskID,
		record.GenerationID,
		record.Coding.WorkerGenerationID,
		idempotencyKey,
		text,
		answer,
	)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, "coding task steering input is invalid")
	}
	invoker, err := runtime.invoker()
	if err != nil {
		return remoteCodingTaskError(record.TaskID, safeRemoteCodingError(err))
	}
	resultJSON, err := invoker.Invoke(
		ctx,
		remoteCodingInvocationAuthorityForWorkspace(
			record,
			record.Coding,
			identity.Workspace,
			input.TurnIdempotencyKey,
		),
		record.Coding.Target,
		nodes.CodingCommandTaskSteer,
		input,
		ephemeral,
	)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, safeRemoteCodingError(err))
	}
	result, err := decodeRemoteCodingResult(resultJSON, record)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, "coding node returned invalid steering status")
	}
	if err := runtime.projectResult(identity.Workspace, tasks, record, result); err != nil {
		return remoteCodingTaskError(record.TaskID, "persist coding task status: "+err.Error())
	}
	runtime.monitor(identity.Workspace, record.TaskID)
	return remoteCodingTaskResult(tasks, record.TaskID, false)
}

func (runtime *remoteCodingRuntime) cancelTask(
	ctx context.Context,
	agentID string,
	args map[string]any,
) *toolshared.ToolResult {
	record, identity, tasks, err := runtime.ownedTask(ctx, agentID, stringArgumentValue(args, "task_id"))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	if record.Coding == nil {
		return remoteCodingTaskError(record.TaskID, "coding task is not ready for cancellation")
	}
	if record.Coding.WorkerGenerationID == "" {
		lock := runtime.taskOperationLock(identity.Workspace, record.TaskID)
		lock.Lock()
		defer lock.Unlock()
		current, found := tasks.Get(record.TaskID)
		if !found || current.GenerationID != record.GenerationID {
			return remoteCodingTaskError(record.TaskID, "coding task changed while cancellation was requested")
		}
		if current.Coding.WorkerGenerationID != "" {
			record = current
		} else {
			deliverable := &taskresult.Deliverable{
				Text: "Coding task " + record.TaskID + " was canceled before node admission.",
				Metadata: map[string]string{
					"task_id": record.TaskID, "target": record.Coding.Target,
					"project": record.Coding.Alias, "node_state": "not_admitted",
				},
				ObjectiveOutcome: &taskresult.Outcome{
					Status: taskresult.OutcomeBlocked, Explanation: "canceled by the authorized requester",
				},
			}
			if settleErr := tasks.Settle(
				record.TaskID,
				taskregistry.StatusCancelled,
				deliverable.Text,
				deliverable,
				taskregistry.DeliveryPending,
			); settleErr != nil {
				return remoteCodingTaskError(
					record.TaskID,
					"persist coding task cancellation: "+settleErr.Error(),
				)
			}
			runtime.monitor(identity.Workspace, record.TaskID)
			return remoteCodingTaskResult(tasks, record.TaskID, false)
		}
	}
	input := nodes.CodingTaskCancelInput{
		TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
		WorkerGenerationID: record.Coding.WorkerGenerationID,
		CancelIdempotencyKey: remoteCodingIdempotencyKey(
			"cancel",
			record.GenerationID,
			record.Coding.WorkerGenerationID,
		),
	}
	invoker, err := runtime.invoker()
	if err != nil {
		return remoteCodingTaskError(record.TaskID, safeRemoteCodingError(err))
	}
	resultJSON, err := invoker.Invoke(
		ctx,
		remoteCodingInvocationAuthorityForWorkspace(
			record,
			record.Coding,
			identity.Workspace,
			input.CancelIdempotencyKey,
		),
		record.Coding.Target,
		nodes.CodingCommandTaskCancel,
		input,
		nil,
	)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, safeRemoteCodingError(err))
	}
	result, err := decodeRemoteCodingResult(resultJSON, record)
	if err != nil {
		return remoteCodingTaskError(record.TaskID, "coding node returned invalid cancellation status")
	}
	if err := runtime.projectResult(identity.Workspace, tasks, record, result); err != nil {
		return remoteCodingTaskError(record.TaskID, "persist coding task status: "+err.Error())
	}
	runtime.monitor(identity.Workspace, record.TaskID)
	return remoteCodingTaskResult(tasks, record.TaskID, false)
}

type remoteCodingIdentity struct {
	AgentID         string
	SessionKey      string
	RouteSessionKey string
	ActorID         string
	SenderID        string
	Workspace       string
	Channel         string
	ChatID          string
	TopicID         string
	ExecutionID     string
	ToolCallID      string
	Inbound         bus.InboundContext
}

func remoteCodingIdentityFromContext(ctx context.Context, expectedAgent string) (remoteCodingIdentity, error) {
	inbound := toolshared.ToolInboundContext(ctx)
	identity := remoteCodingIdentity{
		AgentID:         strings.TrimSpace(toolshared.ToolAgentID(ctx)),
		SessionKey:      strings.TrimSpace(toolshared.ToolSessionKey(ctx)),
		RouteSessionKey: strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx)),
		ActorID:         strings.TrimSpace(toolshared.ToolActorID(ctx)),
		SenderID:        strings.TrimSpace(toolshared.ToolSenderID(ctx)),
		Workspace:       strings.TrimSpace(toolshared.ToolWorkspace(ctx)),
		Channel:         strings.TrimSpace(toolshared.ToolChannel(ctx)),
		ChatID:          strings.TrimSpace(toolshared.ToolChatID(ctx)),
		TopicID:         strings.TrimSpace(toolshared.ToolTopicID(ctx)),
		ExecutionID:     strings.TrimSpace(toolshared.ToolExecutionID(ctx)),
		ToolCallID:      strings.TrimSpace(toolshared.ToolCallID(ctx)),
		Inbound:         inbound,
	}
	if identity.RouteSessionKey == "" {
		identity.RouteSessionKey = identity.SessionKey
	}
	if identity.ActorID == "" {
		identity.ActorID = identity.SenderID
	}
	if identity.AgentID == "" || identity.AgentID != strings.TrimSpace(expectedAgent) ||
		identity.SessionKey == "" || identity.RouteSessionKey == "" || identity.ActorID == "" ||
		identity.SenderID == "" || identity.Workspace == "" || identity.Channel == "" || identity.ChatID == "" ||
		identity.ExecutionID == "" || identity.ToolCallID == "" {
		return remoteCodingIdentity{}, errors.New("coding task requires an authenticated agent, route, and sender")
	}
	return identity, nil
}

func (runtime *remoteCodingRuntime) ownedTask(
	ctx context.Context,
	agentID string,
	taskID string,
) (taskregistry.Record, remoteCodingIdentity, *taskregistry.Registry, error) {
	identity, err := remoteCodingIdentityFromContext(ctx, agentID)
	if err != nil {
		return taskregistry.Record{}, remoteCodingIdentity{}, nil, err
	}
	tasks := runtime.loop.taskRegistryForWorkspace(identity.Workspace)
	record, found := tasks.Get(strings.TrimSpace(taskID))
	if !found || record.Runtime != taskregistry.RuntimeCoding || record.Coding == nil {
		return taskregistry.Record{}, identity, tasks, errors.New("coding task was not found in this owner scope")
	}
	projection := record.Coding
	if record.AgentID != identity.AgentID || record.Channel != identity.Channel ||
		record.ChatID != identity.ChatID || record.TopicID != identity.TopicID ||
		projection.RouteSessionKey != identity.RouteSessionKey ||
		projection.ActorID != identity.ActorID || projection.SenderID != identity.SenderID ||
		projection.AccountID != identity.Inbound.Account || projection.ChatType != identity.Inbound.ChatType ||
		projection.SpaceID != identity.Inbound.SpaceID || projection.SpaceType != identity.Inbound.SpaceType {
		return taskregistry.Record{}, identity, tasks, errors.New("coding task is owned by a different route or sender")
	}
	return record, identity, tasks, nil
}

func (runtime *remoteCodingRuntime) requireCurrentGrant(
	record taskregistry.Record,
	identity remoteCodingIdentity,
) error {
	if record.Coding == nil {
		return errors.New("coding task grant is no longer current")
	}
	projection := record.Coding
	configured, allowed := runtime.loop.GetConfig().RemoteCodingProjectFor(
		projection.Alias,
		identity.AgentID,
		identity.Channel,
		identity.SenderID,
		projection.Mode,
	)
	if !allowed || configured.Target != projection.Target || configured.Project != projection.Project ||
		configured.Revision != projection.Revision {
		return errors.New("coding task grant is no longer current")
	}
	return nil
}

func (runtime *remoteCodingRuntime) invoker() (RemoteCodingInvoker, error) {
	if runtime == nil || runtime.factory == nil || runtime.loop == nil {
		return nil, tools.ErrCodingNodeUnavailable
	}
	invoker, err := runtime.factory(runtime.loop.GetConfig())
	if err != nil || invoker == nil {
		return nil, errors.Join(tools.ErrCodingNodeUnavailable, err)
	}
	return invoker, nil
}

func remoteCodingInvocationAuthority(
	record taskregistry.Record,
	projection *taskregistry.CodingProjection,
	operation string,
) tools.CodingInvocationAuthority {
	return tools.CodingInvocationAuthority{
		AgentID: projectionOwnerAgent(record), SessionID: projection.RouteSessionKey,
		ActorID:     projection.ActorID,
		ExecutionID: "coding-task-" + record.TaskID + "-" + record.GenerationID,
		OperationID: operation,
	}
}

func remoteCodingInvocationAuthorityForWorkspace(
	record taskregistry.Record,
	projection *taskregistry.CodingProjection,
	workspace string,
	operation string,
) tools.CodingInvocationAuthority {
	authority := remoteCodingInvocationAuthority(record, projection, operation)
	authority.Workspace = workspace
	return authority
}

func projectionOwnerAgent(record taskregistry.Record) string {
	return strings.TrimSpace(record.AgentID)
}

func (runtime *remoteCodingRuntime) queryStatus(
	ctx context.Context,
	record taskregistry.Record,
	workspace string,
) (nodes.CodingTaskResult, error) {
	if record.Coding == nil {
		return nodes.CodingTaskResult{}, errors.New("coding task projection is unavailable")
	}
	invoker, err := runtime.invoker()
	if err != nil {
		return nodes.CodingTaskResult{}, err
	}
	authority := remoteCodingInvocationAuthority(record, record.Coding, "status-"+uuid.NewString())
	authority.Workspace = workspace
	raw, err := invoker.Invoke(
		ctx,
		authority,
		record.Coding.Target,
		nodes.CodingCommandTaskStatus,
		nodes.CodingTaskIdentityInput{TaskID: record.TaskID, TaskGenerationID: record.GenerationID},
		nil,
	)
	if err != nil {
		return nodes.CodingTaskResult{}, err
	}
	return decodeRemoteCodingResult(raw, record)
}

func decodeRemoteCodingResult(raw json.RawMessage, record taskregistry.Record) (nodes.CodingTaskResult, error) {
	var result nodes.CodingTaskResult
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil || record.Coding == nil ||
		result.TaskID != record.TaskID || result.TaskGenerationID != record.GenerationID ||
		result.ProjectAlias != record.Coding.Project || result.ProjectRevision != record.Coding.Revision ||
		result.Mode != record.Coding.Mode || !result.State.Valid() || result.Revision == 0 {
		return nodes.CodingTaskResult{}, errors.New("coding task result does not match durable authority")
	}
	return result, nil
}

func (runtime *remoteCodingRuntime) projectResult(
	workspace string,
	tasks *taskregistry.Registry,
	previous taskregistry.Record,
	result nodes.CodingTaskResult,
) error {
	if tasks == nil || previous.Coding == nil {
		return errors.New("coding task registry is unavailable")
	}
	resultDigest, err := remoteCodingResultDigest(result)
	if err != nil {
		return err
	}
	var accepted bool
	var conflict bool
	var priorQuestion *taskregistry.CodingQuestionProjection
	err = tasks.Update(previous.TaskID, func(record *taskregistry.Record) {
		if record.GenerationID != previous.GenerationID || record.Coding == nil {
			return
		}
		projection := record.Coding
		if result.Revision < projection.NodeRevision {
			return
		}
		priorQuestion = cloneRemoteCodingQuestionProjection(projection.Question)
		if result.Revision == projection.NodeRevision {
			if projection.NodeResultDigest != resultDigest {
				conflict = true
				return
			}
			accepted = true
			return
		}
		accepted = true
		projection.ThreadID = result.ThreadID
		projection.WorkerGenerationID = result.WorkerGenerationID
		projection.NodeState = string(result.State)
		projection.NodeRevision = result.Revision
		projection.NodeResultDigest = resultDigest
		projection.Activity = string(result.Activity)
		projection.WorktreeID = result.WorktreeID
		projection.Branch = result.Branch
		projection.HandoffID = result.HandoffID
		projection.FailureCode = result.FailureCode
		projection.UpdatedAt = result.UpdatedAt
		projection.RetainUntil = result.RetainUntil
		if result.Question == nil {
			projection.Question = nil
		} else {
			projected := &taskregistry.CodingQuestionProjection{
				ID: result.Question.QuestionID, Revision: result.Question.Revision,
			}
			if projection.Question != nil && projection.Question.ID == projected.ID &&
				projection.Question.Revision == projected.Revision {
				projected.InteractionID = projection.Question.InteractionID
			}
			for _, option := range result.Question.Options {
				projected.Options = append(projected.Options, taskregistry.CodingQuestionOption{
					ID: option.ID, Label: option.Label,
				})
			}
			projection.Question = projected
		}
		if !result.State.Terminal() {
			record.Status = taskregistry.StatusRunning
			record.ProgressSummary = remoteCodingProgress(result)
		}
	})
	if err != nil {
		return err
	}
	if conflict {
		return errors.New("coding node returned conflicting data for an existing revision")
	}
	if !accepted {
		return nil
	}
	if remoteCodingQuestionChanged(priorQuestion, result.Question) {
		runtime.retireQuestion(workspace, priorQuestion)
	}
	if result.Question != nil {
		if err := runtime.ensureQuestion(workspace, tasks, previous.TaskID, result); err != nil {
			logger.WarnCF("coding_task", "Failed to project coding question", map[string]any{
				"task_id": previous.TaskID, "error": safeRemoteCodingError(err),
			})
		}
	}
	if result.State.Terminal() {
		return runtime.settleTerminal(workspace, tasks, previous.TaskID, result)
	}
	return nil
}

func remoteCodingResultDigest(result nodes.CodingTaskResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode coding task result digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneRemoteCodingQuestionProjection(
	question *taskregistry.CodingQuestionProjection,
) *taskregistry.CodingQuestionProjection {
	if question == nil {
		return nil
	}
	cloned := *question
	cloned.Options = append([]taskregistry.CodingQuestionOption(nil), question.Options...)
	return &cloned
}

func remoteCodingQuestionChanged(
	previous *taskregistry.CodingQuestionProjection,
	current *nodes.CodingQuestionResult,
) bool {
	if previous == nil || previous.InteractionID == "" {
		return false
	}
	return current == nil || previous.ID != current.QuestionID || previous.Revision != current.Revision
}

func (runtime *remoteCodingRuntime) retireQuestion(
	workspace string,
	question *taskregistry.CodingQuestionProjection,
) {
	if runtime == nil || runtime.loop == nil || question == nil || question.InteractionID == "" {
		return
	}
	registry := runtime.loop.interactionRegistryForWorkspace(workspace)
	record, found := registry.Get(question.InteractionID)
	if !found || (record.Status != interactions.StatusCreated && record.Status != interactions.StatusWaiting) {
		return
	}
	canceled, err := registry.Cancel(record.ID, record.Revision, "coding_question_stale")
	if err != nil {
		return
	}
	runtime.loop.syncInteractionControls(
		workspace,
		canceled,
		bus.OutboundInteractionControlsRemove,
	)
}

func remoteCodingProgress(result nodes.CodingTaskResult) string {
	if result.State == codingtask.StateWaitingInput {
		return "coding task is waiting for correlated user input"
	}
	if result.Activity != "" {
		return "coding task is " + string(result.Activity)
	}
	return "coding task is " + string(result.State)
}

func (runtime *remoteCodingRuntime) monitorTask(ctx context.Context, workspace, taskID string) {
	for {
		tasks := runtime.loop.taskRegistryForWorkspace(workspace)
		record, found := tasks.Get(taskID)
		if !found || record.Runtime != taskregistry.RuntimeCoding || record.Coding == nil {
			return
		}
		if record.Status != taskregistry.StatusQueued && record.Status != taskregistry.StatusRunning {
			if record.DeliveryStatus == taskregistry.DeliveryPending {
				if err := runtime.deliverTerminal(ctx, workspace, tasks, record); err != nil {
					if !waitRemoteCoding(ctx, remoteCodingPollOffline) {
						return
					}
					continue
				}
			}
			return
		}
		if record.Coding.ThreadID == "" {
			err := runtime.dispatchStart(ctx, workspace, tasks, record)
			delay := remoteCodingPollRunning
			if err != nil {
				_ = tasks.Heartbeat(taskID, safeRemoteCodingError(err))
				if errors.Is(err, tools.ErrCodingNodeUnavailable) ||
					errors.Is(err, tools.ErrCodingInvocationUncertain) {
					delay = remoteCodingPollOffline
				}
			}
			if !waitRemoteCoding(ctx, delay) {
				return
			}
			continue
		}
		result, err := runtime.queryStatus(ctx, record, workspace)
		delay := remoteCodingPollRunning
		if err != nil {
			_ = tasks.Heartbeat(taskID, "coding node is unavailable; task state remains uncertain")
			delay = remoteCodingPollOffline
		} else {
			projectionErr := runtime.projectResult(workspace, tasks, record, result)
			if projectionErr != nil {
				_ = tasks.Heartbeat(taskID, "coding task reconciliation is pending")
				delay = remoteCodingPollOffline
			} else if result.State.Terminal() {
				continue
			}
			if result.State == codingtask.StateWaitingInput || result.State == codingtask.StateIdle {
				delay = remoteCodingPollWaiting
			}
		}
		if !waitRemoteCoding(ctx, delay) {
			return
		}
	}
}

func (runtime *remoteCodingRuntime) settleGatewayFailure(
	ctx context.Context,
	workspace string,
	tasks *taskregistry.Registry,
	record taskregistry.Record,
	summary string,
) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "coding task could not be admitted by the paired node"
	}
	deliverable := &taskresult.Deliverable{
		Text: fmt.Sprintf("Coding task %s failed before node admission: %s", record.TaskID, summary),
		Metadata: map[string]string{
			"task_id": record.TaskID, "target": record.Coding.Target,
			"project": record.Coding.Alias, "mode": string(record.Coding.Mode),
			"node_state": "not_admitted",
		},
		ObjectiveOutcome: &taskresult.Outcome{
			Status: taskresult.OutcomeBlocked, Explanation: summary,
		},
	}
	if err := tasks.Settle(
		record.TaskID,
		taskregistry.StatusFailed,
		deliverable.Text,
		deliverable,
		taskregistry.DeliveryPending,
	); err != nil {
		return err
	}
	current, _ := tasks.Get(record.TaskID)
	return runtime.deliverTerminal(ctx, workspace, tasks, current)
}

func waitRemoteCoding(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (runtime *remoteCodingRuntime) taskOperationLock(workspace, taskID string) *sync.Mutex {
	key := normalizeRuntimeWorkspace(workspace) + "\x00" + strings.TrimSpace(taskID)
	candidate := &sync.Mutex{}
	actual, _ := runtime.locks.LoadOrStore(key, candidate)
	lock, _ := actual.(*sync.Mutex)
	if lock == nil {
		return candidate
	}
	return lock
}

func stringArgumentValue(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func validRemoteCodingPrompt(value string, required bool, maximum int) bool {
	if maximum <= 0 || !utf8.ValidString(value) || len(value) > maximum || strings.ContainsRune(value, 0) {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func remoteCodingOwnerKey(agentID, routeSession, actorID string) string {
	hash := sha256.Sum256([]byte(agentID + "\x00" + routeSession + "\x00" + actorID))
	return "coding-owner-" + hex.EncodeToString(hash[:])
}

func remoteCodingStartTaskID(identity remoteCodingIdentity) string {
	hash := sha256.New()
	for _, value := range []string{
		identity.AgentID,
		identity.RouteSessionKey,
		identity.ActorID,
		identity.ExecutionID,
		identity.ToolCallID,
	} {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	return "coding-" + hex.EncodeToString(hash.Sum(nil))
}

func remoteCodingStartMatches(
	record taskregistry.Record,
	identity remoteCodingIdentity,
	alias string,
	project config.RemoteCodingProject,
	mode codingtask.TaskMode,
	objective string,
	doneCriteria string,
) bool {
	projection := record.Coding
	return record.Runtime == taskregistry.RuntimeCoding && projection != nil &&
		record.AgentID == identity.AgentID && record.Channel == identity.Channel &&
		record.ChatID == identity.ChatID && record.TopicID == identity.TopicID &&
		record.Task == objective && projection.DoneCriteria == doneCriteria &&
		projection.Alias == alias && projection.Target == project.Target &&
		projection.Project == project.Project && projection.Revision == project.Revision &&
		projection.Mode == mode && projection.RouteSessionKey == identity.RouteSessionKey &&
		projection.ActorID == identity.ActorID && projection.SenderID == identity.SenderID &&
		projection.AccountID == identity.Inbound.Account && projection.ChatType == identity.Inbound.ChatType &&
		projection.SpaceID == identity.Inbound.SpaceID && projection.SpaceType == identity.Inbound.SpaceType
}

func remoteCodingIdempotencyKey(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	return prefix + "-" + hex.EncodeToString(hash.Sum(nil))
}

func safeRemoteCodingError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, tools.ErrCodingInvocationUncertain):
		return "coding task outcome is uncertain; inspect status and do not replay start"
	case errors.Is(err, tools.ErrCodingNodeUnavailable):
		return "coding node or its approved command is unavailable; reconnect or re-approve the configured target"
	default:
		code, classified := tools.CodingNodeOperationErrorCode(err)
		if !classified {
			return "coding task operation failed"
		}
		switch code {
		case nodes.InvocationDispatchCodingProjectNotFound:
			return "coding project is not configured on the selected node; check the node project alias"
		case nodes.InvocationDispatchCodingProjectStale:
			return "coding project policy changed; update the gateway project revision before retrying"
		case nodes.InvocationDispatchCodingModeDenied:
			return "requested coding mode is not allowed for this project"
		case nodes.InvocationDispatchCodingProjectBusy, nodes.InvocationDispatchNodeBusy:
			return "coding project is at capacity; wait for an existing task to finish before starting another"
		case nodes.InvocationDispatchCodingTaskNotFound:
			return "coding task is no longer retained on the selected node"
		case nodes.InvocationDispatchCodingTaskConflict, nodes.InvocationDispatchIdempotencyConflict:
			return "coding task identity conflicts with retained state; inspect the existing task and do not replay start"
		case nodes.InvocationDispatchCodingTaskNotResumable:
			return "coding task is not idle and cannot accept a successor turn"
		case nodes.InvocationDispatchCodingTaskNotRunning:
			return "coding task worker is not running; inspect its latest status"
		case nodes.InvocationDispatchCodingHostUnavailable:
			return "coding host is unavailable on the selected node; restart or upgrade the node companion"
		case nodes.InvocationDispatchCodingCommandTimeout:
			return "coding node operation timed out; inspect task status before retrying"
		case nodes.InvocationDispatchCodingOutputLimit:
			return "coding task status exceeded the configured bounded result limit"
		default:
			return "coding node rejected the operation (" + code + ")"
		}
	}
}

func remoteCodingTaskError(taskID, message string) *toolshared.ToolResult {
	payload, _ := json.Marshal(map[string]any{
		"task_id": strings.TrimSpace(taskID), "status": "error", "error": strings.TrimSpace(message),
	})
	return toolshared.ErrorResult(string(payload))
}

func remoteCodingTaskResult(
	tasks *taskregistry.Registry,
	taskID string,
	forUser bool,
) *toolshared.ToolResult {
	record, found := tasks.Get(taskID)
	if !found || record.Coding == nil {
		return remoteCodingTaskError(taskID, "coding task was not found")
	}
	projection := map[string]any{
		"task_id": record.TaskID, "status": record.Status,
		"delivery_status": record.DeliveryStatus,
		"project":         record.Coding.Alias, "target": record.Coding.Target,
		"mode": record.Coding.Mode, "node_state": record.Coding.NodeState,
		"thread_id":            record.Coding.ThreadID,
		"worker_generation_id": record.Coding.WorkerGenerationID,
		"activity":             record.Coding.Activity, "progress": record.ProgressSummary,
		"branch": record.Coding.Branch, "handoff_id": record.Coding.HandoffID,
		"failure_code": record.Coding.FailureCode,
	}
	if record.Deliverable != nil {
		projection["result"] = record.Deliverable
	}
	data, _ := json.Marshal(projection)
	result := toolshared.NewToolResult(string(data))
	if forUser {
		result.ForUser = string(data)
	}
	return result
}

func (runtime *remoteCodingRuntime) ensureQuestion(
	workspace string,
	tasks *taskregistry.Registry,
	taskID string,
	result nodes.CodingTaskResult,
) error {
	if result.Question == nil {
		return nil
	}
	record, found := tasks.Get(taskID)
	if !found || record.Coding == nil || record.Coding.Question == nil {
		return errors.New("coding question projection is unavailable")
	}
	question := record.Coding.Question
	interactionID := question.InteractionID
	if interactionID == "" {
		interactionID = remoteCodingQuestionID(record, question.ID, question.Revision)
		if err := tasks.Update(taskID, func(current *taskregistry.Record) {
			if current.Coding != nil && current.Coding.Question != nil &&
				current.Coding.Question.ID == question.ID &&
				current.Coding.Question.Revision == question.Revision {
				current.Coding.Question.InteractionID = interactionID
			}
		}); err != nil {
			return err
		}
	}
	registry := runtime.loop.interactionRegistryForWorkspace(workspace)
	if existing, ok := registry.Get(interactionID); ok {
		if existing.Status == interactions.StatusCreated {
			_, _, err := (&humanInteractionRuntime{
				al: runtime.loop, coordinator: &runtime.loop.interactions,
			}).deliverPrompt(context.Background(), registry, workspace, existing)
			return err
		}
		return nil
	}
	options := make([]interactions.Option, 0, len(result.Question.Options))
	if len(result.Question.Options) >= 2 && len(result.Question.Options) <= interactions.MaxOptions {
		for _, option := range result.Question.Options {
			options = append(options, interactions.Option{
				Label:       truncateRemoteCodingText(option.Label, interactions.MaxOptionLabelLength),
				Description: truncateRemoteCodingText(option.Description, interactions.MaxDescriptionLength),
			})
		}
	}
	inbound := remoteCodingInbound(record)
	created, err := runtime.loop.interactions.create(workspace, registry, interactions.CreateRequest{
		ID: interactionID, Kind: interactions.KindQuestion,
		Route: remoteCodingInteractionRoute(record),
		Origin: interactions.Origin{
			TurnID: record.GenerationID, ExecutionID: "coding-task-" + record.TaskID,
			ToolCallID: "question-" + question.ID, ToolName: "coding_task", TaskID: record.TaskID,
			ContinuationSessionKey: record.Coding.SessionKey, ExecutionContext: &inbound,
		},
		Questions: []interactions.Question{{
			ID: "answer", Header: "Coding task",
			Question: truncateRemoteCodingText(result.Question.Prompt, interactions.MaxQuestionLength),
			Options:  options,
		}},
		PromptSummary: "Coding task " + record.TaskID + " is waiting for input.",
		ExpiresAt:     time.Now().Add(remoteCodingQuestionTTL),
	})
	if err != nil {
		return err
	}
	_, _, err = (&humanInteractionRuntime{
		al: runtime.loop, coordinator: &runtime.loop.interactions,
	}).deliverPrompt(context.Background(), registry, workspace, created)
	return err
}

func remoteCodingQuestionID(record taskregistry.Record, questionID string, revision uint64) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d",
		record.TaskID,
		record.GenerationID,
		questionID,
		revision,
	)))
	return "codingq-" + hex.EncodeToString(hash[:])
}

func truncateRemoteCodingText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.ValidString(value[:maximum]) {
		maximum--
	}
	return strings.TrimSpace(value[:maximum])
}

func remoteCodingInteractionRoute(record taskregistry.Record) interactions.Route {
	return interactions.Route{
		AgentID: record.AgentID, SessionKey: record.Coding.SessionKey,
		RouteSessionKey: record.Coding.RouteSessionKey,
		Channel:         record.Channel, AccountID: record.Coding.AccountID,
		ChatID: record.ChatID, ChatType: record.Coding.ChatType, TopicID: record.TopicID,
		SpaceID: record.Coding.SpaceID, SpaceType: record.Coding.SpaceType,
		SenderID: record.Coding.SenderID,
	}
}

func remoteCodingInbound(record taskregistry.Record) bus.InboundContext {
	return bus.InboundContext{
		Channel: record.Channel, Account: record.Coding.AccountID, ChatID: record.ChatID,
		ChatType: record.Coding.ChatType, SenderID: record.Coding.SenderID,
		ActorID: record.Coding.ActorID, TopicID: record.TopicID,
		SpaceID: record.Coding.SpaceID, SpaceType: record.Coding.SpaceType,
		MessageID: record.Coding.OriginMessageID,
	}
}

func (runtime *remoteCodingRuntime) settleTerminal(
	workspace string,
	tasks *taskregistry.Registry,
	taskID string,
	result nodes.CodingTaskResult,
) error {
	record, found := tasks.Get(taskID)
	if !found {
		return errors.New("coding task disappeared before terminal settlement")
	}
	if record.Status == taskregistry.StatusQueued || record.Status == taskregistry.StatusRunning {
		deliverable := remoteCodingDeliverable(record, result)
		if result.State == codingtask.StateCanceled {
			if err := tasks.Settle(
				taskID,
				taskregistry.StatusCancelled,
				deliverable.Text,
				deliverable,
				taskregistry.DeliveryPending,
			); err != nil {
				return err
			}
		} else {
			if err := tasks.Complete(taskID, deliverable.Text, deliverable, taskregistry.DeliveryPending); err != nil {
				return err
			}
		}
		record, _ = tasks.Get(taskID)
	}
	return runtime.deliverTerminal(context.Background(), workspace, tasks, record)
}

func remoteCodingDeliverable(
	record taskregistry.Record,
	result nodes.CodingTaskResult,
) *taskresult.Deliverable {
	report := result.TerminalReport
	if report == nil {
		report = &codingtask.TerminalReport{Summary: "Coding task ended without a bounded terminal summary."}
	}
	text := renderRemoteCodingReport(record, result, *report)
	outcome := taskresult.OutcomeSucceeded
	if result.State != codingtask.StateCompleted {
		outcome = taskresult.OutcomeBlocked
	}
	return &taskresult.Deliverable{
		Text: text,
		Metadata: map[string]string{
			"task_id": record.TaskID, "thread_id": result.ThreadID,
			"target": record.Coding.Target, "project": record.Coding.Alias,
			"node_project": record.Coding.Project, "mode": string(record.Coding.Mode),
			"node_state": string(result.State), "branch": result.Branch,
			"commit": report.Commit, "handoff_id": result.HandoffID,
			"cleanup_state": report.CleanupState,
		},
		ObjectiveOutcome: &taskresult.Outcome{
			Status: outcome, Explanation: report.Unresolved,
		},
	}
}

func renderRemoteCodingReport(
	record taskregistry.Record,
	result nodes.CodingTaskResult,
	report codingtask.TerminalReport,
) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Coding task %s: %s\n", record.TaskID, result.State)
	fmt.Fprintf(&builder, "Project: %s on %s (%s)\n", record.Coding.Alias, record.Coding.Target, record.Coding.Mode)
	if result.ThreadID != "" {
		fmt.Fprintf(&builder, "Thread: %s\n", result.ThreadID)
	}
	if report.Summary != "" {
		fmt.Fprintf(&builder, "\n%s\n", report.Summary)
	}
	if report.SummaryTruncated {
		builder.WriteString("Summary truncated: yes\n")
	}
	if len(report.ChangedPaths) > 0 || report.PathsTruncated {
		builder.WriteString("\nChanged paths:\n")
		for _, path := range report.ChangedPaths {
			fmt.Fprintf(&builder, "- %s\n", path)
		}
	}
	if report.PathsTruncated {
		builder.WriteString("- Additional changed paths omitted.\n")
	}
	if len(report.Validations) > 0 || report.ValidationsTruncated {
		builder.WriteString("\nValidation outcomes:\n")
		for index, validation := range report.Validations {
			fmt.Fprintf(&builder, "- %s %d: %s\n", validation.Kind, index+1, validation.Status)
		}
	}
	if report.ValidationsTruncated {
		builder.WriteString("- Additional validation outcomes omitted.\n")
	}
	if result.Branch != "" {
		fmt.Fprintf(&builder, "\nBranch: %s\n", result.Branch)
	}
	if report.Commit != "" {
		fmt.Fprintf(&builder, "Commit: %s\n", report.Commit)
	}
	if report.CleanupState != "" {
		fmt.Fprintf(&builder, "Cleanup: %s\n", report.CleanupState)
	}
	if report.Unresolved != "" {
		fmt.Fprintf(&builder, "Unresolved: %s\n", report.Unresolved)
	}
	return strings.TrimSpace(builder.String())
}

func (runtime *remoteCodingRuntime) deliverTerminal(
	ctx context.Context,
	workspace string,
	tasks *taskregistry.Registry,
	record taskregistry.Record,
) error {
	if record.Deliverable == nil || record.Coding == nil ||
		record.DeliveryStatus != taskregistry.DeliveryPending {
		return nil
	}
	completionID := "coding-task:" + record.GenerationID
	if runtime.loop.tasks.deliveryAlreadyHandled(workspace, record.TaskID, completionID) {
		return nil
	}
	if !runtime.loop.tasks.claimCompletion(completionID) {
		return nil
	}
	deliverySucceeded := false
	defer func() {
		if !deliverySucceeded {
			runtime.loop.tasks.releaseCompletion(completionID)
		}
	}()
	inbound := remoteCodingInbound(record)
	result := (&toolshared.ToolResult{
		ForLLM: record.Deliverable.Text, ForUser: record.Deliverable.Text,
		Deliverable: taskresult.CloneDeliverable(record.Deliverable),
	}).WithTaskID(record.TaskID).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	result.WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	agent, found := runtime.loop.GetRegistry().GetAgent(record.AgentID)
	runner := runtime.loop.turns.currentRunner()
	if !found || agent == nil || runner == nil || runner.pipeline == nil {
		return errors.New("coding task delivery runtime is unavailable")
	}
	turnState := &turnState{
		agent: agent, agentID: record.AgentID, workspace: workspace,
		channel: record.Channel, chatID: record.ChatID, sessionKey: record.Coding.SessionKey,
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{
			RouteSessionKey: record.Coding.RouteSessionKey,
			SessionKey:      record.Coding.SessionKey,
			InboundContext:  &inbound,
		}}),
		scope: runtime.loop.newTurnEventScope(
			record.AgentID,
			workspace,
			record.Coding.SessionKey,
			newTurnContext(&inbound, nil, nil),
		),
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	deliveryCtx = withOutboundTransaction(deliveryCtx, completionID)
	runner.pipeline.Interaction.ToolDelivery.deliverAsyncToolCompletion(
		AsyncDeliveryRequest{
			Context: deliveryCtx, TurnState: turnState, ToolName: "coding_task",
			CompletionID: completionID, Result: result,
			Decision: decideAsyncToolResultDelivery(result),
			Metadata: bus.OutboundMetadata{
				MessageKind:  bus.OutboundMessageKindFinalReply,
				OutboundKind: bus.OutboundKindFinal,
			},
		},
	)
	transaction := outboundTransactionFromContext(deliveryCtx)
	if transaction == nil {
		return errors.New("coding task delivery transaction is unavailable")
	}
	if err := transaction.awaitDelivered(deliveryCtx); err != nil {
		return err
	}
	current, found := tasks.Get(record.TaskID)
	if found && current.DeliveryStatus == taskregistry.DeliveryPending {
		runtime.loop.tasks.updateDeliveryStatus(
			workspace,
			record.TaskID,
			taskregistry.DeliveryDelivered,
			completionID,
			"",
		)
	}
	deliverySucceeded = true
	return nil
}

func (runtime *remoteCodingRuntime) resumeQuestionInteraction(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
) error {
	if record.Origin.ToolName != "coding_task" || strings.TrimSpace(record.Origin.TaskID) == "" {
		return errors.New("interaction is not a remote coding question")
	}
	current, found := registry.Get(record.ID)
	if !found {
		return interactions.ErrNotFound
	}
	if current.Status == interactions.StatusClaimed {
		var err error
		current, err = registry.MarkResuming(current.ID, current.Revision)
		if err != nil {
			return err
		}
	}
	if current.Status != interactions.StatusResuming || current.Answer == nil ||
		current.Outcome != interactions.OutcomeAnswered {
		return errors.New("coding question does not contain an accepted answer")
	}
	tasks := runtime.loop.taskRegistryForWorkspace(workspace)
	task, found := tasks.Get(current.Origin.TaskID)
	if !found || task.Runtime != taskregistry.RuntimeCoding || task.Coding == nil ||
		task.Coding.Question == nil || task.Coding.Question.InteractionID != current.ID {
		return errors.New("coding question no longer matches the durable task")
	}
	answerText := strings.TrimSpace(current.Answer.Text)
	if answerText == "" {
		return errors.New("coding question answer is empty")
	}
	answer := &nodes.CodingQuestionAnswer{
		QuestionID:       task.Coding.Question.ID,
		QuestionRevision: task.Coding.Question.Revision,
		AnswerID:         "answer-" + current.ID,
	}
	toolArgs := map[string]any{"task_id": task.TaskID, "text": answerText}
	identityContext := remoteCodingToolContext(ctx, workspace, task)
	result := runtime.steerTask(identityContext, task.AgentID, toolArgs, answer)
	if result.IsError {
		_, _ = registry.RecordResumeFailure(current.ID, current.Revision, result.ContentForLLM())
		return errors.New("coding question answer could not be delivered")
	}
	_, err := registry.Resolve(current.ID, current.Revision)
	return err
}

func (runtime *remoteCodingRuntime) cancelQuestionInteraction(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	interaction interactions.Record,
) error {
	if interaction.Origin.ToolName != "coding_task" || interaction.Status != interactions.StatusCanceling {
		return errors.New("interaction is not a canceling coding question")
	}
	tasks := runtime.loop.taskRegistryForWorkspace(workspace)
	record, found := tasks.Get(interaction.Origin.TaskID)
	if !found || record.Runtime != taskregistry.RuntimeCoding || record.Coding == nil ||
		record.Coding.Question == nil || record.Coding.Question.InteractionID != interaction.ID {
		return errors.New("coding question no longer matches the durable task")
	}
	if record.Status == taskregistry.StatusQueued || record.Status == taskregistry.StatusRunning {
		if record.Coding.WorkerGenerationID == "" {
			deliverable := &taskresult.Deliverable{
				Text: "Coding task " + record.TaskID + " was canceled before node admission.",
				Metadata: map[string]string{
					"task_id": record.TaskID, "target": record.Coding.Target,
					"project": record.Coding.Alias, "node_state": "not_admitted",
				},
				ObjectiveOutcome: &taskresult.Outcome{
					Status: taskresult.OutcomeBlocked, Explanation: "canceled by the authorized requester",
				},
			}
			if err := tasks.Settle(
				record.TaskID,
				taskregistry.StatusCancelled,
				deliverable.Text,
				deliverable,
				taskregistry.DeliveryPending,
			); err != nil {
				return err
			}
		} else {
			input := nodes.CodingTaskCancelInput{
				TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
				WorkerGenerationID: record.Coding.WorkerGenerationID,
				CancelIdempotencyKey: remoteCodingIdempotencyKey(
					"cancel",
					record.GenerationID,
					record.Coding.WorkerGenerationID,
				),
			}
			invoker, err := runtime.invoker()
			if err != nil {
				return err
			}
			raw, err := invoker.Invoke(
				ctx,
				remoteCodingInvocationAuthorityForWorkspace(
					record,
					record.Coding,
					workspace,
					input.CancelIdempotencyKey,
				),
				record.Coding.Target,
				nodes.CodingCommandTaskCancel,
				input,
				nil,
			)
			if err != nil {
				return err
			}
			projected, err := decodeRemoteCodingResult(raw, record)
			if err != nil {
				return err
			}
			if err := runtime.projectResult(workspace, tasks, record, projected); err != nil {
				return err
			}
		}
	}
	current, found := registry.Get(interaction.ID)
	if !found {
		return interactions.ErrNotFound
	}
	if current.Status == interactions.StatusCancelled {
		return nil
	}
	_, err := registry.CompleteCancellation(current.ID, current.Revision)
	return err
}

func remoteCodingToolContext(
	ctx context.Context,
	workspace string,
	record taskregistry.Record,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	inbound := remoteCodingInbound(record)
	ctx = toolshared.WithToolInboundMetadata(ctx, inbound)
	ctx = toolshared.WithToolContext(ctx, record.Channel, record.ChatID)
	ctx = toolshared.WithToolTopicID(ctx, record.TopicID)
	ctx = toolshared.WithToolSessionContext(ctx, record.AgentID, record.Coding.SessionKey, nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, record.Coding.RouteSessionKey)
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "coding-task-"+record.GenerationID)
	ctx = toolshared.WithToolCallID(ctx, "coding-question-"+record.GenerationID)
	return ctx
}
