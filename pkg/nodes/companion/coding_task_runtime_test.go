package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestCodingRuntimeExecutesOwnerScopedCommandsWithoutPersistingPrompt(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	runtime := newCodingTestRuntime(t, host, ledger)

	registered := make(map[string]nodes.CommandDescriptor)
	for _, descriptor := range runtime.Catalog().Commands {
		if nodes.IsCodingCommand(descriptor.Name) {
			registered[descriptor.Name] = descriptor
			if descriptor.ModelContract != nil {
				t.Fatalf("coding command %s has a model contract", descriptor.Name)
			}
		}
	}
	if len(registered) != 5 {
		t.Fatalf("registered coding commands = %v", registered)
	}

	projectsPlan := codingTestPlan(t, runtime, nodes.CodingCommandProjects, struct{}{}, "projects", "actor-test")
	projectsRaw, err := runtime.Invoke(t.Context(), projectsPlan)
	if err != nil {
		t.Fatal(err)
	}
	var projects nodes.CodingProjectsResult
	if err = json.Unmarshal(projectsRaw, &projects); err != nil || len(projects.Projects) != 1 ||
		projects.Projects[0].Alias != catalog.List()[0].Alias || !projects.Projects[0].Available ||
		projects.Projects[0].Busy {
		t.Fatalf("projects result = %s, %v", projectsRaw, err)
	}

	objective := "Inspect the private regression evidence."
	done := "Report one bounded root cause."
	startInput, startEphemeral, err := nodes.NewCodingTaskStartInputs(
		"task-runtime",
		"generation-runtime",
		catalog.List()[0].Alias,
		catalog.List()[0].Revision,
		codingtask.TaskModeInvestigate,
		objective,
		done,
		"turn-runtime-one",
	)
	if err != nil {
		t.Fatal(err)
	}
	startPlan := codingTestPlan(
		t,
		runtime,
		nodes.CodingCommandTaskStart,
		startInput,
		"start",
		"actor-test",
	)
	startEphemeralRaw, _ := json.Marshal(startEphemeral)
	startRaw, err := runtime.InvokeWithEphemeral(t.Context(), startPlan, startEphemeralRaw)
	if err != nil {
		t.Fatal(err)
	}
	var started nodes.CodingTaskResult
	if err = json.Unmarshal(startRaw, &started); err != nil || started.TaskID != startInput.TaskID ||
		started.State != codingtask.StateRunning || process.startCalls != 1 {
		t.Fatalf("start result = %s, calls %d, error %v", startRaw, process.startCalls, err)
	}
	if bytes.Contains(startRaw, []byte(objective)) || bytes.Contains(startRaw, []byte(done)) ||
		bytes.Contains(startRaw, []byte(catalog.projects[catalog.List()[0].Alias].project.ProjectRoot)) ||
		bytes.Contains(startRaw, []byte(catalog.projects[catalog.List()[0].Alias].Model)) ||
		bytes.Contains(startRaw, []byte(catalog.projects[catalog.List()[0].Alias].Provider)) {
		t.Fatalf("start result exposed protected authority: %s", startRaw)
	}
	invocationRecord, found := ledger.Get(startPlan.InvocationID)
	if !found || invocationRecord.OwnerDigest == "" || invocationRecord.State != nodes.InvocationSucceeded {
		t.Fatalf("start invocation record = %#v, found %v", invocationRecord, found)
	}
	ledger.mu.Lock()
	durable, marshalErr := json.Marshal(invocationLedgerDocument{
		Version: invocationLedgerVersion, Records: ledger.records, CodingTasks: ledger.codingTasks,
	})
	ledger.mu.Unlock()
	if marshalErr != nil || bytes.Contains(durable, []byte(objective)) || bytes.Contains(durable, []byte(done)) {
		t.Fatalf("durable ledger retained prompt: %s, %v", durable, marshalErr)
	}
	mismatchInput, mismatchEphemeral, err := nodes.NewCodingTaskStartInputs(
		"task-mismatch",
		"generation-mismatch",
		catalog.List()[0].Alias,
		catalog.List()[0].Revision,
		codingtask.TaskModeInvestigate,
		"Original objective.",
		"",
		"turn-mismatch",
	)
	if err != nil {
		t.Fatal(err)
	}
	mismatchPlan := codingTestPlan(
		t,
		runtime,
		nodes.CodingCommandTaskStart,
		mismatchInput,
		"mismatch",
		"actor-test",
	)
	mismatchEphemeral.Objective = "Changed objective."
	mismatchRaw, _ := json.Marshal(mismatchEphemeral)
	if _, err = runtime.InvokeWithEphemeral(t.Context(), mismatchPlan, mismatchRaw); !errors.Is(
		err,
		nodes.ErrCommandDenied,
	) {
		t.Fatalf("mismatched start error = %v", err)
	}
	if _, found = ledger.Get(mismatchPlan.InvocationID); found || backend.launchCalls != 1 {
		t.Fatalf("mismatched start was accepted: found %v, launch calls %d", found, backend.launchCalls)
	}

	replayed, err := runtime.InvokeWithEphemeral(t.Context(), startPlan, nil)
	if err != nil || !bytes.Equal(replayed, startRaw) || process.startCalls != 1 || backend.launchCalls != 1 {
		t.Fatalf("start replay = %s, calls (%d, %d), error %v", replayed, process.startCalls, backend.launchCalls, err)
	}

	ownerCases := []struct {
		name      string
		agentID   string
		sessionID string
		actorID   string
	}{
		{name: "agent", agentID: "different-agent", sessionID: "session-test", actorID: "actor-test"},
		{name: "session", agentID: "agent-test", sessionID: "different-session", actorID: "actor-test"},
		{name: "actor", agentID: "agent-test", sessionID: "session-test", actorID: "different-actor"},
	}
	for _, testCase := range ownerCases {
		wrongOwnerPlan := codingTestPlanOwner(
			t,
			runtime,
			nodes.CodingCommandTaskStatus,
			nodes.CodingTaskIdentityInput{
				TaskID: startInput.TaskID, TaskGenerationID: startInput.TaskGenerationID,
			},
			"wrong-"+testCase.name,
			testCase.agentID,
			testCase.sessionID,
			testCase.actorID,
		)
		if _, err = runtime.Invoke(t.Context(), wrongOwnerPlan); !errors.Is(err, nodes.ErrCommandDenied) {
			t.Fatalf("wrong-%s status error = %v", testCase.name, err)
		}
		if _, found = ledger.Get(wrongOwnerPlan.InvocationID); found {
			t.Fatalf("wrong-%s status entered the invocation ledger", testCase.name)
		}
	}

	question := worker.QuestionState{
		QuestionID: "question-runtime", Revision: 3, Status: worker.QuestionWaiting,
		Prompt: "Which scope?", Options: []worker.QuestionOption{{ID: "focused", Label: "Focused"}},
	}
	process.emit(t, worker.EventQuestionState, worker.QuestionStatePayload{
		ControlIdentity: worker.ControlIdentity{
			TaskID: started.TaskID, TaskGenerationID: started.TaskGenerationID,
			WorkerGenerationID: started.WorkerGenerationID,
		},
		Question: question,
	})
	waitHostTestState(t, host, codingtask.NewStartRequest(
		startInput.TaskID,
		startInput.TaskGenerationID,
		startInput.ProjectAlias,
		startInput.ProjectRevision,
		startInput.Mode,
		objective,
		done,
		startInput.TurnIdempotencyKey,
	), codingtask.StateWaitingInput, nil)
	answer := &nodes.CodingQuestionAnswer{
		QuestionID: question.QuestionID, QuestionRevision: question.Revision, AnswerID: "focused",
	}
	steerInput, steerEphemeral, err := nodes.NewCodingTaskSteerInputs(
		started.TaskID,
		started.TaskGenerationID,
		started.WorkerGenerationID,
		"turn-runtime-two",
		"Use the focused scope.",
		answer,
	)
	if err != nil {
		t.Fatal(err)
	}
	steerPlan := codingTestPlan(
		t,
		runtime,
		nodes.CodingCommandTaskSteer,
		steerInput,
		"steer",
		"actor-test",
	)
	steerEphemeralRaw, _ := json.Marshal(steerEphemeral)
	steerRaw, err := runtime.InvokeWithEphemeral(t.Context(), steerPlan, steerEphemeralRaw)
	if err != nil {
		t.Fatal(err)
	}
	var steered nodes.CodingTaskResult
	if err = json.Unmarshal(steerRaw, &steered); err != nil || steered.State != codingtask.StateRunning ||
		steered.Question != nil || process.steerCalls != 1 {
		t.Fatalf("steer result = %s, calls %d, error %v", steerRaw, process.steerCalls, err)
	}
	staleSteer, staleEphemeral, err := nodes.NewCodingTaskSteerInputs(
		steered.TaskID,
		steered.TaskGenerationID,
		"different-worker",
		"turn-runtime-stale",
		"This must not be delivered.",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	stalePlan := codingTestPlan(
		t,
		runtime,
		nodes.CodingCommandTaskSteer,
		staleSteer,
		"stale-steer",
		"actor-test",
	)
	staleRaw, _ := json.Marshal(staleEphemeral)
	if _, err = runtime.InvokeWithEphemeral(t.Context(), stalePlan, staleRaw); !errors.Is(
		err,
		nodes.ErrCommandDenied,
	) {
		t.Fatalf("stale worker steer error = %v", err)
	}
	if _, found = ledger.Get(stalePlan.InvocationID); found || process.steerCalls != 1 {
		t.Fatalf("stale worker steer was accepted: found %v, calls %d", found, process.steerCalls)
	}

	cancelInput := nodes.CodingTaskCancelInput{
		TaskID: steered.TaskID, TaskGenerationID: steered.TaskGenerationID,
		WorkerGenerationID: steered.WorkerGenerationID, CancelIdempotencyKey: "cancel-runtime",
	}
	cancelPlan := codingTestPlan(
		t,
		runtime,
		nodes.CodingCommandTaskCancel,
		cancelInput,
		"cancel",
		"actor-test",
	)
	cancelRaw, err := runtime.Invoke(t.Context(), cancelPlan)
	if err != nil || process.cancelCalls != 1 || !bytes.Contains(cancelRaw, []byte(`"activity":"interrupting"`)) {
		t.Fatalf("cancel result = %s, calls %d, error %v", cancelRaw, process.cancelCalls, err)
	}
}

func TestCodingRuntimeTruncatesQuestionDescriptionsToOutputLimit(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	runtime := newCodingTestRuntime(t, host, ledger)
	startInput, startEphemeral, err := nodes.NewCodingTaskStartInputs(
		"task-question",
		"generation-question",
		catalog.List()[0].Alias,
		catalog.List()[0].Revision,
		codingtask.TaskModeInvestigate,
		"Inspect the repository.",
		"",
		"turn-question-one",
	)
	if err != nil {
		t.Fatal(err)
	}
	startPlan := codingTestPlan(t, runtime, nodes.CodingCommandTaskStart, startInput, "question-start", "actor-test")
	ephemeralRaw, _ := json.Marshal(startEphemeral)
	startRaw, err := runtime.InvokeWithEphemeral(t.Context(), startPlan, ephemeralRaw)
	if err != nil {
		t.Fatal(err)
	}
	var started nodes.CodingTaskResult
	if err = json.Unmarshal(startRaw, &started); err != nil {
		t.Fatal(err)
	}
	options := make([]worker.QuestionOption, codingtask.MaxQuestionOptions)
	for index := range options {
		options[index] = worker.QuestionOption{
			ID:          fmt.Sprintf("option-%d", index),
			Label:       strings.Repeat("l", codingtask.MaxQuestionLabelBytes),
			Description: strings.Repeat("d", codingtask.MaxQuestionTextBytes),
		}
	}
	process.emit(t, worker.EventQuestionState, worker.QuestionStatePayload{
		ControlIdentity: worker.ControlIdentity{
			TaskID: started.TaskID, TaskGenerationID: started.TaskGenerationID,
			WorkerGenerationID: started.WorkerGenerationID,
		},
		Question: worker.QuestionState{
			QuestionID: "question-large", Revision: 1, Status: worker.QuestionWaiting,
			Prompt: strings.Repeat("q", codingtask.MaxQuestionTextBytes), Options: options,
		},
	})
	request := codingtask.NewStartRequest(
		startInput.TaskID,
		startInput.TaskGenerationID,
		startInput.ProjectAlias,
		startInput.ProjectRevision,
		startInput.Mode,
		startEphemeral.Objective,
		startEphemeral.DoneCriteria,
		startInput.TurnIdempotencyKey,
	)
	waitHostTestState(t, host, request, codingtask.StateWaitingInput, nil)
	statusPlan := codingTestPlanWithOutputLimit(
		t,
		runtime,
		nodes.CodingCommandTaskStatus,
		nodes.CodingTaskIdentityInput{
			TaskID: started.TaskID, TaskGenerationID: started.TaskGenerationID,
		},
		"question-status",
		"actor-test",
		nodes.MaxInvocationOutput,
	)
	raw, err := runtime.Invoke(t.Context(), statusPlan)
	if err != nil || len(raw) > nodes.MinCodingTaskOutputBytes {
		t.Fatalf("status = %d bytes, error %v", len(raw), err)
	}
	var result nodes.CodingTaskResult
	if err = json.Unmarshal(raw, &result); err != nil || result.Question == nil ||
		!result.QuestionTruncated || result.Question.Options[0].Description != "" {
		t.Fatalf("truncated question = %#v, error %v", result.Question, err)
	}
}

func TestCodingRuntimeReportsRecoveredTaskWithoutLaunching(t *testing.T) {
	catalog, err := NewCodingProjectCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger := newMemoryInvocationLedger()
	startPlan := testCodingTaskLedgerPlan(t, "runtime-restart", time.Now())
	bound := bindTestCodingTask(t, ledger, startPlan, "runtime-restart")
	if _, err = ledger.MarkUnknown(startPlan.InvocationID); err != nil {
		t.Fatal(err)
	}
	backend := &hostTestBackend{}
	host, err := newCodingTaskHost(catalog, ledger, "test-build", backend)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newCodingTestRuntime(t, host, ledger)
	var codingCommands []string
	for _, descriptor := range runtime.Catalog().Commands {
		if nodes.IsCodingCommand(descriptor.Name) {
			codingCommands = append(codingCommands, descriptor.Name)
		}
	}
	if len(codingCommands) != 1 || codingCommands[0] != nodes.CodingCommandTaskStatus {
		t.Fatalf("recovery coding commands = %v", codingCommands)
	}
	statusPlan := codingTestPlanOwner(
		t,
		runtime,
		nodes.CodingCommandTaskStatus,
		nodes.CodingTaskIdentityInput{
			TaskID: bound.TaskID, TaskGenerationID: bound.TaskGenerationID,
		},
		"restart-status",
		"agent_test",
		"session_test",
		"actor_test",
	)
	raw, err := runtime.Invoke(t.Context(), statusPlan)
	if err != nil {
		t.Fatal(err)
	}
	var result nodes.CodingTaskResult
	if err = json.Unmarshal(raw, &result); err != nil || result.State != codingtask.StateUncertain ||
		result.FailureCode != "HOST_RESTARTED" || backend.prepareCalls != 0 || backend.launchCalls != 0 {
		t.Fatalf(
			"recovered status = %s, backend (%d, %d), error %v",
			raw,
			backend.prepareCalls,
			backend.launchCalls,
			err,
		)
	}
}

func TestCodingRuntimeDoesNotAdvertiseCommandsWithoutProjectsOrOutputBudget(t *testing.T) {
	ledger := newMemoryInvocationLedger()
	catalog, err := NewCodingProjectCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	host, err := newCodingTaskHost(catalog, ledger, "test-build", &hostTestBackend{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(
		nodes.ID("node-test"),
		"test",
		codingTestPolicy(nodes.MaxInvocationOutput),
		ledger,
		WithCodingTaskHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range runtime.Catalog().Commands {
		if nodes.IsCodingCommand(descriptor.Name) {
			t.Fatalf("empty project catalog advertised %s", descriptor.Name)
		}
	}

	projectFixture := newCodingProjectFixture(t, []codingtask.TaskMode{codingtask.TaskModeInvestigate})
	projectCatalog, err := NewCodingProjectCatalog(projectFixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	otherLedger := newMemoryInvocationLedger()
	projectHost, err := newCodingTaskHost(projectCatalog, otherLedger, "test-build", &hostTestBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRuntime(
		nodes.ID("node-test"),
		"test",
		codingTestPolicy(nodes.MaxInvocationOutput),
		ledger,
		WithCodingTaskHost(projectHost),
	); err == nil {
		t.Fatal("NewRuntime() accepted a coding host backed by another ledger")
	}
	lowBudget, err := NewRuntime(
		nodes.ID("node-test"),
		"test",
		codingTestPolicy(nodes.MinCodingTaskOutputBytes-1),
		otherLedger,
		WithCodingTaskHost(projectHost),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range lowBudget.Catalog().Commands {
		if nodes.IsCodingCommand(descriptor.Name) {
			t.Fatalf("low output budget advertised %s", descriptor.Name)
		}
	}
}

func newCodingTestRuntime(
	t *testing.T,
	host *CodingTaskHost,
	ledger *InvocationLedger,
) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(
		nodes.ID("node-test"),
		"test",
		codingTestPolicy(nodes.MaxInvocationOutput),
		ledger,
		WithCodingTaskHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func codingTestPolicy(outputLimit int) nodes.LocalCommandPolicy {
	return nodes.LocalCommandPolicy{
		Revision: "policy-coding-test",
		AllowedCommands: []string{
			nodes.CodingCommandProjects,
			nodes.CodingCommandTaskStart,
			nodes.CodingCommandTaskStatus,
			nodes.CodingCommandTaskSteer,
			nodes.CodingCommandTaskCancel,
		},
		MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 30, MaxOutputBytes: outputLimit,
	}
}

func codingTestPlan(
	t *testing.T,
	runtime *Runtime,
	command string,
	input any,
	suffix string,
	actorID string,
) nodes.ExecutionPlan {
	return codingTestPlanOwnerWithOutputLimit(
		t,
		runtime,
		command,
		input,
		suffix,
		"agent-test",
		"session-test",
		actorID,
		nodes.MinCodingTaskOutputBytes,
	)
}

func codingTestPlanWithOutputLimit(
	t *testing.T,
	runtime *Runtime,
	command string,
	input any,
	suffix string,
	actorID string,
	outputLimit int,
) nodes.ExecutionPlan {
	return codingTestPlanOwnerWithOutputLimit(
		t,
		runtime,
		command,
		input,
		suffix,
		"agent-test",
		"session-test",
		actorID,
		outputLimit,
	)
}

func codingTestPlanOwner(
	t *testing.T,
	runtime *Runtime,
	command string,
	input any,
	suffix string,
	agentID string,
	sessionID string,
	actorID string,
) nodes.ExecutionPlan {
	return codingTestPlanOwnerWithOutputLimit(
		t,
		runtime,
		command,
		input,
		suffix,
		agentID,
		sessionID,
		actorID,
		nodes.MinCodingTaskOutputBytes,
	)
}

func codingTestPlanOwnerWithOutputLimit(
	t *testing.T,
	runtime *Runtime,
	command string,
	input any,
	suffix string,
	agentID string,
	sessionID string,
	actorID string,
	outputLimit int,
) nodes.ExecutionPlan {
	t.Helper()
	catalog := runtime.Catalog()
	catalogHash, err := catalog.HashForProtocol(nodes.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor nodes.CommandDescriptor
	for _, candidate := range catalog.Commands {
		if candidate.Name == command {
			descriptor = candidate
			break
		}
	}
	if descriptor.Name == "" {
		t.Fatalf("command %s is not advertised", command)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := nodes.PrepareExecutionPlanForProtocol(nodes.ProtocolV2, nodes.InvocationRequest{
		InvocationID: "inv-coding-" + suffix, IdempotencyKey: "idem-coding-" + suffix,
		NodeID: runtime.nodeID, CatalogHash: catalogHash, Command: command, Input: raw,
		AgentID: agentID, SessionID: sessionID, ActorID: actorID,
		TimeoutSeconds: 10, OutputLimitBytes: outputLimit,
	}, descriptor, LocalExecutor, runtime.policy.Revision, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
