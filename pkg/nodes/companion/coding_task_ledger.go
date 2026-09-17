package companion

import (
	"errors"
	"fmt"
	"sort"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

var (
	ErrCodingTaskNotFound = errors.New("coding task not found")
	ErrCodingTaskConflict = errors.New("coding task projection conflict")
)

// bindCodingTask persists the node-local task authority on the accepted start
// invocation before a worker is launched. Mutable task content is initialized
// by the ledger clock and is never returned through the public invocation API.
func (ledger *InvocationLedger) bindCodingTask(
	invocationID string,
	record codingtask.Record,
) (codingtask.Record, bool, error) {
	if ledger == nil {
		return codingtask.Record{}, false, ErrCodingTaskNotFound
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	invocation, found := ledger.records[invocationID]
	if !found {
		return codingtask.Record{}, false, ErrInvocationNotFound
	}
	if invocation.Command != nodes.CodingCommandTaskStart || invocation.State != nodes.InvocationRunning {
		return codingtask.Record{}, false, fmt.Errorf(
			"%w: invocation cannot accept a coding task",
			ErrCodingTaskConflict,
		)
	}
	if !validUnboundCodingTask(invocationID, record) {
		return codingtask.Record{}, false, fmt.Errorf(
			"%w: malformed initial coding task",
			ErrCodingTaskConflict,
		)
	}
	if existing, exists := ledger.codingTasks[invocationID]; exists {
		record.AcceptedAt = existing.AcceptedAt
		record.UpdatedAt = existing.AcceptedAt
		record.Revision = 1
		if !record.SameIdentity(existing) {
			return codingtask.Record{}, false, ErrCodingTaskConflict
		}
		return existing.Clone(), true, nil
	}
	for existingInvocationID, existing := range ledger.codingTasks {
		if existingInvocationID != invocationID && existing.TaskID == record.TaskID &&
			existing.TaskGenerationID == record.TaskGenerationID {
			return codingtask.Record{}, false, ErrCodingTaskConflict
		}
	}
	now := ledger.now().UnixNano()
	record.AcceptedAt = now
	record.UpdatedAt = now
	record.Revision = 1
	if invocation.StartedAt == 0 || record.AcceptedAt < invocation.StartedAt || record.Validate() != nil {
		return codingtask.Record{}, false, fmt.Errorf(
			"%w: invalid initial coding task authority",
			ErrCodingTaskConflict,
		)
	}
	previous := ledger.snapshotLocked()
	ledger.codingTasks[invocationID] = record.Clone()
	if err := ledger.persistLocked(invocationID); err != nil {
		ledger.rollbackIfUncommittedLocked(previous, err)
		return codingtask.Record{}, false, fmt.Errorf("persist coding task binding: %w", err)
	}
	return record.Clone(), false, nil
}

// updateCodingTask serializes one revisioned projection transition with the
// invocation ledger. The callback may change lifecycle evidence only; task,
// thread, project, and worker authority remain immutable.
func (ledger *InvocationLedger) updateCodingTask(
	invocationID string,
	expectedRevision uint64,
	update func(*codingtask.Record, int64) error,
) (codingtask.Record, error) {
	if ledger == nil || update == nil {
		return codingtask.Record{}, ErrCodingTaskNotFound
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	current, found := ledger.codingTasks[invocationID]
	if !found {
		return codingtask.Record{}, ErrCodingTaskNotFound
	}
	if current.Revision != expectedRevision {
		return codingtask.Record{}, ErrCodingTaskConflict
	}
	next := current.Clone()
	now := ledger.now().UnixNano()
	if err := update(&next, now); err != nil {
		return codingtask.Record{}, err
	}
	next.AcceptedAt = current.AcceptedAt
	next.UpdatedAt = now
	next.Revision = current.Revision + 1
	if !current.SameIdentity(next) || !current.State.CanTransitionTo(next.State) {
		return codingtask.Record{}, ErrCodingTaskConflict
	}
	if err := next.Validate(); err != nil {
		return codingtask.Record{}, fmt.Errorf("%w: %w", ErrCodingTaskConflict, err)
	}
	previous := ledger.snapshotLocked()
	ledger.codingTasks[invocationID] = next.Clone()
	if err := ledger.persistLocked(invocationID); err != nil {
		ledger.rollbackIfUncommittedLocked(previous, err)
		return codingtask.Record{}, fmt.Errorf("persist coding task transition: %w", err)
	}
	return next.Clone(), nil
}

// advanceCodingTaskWorker durably admits one explicit successor generation
// before repository preparation or process launch. It is the only task-ledger
// transition allowed to change worker identity and thread open mode.
func (ledger *InvocationLedger) advanceCodingTaskWorker(
	invocationID string,
	expectedRevision uint64,
	request codingtask.ResumeRequest,
	workerGenerationID string,
) (codingtask.Record, bool, error) {
	if ledger == nil || request.Validate() != nil || !codingtask.ValidIdentifier(workerGenerationID) {
		return codingtask.Record{}, false, codingtask.ErrInvalidRequest
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	current, found := ledger.codingTasks[invocationID]
	if !found {
		return codingtask.Record{}, false, ErrCodingTaskNotFound
	}
	if current.MatchesResumeRequest(request) {
		return current.Clone(), true, nil
	}
	if current.Revision != expectedRevision || current.TaskID != request.TaskID ||
		current.TaskGenerationID != request.TaskGenerationID ||
		current.WorkerGenerationID != request.PreviousWorkerGenerationID ||
		current.WorkerGenerationID == workerGenerationID || current.State != codingtask.StateIdle {
		return codingtask.Record{}, false, ErrCodingTaskConflict
	}
	next := current.Clone()
	next.ThreadOpenMode = codingtask.ThreadOpenResume
	next.WorkerGenerationID = workerGenerationID
	next.ResumeSequence++
	next.ResumeRequestDigest = request.RequestDigest
	next.ResumeIdempotencyKey = request.TurnIdempotencyKey
	next.State = codingtask.StatePreparing
	next.Activity = ""
	next.Status = ""
	next.Question = nil
	next.HandoffID = ""
	next.Failure = nil
	next.RetainUntil = 0
	now := ledger.now().UnixNano()
	next.AcceptedAt = current.AcceptedAt
	next.UpdatedAt = now
	next.Revision = current.Revision + 1
	if err := next.Validate(); err != nil {
		return codingtask.Record{}, false, fmt.Errorf("%w: %w", ErrCodingTaskConflict, err)
	}
	previous := ledger.snapshotLocked()
	ledger.codingTasks[invocationID] = next.Clone()
	if err := ledger.persistLocked(invocationID); err != nil {
		ledger.rollbackIfUncommittedLocked(previous, err)
		return codingtask.Record{}, false, fmt.Errorf("persist coding task successor: %w", err)
	}
	return next.Clone(), false, nil
}

func (ledger *InvocationLedger) codingTask(invocationID string) (codingtask.Record, bool) {
	if ledger == nil {
		return codingtask.Record{}, false
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	record, found := ledger.codingTasks[invocationID]
	return record.Clone(), found
}

func (ledger *InvocationLedger) codingTaskRecords() []codingtask.Record {
	if ledger == nil {
		return nil
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	records := make([]codingtask.Record, 0, len(ledger.codingTasks))
	for _, record := range ledger.codingTasks {
		records = append(records, record.Clone())
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].AcceptedAt == records[right].AcceptedAt {
			return records[left].InvocationID < records[right].InvocationID
		}
		return records[left].AcceptedAt < records[right].AcceptedAt
	})
	return records
}

// HasCodingTasks reports whether startup must recover retained coding-task
// lifecycle even when the operator has removed every current project alias.
func (ledger *InvocationLedger) HasCodingTasks() bool {
	if ledger == nil {
		return false
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.codingTasks) > 0
}

func validUnboundCodingTask(invocationID string, record codingtask.Record) bool {
	return record.InvocationID == invocationID && record.State == codingtask.StateAccepted &&
		record.ThreadOpenMode == codingtask.ThreadOpenNew && record.Revision == 0 &&
		record.AcceptedAt == 0 && record.UpdatedAt == 0 && record.Activity == "" && record.Status == "" &&
		record.Question == nil && record.HandoffID == "" && record.Branch == "" && record.Failure == nil &&
		record.RetainUntil == 0
}

func validatePersistedCodingTasks(
	invocations map[string]nodes.InvocationRecord,
	tasks map[string]codingtask.Record,
) error {
	identities := make(map[string]struct{}, len(tasks))
	for invocationID, record := range tasks {
		invocation, found := invocations[invocationID]
		if !found || invocationID != record.InvocationID || invocation.Command != nodes.CodingCommandTaskStart ||
			invocation.StartedAt == 0 || record.AcceptedAt < invocation.StartedAt ||
			(invocation.CompletedAt != 0 && record.AcceptedAt > invocation.CompletedAt) {
			return errors.New("node invocation ledger contains an unrelated coding task")
		}
		if err := record.Validate(); err != nil {
			return fmt.Errorf("validate node invocation ledger coding task: %w", err)
		}
		identity := record.TaskID + "\x00" + record.TaskGenerationID
		if _, duplicate := identities[identity]; duplicate {
			return errors.New("node invocation ledger contains a duplicate coding task identity")
		}
		identities[identity] = struct{}{}
	}
	return nil
}

func cloneCodingTaskRecords(records map[string]codingtask.Record) map[string]codingtask.Record {
	cloned := make(map[string]codingtask.Record, len(records))
	for invocationID, record := range records {
		cloned[invocationID] = record.Clone()
	}
	return cloned
}
