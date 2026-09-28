package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeCodingRemoteTaskClient struct {
	snapshot      codingremote.CapabilitySnapshot
	discoverErr   error
	discoverCalls []codingremote.Request
	taskCalls     []codingremote.Request
	task          func(codingremote.Request) (codingremote.TaskResult, error)
}

func (client *fakeCodingRemoteTaskClient) Discover(
	_ context.Context,
	request codingremote.Request,
) (codingremote.CapabilitySnapshot, error) {
	client.discoverCalls = append(client.discoverCalls, request)
	return client.snapshot, client.discoverErr
}

func (client *fakeCodingRemoteTaskClient) Task(
	_ context.Context,
	request codingremote.Request,
) (codingremote.TaskResult, error) {
	client.taskCalls = append(client.taskCalls, request)
	if client.task != nil {
		return client.task(request)
	}
	return codingRemoteTaskTestResult(request), nil
}

func TestCodingRemoteTaskToolUsesExactStartAndControlAuthority(t *testing.T) {
	authority := codingRemoteTaskTestAuthority()
	client := &fakeCodingRemoteTaskClient{snapshot: codingRemoteTaskTestSnapshot("discovery-v1")}
	client.task = func(request codingremote.Request) (codingremote.TaskResult, error) {
		result := codingRemoteTaskTestResult(request)
		if request.Operation == codingremote.OperationTaskStatus {
			result.NodeState = string(codingtask.StateWaitingInput)
			result.Activity = string(codingtask.ActivityWaitingInput)
			result.Question = &codingremote.TaskQuestion{
				ID: "question_1", Revision: 3, Prompt: "Which file should I inspect?",
				Options: []codingremote.TaskQuestionOption{{ID: "agents", Label: "AGENTS.md"}},
			}
		}
		return result, nil
	}
	tool, err := NewCodingRemoteTaskTool(client, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := codingRemoteTaskTestContext(authority, "turn-start", "provider-start")
	started := tool.Execute(ctx, map[string]any{
		"action": "start", "scope": "mintclaw-dev", "profile": "mutate",
		"objective":     "Fix the failing test in an isolated worktree.",
		"done_criteria": "The focused test passes and the source checkout remains clean.",
	})
	if started == nil || started.IsError {
		t.Fatalf("start = %#v", started)
	}
	startResult := decodeCodingRemoteTaskResult(t, started)
	if startResult.Outcome != "observed" || startResult.TaskID == "" ||
		startResult.BindingDiscoveryRevision != "discovery-v1" ||
		startResult.ScopeRevision != "scope-v1" {
		t.Fatalf("start result = %#v", startResult)
	}
	if started.Observation == nil || started.Observation.Command == nil ||
		started.Observation.Command.Source != "remote" ||
		started.Observation.Command.SessionID != "" ||
		!strings.Contains(started.Observation.Command.Output, "task_id="+startResult.TaskID) ||
		!strings.Contains(started.Observation.Command.Output, "placement=remote") {
		t.Fatalf("start observation = %#v", started.Observation)
	}
	if len(client.taskCalls) != 1 {
		t.Fatalf("task calls = %d", len(client.taskCalls))
	}
	startRequest := client.taskCalls[0]
	if startRequest.TaskID != codingremote.DeriveTaskID(startRequest) || startRequest.Principal == nil ||
		startRequest.CallID == "provider-start" || !strings.HasPrefix(startRequest.CallID, "call_") ||
		startRequest.TaskScopeRevision != "scope-v1" || startRequest.DiscoveryRevision != "discovery-v1" {
		t.Fatalf("start authority = %#v", startRequest)
	}

	client.snapshot = codingRemoteTaskTestSnapshot("discovery-v2")
	status := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-status", "provider-status"),
		map[string]any{"action": "status", "task_id": startResult.TaskID},
	)
	statusResult := decodeCodingRemoteTaskResult(t, status)
	if status.IsError || statusResult.Question == nil || statusResult.Question.Revision != 3 ||
		client.taskCalls[1].DiscoveryRevision != "discovery-v1" || len(client.discoverCalls) != 1 {
		t.Fatalf("status = %#v; calls = %#v", statusResult, client.taskCalls)
	}

	steered := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-steer", "provider-steer"),
		map[string]any{
			"action":  "steer",
			"task_id": startResult.TaskID,
			"text":    "Also inspect the recent config change.",
		},
	)
	steerResult := decodeCodingRemoteTaskResult(t, steered)
	if steered.IsError || steerResult.DiscoveryRevision != "discovery-v2" ||
		steerResult.BindingDiscoveryRevision != "discovery-v1" ||
		client.taskCalls[2].Operation != codingremote.OperationTaskSteer ||
		client.taskCalls[2].DiscoveryRevision != "discovery-v2" {
		t.Fatalf("steer = %#v; call = %#v", steerResult, client.taskCalls[2])
	}

	status = tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-status-2", "provider-status-2"),
		map[string]any{"action": "status", "task_id": startResult.TaskID},
	)
	statusResult = decodeCodingRemoteTaskResult(t, status)
	answered := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-answer", "provider-answer"),
		map[string]any{
			"action": "answer", "task_id": startResult.TaskID, "text": "AGENTS.md",
			"question_id": "question_1", "question_revision": float64(3),
		},
	)
	if answered.IsError {
		t.Fatalf("answer = %#v", decodeCodingRemoteTaskResult(t, answered))
	}
	answerRequest := client.taskCalls[4]
	if answerRequest.Operation != codingremote.OperationTaskAnswer ||
		answerRequest.TaskAnswerID != codingremote.DeriveTaskAnswerID(answerRequest) ||
		answerRequest.DiscoveryRevision != "discovery-v2" || statusResult.Question == nil {
		t.Fatalf("answer authority = %#v", answerRequest)
	}

	canceled := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-cancel", "provider-cancel"),
		map[string]any{"action": "cancel", "task_id": startResult.TaskID},
	)
	if canceled.IsError || client.taskCalls[5].Operation != codingremote.OperationTaskCancel ||
		client.taskCalls[5].DiscoveryRevision != "discovery-v1" {
		t.Fatalf("cancel = %#v; call = %#v", canceled, client.taskCalls[5])
	}
}

func TestCodingRemoteTaskToolRetainsUncertainStartAcrossRestartAndIsolatesFork(t *testing.T) {
	authority := codingRemoteTaskTestAuthority()
	client := &fakeCodingRemoteTaskClient{snapshot: codingRemoteTaskTestSnapshot("discovery-v1")}
	client.task = func(codingremote.Request) (codingremote.TaskResult, error) {
		return codingremote.TaskResult{}, errors.New("response lost after dispatch")
	}
	tool, err := NewCodingRemoteTaskTool(client, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-start", "provider-lost"),
		map[string]any{
			"action": "start", "scope": "mintclaw-dev", "profile": "investigate",
			"objective": "Investigate the production failure without changing files.",
		},
	)
	if uncertain == nil || !uncertain.IsError {
		t.Fatalf("uncertain start = %#v", uncertain)
	}
	uncertainResult := decodeCodingRemoteTaskResult(t, uncertain)
	if uncertainResult.Outcome != "uncertain" || !uncertainResult.Retained ||
		!strings.Contains(uncertainResult.RecoveryAction, "do not replay") ||
		!strings.Contains(tool.CodingContinuityContext(), uncertainResult.TaskID) {
		t.Fatalf("uncertain result = %#v; continuity = %q", uncertainResult, tool.CodingContinuityContext())
	}

	history := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "call-remote-task", Name: "remote_coding_task",
		}}},
		{
			Role: "tool", ToolCallID: "call-remote-task", Content: uncertain.ContentForLLM(),
			ToolResultStatus: providers.ToolResultStatusError,
		},
	}
	ignored, err := NewCodingRemoteTaskTool(client, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	unresolvedHistory := append([]providers.Message(nil), history...)
	unresolvedHistory[1].ToolResultStatus = providers.ToolResultStatusUnknown
	ignored.RestoreHistory(unresolvedHistory)
	if ignored.CodingContinuityContext() != "" {
		t.Fatalf("unresolved history became task authority: %q", ignored.CodingContinuityContext())
	}
	restartedClient := &fakeCodingRemoteTaskClient{snapshot: client.snapshot}
	restarted, err := NewCodingRemoteTaskTool(restartedClient, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restarted.RestoreHistory(history)
	if !strings.Contains(restarted.CodingContinuityContext(), uncertainResult.TaskID) {
		t.Fatalf("restored continuity = %q", restarted.CodingContinuityContext())
	}
	status := restarted.Execute(
		codingRemoteTaskTestContext(authority, "turn-status", "provider-status"),
		map[string]any{"action": "status", "task_id": uncertainResult.TaskID},
	)
	if status.IsError || len(restartedClient.taskCalls) != 1 ||
		restartedClient.taskCalls[0].Operation != codingremote.OperationTaskStatus {
		t.Fatalf("restored status = %#v; calls = %#v", status, restartedClient.taskCalls)
	}

	forkAuthority := authority
	forkAuthority.ThreadID = uuid.NewString()
	forkAuthority.SessionKey = "coding:" + forkAuthority.ThreadID
	forked, err := NewCodingRemoteTaskTool(restartedClient, forkAuthority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	forked.RestoreHistory(history)
	forkContext := forked.CodingContinuityContext()
	if !strings.Contains(forkContext, "historical") || strings.Contains(forkContext, uncertainResult.TaskID) {
		t.Fatalf("fork continuity = %q", forkContext)
	}
	denied := forked.Execute(
		codingRemoteTaskTestContext(forkAuthority, "fork-status", "provider-fork-status"),
		map[string]any{"action": "status", "task_id": uncertainResult.TaskID},
	)
	if denied == nil || !denied.IsError || len(restartedClient.taskCalls) != 1 {
		t.Fatalf("fork status = %#v; calls = %#v", denied, restartedClient.taskCalls)
	}
}

func TestCodingRemoteTaskToolFailsClosedOnIdentityQuestionAndRetentionBounds(t *testing.T) {
	authority := codingRemoteTaskTestAuthority()
	client := &fakeCodingRemoteTaskClient{snapshot: codingRemoteTaskTestSnapshot("discovery-v1")}
	tool, err := NewCodingRemoteTaskTool(client, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	withoutIdentity := tool.Execute(t.Context(), map[string]any{
		"action": "start", "scope": "mintclaw-dev", "profile": "mutate", "objective": "Fix it.",
	})
	if withoutIdentity == nil || !withoutIdentity.IsError || len(client.taskCalls) != 0 {
		t.Fatalf("missing identity = %#v; calls = %#v", withoutIdentity, client.taskCalls)
	}
	staleAnswer := tool.Execute(
		codingRemoteTaskTestContext(authority, "turn-answer", "provider-answer"),
		map[string]any{
			"action": "answer", "task_id": "missing_task", "text": "AGENTS.md",
			"question_id": "question_1", "question_revision": float64(1),
		},
	)
	if staleAnswer == nil || !staleAnswer.IsError || len(client.taskCalls) != 0 {
		t.Fatalf("stale answer = %#v; calls = %#v", staleAnswer, client.taskCalls)
	}
	for index := 0; index < codingRemoteTaskMaxLinks; index++ {
		result := codingRemoteTaskTestLocalResult(
			authority,
			fmt.Sprintf("coding-task-%s-%03d", strings.Repeat("a", 50), index),
		)
		if !tool.retainTaskResult(result) {
			t.Fatalf("retain active link %d failed", index)
		}
	}
	overflow := codingRemoteTaskTestLocalResult(authority, "coding-overflow")
	if tool.retainTaskResult(overflow) || !strings.Contains(tool.CodingContinuityContext(), "retention") {
		t.Fatalf(
			"overflow retained = %v; continuity = %q",
			tool.taskRetained("coding-overflow"),
			tool.CodingContinuityContext(),
		)
	}
}

func TestCodingRemoteTaskToolBoundsInheritedForkReferences(t *testing.T) {
	authority := codingRemoteTaskTestAuthority()
	client := &fakeCodingRemoteTaskClient{snapshot: codingRemoteTaskTestSnapshot("discovery-v1")}
	tool, err := NewCodingRemoteTaskTool(client, authority, client.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	parent := authority
	parent.ThreadID = uuid.NewString()
	parent.SessionKey = "coding:" + parent.ThreadID
	history := make([]providers.Message, 0, (codingRemoteTaskMaxLinks+1)*2)
	for index := 0; index <= codingRemoteTaskMaxLinks; index++ {
		callID := fmt.Sprintf("inherited-call-%03d", index)
		result := codingRemoteTaskTestLocalResult(parent, fmt.Sprintf("coding-inherited-%03d", index))
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		history = append(
			history,
			providers.Message{
				Role: "assistant", ToolCalls: []providers.ToolCall{{ID: callID, Name: "remote_coding_task"}},
			},
			providers.Message{
				Role: "tool", ToolCallID: callID, Content: string(encoded),
				ToolResultStatus: providers.ToolResultStatusError,
			},
		)
	}
	tool.RestoreHistory(history)
	continuity := tool.CodingContinuityContext()
	if !strings.Contains(continuity, "at least 64 historical reference(s)") ||
		strings.Contains(continuity, "coding-inherited-") {
		t.Fatalf("bounded inherited continuity = %q", continuity)
	}
}

func (tool *CodingRemoteTaskTool) taskRetained(taskID string) bool {
	_, found := tool.taskLink(taskID)
	return found
}

func codingRemoteTaskTestAuthority() CodingRemoteToolAuthority {
	threadID := uuid.NewString()
	return CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: "coding:" + threadID,
		ProjectKey: "git_worktree:" + strings.Repeat("a", 64), LocalProfile: codingscope.ProfileMutate,
	}
}

func codingRemoteTaskTestSnapshot(discovery string) codingremote.CapabilitySnapshot {
	return codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: discovery, GeneratedAtUnixMS: time.Now().UnixMilli(),
		Capabilities: []codingremote.CapabilityDescriptor{},
		TaskScopes: []codingremote.TaskScopeDescriptor{{
			Alias: "mintclaw-dev", Revision: "scope-v1", Target: "laptop",
			Profiles:     []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Availability: codingremote.AvailabilityAvailable,
		}},
	}
}

func codingRemoteTaskTestContext(
	authority CodingRemoteToolAuthority,
	executionID string,
	providerCallID string,
) context.Context {
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: authority.SessionKey, ExecutionID: executionID,
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	return toolshared.WithToolCallID(ctx, providerCallID)
}

func codingRemoteTaskTestResult(request codingremote.Request) codingremote.TaskResult {
	return codingremote.TaskResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		TaskID:            request.TaskID, GenerationID: "generation_1",
		Scope: request.TaskScope, Target: "laptop", Profile: request.TaskProfile,
		Status: "running", NodeState: string(codingtask.StateRunning),
		ThreadID: "thread_1", WorkerGenerationID: "worker_1",
		Activity: string(codingtask.ActivityRunning), Progress: "running focused validation",
	}
}

func codingRemoteTaskTestLocalResult(
	authority CodingRemoteToolAuthority,
	taskID string,
) codingRemoteTaskToolResult {
	return codingRemoteTaskToolResult{
		Schema: codingRemoteTaskResultSchema, Placement: "remote", Action: "start",
		Outcome: "uncertain", Retained: true, OwnerThreadID: authority.ThreadID,
		Grant: authority.Grant, GrantRevision: authority.GrantRevision,
		DiscoveryRevision: "discovery-v1", BindingDiscoveryRevision: "discovery-v1",
		TaskID: taskID, Scope: "mintclaw-dev", ScopeRevision: "scope-v1",
		Target: "laptop", Profile: codingscope.ProfileMutate,
		ErrorCode:      "BROKER_UNAVAILABLE",
		RecoveryAction: "Call remote_coding_task status or cancel with this task_id; do not replay start.",
	}
}

func decodeCodingRemoteTaskResult(
	t *testing.T,
	result *toolshared.ToolResult,
) codingRemoteTaskToolResult {
	t.Helper()
	if result == nil {
		t.Fatal("nil remote coding task result")
	}
	var decoded codingRemoteTaskToolResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &decoded); err != nil {
		t.Fatalf("decode remote coding task result: %v; content = %s", err, result.ContentForLLM())
	}
	return decoded
}
