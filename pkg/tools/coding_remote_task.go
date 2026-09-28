package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	codingRemoteTaskResultSchema         = "mintclaw.remote_coding_task.v1"
	codingRemoteTaskMaxLinks             = 64
	codingRemoteTaskMaxStartPreparations = 64
)

var codingRemoteTaskIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type CodingRemoteTaskClient interface {
	Discover(context.Context, codingremote.Request) (codingremote.CapabilitySnapshot, error)
	Task(context.Context, codingremote.Request) (codingremote.TaskResult, error)
}

// CodingRemoteTaskTool links one local coding thread to bounded projections
// of gateway-owned P7.4/P7.7 tasks. It never owns a remote transcript,
// repository, worktree, or worker lifecycle.
type CodingRemoteTaskTool struct {
	client    CodingRemoteTaskClient
	authority CodingRemoteToolAuthority

	mu                sync.RWMutex
	snapshot          codingremote.CapabilitySnapshot
	links             map[string]codingRemoteTaskLink
	starts            map[string]codingRemoteTaskStartPreparation
	order             []string
	inheritedLinks    int
	inheritedOverflow bool
	retentionFailed   bool
}

type codingRemoteTaskLink struct {
	result codingRemoteTaskToolResult
}

type codingRemoteTaskStartPreparation struct {
	request  codingremote.Request
	target   string
	prebound codingRemoteTaskToolResult
	failure  *toolshared.ToolResult
}

type codingRemoteTaskToolResult struct {
	Schema                   string                     `json:"schema"`
	Placement                string                     `json:"placement"`
	Action                   string                     `json:"action"`
	Outcome                  string                     `json:"outcome"`
	Retained                 bool                       `json:"retained"`
	OwnerThreadID            string                     `json:"owner_thread_id"`
	Grant                    string                     `json:"grant"`
	GrantRevision            string                     `json:"grant_revision"`
	DiscoveryRevision        string                     `json:"discovery_revision"`
	BindingDiscoveryRevision string                     `json:"binding_discovery_revision"`
	TaskID                   string                     `json:"task_id"`
	GenerationID             string                     `json:"generation_id,omitempty"`
	Scope                    string                     `json:"scope"`
	ScopeRevision            string                     `json:"scope_revision"`
	Target                   string                     `json:"target"`
	Profile                  codingscope.Profile        `json:"profile"`
	TaskStatus               string                     `json:"task_status,omitempty"`
	NodeState                string                     `json:"node_state,omitempty"`
	RemoteThreadID           string                     `json:"remote_thread_id,omitempty"`
	WorkerGenerationID       string                     `json:"worker_generation_id,omitempty"`
	Activity                 string                     `json:"activity,omitempty"`
	Progress                 string                     `json:"progress,omitempty"`
	Branch                   string                     `json:"branch,omitempty"`
	HandoffID                string                     `json:"handoff_id,omitempty"`
	FailureCode              string                     `json:"failure_code,omitempty"`
	Question                 *codingremote.TaskQuestion `json:"question,omitempty"`
	TerminalSummary          string                     `json:"terminal_summary,omitempty"`
	ErrorCode                string                     `json:"error_code,omitempty"`
	RecoveryAction           string                     `json:"recovery_action,omitempty"`
}

func NewCodingRemoteTaskTool(
	client CodingRemoteTaskClient,
	authority CodingRemoteToolAuthority,
	snapshot codingremote.CapabilitySnapshot,
) (*CodingRemoteTaskTool, error) {
	if codingRemoteTaskClientNil(client) || !codingremote.ValidAlias(authority.Grant) ||
		authority.GrantRevision == "" || !codingremote.LocalProfileAllowed(authority.LocalProfile) {
		return nil, errors.New("coding remote task tool authority is unavailable")
	}
	if snapshot.Schema != "" && (snapshot.Validate() != nil || snapshot.Grant != authority.Grant ||
		snapshot.GrantRevision != authority.GrantRevision) {
		return nil, errors.New("coding remote task tool snapshot is unavailable")
	}
	probe := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "authority-probe",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     authority.Grant, GrantRevision: authority.GrantRevision,
		ThreadID: authority.ThreadID, SessionKey: authority.SessionKey,
		ProjectKey: authority.ProjectKey, LocalProfile: authority.LocalProfile,
	}
	if err := probe.Validate(); err != nil {
		return nil, errors.New("coding remote task tool authority is invalid")
	}
	return &CodingRemoteTaskTool{
		client: client, authority: authority, snapshot: cloneCapabilitySnapshot(snapshot),
		links: make(map[string]codingRemoteTaskLink), starts: make(map[string]codingRemoteTaskStartPreparation),
	}, nil
}

func codingRemoteTaskClientNil(client CodingRemoteTaskClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (*CodingRemoteTaskTool) Name() string { return "remote_coding_task" }

func (*CodingRemoteTaskTool) Description() string {
	return "Start or control one durable coding task on an explicitly granted paired companion. " +
		"Use status to recover progress or an uncertain call, steer for additional guidance, answer only the " +
		"exact current blocking question, and cancel to request termination. Remote placement is explicit. " +
		"A failed or uncertain effect must not be replayed; use status or cancel with the retained task_id."
}

func (tool *CodingRemoteTaskTool) Parameters() map[string]any {
	snapshot := tool.currentTaskSnapshot()
	scopes := make([]string, 0, len(snapshot.TaskScopes))
	profiles := make([]string, 0, 5)
	for _, scope := range snapshot.TaskScopes {
		scopes = append(scopes, scope.Alias)
		for _, profile := range scope.Profiles {
			profiles = append(profiles, string(profile))
		}
	}
	tool.mu.RLock()
	for _, link := range tool.links {
		scopes = append(scopes, link.result.Scope)
		profiles = append(profiles, string(link.result.Profile))
	}
	tool.mu.RUnlock()
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	slices.Sort(profiles)
	profiles = slices.Compact(profiles)
	scopeProperty := map[string]any{
		"type": "string", "description": "Exact granted remote coding scope returned by discovery.",
	}
	if len(scopes) > 0 {
		scopeProperty["enum"] = scopes
	}
	profileProperty := map[string]any{
		"type": "string", "description": "Exact task profile granted for the selected scope.",
	}
	if len(profiles) > 0 {
		profileProperty["enum"] = profiles
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "enum": []string{"start", "status", "steer", "answer", "cancel"},
			},
			"task_id": map[string]any{
				"type": "string", "description": "Retained task ID; required except for start.",
			},
			"scope":   scopeProperty,
			"profile": profileProperty,
			"objective": map[string]any{
				"type": "string", "description": "Bounded remote-worker objective; required for start.",
			},
			"done_criteria": map[string]any{
				"type": "string", "description": "Optional bounded worker-verifiable completion criteria.",
			},
			"text": map[string]any{
				"type": "string", "description": "Bounded guidance for steer or exact answer text for answer.",
			},
			"question_id": map[string]any{
				"type": "string", "description": "Exact current question ID; required for answer.",
			},
			"question_revision": map[string]any{
				"type": "integer", "minimum": 1, "description": "Exact current question revision.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (tool *CodingRemoteTaskTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	action := strings.TrimSpace(stringToolArgument(args, "action"))
	if !validCodingRemoteTaskAction(action) {
		return nil
	}
	command := "remote coding task"
	if action == "start" {
		command = strings.TrimSpace(stringToolArgument(args, "scope")) + "/" +
			strings.TrimSpace(stringToolArgument(args, "profile"))
	} else if link, found := tool.taskLink(strings.TrimSpace(stringToolArgument(args, "task_id"))); found {
		command = codingRemoteTaskCommand(link.result)
	}
	return toolshared.SanitizeToolObservation(&toolshared.ToolObservation{
		Command: &toolshared.CommandObservation{
			Action: action, Command: command, Source: "remote", Status: "running",
		},
	})
}

func (tool *CodingRemoteTaskTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if tool == nil || codingRemoteTaskClientNil(tool.client) {
		return remoteToolError("BROKER_UNAVAILABLE", "remote coding task broker is unavailable")
	}
	action := strings.TrimSpace(stringToolArgument(args, "action"))
	switch action {
	case "start":
		return tool.start(ctx, args)
	case "status", "steer", "answer", "cancel":
		return tool.control(ctx, action, args)
	default:
		return remoteToolError("INVALID_ACTION", "select start, status, steer, answer, or cancel")
	}
}

func (tool *CodingRemoteTaskTool) start(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if !validCodingRemoteTaskStartArguments(args) {
		return remoteToolError(
			"INVALID_ARGUMENTS",
			"start requires scope, profile, objective, and optional done_criteria",
		)
	}
	principal, providerCallID, err := tool.taskIdentity(ctx)
	if err != nil {
		return remoteToolError("IDENTITY_UNAVAILABLE", err.Error())
	}
	preparationKey := trustedCallID(principal, providerCallID)
	preparation, prepared := tool.takeStartPreparation(preparationKey)
	if !prepared {
		if _, err = tool.DurableStartRecovery(ctx, args); err != nil {
			return remoteToolError("START_PREPARATION_UNAVAILABLE", "remote coding task start could not be prepared")
		}
		preparation, prepared = tool.takeStartPreparation(preparationKey)
	}
	if !prepared {
		return remoteToolError("START_PREPARATION_UNAVAILABLE", "remote coding task start could not be prepared")
	}
	if preparation.failure != nil {
		return preparation.failure
	}
	request := preparation.request
	prebound := preparation.prebound
	if request.CallID != preparationKey || request.TaskScope != strings.TrimSpace(stringToolArgument(args, "scope")) ||
		request.TaskProfile != codingscope.Profile(strings.TrimSpace(stringToolArgument(args, "profile"))) ||
		request.TaskObjective != strings.TrimSpace(stringToolArgument(args, "objective")) ||
		request.TaskDoneCriteria != strings.TrimSpace(stringToolArgument(args, "done_criteria")) {
		tool.forgetTaskResult(prebound)
		return remoteToolError(
			"START_PREPARATION_CHANGED",
			"remote coding task start arguments changed after preparation",
		)
	}
	if err = tool.admitNewTaskLink(prebound); err != nil {
		return remoteToolError("TASK_LINK_LIMIT", err.Error())
	}
	if !tool.retainTaskResult(prebound) {
		return remoteToolError("TASK_LINK_LIMIT", "remote coding task link could not be retained safely")
	}
	result, err := tool.client.Task(ctx, request)
	if err != nil {
		if codingRemoteTaskEffectUncertain("start", err) {
			prebound.ErrorCode = codingRemoteTaskErrorCode(err)
			_ = tool.retainTaskResult(prebound)
			return codingRemoteTaskResultForTool(prebound)
		}
		return tool.codingRemoteTaskErrorForTool("start", prebound, err, false)
	}
	projected, ok := tool.projectTaskResult("start", request, preparation.target, result)
	if !ok {
		prebound.ErrorCode = "TASK_RESULT_UNAVAILABLE"
		_ = tool.retainTaskResult(prebound)
		return codingRemoteTaskResultForTool(prebound)
	}
	if !tool.retainTaskResult(projected) {
		return remoteToolError("TASK_RESULT_UNAVAILABLE", "remote coding task identity conflicts")
	}
	return codingRemoteTaskResultForTool(projected)
}

// DurableStartRecovery freezes exact task authority and stable identity before
// the agent journals its non-idempotent start marker. It performs discovery
// only; the remote task effect remains in Execute.
func (tool *CodingRemoteTaskTool) DurableStartRecovery(
	ctx context.Context,
	args map[string]any,
) (string, error) {
	if strings.TrimSpace(stringToolArgument(args, "action")) != "start" {
		return "", nil
	}
	if !validCodingRemoteTaskStartArguments(args) {
		return "", nil
	}
	principal, providerCallID, err := tool.taskIdentity(ctx)
	if err != nil {
		return "", err
	}
	key := trustedCallID(principal, providerCallID)
	if prepared, found := tool.startPreparation(key); found {
		return codingRemoteTaskStartRecovery(prepared), nil
	}
	prepared := tool.prepareStart(ctx, principal, providerCallID, args)
	if !tool.retainStartPreparation(key, prepared) {
		return "", errors.New("too many remote coding task starts are awaiting execution")
	}
	prepared, _ = tool.startPreparation(key)
	return codingRemoteTaskStartRecovery(prepared), nil
}

func (tool *CodingRemoteTaskTool) prepareStart(
	ctx context.Context,
	principal runtimecap.Principal,
	providerCallID string,
	args map[string]any,
) codingRemoteTaskStartPreparation {
	snapshot, err := tool.refreshTaskSnapshot(ctx)
	if err != nil {
		return codingRemoteTaskStartPreparation{failure: remoteBrokerToolError(err)}
	}
	scopeAlias := strings.TrimSpace(stringToolArgument(args, "scope"))
	profile := codingscope.Profile(strings.TrimSpace(stringToolArgument(args, "profile")))
	descriptor, found := codingRemoteTaskScope(snapshot, scopeAlias)
	if !found || !slices.Contains(descriptor.Profiles, profile) {
		return codingRemoteTaskStartPreparation{failure: remoteToolError(
			"TASK_SCOPE_UNAVAILABLE",
			"remote coding task scope is unavailable; refresh discovery",
		)}
	}
	if descriptor.Availability != codingremote.AvailabilityAvailable {
		return codingRemoteTaskStartPreparation{
			failure: remoteToolError("TASK_SCOPE_OFFLINE", "remote coding task scope is offline"),
		}
	}
	objective := strings.TrimSpace(stringToolArgument(args, "objective"))
	doneCriteria := strings.TrimSpace(stringToolArgument(args, "done_criteria"))
	if !validCodingRemoteTaskText(objective, codingremote.MaxTaskObjectiveBytes, true) ||
		!validCodingRemoteTaskText(doneCriteria, codingremote.MaxTaskDoneCriteriaBytes, false) ||
		len(objective)+len(doneCriteria) > codingremote.MaxTaskTextBytes {
		return codingRemoteTaskStartPreparation{
			failure: remoteToolError("INVALID_ARGUMENTS", "remote coding task content is invalid or too large"),
		}
	}
	request := tool.taskRequest(
		ctx,
		principal,
		providerCallID,
		codingremote.OperationTaskStart,
		snapshot.DiscoveryRevision,
	)
	request.TaskScope = descriptor.Alias
	request.TaskScopeRevision = descriptor.Revision
	request.TaskProfile = profile
	request.TaskObjective = objective
	request.TaskDoneCriteria = doneCriteria
	request.TaskID = codingremote.DeriveTaskID(request)
	if err = request.Validate(); err != nil {
		return codingRemoteTaskStartPreparation{
			failure: remoteToolError("INVALID_ARGUMENTS", "remote coding task start authority is invalid"),
		}
	}
	prebound := codingRemoteTaskToolResult{
		Schema: codingRemoteTaskResultSchema, Placement: "remote", Action: "start",
		Outcome: "uncertain", Retained: true, OwnerThreadID: tool.authority.ThreadID,
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision:        request.DiscoveryRevision,
		BindingDiscoveryRevision: request.DiscoveryRevision,
		TaskID:                   request.TaskID, Scope: descriptor.Alias, ScopeRevision: descriptor.Revision,
		Target: descriptor.Target, Profile: profile,
		RecoveryAction: "Call remote_coding_task status or cancel with this task_id; do not replay start.",
	}
	return codingRemoteTaskStartPreparation{request: request, target: descriptor.Target, prebound: prebound}
}

func codingRemoteTaskStartRecovery(prepared codingRemoteTaskStartPreparation) string {
	if prepared.failure != nil || prepared.prebound.TaskID == "" {
		return ""
	}
	return codingRemoteTaskResultForTool(prepared.prebound).ContentForLLM()
}

func (tool *CodingRemoteTaskTool) startPreparation(
	key string,
) (codingRemoteTaskStartPreparation, bool) {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	prepared, found := tool.starts[key]
	prepared.prebound = cloneCodingRemoteTaskToolResult(prepared.prebound)
	return prepared, found
}

func (tool *CodingRemoteTaskTool) retainStartPreparation(
	key string,
	prepared codingRemoteTaskStartPreparation,
) bool {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if _, found := tool.starts[key]; found {
		return true
	}
	if len(tool.starts) >= codingRemoteTaskMaxStartPreparations {
		return false
	}
	tool.starts[key] = prepared
	return true
}

func (tool *CodingRemoteTaskTool) takeStartPreparation(
	key string,
) (codingRemoteTaskStartPreparation, bool) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	prepared, found := tool.starts[key]
	delete(tool.starts, key)
	prepared.prebound = cloneCodingRemoteTaskToolResult(prepared.prebound)
	return prepared, found
}

func (tool *CodingRemoteTaskTool) control(
	ctx context.Context,
	action string,
	args map[string]any,
) *toolshared.ToolResult {
	if !validCodingRemoteTaskControlArguments(action, args) {
		return remoteToolError("INVALID_ARGUMENTS", "remote coding task control arguments are invalid")
	}
	principal, providerCallID, err := tool.taskIdentity(ctx)
	if err != nil {
		return remoteToolError("IDENTITY_UNAVAILABLE", err.Error())
	}
	taskID := strings.TrimSpace(stringToolArgument(args, "task_id"))
	link, found := tool.taskLink(taskID)
	if !found {
		return remoteToolError("TASK_LINK_UNAVAILABLE", "remote coding task is not retained by this thread")
	}
	discoveryRevision := link.result.BindingDiscoveryRevision
	if action == "steer" || action == "answer" {
		snapshot, refreshErr := tool.refreshTaskSnapshot(ctx)
		if refreshErr != nil {
			return tool.codingRemoteTaskErrorForTool(action, link.result, refreshErr, true)
		}
		descriptor, current := codingRemoteTaskScope(snapshot, link.result.Scope)
		if !current || descriptor.Revision != link.result.ScopeRevision ||
			descriptor.Target != link.result.Target || !slices.Contains(descriptor.Profiles, link.result.Profile) {
			return tool.codingRemoteTaskErrorForTool(
				action,
				link.result,
				&codingremote.BrokerError{Code: "TASK_SCOPE_CHANGED"},
				true,
			)
		}
		discoveryRevision = snapshot.DiscoveryRevision
	}
	request := tool.taskRequest(
		ctx,
		principal,
		providerCallID,
		codingRemoteTaskOperation(action),
		discoveryRevision,
	)
	request.TaskID = link.result.TaskID
	request.TaskScope = link.result.Scope
	request.TaskScopeRevision = link.result.ScopeRevision
	request.TaskProfile = link.result.Profile
	if action == "steer" || action == "answer" {
		request.TaskText = strings.TrimSpace(stringToolArgument(args, "text"))
		if !validCodingRemoteTaskText(request.TaskText, codingremote.MaxTaskTextBytes, true) {
			return remoteToolError("INVALID_ARGUMENTS", "remote coding task guidance is invalid or too large")
		}
	}
	if action == "answer" {
		questionRevision, revisionOK := codingRemoteTaskUint64(args["question_revision"])
		questionID := strings.TrimSpace(stringToolArgument(args, "question_id"))
		if !revisionOK || link.result.Question == nil || link.result.Question.ID != questionID ||
			link.result.Question.Revision != questionRevision {
			return remoteToolError("QUESTION_STALE", "remote coding task question is no longer current")
		}
		request.TaskQuestionID = questionID
		request.TaskQuestionRevision = questionRevision
		request.TaskAnswerID = codingremote.DeriveTaskAnswerID(request)
	}
	if err = request.Validate(); err != nil {
		return remoteToolError("INVALID_ARGUMENTS", "remote coding task control authority is invalid")
	}
	result, err := tool.client.Task(ctx, request)
	if err != nil {
		return tool.codingRemoteTaskErrorForTool(
			action,
			link.result,
			err,
			codingRemoteTaskEffectUncertain(action, err),
		)
	}
	projected, ok := tool.projectTaskResult(action, request, link.result.Target, result)
	if !ok {
		return tool.codingRemoteTaskErrorForTool(
			action,
			link.result,
			&codingremote.BrokerError{Code: "TASK_RESULT_UNAVAILABLE"},
			action != "status",
		)
	}
	projected.BindingDiscoveryRevision = link.result.BindingDiscoveryRevision
	if !tool.retainTaskResult(projected) {
		return remoteToolError("TASK_RESULT_UNAVAILABLE", "remote coding task identity conflicts")
	}
	return codingRemoteTaskResultForTool(projected)
}

func (tool *CodingRemoteTaskTool) taskIdentity(
	ctx context.Context,
) (runtimecap.Principal, string, error) {
	runtime, ok := toolshared.RuntimeCapabilities(ctx)
	if !ok {
		return runtimecap.Principal{}, "", errors.New("turn-bound coding identity is unavailable")
	}
	principal, ok := runtime.Principal()
	if !ok || principal.Runtime != runtimecap.KindCoding || principal.Validate() != nil ||
		principal.SessionID != tool.authority.SessionKey {
		return runtimecap.Principal{}, "", errors.New("turn-bound coding identity is unavailable")
	}
	providerCallID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	if providerCallID == "" {
		return runtimecap.Principal{}, "", errors.New("tool-call identity is unavailable")
	}
	return principal, providerCallID, nil
}

func (tool *CodingRemoteTaskTool) taskRequest(
	ctx context.Context,
	principal runtimecap.Principal,
	providerCallID string,
	operation codingremote.Operation,
	discoveryRevision string,
) codingremote.Request {
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Operation: operation, Grant: tool.authority.Grant, GrantRevision: tool.authority.GrantRevision,
		ThreadID: tool.authority.ThreadID, SessionKey: tool.authority.SessionKey,
		ProjectKey: tool.authority.ProjectKey, LocalProfile: tool.authority.LocalProfile,
		Principal: &principal, CallID: trustedCallID(principal, providerCallID),
		DiscoveryRevision: discoveryRevision,
	}
	deadline := time.Now().Add(codingRemoteOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	request.DeadlineUnixMS = deadline.UnixMilli()
	return request
}

func (tool *CodingRemoteTaskTool) refreshTaskSnapshot(
	ctx context.Context,
) (codingremote.CapabilitySnapshot, error) {
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     tool.authority.Grant, GrantRevision: tool.authority.GrantRevision,
		ThreadID: tool.authority.ThreadID, SessionKey: tool.authority.SessionKey,
		ProjectKey: tool.authority.ProjectKey, LocalProfile: tool.authority.LocalProfile,
	}
	refreshed, err := tool.client.Discover(ctx, request)
	if err != nil {
		return codingremote.CapabilitySnapshot{}, err
	}
	if refreshed.Validate() != nil || refreshed.Grant != tool.authority.Grant ||
		refreshed.GrantRevision != tool.authority.GrantRevision {
		return codingremote.CapabilitySnapshot{}, &codingremote.BrokerError{Code: "RESULT_UNAVAILABLE"}
	}
	tool.mu.Lock()
	tool.snapshot = cloneCapabilitySnapshot(refreshed)
	tool.mu.Unlock()
	return cloneCapabilitySnapshot(refreshed), nil
}

func (tool *CodingRemoteTaskTool) currentTaskSnapshot() codingremote.CapabilitySnapshot {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return cloneCapabilitySnapshot(tool.snapshot)
}

func codingRemoteTaskScope(
	snapshot codingremote.CapabilitySnapshot,
	alias string,
) (codingremote.TaskScopeDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		snapshot.TaskScopes,
		alias,
		func(value codingremote.TaskScopeDescriptor, wanted string) int {
			return strings.Compare(value.Alias, wanted)
		},
	)
	if !found {
		return codingremote.TaskScopeDescriptor{}, false
	}
	return snapshot.TaskScopes[index], true
}

func (tool *CodingRemoteTaskTool) projectTaskResult(
	action string,
	request codingremote.Request,
	expectedTarget string,
	result codingremote.TaskResult,
) (codingRemoteTaskToolResult, bool) {
	if result.Validate() != nil || result.Grant != request.Grant ||
		result.GrantRevision != request.GrantRevision ||
		result.DiscoveryRevision != request.DiscoveryRevision || result.TaskID != request.TaskID ||
		result.Scope != request.TaskScope || result.Profile != request.TaskProfile ||
		result.Target != expectedTarget {
		return codingRemoteTaskToolResult{}, false
	}
	return codingRemoteTaskToolResult{
		Schema: codingRemoteTaskResultSchema, Placement: "remote", Action: action,
		Outcome: "observed", Retained: true, OwnerThreadID: tool.authority.ThreadID,
		Grant: result.Grant, GrantRevision: result.GrantRevision,
		DiscoveryRevision:        result.DiscoveryRevision,
		BindingDiscoveryRevision: result.DiscoveryRevision,
		TaskID:                   result.TaskID, GenerationID: result.GenerationID,
		Scope: result.Scope, ScopeRevision: request.TaskScopeRevision,
		Target: result.Target, Profile: result.Profile,
		TaskStatus: result.Status, NodeState: result.NodeState,
		RemoteThreadID: result.ThreadID, WorkerGenerationID: result.WorkerGenerationID,
		Activity: result.Activity, Progress: result.Progress,
		Branch: result.Branch, HandoffID: result.HandoffID,
		FailureCode: result.FailureCode, Question: cloneCodingRemoteTaskQuestion(result.Question),
		TerminalSummary: result.TerminalSummary,
	}, true
}

func cloneCodingRemoteTaskQuestion(question *codingremote.TaskQuestion) *codingremote.TaskQuestion {
	if question == nil {
		return nil
	}
	cloned := *question
	cloned.Options = append([]codingremote.TaskQuestionOption(nil), question.Options...)
	return &cloned
}

func (tool *CodingRemoteTaskTool) admitNewTaskLink(result codingRemoteTaskToolResult) error {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if _, exists := tool.links[result.TaskID]; exists || len(tool.links) < codingRemoteTaskMaxLinks {
		return nil
	}
	for _, taskID := range tool.order {
		link, found := tool.links[taskID]
		if found && codingRemoteTaskTerminal(link.result.TaskStatus) {
			delete(tool.links, taskID)
			tool.removeTaskOrderLocked(taskID)
			return nil
		}
	}
	return errors.New("too many active remote coding task links; finish or cancel an existing task")
}

func (tool *CodingRemoteTaskTool) retainTaskResult(result codingRemoteTaskToolResult) bool {
	if result.validate() != nil || !result.Retained || result.OwnerThreadID != tool.authority.ThreadID ||
		result.Grant != tool.authority.Grant || result.GrantRevision != tool.authority.GrantRevision {
		return false
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if existing, found := tool.links[result.TaskID]; found {
		if !sameCodingRemoteTaskBinding(existing.result, result) {
			return false
		}
		tool.links[result.TaskID] = codingRemoteTaskLink{result: cloneCodingRemoteTaskToolResult(result)}
		return true
	}
	if len(tool.links) >= codingRemoteTaskMaxLinks {
		for _, taskID := range tool.order {
			link := tool.links[taskID]
			if codingRemoteTaskTerminal(link.result.TaskStatus) {
				delete(tool.links, taskID)
				tool.removeTaskOrderLocked(taskID)
				break
			}
		}
	}
	if len(tool.links) >= codingRemoteTaskMaxLinks {
		tool.retentionFailed = true
		return false
	}
	tool.links[result.TaskID] = codingRemoteTaskLink{result: cloneCodingRemoteTaskToolResult(result)}
	tool.order = append(tool.order, result.TaskID)
	return true
}

func (tool *CodingRemoteTaskTool) removeTaskOrderLocked(taskID string) {
	tool.order = slices.DeleteFunc(tool.order, func(value string) bool { return value == taskID })
}

func (tool *CodingRemoteTaskTool) taskLink(taskID string) (codingRemoteTaskLink, bool) {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	link, found := tool.links[taskID]
	link.result = cloneCodingRemoteTaskToolResult(link.result)
	return link, found
}

func (tool *CodingRemoteTaskTool) forgetTaskResult(result codingRemoteTaskToolResult) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	existing, found := tool.links[result.TaskID]
	if !found || !sameCodingRemoteTaskBinding(existing.result, result) {
		return
	}
	delete(tool.links, result.TaskID)
	tool.removeTaskOrderLocked(result.TaskID)
}

func sameCodingRemoteTaskBinding(left, right codingRemoteTaskToolResult) bool {
	return left.OwnerThreadID == right.OwnerThreadID && left.Grant == right.Grant &&
		left.GrantRevision == right.GrantRevision &&
		left.BindingDiscoveryRevision == right.BindingDiscoveryRevision &&
		left.TaskID == right.TaskID && left.Scope == right.Scope &&
		left.ScopeRevision == right.ScopeRevision && left.Target == right.Target &&
		left.Profile == right.Profile &&
		(left.GenerationID == "" || right.GenerationID == "" || left.GenerationID == right.GenerationID)
}

func cloneCodingRemoteTaskToolResult(result codingRemoteTaskToolResult) codingRemoteTaskToolResult {
	cloned := result
	cloned.Question = cloneCodingRemoteTaskQuestion(result.Question)
	return cloned
}

func (tool *CodingRemoteTaskTool) codingRemoteTaskErrorForTool(
	action string,
	base codingRemoteTaskToolResult,
	err error,
	uncertain bool,
) *toolshared.ToolResult {
	result := cloneCodingRemoteTaskToolResult(base)
	result.Schema = codingRemoteTaskResultSchema
	result.Placement = "remote"
	result.Action = action
	result.ErrorCode = codingRemoteTaskErrorCode(err)
	result.Question = nil
	if uncertain {
		result.Outcome = "uncertain"
		result.Retained = true
		result.RecoveryAction = "Call remote_coding_task status or cancel with this task_id; do not replay the operation."
	} else {
		result.Outcome = codingRemoteTaskErrorOutcome(err)
		result.RecoveryAction = "Inspect current discovery or task status before attempting another effect."
		if action == "start" {
			result.Retained = false
			tool.forgetTaskResult(base)
		}
	}
	if result.Retained && !tool.retainTaskResult(result) {
		return remoteToolError("TASK_RESULT_UNAVAILABLE", "remote coding task identity conflicts")
	}
	return codingRemoteTaskResultForTool(result)
}

func codingRemoteTaskEffectUncertain(action string, err error) bool {
	if action == "status" {
		return false
	}
	var brokerErr *codingremote.BrokerError
	if !errors.As(err, &brokerErr) {
		return true
	}
	switch brokerErr.Code {
	case "TASK_UNAVAILABLE", "TASK_RESULT_UNAVAILABLE", "INVOCATION_UNCERTAIN":
		return true
	default:
		return false
	}
}

func codingRemoteTaskErrorCode(err error) string {
	code := "BROKER_UNAVAILABLE"
	var brokerErr *codingremote.BrokerError
	if errors.As(err, &brokerErr) && safeBrokerCodePattern.MatchString(brokerErr.Code) {
		code = brokerErr.Code
	}
	return code
}

func codingRemoteTaskErrorOutcome(err error) string {
	code := codingRemoteTaskErrorCode(err)
	switch code {
	case "BROKER_UNAVAILABLE", "BROKER_DISABLED", "TASK_COORDINATOR_UNAVAILABLE", "TASK_UNAVAILABLE":
		return "offline"
	case "DISCOVERY_STALE", "GRANT_CHANGED", "TASK_SCOPE_CHANGED":
		return "stale"
	default:
		return "denied"
	}
}

func codingRemoteTaskResultForTool(result codingRemoteTaskToolResult) *toolshared.ToolResult {
	if result.validate() != nil {
		return remoteToolError("TASK_RESULT_UNAVAILABLE", "remote coding task result is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return remoteToolError("TASK_RESULT_UNAVAILABLE", "remote coding task result is unavailable")
	}
	toolResult := toolshared.NewToolResult(string(encoded))
	toolResult.IsError = result.Outcome != "observed" ||
		slices.Contains([]string{"failed", "timed_out", "lost"}, result.TaskStatus)
	toolResult.WithObservation(codingRemoteTaskObservation(result))
	return toolResult
}

func codingRemoteTaskObservation(result codingRemoteTaskToolResult) toolshared.CommandObservation {
	status := "running"
	if result.Outcome != "observed" {
		status = "failed"
	} else {
		switch result.TaskStatus {
		case "succeeded":
			status = "succeeded"
		case "failed", "lost":
			status = "failed"
		case "timed_out":
			status = "timed_out"
		case string(taskregistry.StatusCancelled):
			status = "canceled"
		}
	}
	return toolshared.CommandObservation{
		Action: result.Action, Command: codingRemoteTaskCommand(result), Source: "remote",
		Status: status, Canceled: result.TaskStatus == string(taskregistry.StatusCancelled),
		TimedOut: result.TaskStatus == "timed_out", Output: codingRemoteTaskObservationOutput(result),
	}
}

func codingRemoteTaskCommand(result codingRemoteTaskToolResult) string {
	return fmt.Sprintf(
		"remote task %s on %s (%s, %s)",
		result.TaskID,
		result.Target,
		result.Scope,
		result.Profile,
	)
}

func codingRemoteTaskObservationOutput(result codingRemoteTaskToolResult) string {
	lines := []string{
		"placement=remote", "outcome=" + result.Outcome, "task_id=" + result.TaskID,
		"target=" + result.Target, "scope=" + result.Scope, "profile=" + string(result.Profile),
	}
	for _, field := range []struct{ name, value string }{
		{name: "task_status", value: result.TaskStatus},
		{name: "node_state", value: result.NodeState},
		{name: "remote_thread_id", value: result.RemoteThreadID},
		{name: "branch", value: result.Branch},
		{name: "handoff_id", value: result.HandoffID},
		{name: "failure_code", value: result.FailureCode},
		{name: "error_code", value: result.ErrorCode},
		{name: "progress", value: result.Progress},
		{name: "terminal_summary", value: result.TerminalSummary},
		{name: "recovery", value: result.RecoveryAction},
	} {
		if field.value != "" {
			lines = append(lines, field.name+"="+field.value)
		}
	}
	if result.Question != nil {
		lines = append(
			lines,
			"question_id="+result.Question.ID,
			"question_revision="+strconv.FormatUint(result.Question.Revision, 10),
		)
	}
	return strings.Join(lines, "\n")
}

func (result codingRemoteTaskToolResult) validate() error {
	if result.Schema != codingRemoteTaskResultSchema || result.Placement != "remote" ||
		!validCodingRemoteTaskAction(result.Action) || !validCodingRemoteTaskOutcome(result.Outcome) ||
		!codingRemoteTaskIdentifier.MatchString(result.OwnerThreadID) || !codingremote.ValidAlias(result.Grant) ||
		!codingRemoteTaskIdentifier.MatchString(result.GrantRevision) ||
		!codingRemoteTaskIdentifier.MatchString(result.DiscoveryRevision) ||
		!codingRemoteTaskIdentifier.MatchString(result.BindingDiscoveryRevision) ||
		!codingRemoteTaskIdentifier.MatchString(result.TaskID) || !codingremote.ValidAlias(result.Scope) ||
		!codingRemoteTaskIdentifier.MatchString(result.ScopeRevision) || !codingremote.ValidAlias(result.Target) ||
		!result.Profile.AdmittedInV5() ||
		(result.ErrorCode != "" && !safeBrokerCodePattern.MatchString(result.ErrorCode)) ||
		len(result.RecoveryAction) > 2048 || !utf8.ValidString(result.RecoveryAction) {
		return errors.New("invalid remote coding task result")
	}
	hasProjection := result.GenerationID != "" || result.TaskStatus != ""
	if hasProjection {
		remote := result.remoteTaskResult()
		if result.GenerationID == "" || result.TaskStatus == "" || remote.Validate() != nil {
			return errors.New("invalid remote coding task projection")
		}
	} else if result.NodeState != "" || result.RemoteThreadID != "" || result.WorkerGenerationID != "" ||
		result.Activity != "" || result.Progress != "" || result.Branch != "" || result.HandoffID != "" ||
		result.FailureCode != "" || result.Question != nil || result.TerminalSummary != "" {
		return errors.New("incomplete remote coding task projection")
	}
	if result.Outcome == "observed" {
		if !hasProjection || result.ErrorCode != "" || result.RecoveryAction != "" || !result.Retained {
			return errors.New("invalid observed remote coding task result")
		}
		return nil
	}
	if result.Outcome == "uncertain" && (!result.Retained || result.RecoveryAction == "") {
		return errors.New("invalid uncertain remote coding task result")
	}
	return nil
}

func (result codingRemoteTaskToolResult) remoteTaskResult() codingremote.TaskResult {
	return codingremote.TaskResult{
		Grant: result.Grant, GrantRevision: result.GrantRevision,
		DiscoveryRevision: result.DiscoveryRevision,
		TaskID:            result.TaskID, GenerationID: result.GenerationID,
		Scope: result.Scope, Target: result.Target, Profile: result.Profile,
		Status: result.TaskStatus, NodeState: result.NodeState,
		ThreadID: result.RemoteThreadID, WorkerGenerationID: result.WorkerGenerationID,
		Activity: result.Activity, Progress: result.Progress,
		Branch: result.Branch, HandoffID: result.HandoffID,
		FailureCode: result.FailureCode, Question: cloneCodingRemoteTaskQuestion(result.Question),
		TerminalSummary: result.TerminalSummary,
	}
}

func validCodingRemoteTaskAction(action string) bool {
	return slices.Contains([]string{"start", "status", "steer", "answer", "cancel"}, action)
}

func validCodingRemoteTaskOutcome(outcome string) bool {
	return slices.Contains([]string{"observed", "denied", "offline", "stale", "uncertain"}, outcome)
}

func validCodingRemoteTaskControlArguments(action string, args map[string]any) bool {
	switch action {
	case "status", "cancel":
		return len(args) == 2 && strings.TrimSpace(stringToolArgument(args, "task_id")) != ""
	case "steer":
		return len(args) == 3 && strings.TrimSpace(stringToolArgument(args, "task_id")) != "" &&
			strings.TrimSpace(stringToolArgument(args, "text")) != ""
	case "answer":
		_, revisionOK := codingRemoteTaskUint64(args["question_revision"])
		return len(args) == 5 && strings.TrimSpace(stringToolArgument(args, "task_id")) != "" &&
			strings.TrimSpace(stringToolArgument(args, "text")) != "" &&
			strings.TrimSpace(stringToolArgument(args, "question_id")) != "" && revisionOK
	default:
		return false
	}
}

func validCodingRemoteTaskStartArguments(args map[string]any) bool {
	if len(args) < 4 || len(args) > 5 || strings.TrimSpace(stringToolArgument(args, "scope")) == "" ||
		strings.TrimSpace(stringToolArgument(args, "profile")) == "" ||
		strings.TrimSpace(stringToolArgument(args, "objective")) == "" {
		return false
	}
	for key := range args {
		if !slices.Contains([]string{"action", "scope", "profile", "objective", "done_criteria"}, key) {
			return false
		}
	}
	return true
}

func codingRemoteTaskUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 1 || typed > math.MaxUint64 || typed != math.Trunc(typed) {
			return 0, false
		}
		return uint64(typed), true
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil && parsed > 0
	case uint64:
		return typed, typed > 0
	case int:
		return uint64(typed), typed > 0
	default:
		return 0, false
	}
}

func validCodingRemoteTaskText(value string, maximum int, required bool) bool {
	if required && value == "" || len(value) > maximum || !utf8.ValidString(value) ||
		value != strings.TrimSpace(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(character rune) bool {
		return character < 0x20 && character != '\n' && character != '\t'
	})
}

func codingRemoteTaskOperation(action string) codingremote.Operation {
	switch action {
	case "status":
		return codingremote.OperationTaskStatus
	case "steer":
		return codingremote.OperationTaskSteer
	case "answer":
		return codingremote.OperationTaskAnswer
	case "cancel":
		return codingremote.OperationTaskCancel
	default:
		return ""
	}
}

func codingRemoteTaskTerminal(status string) bool {
	return slices.Contains(
		[]string{"succeeded", "failed", "timed_out", string(taskregistry.StatusCancelled), "lost"},
		status,
	)
}

func (tool *CodingRemoteTaskTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*CodingRemoteTaskTool) DurableArguments(args map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) > codingremote.MaxTaskTextBytes+codingremote.MaxTaskObjectiveBytes {
		return nil, errors.New("remote coding task arguments are unavailable")
	}
	projected := make(map[string]any, len(args))
	if err = json.Unmarshal(encoded, &projected); err != nil {
		return nil, errors.New("remote coding task arguments are unavailable")
	}
	return projected, nil
}

func (*CodingRemoteTaskTool) ProtectedDurableArguments(map[string]any) bool { return false }

func (*CodingRemoteTaskTool) ProtectedDurableResult(map[string]any) bool { return false }

// RestoreHistory rebuilds bounded task links only from canonical tool
// call/result pairs. Results copied into a fork remain historical because the
// owner thread ID cannot match the new runtime authority.
func (tool *CodingRemoteTaskTool) RestoreHistory(history []providers.Message) {
	if tool == nil {
		return
	}
	tool.mu.Lock()
	tool.links = make(map[string]codingRemoteTaskLink)
	tool.starts = make(map[string]codingRemoteTaskStartPreparation)
	tool.order = nil
	tool.inheritedLinks = 0
	tool.inheritedOverflow = false
	tool.retentionFailed = false
	tool.mu.Unlock()
	toolNames := make(map[string]string)
	inherited := make(map[string]struct{})
	inheritedOverflow := false
	for _, message := range history {
		if message.Role == "assistant" {
			toolNames = make(map[string]string)
			for _, call := range message.ToolCalls {
				if call.ID != "" && call.Name == tool.Name() && len(toolNames) < codingRemoteTaskMaxLinks {
					toolNames[call.ID] = call.Name
				}
			}
			continue
		}
		toolName, pending := toolNames[message.ToolCallID]
		if message.Role != "tool" || !pending {
			continue
		}
		delete(toolNames, message.ToolCallID)
		if toolName != tool.Name() ||
			(message.ToolResultStatus != providers.ToolResultStatusSuccess &&
				message.ToolResultStatus != providers.ToolResultStatusError) {
			continue
		}
		result, ok := decodeCodingRemoteTaskToolResult(message.Content)
		if !ok || !result.Retained || result.Grant != tool.authority.Grant ||
			result.GrantRevision != tool.authority.GrantRevision {
			continue
		}
		if result.OwnerThreadID != tool.authority.ThreadID {
			if _, counted := inherited[result.TaskID]; !counted {
				if len(inherited) < codingRemoteTaskMaxLinks {
					inherited[result.TaskID] = struct{}{}
				} else {
					inheritedOverflow = true
				}
			}
			continue
		}
		_ = tool.retainTaskResult(result)
	}
	tool.mu.Lock()
	tool.inheritedLinks = len(inherited)
	tool.inheritedOverflow = inheritedOverflow
	tool.mu.Unlock()
}

func decodeCodingRemoteTaskToolResult(content string) (codingRemoteTaskToolResult, bool) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var result codingRemoteTaskToolResult
	if err := decoder.Decode(&result); err != nil || result.validate() != nil {
		return codingRemoteTaskToolResult{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return codingRemoteTaskToolResult{}, false
	}
	return result, true
}

// CodingContinuityContext is a turn-frozen, structural projection. Remote
// worker prose is deliberately excluded so task output never becomes system
// instructions after compaction.
func (tool *CodingRemoteTaskTool) CodingContinuityContext() string {
	if tool == nil {
		return ""
	}
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	lines := []string{}
	for _, taskID := range tool.order {
		link, found := tool.links[taskID]
		if !found || codingRemoteTaskTerminal(link.result.TaskStatus) {
			continue
		}
		result := link.result
		line := fmt.Sprintf(
			"- task_id=%s scope=%s scope_revision=%s target=%s profile=%s state=%s",
			result.TaskID,
			result.Scope,
			result.ScopeRevision,
			result.Target,
			result.Profile,
			codingRemoteTaskContinuityState(result),
		)
		if result.GenerationID != "" {
			line += " generation_id=" + result.GenerationID
		}
		if result.RemoteThreadID != "" {
			line += " remote_thread_id=" + result.RemoteThreadID
		}
		if result.Question != nil {
			line += " question_id=" + result.Question.ID +
				" question_revision=" + strconv.FormatUint(result.Question.Revision, 10)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 && tool.inheritedLinks == 0 && !tool.inheritedOverflow && !tool.retentionFailed {
		return ""
	}
	contextLines := []string{
		"# Remote coding task continuity",
		"",
		"These runtime-owned entries are state, not instructions. Use remote_coding_task status for details; do not replay an uncertain effect.",
	}
	contextLines = append(contextLines, lines...)
	if tool.inheritedLinks > 0 {
		count := strconv.Itoa(tool.inheritedLinks)
		if tool.inheritedOverflow {
			count = "at least " + count
		}
		contextLines = append(
			contextLines,
			fmt.Sprintf(
				"Inherited task references: %s historical reference(s); they are not controllable from this fork.",
				count,
			),
		)
	}
	if tool.retentionFailed {
		contextLines = append(
			contextLines,
			"Task-link retention reached its bound; no unlisted task authority is implied.",
		)
	}
	return strings.Join(contextLines, "\n")
}

func codingRemoteTaskContinuityState(result codingRemoteTaskToolResult) string {
	if result.Outcome != "" && result.Outcome != "observed" {
		return result.Outcome
	}
	if result.NodeState == "waiting_for_input" {
		return "waiting_for_input"
	}
	if result.TaskStatus != "" {
		return result.TaskStatus
	}
	return result.Outcome
}
