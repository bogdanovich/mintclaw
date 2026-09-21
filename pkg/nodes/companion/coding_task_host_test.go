package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestCodingTaskHostStartsProjectsAndDeduplicatesInvestigation(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "investigate")
	request := hostTestRequest(t, catalog, "investigate", codingtask.TaskModeInvestigate)
	record, existing, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil || existing || record.State != codingtask.StateRunning ||
		record.ExecutionRoot != record.Project.ProjectRoot || record.WorktreeID != "" {
		t.Fatalf("Start() = %#v, existing %v, error %v", record, existing, err)
	}
	if process.startCalls != 1 || process.startKey != request.TurnIdempotencyKey ||
		process.startText != request.Prompt() || backend.prepareCalls != 1 || backend.launchCalls != 1 {
		t.Fatalf("start calls = %#v, backend = (%d, %d)", process, backend.prepareCalls, backend.launchCalls)
	}

	process.emit(t, worker.EventStatusChanged, worker.StatusChangedPayload{
		ControlIdentity: hostTestControl(record), Activity: worker.ActivityReviewing,
		Status: "reviewing repository",
	})
	waitHostTestState(t, host, request, codingtask.StateRunning, func(record codingtask.Record) bool {
		return record.Activity == codingtask.ActivityReviewing && record.Status == "reviewing repository"
	})
	question := worker.QuestionState{
		QuestionID: "question-one", Revision: 2, Status: worker.QuestionWaiting,
		Prompt: "Which scope?", Options: []worker.QuestionOption{{ID: "focused", Label: "Focused"}},
	}
	process.emit(t, worker.EventQuestionState, worker.QuestionStatePayload{
		ControlIdentity: hostTestControl(record), Question: question,
	})
	record = waitHostTestState(t, host, request, codingtask.StateWaitingInput, nil)
	if record.Question == nil || record.Question.QuestionID != question.QuestionID ||
		record.Question.Revision != question.Revision {
		t.Fatalf("waiting projection = %#v", record)
	}
	answer := &worker.QuestionAnswerRef{
		QuestionID: question.QuestionID, QuestionRevision: question.Revision, AnswerID: "focused",
	}
	record, err = host.Steer(
		t.Context(), request.TaskID, request.TaskGenerationID, record.WorkerGenerationID,
		"steer-one", "Use the focused scope.", answer,
	)
	if err != nil || record.State != codingtask.StateRunning || record.Question != nil ||
		process.steerCalls != 1 {
		t.Fatalf("Steer() = %#v, calls %d, error %v", record, process.steerCalls, err)
	}
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	record = waitHostTestState(t, host, request, codingtask.StateCompleted, nil)
	if record.RetainUntil <= record.UpdatedAt {
		t.Fatalf("completed retention = %#v", record)
	}

	repeated, existing, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil || !existing || !repeated.SameIdentity(record) || backend.launchCalls != 1 ||
		process.startCalls != 1 {
		t.Fatalf("duplicate Start() = %#v, existing %v, error %v", repeated, existing, err)
	}
}

func TestCodingTaskHostPersistsMutationPreparationAndRejectsMissingHandoff(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeMutate},
		backend,
	)
	backend.ledger = ledger
	backend.mutationRoot = t.TempDir()
	plan := acceptHostTestInvocation(t, ledger, "mutate")
	request := hostTestRequest(t, catalog, "mutate", codingtask.TaskModeMutate)
	record, existing, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil || existing || record.State != codingtask.StateRunning ||
		record.ExecutionRoot != backend.mutationRoot || record.Branch != "mintclaw/host-test" ||
		record.WorktreeID != codingtask.WorktreeIDForThread(record.ThreadID) {
		t.Fatalf("mutation Start() = %#v, existing %v, error %v", record, existing, err)
	}
	if !backend.sawPreparing || !backend.sawPreparedBeforeLaunch {
		t.Fatalf(
			"durable preparation order = preparing %v, prepared %v",
			backend.sawPreparing,
			backend.sawPreparedBeforeLaunch,
		)
	}
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	record = waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if record.Failure == nil || record.Failure.Code != "WORKER_OUTCOME_UNCERTAIN" || record.HandoffID != "" {
		t.Fatalf("mutation without handoff = %#v", record)
	}
}

func TestCodingTaskHostSerializesDuplicateStartsAndEnforcesProjectCapacity(t *testing.T) {
	first := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{first}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "duplicate-start")
	request := hostTestRequest(t, catalog, "duplicate-start", codingtask.TaskModeInvestigate)

	const callers = 8
	var wait sync.WaitGroup
	wait.Add(callers)
	results := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			_, _, err := host.Start(t.Context(), plan.InvocationID, request)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("duplicate start error = %v", err)
		}
	}
	if backend.prepareCalls != 1 || backend.launchCalls != 1 || first.startCalls != 1 {
		t.Fatalf(
			"duplicate calls = prepare %d, launch %d, start %d",
			backend.prepareCalls,
			backend.launchCalls,
			first.startCalls,
		)
	}

	secondPlan := acceptHostTestInvocation(t, ledger, "busy")
	secondRequest := hostTestRequest(t, catalog, "busy", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(
		t.Context(),
		secondPlan.InvocationID,
		secondRequest,
	); !errors.Is(err, ErrCodingTaskBusy) {
		t.Fatalf("busy Start() error = %v", err)
	}
	if _, found := ledger.codingTask(secondPlan.InvocationID); found {
		t.Fatal("busy start retained a coding task")
	}
	first.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	waitHostTestState(t, host, request, codingtask.StateCompleted, nil)
}

func TestCodingTaskHostDoesNotReplayUncertainTurnAcceptance(t *testing.T) {
	process := newHostTestProcess()
	process.startErr = &worker.OutcomeUncertainError{Cause: errors.New("ack lost")}
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "uncertain-start")
	request := hostTestRequest(t, catalog, "uncertain-start", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); err == nil {
		t.Fatal("uncertain Start() succeeded")
	}
	record := waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if record.Failure == nil || record.Failure.Code != "TURN_ACCEPTANCE_UNCERTAIN" ||
		process.terminateCalls != 1 || process.startCalls != 1 {
		t.Fatalf("uncertain start = %#v, process %#v", record, process)
	}
	repeated, existing, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil || !existing || repeated.State != codingtask.StateUncertain || process.startCalls != 1 ||
		backend.launchCalls != 1 {
		t.Fatalf("uncertain replay = %#v, existing %v, error %v", repeated, existing, err)
	}
}

func TestCodingTaskHostResumesIdleThreadIdempotentlyAcrossSuccessors(t *testing.T) {
	first := newHostTestProcess()
	second := newHostTestProcess()
	third := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{first, second, third}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "idle-successor")
	request := hostTestRequest(t, catalog, "idle-successor", codingtask.TaskModeInvestigate)
	initial, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	first.finish(codingTaskProcessResult{outcome: codingTaskOutcomeIdle}, nil)
	idle := waitHostTestState(t, host, request, codingtask.StateIdle, nil)

	const callers = 8
	resumeText := "Continue from the retained thread with new evidence only."
	results := make(chan codingtask.Record, callers)
	errorsByCall := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resumed, resumeErr := host.Steer(
				context.Background(),
				request.TaskID,
				request.TaskGenerationID,
				idle.WorkerGenerationID,
				"turn-idle-successor-two",
				resumeText,
				nil,
			)
			results <- resumed
			errorsByCall <- resumeErr
		}()
	}
	wait.Wait()
	close(results)
	close(errorsByCall)
	for resumeErr := range errorsByCall {
		if resumeErr != nil {
			t.Fatalf("concurrent resume error = %v", resumeErr)
		}
	}
	var resumed codingtask.Record
	for result := range results {
		if resumed.InvocationID == "" {
			resumed = result
			continue
		}
		if !result.SameIdentity(resumed) {
			t.Fatalf("concurrent resume identities = %#v and %#v", resumed, result)
		}
	}
	if resumed.State != codingtask.StateRunning || resumed.ThreadID != initial.ThreadID ||
		resumed.WorkerGenerationID == initial.WorkerGenerationID ||
		resumed.ThreadOpenMode != codingtask.ThreadOpenResume || resumed.ResumeSequence != 1 ||
		second.startCalls != 1 || second.startText != resumeText ||
		second.startKey != "turn-idle-successor-two" || backend.prepareCalls != 2 || backend.launchCalls != 2 {
		t.Fatalf("first successor = %#v, process %#v, backend %#v", resumed, second, backend)
	}
	if len(backend.preparedRecords) != 2 ||
		backend.preparedRecords[1].ThreadOpenMode != codingtask.ThreadOpenResume ||
		backend.preparedRecords[1].ThreadID != initial.ThreadID {
		t.Fatalf("prepared successor records = %#v", backend.preparedRecords)
	}
	firstResume := codingtask.NewResumeRequest(
		request.TaskID,
		request.TaskGenerationID,
		idle.WorkerGenerationID,
		resumeText,
		"turn-idle-successor-two",
	)
	if !resumed.MatchesResumeRequest(firstResume) || first.startCalls != 1 {
		t.Fatalf("resume evidence = %#v, initial process %#v", resumed, first)
	}

	second.finish(codingTaskProcessResult{outcome: codingTaskOutcomeIdle}, nil)
	idle = waitHostTestState(t, host, request, codingtask.StateIdle, nil)
	secondWorker := idle.WorkerGenerationID
	thirdText := "Continue once more without replaying either prior turn."
	resumedAgain, err := host.Steer(
		t.Context(),
		request.TaskID,
		request.TaskGenerationID,
		secondWorker,
		"turn-idle-successor-three",
		thirdText,
		nil,
	)
	if err != nil || resumedAgain.State != codingtask.StateRunning || resumedAgain.ResumeSequence != 2 ||
		resumedAgain.ThreadID != initial.ThreadID || resumedAgain.WorkerGenerationID == secondWorker ||
		third.startCalls != 1 || third.startText != thirdText || backend.prepareCalls != 3 ||
		backend.launchCalls != 3 {
		t.Fatalf("second successor = %#v, process %#v, backend %#v, error %v", resumedAgain, third, backend, err)
	}
	third.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	completed := waitHostTestState(t, host, request, codingtask.StateCompleted, nil)
	repeated, err := host.Steer(
		t.Context(),
		request.TaskID,
		request.TaskGenerationID,
		secondWorker,
		"turn-idle-successor-three",
		thirdText,
		nil,
	)
	if err != nil || !repeated.SameIdentity(completed) || backend.prepareCalls != 3 || backend.launchCalls != 3 ||
		third.startCalls != 1 {
		t.Fatalf("terminal duplicate resume = %#v, backend %#v, process %#v, error %v", repeated, backend, third, err)
	}
	if _, err := host.Steer(
		t.Context(),
		request.TaskID,
		request.TaskGenerationID,
		idle.WorkerGenerationID,
		"turn-stale-successor",
		"Do not accept this stale generation.",
		nil,
	); !errors.Is(err, ErrCodingTaskNotRunning) {
		t.Fatalf("stale successor error = %v", err)
	}
}

func TestCodingTaskHostMarksInvalidPreparationUncertainWhenAbortFails(t *testing.T) {
	abortErr := errors.New("owner release uncertain")
	backend := &hostTestBackend{invalidPreparation: true, abortErr: abortErr}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "invalid-preparation")
	request := hostTestRequest(t, catalog, "invalid-preparation", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); !errors.Is(err, abortErr) {
		t.Fatalf("Start() error = %v, want abort error", err)
	}
	record := waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if record.Failure == nil || record.Failure.Code != "TASK_PREPARATION_UNCERTAIN" ||
		backend.abortCalls != 1 || backend.launchCalls != 0 {
		t.Fatalf("invalid preparation = %#v, backend = %#v", record, backend)
	}
}

func TestCodingTaskHostRetainsCapacityUntilUncertainProcessStops(t *testing.T) {
	stubborn := newHostTestProcess()
	stubborn.startErr = &worker.OutcomeUncertainError{Cause: errors.New("ack lost")}
	stubborn.terminateDoesNotFinish = true
	second := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{stubborn, second}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	firstPlan := acceptHostTestInvocation(t, ledger, "stubborn-process")
	firstRequest := hostTestRequest(t, catalog, "stubborn-process", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), firstPlan.InvocationID, firstRequest); err == nil {
		t.Fatal("uncertain Start() succeeded")
	}
	waitHostTestState(t, host, firstRequest, codingtask.StateUncertain, nil)
	secondPlan := acceptHostTestInvocation(t, ledger, "after-stubborn")
	secondRequest := hostTestRequest(t, catalog, "after-stubborn", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(
		t.Context(),
		secondPlan.InvocationID,
		secondRequest,
	); !errors.Is(err, ErrCodingTaskBusy) {
		t.Fatalf("start while uncertain process remained live = %v", err)
	}
	stubborn.finish(codingTaskProcessResult{outcome: codingTaskOutcomeUncertain}, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _, err := host.Start(t.Context(), secondPlan.InvocationID, secondRequest)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrCodingTaskBusy) || time.Now().After(deadline) {
			t.Fatalf("start after uncertain process stopped = %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	second.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	waitHostTestState(t, host, secondRequest, codingtask.StateCompleted, nil)
}

func TestCodingTaskHostRecoversUnfinishedProjectionWithoutLaunching(t *testing.T) {
	fixture := newCodingScopeFixture(t, []codingtask.TaskMode{codingtask.TaskModeInvestigate})
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	ledger := newMemoryInvocationLedger()
	plan := testCodingTaskLedgerPlan(t, "host-restart", time.Now())
	bound := bindTestCodingTask(t, ledger, plan, "host-restart")
	backend := &hostTestBackend{}
	host, err := newCodingTaskHost(catalog, ledger, "test-build", backend)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := host.Status(bound.TaskID, bound.TaskGenerationID)
	if err != nil || recovered.State != codingtask.StateUncertain || recovered.Failure == nil ||
		recovered.Failure.Code != "HOST_RESTARTED" || backend.prepareCalls != 0 || backend.launchCalls != 0 {
		t.Fatalf("recovered task = %#v, error %v", recovered, err)
	}
}

func TestCodingTaskHostCancellationTargetsExactGeneration(t *testing.T) {
	process := newHostTestProcess()
	process.cancelResult = codingTaskProcessResult{outcome: codingTaskOutcomeCanceled}
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "cancel")
	request := hostTestRequest(t, catalog, "cancel", codingtask.TaskModeInvestigate)
	record, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Cancel(
		t.Context(), request.TaskID, request.TaskGenerationID, "wrong-worker", "cancel-wrong",
	); !errors.Is(err, ErrCodingTaskNotRunning) {
		t.Fatalf("wrong generation cancellation error = %v", err)
	}
	if _, err := host.Cancel(
		t.Context(), request.TaskID, request.TaskGenerationID, record.WorkerGenerationID, "cancel-one",
	); err != nil {
		t.Fatal(err)
	}
	record = waitHostTestState(t, host, request, codingtask.StateCanceled, nil)
	if process.cancelCalls != 1 || process.cancelKey != "cancel-one" || record.RetainUntil <= record.UpdatedAt {
		t.Fatalf("canceled task = %#v, process %#v", record, process)
	}
}

func TestCodingTaskHostRejectsMismatchedWorkerEventIdentity(t *testing.T) {
	process := newHostTestProcess()
	process.terminateDoesNotFinish = true
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	host.controlTimeout = 20 * time.Millisecond
	plan := acceptHostTestInvocation(t, ledger, "event-identity")
	request := hostTestRequest(t, catalog, "event-identity", codingtask.TaskModeInvestigate)
	record, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	identity := hostTestControl(record)
	identity.WorkerGenerationID = "worker-unrelated"
	process.emit(t, worker.EventStatusChanged, worker.StatusChangedPayload{
		ControlIdentity: identity, Activity: worker.ActivityRunning, Status: "unrelated status",
	})
	record = waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if record.Failure == nil || record.Failure.Code != "WORKER_CONTROL_UNCERTAIN" ||
		record.Status == "unrelated status" || process.terminateCalls != 1 {
		t.Fatalf("mismatched event projection = %#v, process %#v", record, process)
	}
	time.Sleep(3 * host.controlTimeout)
	if process.cancelCalls != 0 {
		t.Fatalf("mismatched event continued through task timeout: %#v", process)
	}
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeUncertain}, nil)
}

func TestCodingTaskHostRejectsMismatchedSnapshotThread(t *testing.T) {
	process := newHostTestProcess()
	process.historyGap = true
	process.snapshotThreadID = "thread-unrelated"
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "snapshot-thread")
	request := hostTestRequest(t, catalog, "snapshot-thread", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); err != nil {
		t.Fatal(err)
	}
	record := waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if record.Failure == nil || record.Failure.Code != "WORKER_CONTROL_UNCERTAIN" ||
		process.terminateCalls != 1 {
		t.Fatalf("mismatched snapshot = %#v, process %#v", record, process)
	}
}

func TestCodingTaskHostGapSnapshotAtomicallyClearsResolvedQuestion(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "resolved-gap-question")
	request := hostTestRequest(t, catalog, "resolved-gap-question", codingtask.TaskModeInvestigate)
	record, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	question := worker.QuestionState{
		QuestionID: "question-gap", Revision: 1, Status: worker.QuestionWaiting,
		Prompt: "Continue?", Options: []worker.QuestionOption{{ID: "yes", Label: "Yes"}},
	}
	process.emit(t, worker.EventQuestionState, worker.QuestionStatePayload{
		ControlIdentity: hostTestControl(record), Question: question,
	})
	waitHostTestState(t, host, request, codingtask.StateWaitingInput, nil)
	process.setGapSnapshot(record.ThreadID, worker.ActivityRunning, "work resumed", nil)
	waitHostTestState(t, host, request, codingtask.StateRunning, func(record codingtask.Record) bool {
		return record.Question == nil && record.Activity == codingtask.ActivityRunning &&
			record.Status == "work resumed"
	})
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	waitHostTestState(t, host, request, codingtask.StateCompleted, nil)
}

func TestCodingTaskHostKeepsLiveIdleSnapshotUnsettledUntilProcessOutcome(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "live-idle-snapshot")
	request := hostTestRequest(t, catalog, "live-idle-snapshot", codingtask.TaskModeInvestigate)
	record, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	process.setGapSnapshot(record.ThreadID, worker.ActivityIdle, "", nil)
	waitHostTestState(t, host, request, codingtask.StateRunning, func(record codingtask.Record) bool {
		return record.Activity == codingtask.ActivityRunning &&
			record.Status == "coding worker awaiting finalization"
	})
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeIdle}, nil)
	waitHostTestState(t, host, request, codingtask.StateIdle, nil)
}

func TestCodingTaskHostTimeoutUsesTerminationBackstopAfterCancelAck(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	host.controlTimeout = 20 * time.Millisecond
	plan := acceptHostTestInvocation(t, ledger, "timeout-backstop")
	request := hostTestRequest(t, catalog, "timeout-backstop", codingtask.TaskModeInvestigate)
	record, _, err := host.Start(t.Context(), plan.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	active, err := host.activeTask(request.TaskID, request.TaskGenerationID, record.WorkerGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	active.cancelTask()
	record = waitHostTestState(t, host, request, codingtask.StateUncertain, nil)
	if process.cancelCalls != 1 || process.terminateCalls != 1 || record.Failure == nil ||
		record.Failure.Code != "WORKER_OUTCOME_UNCERTAIN" {
		t.Fatalf("timeout backstop = %#v, process %#v", record, process)
	}
}

func TestCodingTaskHostShutdownCancelsAndWaitsForInFlightPreparation(t *testing.T) {
	backend := &hostTestBackend{prepareStarted: make(chan struct{}), blockPrepare: true}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "shutdown-prepare")
	request := hostTestRequest(t, catalog, "shutdown-prepare", codingtask.TaskModeInvestigate)
	startDone := make(chan error, 1)
	go func() {
		_, _, err := host.Start(context.Background(), plan.InvocationID, request)
		startDone <- err
	}()
	select {
	case <-backend.prepareStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation did not start")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight Start() error = %v", err)
	}
	record := waitHostTestState(t, host, request, codingtask.StateFailed, nil)
	if record.Failure == nil || record.Failure.Code != "TASK_PREPARATION_FAILED" || backend.launchCalls != 0 {
		t.Fatalf("shutdown preparation = %#v, launch calls %d", record, backend.launchCalls)
	}
}

func TestCodingTaskHostDuplicateWaitsForInFlightPreparation(t *testing.T) {
	backend := &hostTestBackend{prepareStarted: make(chan struct{}), blockPrepare: true}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "duplicate-prepare")
	request := hostTestRequest(t, catalog, "duplicate-prepare", codingtask.TaskModeInvestigate)
	startContext, cancelStart := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() {
		_, _, err := host.Start(startContext, plan.InvocationID, request)
		startDone <- err
	}()
	select {
	case <-backend.prepareStarted:
	case <-time.After(3 * time.Second):
		cancelStart()
		t.Fatal("preparation did not start")
	}
	type duplicateResult struct {
		record   codingtask.Record
		existing bool
		err      error
	}
	duplicateDone := make(chan duplicateResult, 1)
	go func() {
		record, existing, err := host.Start(context.Background(), plan.InvocationID, request)
		duplicateDone <- duplicateResult{record: record, existing: existing, err: err}
	}()
	select {
	case result := <-duplicateDone:
		cancelStart()
		t.Fatalf("duplicate returned before preparation settled: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	cancelStart()
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight Start() error = %v", err)
	}
	select {
	case result := <-duplicateDone:
		if result.err != nil || !result.existing || result.record.State != codingtask.StateFailed {
			t.Fatalf("settled duplicate = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("duplicate did not resume after preparation settled")
	}
	if backend.prepareCalls != 1 || backend.launchCalls != 0 {
		t.Fatalf("backend calls = prepare %d, launch %d", backend.prepareCalls, backend.launchCalls)
	}
}

func TestCodingTaskHostRetainsPreLaunchSettlementPersistenceFailure(t *testing.T) {
	prepareErr := errors.New("repository preparation failed")
	persistErr := errors.New("durable pre-launch settlement unavailable")
	backend := &hostTestBackend{prepareErr: prepareErr}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "pre-launch-persist")
	request := hostTestRequest(t, catalog, "pre-launch-persist", codingtask.TaskModeInvestigate)
	writes := 0
	ledger.mu.Lock()
	ledger.path = filepath.Join(t.TempDir(), "invocations.json")
	ledger.writeFile = func(string, []byte, os.FileMode) error {
		writes++
		if writes >= 3 {
			return persistErr
		}
		return nil
	}
	ledger.mu.Unlock()
	if _, existing, err := host.Start(t.Context(), plan.InvocationID, request); existing ||
		!errors.Is(err, prepareErr) || !errors.Is(err, persistErr) {
		t.Fatalf("Start() = existing %v, error %v", existing, err)
	}
	record, err := host.Status(request.TaskID, request.TaskGenerationID)
	if err != nil || record.State != codingtask.StatePreparing {
		t.Fatalf("orphaned durable record = %#v, error %v", record, err)
	}
	if _, existing, err := host.Start(t.Context(), plan.InvocationID, request); existing ||
		!errors.Is(err, ErrCodingTaskUnsettled) {
		t.Fatalf("duplicate orphan = existing %v, error %v", existing, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); !errors.Is(err, persistErr) {
		t.Fatalf("Shutdown() error = %v, want persistence failure", err)
	}
	if backend.prepareCalls != 1 || backend.launchCalls != 0 {
		t.Fatalf("backend calls = prepare %d, launch %d", backend.prepareCalls, backend.launchCalls)
	}
}

func TestCodingTaskHostShutdownWaitsForDurableSettlement(t *testing.T) {
	process := newHostTestProcess()
	process.waitStarted = make(chan struct{})
	process.releaseWait = make(chan struct{})
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "shutdown-settlement")
	request := hostTestRequest(t, catalog, "shutdown-settlement", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownDone <- host.Shutdown(ctx)
	}()
	select {
	case <-process.waitStarted:
	case <-time.After(3 * time.Second):
		close(process.releaseWait)
		t.Fatal("host settlement did not start")
	}
	select {
	case err := <-shutdownDone:
		close(process.releaseWait)
		t.Fatalf("Shutdown() returned before settlement: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(process.releaseWait)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	record, err := host.Status(request.TaskID, request.TaskGenerationID)
	if err != nil || record.State != codingtask.StateCanceled {
		t.Fatalf("settled shutdown task = %#v, error %v", record, err)
	}
}

func TestCodingTaskHostShutdownReportsSettlementPersistenceFailure(t *testing.T) {
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	plan := acceptHostTestInvocation(t, ledger, "shutdown-persist")
	request := hostTestRequest(t, catalog, "shutdown-persist", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); err != nil {
		t.Fatal(err)
	}
	record, err := host.Status(request.TaskID, request.TaskGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := host.activeTask(
		request.TaskID,
		request.TaskGenerationID,
		record.WorkerGenerationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("durable settlement unavailable")
	ledger.mu.Lock()
	ledger.path = filepath.Join(t.TempDir(), "invocations.json")
	ledger.writeFile = func(string, []byte, os.FileMode) error { return persistErr }
	ledger.mu.Unlock()
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted}, nil)
	select {
	case <-active.settled:
	case <-time.After(3 * time.Second):
		t.Fatal("failed settlement was not removed from the active set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); !errors.Is(err, persistErr) {
		t.Fatalf("Shutdown() error = %v, want persistence failure", err)
	}
	record, err = host.Status(request.TaskID, request.TaskGenerationID)
	if err != nil || record.State != codingtask.StateRunning {
		t.Fatalf("uncommitted settlement = %#v, error %v", record, err)
	}
}

func TestCodingTaskHostShutdownReportsLiveSettlementFailureAtDeadline(t *testing.T) {
	process := newHostTestProcess()
	process.shutdownDoesNotFinish = true
	process.terminateDoesNotFinish = true
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	host.controlTimeout = 10 * time.Millisecond
	plan := acceptHostTestInvocation(t, ledger, "shutdown-live-persist")
	request := hostTestRequest(t, catalog, "shutdown-live-persist", codingtask.TaskModeInvestigate)
	if _, _, err := host.Start(t.Context(), plan.InvocationID, request); err != nil {
		t.Fatal(err)
	}
	record, err := host.Status(request.TaskID, request.TaskGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := host.activeTask(
		request.TaskID,
		request.TaskGenerationID,
		record.WorkerGenerationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("durable live settlement unavailable")
	ledger.mu.Lock()
	ledger.path = filepath.Join(t.TempDir(), "invocations.json")
	ledger.writeFile = func(string, []byte, os.FileMode) error { return persistErr }
	ledger.mu.Unlock()
	host.settleControlUncertain(active)
	if !errors.Is(active.settlementError(), persistErr) {
		t.Fatalf("active settlement error = %v, want persistence failure", active.settlementError())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	shutdownErr := host.Shutdown(ctx)
	if !errors.Is(shutdownErr, context.DeadlineExceeded) || !errors.Is(shutdownErr, persistErr) {
		t.Fatalf("Shutdown() error = %v, want deadline and persistence failures", shutdownErr)
	}
	select {
	case <-active.settled:
		t.Fatal("stubborn live task settled before its process exited")
	default:
	}
	process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeUncertain}, nil)
	select {
	case <-active.settled:
	case <-time.After(3 * time.Second):
		t.Fatal("live task was not released after process exit")
	}
}

func TestNativeCodingTaskBackendPreparesAndReleasesMutationOwner(t *testing.T) {
	fixture := newCodingScopeFixture(t, []codingtask.TaskMode{codingtask.TaskModeMutate})
	policy := fixture.scopes["mintclaw"]
	record := codingtask.Record{
		SchemaVersion: codingtask.SchemaVersion, InvocationID: "inv-native-prepare",
		RequestDigest: strings.Repeat("a", 64), TaskID: "task-native-prepare",
		TaskGenerationID: "generation-native-prepare", ScopeAlias: "mintclaw",
		ScopeRevision: policy.descriptorRevision, Profile: codingtask.TaskModeMutate,
		ThreadID: uuid.NewString(), ThreadOpenMode: codingtask.ThreadOpenNew,
		WorkerGenerationID: "worker-native-prepare", Project: policy.project,
		WorktreeID: codingtask.WorktreeIDForThread("placeholder"), ProviderProfile: policy.ProviderProfile,
		Model: policy.Model, Provider: policy.Provider, ExpectedWorkerBuildID: policy.workerBuildID,
		State: codingtask.StatePreparing, Revision: 1, AcceptedAt: time.Now().UnixNano(),
		UpdatedAt: time.Now().UnixNano(),
	}
	record.WorktreeID = codingtask.WorktreeIDForThread(record.ThreadID)
	prepared, err := (nativeCodingTaskBackend{}).Prepare(t.Context(), policy, record, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.record.ExecutionRoot == "" || prepared.record.ExecutionRoot == policy.project.ProjectRoot ||
		prepared.record.Branch == "" || prepared.record.WorktreeID != record.WorktreeID {
		t.Fatalf("native preparation = %#v", prepared.record)
	}
	if err := prepared.abort(); err != nil {
		t.Fatal(err)
	}
	successorRecord := prepared.record
	successorRecord.ThreadOpenMode = codingtask.ThreadOpenResume
	successorRecord.WorkerGenerationID = "worker-native-successor"
	successorRecord.ResumeSequence = 1
	successorRecord.ResumeRequestDigest = strings.Repeat("c", 64)
	successorRecord.ResumeIdempotencyKey = "turn-native-successor"
	successor, err := (nativeCodingTaskBackend{}).Prepare(t.Context(), policy, successorRecord, "test-build")
	if err != nil {
		t.Fatalf("Prepare(successor) error = %v", err)
	}
	if successor.record.ExecutionRoot != prepared.record.ExecutionRoot ||
		successor.record.ExecutionRootIdentity != prepared.record.ExecutionRootIdentity ||
		successor.record.WorktreeID != prepared.record.WorktreeID || successor.record.Branch != prepared.record.Branch {
		t.Fatalf("native successor preparation = %#v, initial %#v", successor.record, prepared.record)
	}
	if err := successor.abort(); err != nil {
		t.Fatalf("abort successor: %v", err)
	}
	manager, err := worktree.NewManager(worktree.Config{
		StateRoot: filepath.Join(policy.MintClawHome, "coding"), WorktreeParent: policy.WorktreeParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := manager.AcquireOwner(t.Context(), worktree.OwnerRequest{
		WorktreeID: record.WorktreeID, TaskID: record.TaskID,
		TaskGenerationID: record.TaskGenerationID, ThreadID: record.ThreadID,
		WorkerGenerationID: "worker-successor",
	})
	if err != nil {
		t.Fatalf("owner after abort: %v", err)
	}
	handoff, err := owner.CaptureHandoff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !codingTaskHandoffMatches(prepared.record, &handoff) {
		t.Fatalf("exact handoff did not match prepared task: %#v", handoff)
	}
	mismatch := handoff
	mismatch.TaskGenerationID = "generation-other"
	if codingTaskHandoffMatches(prepared.record, &mismatch) {
		t.Fatal("mismatched handoff was accepted")
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCodingWorkerEnvironmentOverridesOnlyMintClawHome(t *testing.T) {
	t.Setenv("MINTCLAW_HOME", "/untrusted/home")
	t.Setenv("mintclaw_home", "/case-variant/home")
	t.Setenv("CODING_HOST_TEST", "retained")
	environment := codingWorkerEnvironment("/operator/home")
	if countEnvironmentKey(environment, "MINTCLAW_HOME") != 1 ||
		countFoldedEnvironmentKey(environment, "MINTCLAW_HOME") != 1 ||
		!containsEnvironmentValue(environment, "MINTCLAW_HOME", "/operator/home") ||
		!containsEnvironmentValue(environment, "CODING_HOST_TEST", "retained") {
		t.Fatalf("worker environment = %#v", environment)
	}
}

type hostTestBackend struct {
	mu                 sync.Mutex
	ledger             *InvocationLedger
	processes          []*hostTestProcess
	mutationRoot       string
	prepareErr         error
	prepareStarted     chan struct{}
	blockPrepare       bool
	invalidPreparation bool
	abortErr           error
	startedOnce        sync.Once

	prepareCalls            int
	launchCalls             int
	abortCalls              int
	preparedRecords         []codingtask.Record
	sawPreparing            bool
	sawPreparedBeforeLaunch bool
}

func (backend *hostTestBackend) Prepare(
	ctx context.Context,
	_ CodingScopePolicy,
	record codingtask.Record,
	_ string,
) (codingPreparedTask, error) {
	backend.mu.Lock()
	backend.prepareCalls++
	backend.preparedRecords = append(backend.preparedRecords, record.Clone())
	prepareErr := backend.prepareErr
	blockPrepare := backend.blockPrepare
	backend.mu.Unlock()
	if backend.prepareStarted != nil {
		backend.startedOnce.Do(func() { close(backend.prepareStarted) })
	}
	if blockPrepare {
		<-ctx.Done()
		return codingPreparedTask{}, ctx.Err()
	}
	if prepareErr != nil {
		return codingPreparedTask{}, prepareErr
	}
	if backend.invalidPreparation {
		return codingPreparedTask{
			record: codingtask.Record{},
			launch: func(context.Context) (codingTaskProcess, error) {
				return nil, errors.New("invalid preparation launched")
			},
			abort: func() error {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				backend.abortCalls++
				return backend.abortErr
			},
		}, nil
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.ledger != nil {
		persisted, found := backend.ledger.codingTask(record.InvocationID)
		backend.sawPreparing = found && persisted.State == codingtask.StatePreparing &&
			persisted.ExecutionRoot == ""
	}
	if backend.mutationRoot != "" {
		record.ExecutionRoot = backend.mutationRoot
		record.ExecutionRootIdentity = codingtask.ExecutionRootIdentity(record.ExecutionRoot)
		record.Branch = "mintclaw/host-test"
	}
	if len(backend.processes) == 0 {
		return codingPreparedTask{}, errors.New("no test process")
	}
	process := backend.processes[0]
	backend.processes = backend.processes[1:]
	return codingPreparedTask{
		record: record,
		launch: func(context.Context) (codingTaskProcess, error) {
			backend.mu.Lock()
			defer backend.mu.Unlock()
			backend.launchCalls++
			if backend.ledger != nil {
				persisted, found := backend.ledger.codingTask(record.InvocationID)
				backend.sawPreparedBeforeLaunch = found && persisted.State == codingtask.StatePreparing &&
					persisted.ExecutionRoot == record.ExecutionRoot &&
					persisted.ExecutionRootIdentity == record.ExecutionRootIdentity
			}
			process.identity = hostTestControl(record)
			return process, nil
		},
		abort: func() error { return nil },
	}, nil
}

type hostTestProcess struct {
	mu               sync.Mutex
	done             chan struct{}
	wake             chan struct{}
	identity         worker.ControlIdentity
	events           []worker.RetainedEvent
	result           codingTaskProcessResult
	waitErr          error
	finished         bool
	historyGap       bool
	snapshotThreadID string
	snapshotActivity worker.Activity
	snapshotStatus   string
	snapshotQuestion *worker.QuestionState
	waitStarted      chan struct{}
	releaseWait      chan struct{}
	waitOnce         sync.Once

	startCalls             int
	startKey               string
	startText              string
	startErr               error
	steerCalls             int
	cancelCalls            int
	cancelKey              string
	shutdownCalls          int
	terminateCalls         int
	cancelResult           codingTaskProcessResult
	shutdownDoesNotFinish  bool
	terminateDoesNotFinish bool
}

func newHostTestProcess() *hostTestProcess {
	return &hostTestProcess{done: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (process *hostTestProcess) StartTurn(
	_ context.Context,
	key string,
	text string,
	_ []worker.TurnAttachment,
) error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.startCalls++
	process.startKey = key
	process.startText = text
	return process.startErr
}

func (process *hostTestProcess) Steer(
	_ context.Context,
	_ string,
	_ string,
	_ *worker.QuestionAnswerRef,
) error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.steerCalls++
	return nil
}

func (process *hostTestProcess) HardCancel(_ context.Context, key string) error {
	process.mu.Lock()
	process.cancelCalls++
	process.cancelKey = key
	result := process.cancelResult
	process.mu.Unlock()
	if result.outcome != "" {
		process.finish(result, nil)
	}
	return nil
}

func (process *hostTestProcess) Shutdown(_ context.Context, _ string) error {
	process.mu.Lock()
	process.shutdownCalls++
	stubborn := process.shutdownDoesNotFinish
	process.mu.Unlock()
	if !stubborn {
		process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeCanceled}, nil)
	}
	return nil
}

func (process *hostTestProcess) Snapshot(context.Context) (worker.SnapshotResult, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	threadID := process.snapshotThreadID
	if threadID == "" {
		threadID = uuid.NewString()
	}
	activity := process.snapshotActivity
	if activity == "" {
		activity = worker.ActivityRunning
	}
	return worker.SnapshotResult{
		ControlIdentity: process.identity,
		Snapshot: worker.Snapshot{
			ThreadID: threadID, Activity: activity, Status: process.snapshotStatus,
			Question: process.snapshotQuestion,
		},
	}, nil
}

func (process *hostTestProcess) EventsAfter(cursor uint64) worker.EventPage {
	process.mu.Lock()
	defer process.mu.Unlock()
	page := worker.EventPage{NextCursor: uint64(len(process.events)), HistoryGap: process.historyGap}
	process.historyGap = false
	for _, event := range process.events {
		if event.Cursor > cursor {
			page.Events = append(page.Events, event)
		}
	}
	return page
}

func (process *hostTestProcess) Wake() <-chan struct{} { return process.wake }
func (process *hostTestProcess) Done() <-chan struct{} { return process.done }

func (process *hostTestProcess) Wait(ctx context.Context) (codingTaskProcessResult, error) {
	select {
	case <-process.done:
		if process.waitStarted != nil {
			process.waitOnce.Do(func() { close(process.waitStarted) })
		}
		if process.releaseWait != nil {
			select {
			case <-process.releaseWait:
			case <-ctx.Done():
				return codingTaskProcessResult{}, ctx.Err()
			}
		}
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.result, process.waitErr
	case <-ctx.Done():
		return codingTaskProcessResult{}, ctx.Err()
	}
}

func (process *hostTestProcess) setGapSnapshot(
	threadID string,
	activity worker.Activity,
	status string,
	question *worker.QuestionState,
) {
	process.mu.Lock()
	process.historyGap = true
	process.snapshotThreadID = threadID
	process.snapshotActivity = activity
	process.snapshotStatus = status
	process.snapshotQuestion = question
	process.mu.Unlock()
	select {
	case process.wake <- struct{}{}:
	default:
	}
}

func (process *hostTestProcess) Terminate(context.Context) error {
	process.mu.Lock()
	process.terminateCalls++
	stubborn := process.terminateDoesNotFinish
	process.mu.Unlock()
	if !stubborn {
		process.finish(codingTaskProcessResult{outcome: codingTaskOutcomeUncertain}, nil)
	}
	return nil
}

func (process *hostTestProcess) Close() error { return nil }

func (process *hostTestProcess) emit(t *testing.T, event worker.EventName, payload any) {
	t.Helper()
	raw, err := worker.MarshalPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	process.events = append(process.events, worker.RetainedEvent{
		Cursor: uint64(len(process.events) + 1),
		Record: worker.Record{SchemaVersion: worker.ProtocolV2, Type: worker.RecordEvent, Event: event, Payload: raw},
	})
	process.mu.Unlock()
	select {
	case process.wake <- struct{}{}:
	default:
	}
}

func (process *hostTestProcess) finish(result codingTaskProcessResult, err error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.finished {
		return
	}
	process.finished = true
	process.result = result
	process.waitErr = err
	close(process.done)
}

func newHostTestFixture(
	t *testing.T,
	modes []codingtask.TaskMode,
	backend codingTaskBackend,
) (*CodingTaskHost, *InvocationLedger, *CodingScopeCatalog) {
	t.Helper()
	fixture := newCodingScopeFixture(t, modes)
	catalog, err := NewCodingScopeCatalog(fixture.scopes)
	if err != nil {
		t.Fatal(err)
	}
	ledger := newMemoryInvocationLedger()
	host, err := newCodingTaskHost(catalog, ledger, "test-build", backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = host.Shutdown(ctx)
	})
	return host, ledger, catalog
}

func acceptHostTestInvocation(t *testing.T, ledger *InvocationLedger, suffix string) nodes.ExecutionPlan {
	t.Helper()
	plan := testCodingTaskLedgerPlan(t, "host-"+suffix, time.Now())
	if _, _, err := ledger.Accept(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkRunning(plan.InvocationID); err != nil {
		t.Fatal(err)
	}
	return plan
}

func hostTestRequest(
	t *testing.T,
	catalog *CodingScopeCatalog,
	suffix string,
	mode codingtask.TaskMode,
) codingtask.StartRequest {
	t.Helper()
	descriptors := catalog.List()
	if len(descriptors) != 1 {
		t.Fatalf("catalog = %#v", descriptors)
	}
	return codingtask.NewStartRequest(
		"task-"+suffix,
		"generation-"+suffix,
		descriptors[0].Alias,
		descriptors[0].Revision,
		mode,
		"Inspect the repository.",
		"Return a bounded result.",
		"turn-"+suffix,
	)
}

func hostTestControl(record codingtask.Record) worker.ControlIdentity {
	return worker.ControlIdentity{
		TaskID: record.TaskID, TaskGenerationID: record.TaskGenerationID,
		WorkerGenerationID: record.WorkerGenerationID,
	}
}

func waitHostTestState(
	t *testing.T,
	host *CodingTaskHost,
	request codingtask.StartRequest,
	state codingtask.State,
	check func(codingtask.Record) bool,
) codingtask.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err := host.Status(request.TaskID, request.TaskGenerationID)
		if err == nil && record.State == state && (check == nil || check(record)) {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := host.Status(request.TaskID, request.TaskGenerationID)
	t.Fatalf("task state = %#v, error %v; want %s", record, err, state)
	return codingtask.Record{}
}

func countEnvironmentKey(environment []string, key string) int {
	count := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, key+"=") {
			count++
		}
	}
	return count
}

func containsEnvironmentValue(environment []string, key string, value string) bool {
	want := key + "=" + value
	for _, entry := range environment {
		if entry == want {
			return true
		}
	}
	return false
}

func countFoldedEnvironmentKey(environment []string, key string) int {
	count := 0
	for _, entry := range environment {
		entryKey, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(entryKey, key) {
			count++
		}
	}
	return count
}
