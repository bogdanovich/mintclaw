package companion

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestInvocationLedgerPersistsCodingTaskWithoutPublicPathDisclosure(t *testing.T) {
	clock := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "invocations.json")
	ledger, err := NewFileInvocationLedger(path, 4, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	ledger.now = func() time.Time { return clock }
	plan := testCodingTaskLedgerPlan(t, "persisted", clock)
	if _, _, err := ledger.Accept(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkRunning(plan.InvocationID); err != nil {
		t.Fatal(err)
	}
	unbound := testUnboundCodingTask(t, plan.InvocationID, "persisted")
	bound, existing, err := ledger.bindCodingTask(plan.InvocationID, unbound)
	if err != nil || existing || bound.Revision != 1 || bound.AcceptedAt != clock.UnixNano() {
		t.Fatalf("bindCodingTask() = %#v, existing %v, error %v", bound, existing, err)
	}
	repeated, existing, err := ledger.bindCodingTask(plan.InvocationID, unbound)
	if err != nil || !existing || !repeated.SameIdentity(bound) {
		t.Fatalf("repeated bindCodingTask() = %#v, existing %v, error %v", repeated, existing, err)
	}
	conflict := unbound
	conflict.ThreadID = uuid.NewString()
	if _, _, err := ledger.bindCodingTask(plan.InvocationID, conflict); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("conflicting bind error = %v", err)
	}
	duplicatePlan := testCodingTaskLedgerPlan(t, "duplicate-identity", clock)
	if _, _, err := ledger.Accept(duplicatePlan); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkRunning(duplicatePlan.InvocationID); err != nil {
		t.Fatal(err)
	}
	duplicateIdentity := unbound
	duplicateIdentity.InvocationID = duplicatePlan.InvocationID
	if _, _, err := ledger.bindCodingTask(
		duplicatePlan.InvocationID,
		duplicateIdentity,
	); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("duplicate task identity error = %v", err)
	}
	if _, err := ledger.CompleteFailure(duplicatePlan.InvocationID, nodes.InvocationFailure{
		Code: "TASK_CONFLICT", Message: "coding task identity conflicts with retained authority",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CompleteSuccess(plan.InvocationID, json.RawMessage(`{"accepted":true}`)); err != nil {
		t.Fatal(err)
	}

	public, found := ledger.Get(plan.InvocationID)
	if !found {
		t.Fatal("bound start invocation disappeared")
	}
	publicJSON, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), bound.Project.ProjectRoot) ||
		strings.Contains(string(publicJSON), "coding_tasks") {
		t.Fatalf("public invocation disclosed node-local coding authority: %s", publicJSON)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"coding_tasks"`) ||
		!strings.Contains(string(data), bound.Project.ProjectRoot) {
		t.Fatalf("private ledger lacks coding projection: %s", data)
	}

	ledger.Close()
	reloaded, err := NewFileInvocationLedger(path, 4, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	persisted, found := reloaded.codingTask(plan.InvocationID)
	if !found || !persisted.SameIdentity(bound) || persisted.Revision != bound.Revision {
		t.Fatalf("reloaded coding task = %#v, found %v", persisted, found)
	}
	list := reloaded.codingTaskRecords()
	if len(list) != 1 || !list[0].SameIdentity(bound) {
		t.Fatalf("coding task recovery list = %#v", list)
	}
}

func TestInvocationLedgerRollsBackUncommittedCodingTaskWrites(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger(
		filepath.Join(t.TempDir(), "invocations.json"),
		4,
		1024*1024,
		func() time.Time { return clock },
	)
	plan := testCodingTaskLedgerPlan(t, "write-failure", clock)
	if _, _, err := ledger.Accept(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkRunning(plan.InvocationID); err != nil {
		t.Fatal(err)
	}
	writeFile := ledger.writeFile
	ledger.writeFile = func(string, []byte, os.FileMode) error {
		return errors.New("storage unavailable")
	}
	if _, _, err := ledger.bindCodingTask(
		plan.InvocationID,
		testUnboundCodingTask(t, plan.InvocationID, "write-failure"),
	); err == nil {
		t.Fatal("bindCodingTask() succeeded without durable storage")
	}
	if _, found := ledger.codingTask(plan.InvocationID); found {
		t.Fatal("uncommitted coding task remained in memory")
	}

	ledger.writeFile = writeFile
	bound, _, err := ledger.bindCodingTask(
		plan.InvocationID,
		testUnboundCodingTask(t, plan.InvocationID, "write-failure"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ledger.writeFile = func(string, []byte, os.FileMode) error {
		return errors.New("storage unavailable")
	}
	clock = clock.Add(time.Second)
	if _, err := ledger.updateCodingTask(plan.InvocationID, bound.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StatePreparing
		return nil
	}); err == nil {
		t.Fatal("updateCodingTask() succeeded without durable storage")
	}
	stored, found := ledger.codingTask(plan.InvocationID)
	if !found || stored.Revision != bound.Revision || stored.State != codingtask.StateAccepted {
		t.Fatalf("uncommitted coding transition changed memory: %#v, found %v", stored, found)
	}
}

func TestInvocationLedgerPersistsIdempotentCodingTaskSuccessorWithoutText(t *testing.T) {
	clock := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "invocations.json")
	ledger, err := NewFileInvocationLedger(path, 4, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	ledger.now = func() time.Time { return clock }
	plan := testCodingTaskLedgerPlan(t, "successor", clock)
	record := bindTestCodingTask(t, ledger, plan, "successor")
	record = advanceTestCodingTaskToIdle(t, ledger, record)
	request := codingtask.NewResumeRequest(
		record.TaskID,
		record.TaskGenerationID,
		record.WorkerGenerationID,
		"Continue with the retained thread, but do not replay earlier work.",
		"turn-successor-two",
	)
	clock = clock.Add(time.Second)
	successor, existing, err := ledger.advanceCodingTaskWorker(
		record.InvocationID,
		record.Revision,
		request,
		"worker-successor-two",
	)
	if err != nil || existing || successor.State != codingtask.StatePreparing ||
		successor.ThreadID != record.ThreadID || successor.ThreadOpenMode != codingtask.ThreadOpenResume ||
		successor.WorkerGenerationID != "worker-successor-two" || successor.ResumeSequence != 1 ||
		successor.ResumeRequestDigest != request.RequestDigest ||
		successor.ResumeIdempotencyKey != request.TurnIdempotencyKey {
		t.Fatalf("advanceCodingTaskWorker() = %#v, existing %v, error %v", successor, existing, err)
	}
	repeated, existing, err := ledger.advanceCodingTaskWorker(
		record.InvocationID,
		record.Revision,
		request,
		"worker-unused-duplicate",
	)
	if err != nil || !existing || !repeated.SameIdentity(successor) {
		t.Fatalf("duplicate successor = %#v, existing %v, error %v", repeated, existing, err)
	}
	conflict := codingtask.NewResumeRequest(
		record.TaskID,
		record.TaskGenerationID,
		record.WorkerGenerationID,
		"Use different text with the same idempotency key.",
		request.TurnIdempotencyKey,
	)
	if _, _, err := ledger.advanceCodingTaskWorker(
		record.InvocationID,
		successor.Revision,
		conflict,
		"worker-conflict",
	); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("conflicting successor error = %v", err)
	}
	if _, err := ledger.updateCodingTask(successor.InvocationID, successor.Revision, func(
		next *codingtask.Record,
		_ int64,
	) error {
		next.WorkerGenerationID = "worker-generic-update"
		return nil
	}); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("generic worker rewrite error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), request.Text) || !strings.Contains(string(data), request.RequestDigest) {
		t.Fatalf("persisted successor evidence = %s", data)
	}
	ledger.Close()
	reloaded, err := NewFileInvocationLedger(path, 4, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	persisted, found := reloaded.codingTask(record.InvocationID)
	if !found || !persisted.SameIdentity(successor) {
		t.Fatalf("reloaded successor = %#v, found %v", persisted, found)
	}
}

func TestInvocationLedgerRollsBackUncommittedCodingTaskSuccessor(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger(
		filepath.Join(t.TempDir(), "invocations.json"),
		4,
		1024*1024,
		func() time.Time { return clock },
	)
	plan := testCodingTaskLedgerPlan(t, "successor-write-failure", clock)
	record := bindTestCodingTask(t, ledger, plan, "successor-write-failure")
	record = advanceTestCodingTaskToIdle(t, ledger, record)
	request := codingtask.NewResumeRequest(
		record.TaskID,
		record.TaskGenerationID,
		record.WorkerGenerationID,
		"Continue only if successor admission is durable.",
		"turn-successor-write-failure",
	)
	ledger.writeFile = func(string, []byte, os.FileMode) error {
		return errors.New("storage unavailable")
	}
	if _, _, err := ledger.advanceCodingTaskWorker(
		record.InvocationID,
		record.Revision,
		request,
		"worker-successor-write-failure",
	); err == nil {
		t.Fatal("advanceCodingTaskWorker() succeeded without durable storage")
	}
	stored, found := ledger.codingTask(record.InvocationID)
	if !found || !stored.SameIdentity(record) || stored.Revision != record.Revision ||
		stored.State != codingtask.StateIdle {
		t.Fatalf("uncommitted successor changed memory: %#v, found %v", stored, found)
	}
}

func TestInvocationLedgerRejectsCodingTaskBindAfterClockMovesBehindStart(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger("", 4, 1024*1024, func() time.Time { return clock })
	plan := testCodingTaskLedgerPlan(t, "backward-bind-clock", clock)
	if _, _, err := ledger.Accept(plan); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Second)
	if _, err := ledger.MarkRunning(plan.InvocationID); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(-time.Second)
	if _, _, err := ledger.bindCodingTask(
		plan.InvocationID,
		testUnboundCodingTask(t, plan.InvocationID, "backward-bind-clock"),
	); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("backward-clock bind error = %v", err)
	}
	if _, found := ledger.codingTask(plan.InvocationID); found {
		t.Fatal("backward-clock bind retained a coding task")
	}
}

func TestInvocationLedgerSerializesCodingTaskProjectionTransitions(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger("", 4, 1024*1024, func() time.Time { return clock })
	plan := testCodingTaskLedgerPlan(t, "transitions", clock)
	bound := bindTestCodingTask(t, ledger, plan, "transitions")

	clock = clock.Add(time.Second)
	preparing, err := ledger.updateCodingTask(plan.InvocationID, bound.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StatePreparing
		return nil
	})
	if err != nil || preparing.Revision != 2 {
		t.Fatalf("preparing transition = %#v, %v", preparing, err)
	}
	clock = clock.Add(time.Second)
	running, err := ledger.updateCodingTask(plan.InvocationID, preparing.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StateRunning
		record.Activity = codingtask.ActivityRunning
		record.Status = "inspecting repository"
		return nil
	})
	if err != nil || running.Revision != 3 {
		t.Fatalf("running transition = %#v, %v", running, err)
	}
	if _, err := ledger.updateCodingTask(plan.InvocationID, preparing.Revision, func(
		*codingtask.Record,
		int64,
	) error {
		return nil
	}); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := ledger.updateCodingTask(plan.InvocationID, running.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.Model = "changed-model"
		return nil
	}); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("immutable authority error = %v", err)
	}
	stableClock := clock
	clock = clock.Add(-time.Hour)
	before, _ := ledger.codingTask(plan.InvocationID)
	if _, err := ledger.updateCodingTask(plan.InvocationID, before.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.Status = "clock moved backward"
		return nil
	}); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("backward clock error = %v", err)
	}
	after, _ := ledger.codingTask(plan.InvocationID)
	if after.Revision != before.Revision || after.UpdatedAt != before.UpdatedAt || after.Status != before.Status {
		t.Fatalf("failed transition changed stored task: before %#v, after %#v", before, after)
	}
	clock = stableClock

	clock = clock.Add(time.Second)
	waiting, err := ledger.updateCodingTask(plan.InvocationID, running.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StateWaitingInput
		record.Activity = codingtask.ActivityWaitingInput
		record.Question = &codingtask.QuestionState{
			QuestionID: "question-one", Revision: 1, Status: codingtask.QuestionWaiting,
			Prompt: "Which package?", Options: []codingtask.QuestionOption{{ID: "core", Label: "Core"}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting.Question.Options[0].Label = "Changed"
	stored, found := ledger.codingTask(plan.InvocationID)
	if !found || stored.Question == nil || stored.Question.Options[0].Label != "Core" {
		t.Fatalf("stored question aliased caller data: %#v", stored)
	}

	clock = clock.Add(time.Second)
	completed, err := ledger.updateCodingTask(plan.InvocationID, stored.Revision, func(
		record *codingtask.Record,
		now int64,
	) error {
		record.State = codingtask.StateCompleted
		record.Activity = codingtask.ActivityIdle
		record.Status = "investigation completed"
		record.Question = nil
		record.RetainUntil = now + int64(time.Hour)
		return nil
	})
	if err != nil || completed.State != codingtask.StateCompleted {
		t.Fatalf("completed transition = %#v, %v", completed, err)
	}
	if _, err := ledger.updateCodingTask(plan.InvocationID, completed.Revision, func(
		*codingtask.Record,
		int64,
	) error {
		return nil
	}); !errors.Is(err, ErrCodingTaskConflict) {
		t.Fatalf("terminal rewrite error = %v", err)
	}
}

func TestInvocationLedgerCodingTaskRevisionCAS(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger("", 4, 1024*1024, func() time.Time { return clock })
	plan := testCodingTaskLedgerPlan(t, "concurrent", clock)
	bound := bindTestCodingTask(t, ledger, plan, "concurrent")
	clock = clock.Add(time.Second)

	errorsByUpdate := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := ledger.updateCodingTask(plan.InvocationID, bound.Revision, func(
				record *codingtask.Record,
				_ int64,
			) error {
				record.State = codingtask.StatePreparing
				return nil
			})
			errorsByUpdate <- err
		}()
	}
	wait.Wait()
	close(errorsByUpdate)
	var succeeded, conflicted int
	for err := range errorsByUpdate {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrCodingTaskConflict):
			conflicted++
		default:
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent updates: succeeded %d, conflicted %d", succeeded, conflicted)
	}
	stored, found := ledger.codingTask(plan.InvocationID)
	if !found || stored.Revision != bound.Revision+1 || stored.State != codingtask.StatePreparing {
		t.Fatalf("concurrent stored task = %#v, found %v", stored, found)
	}
}

func TestInvocationLedgerProtectsCodingTaskThroughRetention(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger("", 1, 1024*1024, func() time.Time { return clock })
	plan := testCodingTaskLedgerPlan(t, "retained", clock)
	bound := bindTestCodingTask(t, ledger, plan, "retained")
	if _, err := ledger.CompleteSuccess(plan.InvocationID, json.RawMessage(`{"accepted":true}`)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	preparing, err := ledger.updateCodingTask(plan.InvocationID, bound.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StatePreparing
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	running, err := ledger.updateCodingTask(plan.InvocationID, preparing.Revision, func(
		record *codingtask.Record,
		_ int64,
	) error {
		record.State = codingtask.StateRunning
		record.Activity = codingtask.ActivityRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	completed, err := ledger.updateCodingTask(plan.InvocationID, running.Revision, func(
		record *codingtask.Record,
		now int64,
	) error {
		record.State = codingtask.StateCompleted
		record.Activity = codingtask.ActivityIdle
		record.RetainUntil = now + int64(time.Hour)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(2 * time.Minute)
	fresh := testCodingTaskLedgerPlan(t, "before-retention", clock)
	if _, _, err := ledger.Accept(fresh); !errors.Is(err, ErrInvocationLedgerFull) {
		t.Fatalf("retained coding task capacity error = %v", err)
	}
	if _, found := ledger.codingTask(plan.InvocationID); !found {
		t.Fatal("coding task was pruned before its retention boundary")
	}
	clock = time.Unix(0, completed.RetainUntil).Add(time.Second)
	fresh = testCodingTaskLedgerPlan(t, "after-retention", clock)
	if _, _, err := ledger.Accept(fresh); err != nil {
		t.Fatalf("expired retained task blocked capacity: %v", err)
	}
	if _, found := ledger.codingTask(plan.InvocationID); found {
		t.Fatal("expired coding projection survived invocation pruning")
	}
}

func TestInvocationLedgerRejectsUnrelatedPersistedCodingTasks(t *testing.T) {
	clock := time.Now().UTC()
	ledger := newInvocationLedger("", 4, 1024*1024, func() time.Time { return clock })
	plan := testCodingTaskLedgerPlan(t, "validation", clock)
	bindTestCodingTask(t, ledger, plan, "validation")
	if err := validatePersistedCodingTasks(ledger.records, ledger.codingTasks); err != nil {
		t.Fatalf("valid persisted coding task error = %v", err)
	}

	orphanedInvocations := cloneInvocationRecords(ledger.records)
	delete(orphanedInvocations, plan.InvocationID)
	if err := validatePersistedCodingTasks(orphanedInvocations, ledger.codingTasks); err == nil {
		t.Fatal("orphaned coding task validated")
	}
	wrongCommand := cloneInvocationRecords(ledger.records)
	record := wrongCommand[plan.InvocationID]
	record.Command = "node.info.v1"
	wrongCommand[plan.InvocationID] = record
	if err := validatePersistedCodingTasks(wrongCommand, ledger.codingTasks); err == nil {
		t.Fatal("coding task bound to a non-start command validated")
	}
	acceptedInvocation := cloneInvocationRecords(ledger.records)
	record = acceptedInvocation[plan.InvocationID]
	record.State = nodes.InvocationAccepted
	record.StartedAt = 0
	acceptedInvocation[plan.InvocationID] = record
	if err := validatePersistedCodingTasks(acceptedInvocation, ledger.codingTasks); err == nil {
		t.Fatal("coding task bound before a running invocation validated")
	}

	duplicateInvocations := cloneInvocationRecords(ledger.records)
	duplicateTasks := cloneCodingTaskRecords(ledger.codingTasks)
	duplicateInvocation := duplicateInvocations[plan.InvocationID]
	duplicateInvocation.InvocationID = "inv_validation_duplicate"
	duplicateInvocation.IdempotencyKey = "idem_validation_duplicate"
	duplicateInvocations[duplicateInvocation.InvocationID] = duplicateInvocation
	duplicateTask := duplicateTasks[plan.InvocationID]
	duplicateTask.InvocationID = duplicateInvocation.InvocationID
	duplicateTasks[duplicateInvocation.InvocationID] = duplicateTask
	if err := validatePersistedCodingTasks(duplicateInvocations, duplicateTasks); err == nil {
		t.Fatal("duplicate coding task identity validated")
	}
}

func bindTestCodingTask(
	t *testing.T,
	ledger *InvocationLedger,
	plan nodes.ExecutionPlan,
	suffix string,
) codingtask.Record {
	return bindTestCodingTaskWithMode(t, ledger, plan, suffix, codingtask.TaskModeInvestigate)
}

func bindTestCodingTaskWithMode(
	t *testing.T,
	ledger *InvocationLedger,
	plan nodes.ExecutionPlan,
	suffix string,
	mode codingtask.TaskMode,
) codingtask.Record {
	t.Helper()
	if _, _, err := ledger.Accept(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkRunning(plan.InvocationID); err != nil {
		t.Fatal(err)
	}
	record, _, err := ledger.bindCodingTask(
		plan.InvocationID,
		testUnboundCodingTaskWithMode(t, plan.InvocationID, suffix, mode),
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func advanceTestCodingTaskToIdle(
	t *testing.T,
	ledger *InvocationLedger,
	record codingtask.Record,
) codingtask.Record {
	t.Helper()
	preparing, err := ledger.updateCodingTask(record.InvocationID, record.Revision, func(
		next *codingtask.Record,
		_ int64,
	) error {
		next.State = codingtask.StatePreparing
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := ledger.updateCodingTask(record.InvocationID, preparing.Revision, func(
		next *codingtask.Record,
		_ int64,
	) error {
		next.State = codingtask.StateRunning
		next.Activity = codingtask.ActivityRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := ledger.updateCodingTask(record.InvocationID, running.Revision, func(
		next *codingtask.Record,
		_ int64,
	) error {
		next.State = codingtask.StateIdle
		next.Activity = codingtask.ActivityIdle
		next.Status = "coding worker idle"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return idle
}

func testUnboundCodingTask(t *testing.T, invocationID string, suffix string) codingtask.Record {
	return testUnboundCodingTaskWithMode(t, invocationID, suffix, codingtask.TaskModeInvestigate)
}

func testUnboundCodingTaskWithMode(
	t *testing.T,
	invocationID string,
	suffix string,
	mode codingtask.TaskMode,
) codingtask.Record {
	t.Helper()
	root := t.TempDir()
	identity := project.ProjectIdentity{
		Kind: project.ProjectKindDirectory, ProjectRoot: root, InvocationCWD: root,
	}
	identity.ProjectKey = project.ProjectKey(identity.Kind, identity.ProjectRoot)
	threadID := uuid.NewString()
	record := codingtask.Record{
		SchemaVersion: codingtask.SchemaVersion, InvocationID: invocationID,
		RequestDigest: strings.Repeat("a", 64), TaskID: "task-" + suffix,
		TaskGenerationID: "generation-" + suffix, ScopeAlias: "mintclaw",
		ScopeRevision: "revision-one", Profile: mode,
		ThreadID: threadID, ThreadOpenMode: codingtask.ThreadOpenNew,
		WorkerGenerationID: "worker-" + suffix, Project: identity,
		ExecutionRoot: root, ExecutionRootIdentity: codingtask.ExecutionRootIdentity(root),
		ProviderProfile: "default", Model: "gpt-test", Provider: "openai",
		ExpectedWorkerBuildID: "sha256:" + strings.Repeat("b", 64),
		State:                 codingtask.StateAccepted,
	}
	if mode.UsesIsolatedWorktree() {
		record.WorktreeID = codingtask.WorktreeIDForThread(threadID)
		record.ExecutionRoot = ""
		record.ExecutionRootIdentity = ""
	}
	return record
}

func testCodingTaskLedgerPlan(t *testing.T, suffix string, preparedAt time.Time) nodes.ExecutionPlan {
	return testCodingTaskLedgerPlanWithMode(t, suffix, preparedAt, codingtask.TaskModeInvestigate)
}

func testCodingTaskLedgerPlanWithMode(
	t *testing.T,
	suffix string,
	preparedAt time.Time,
	mode codingtask.TaskMode,
) nodes.ExecutionPlan {
	t.Helper()
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	var descriptor nodes.CommandDescriptor
	for _, candidate := range descriptors {
		if candidate.Name == nodes.CodingCommandTaskStart {
			descriptor = candidate
			break
		}
	}
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{descriptor}}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := nodes.NewCodingTaskStartInputs(
		"task-plan-"+suffix,
		"generation-plan-"+suffix,
		"mintclaw",
		"revision-one",
		mode,
		"Inspect the repository.",
		"",
		"turn-plan-"+suffix,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "inv_" + suffix, IdempotencyKey: "idem_" + suffix,
		NodeID: nodes.ID("node_test"), CatalogHash: catalogHash, Command: descriptor.Name,
		Input: rawInput, AgentID: "agent_test", SessionID: "session_test",
		ActorID: "actor_test", TimeoutSeconds: 5, OutputLimitBytes: 4096,
	}, descriptor, LocalExecutor, "policy-test", preparedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
