package tasks

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func (r *Registry) Create(rec Record) error {
	err := r.storeNewGeneration(rec, true)
	if fileutil.IsCommittedWriteError(err) {
		return nil
	}
	return err
}

func (r *Registry) Upsert(rec Record) error {
	return r.storeNewGeneration(rec, false)
}

func (r *Registry) storeNewGeneration(rec Record, rejectExisting bool) error {
	if r == nil || strings.TrimSpace(rec.TaskID) == "" {
		return nil
	}
	rec = cloneTaskRecord(rec)
	now := time.Now().UnixMilli()
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	if rec.LastEventAt == 0 {
		rec.LastEventAt = now
	}
	if rec.Status == "" {
		rec.Status = StatusQueued
	}
	if rec.DeliveryStatus == "" {
		rec.DeliveryStatus = DeliveryPending
	}
	if rec.NotifyPolicy == "" {
		rec.NotifyPolicy = NotifyDoneOnly
	}
	if rec.Runtime == "" {
		rec.Runtime = RuntimeTool
	}
	rec = r.normalizeRecord(rec, now)
	rec.GenerationID = uuid.NewString()
	rec.LastEventSeq = 0

	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	if _, ok := r.records[rec.TaskID]; ok {
		if rejectExisting {
			r.mu.Unlock()
			return fmt.Errorf("task %q: %w", rec.TaskID, ErrTaskAlreadyExists)
		}
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	r.records[rec.TaskID] = rec
	r.appendEventLocked(rec, EventTaskUpserted, now, map[string]string{
		"task_kind": rec.TaskKind,
		"label":     rec.Label,
	})
	newEvents := r.eventsSinceLocked(eventStart)
	r.pruneMutationLocked(now, newEvents)
	err := r.saveLocked()
	r.completeMutationLocked(err, rollbackState)
	r.mu.Unlock()
	return err
}

func (r *Registry) Update(taskID string, mutate func(*Record)) error {
	if r == nil || strings.TrimSpace(taskID) == "" || mutate == nil {
		return nil
	}
	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	rec, ok := r.records[taskID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("task %q not found", taskID)
	}
	before := cloneTaskRecord(rec)
	rec = cloneTaskRecord(rec)
	mutate(&rec)
	rec.GenerationID = before.GenerationID
	rec.LastEventSeq = before.LastEventSeq
	now := time.Now().UnixMilli()
	if rec.LastEventAt == 0 || recordChanged(before, rec) {
		rec.LastEventAt = now
	}
	rec = r.normalizeRecord(rec, rec.LastEventAt)
	r.records[taskID] = rec
	r.appendUpdateEventsLocked(before, rec, rec.LastEventAt)
	newEvents := r.eventsSinceLocked(eventStart)
	r.pruneMutationLocked(rec.LastEventAt, newEvents)
	err := r.saveLocked()
	r.completeMutationLocked(err, rollbackState)
	r.mu.Unlock()
	return err
}

func (r *Registry) AppendEvent(taskID string, eventType EventType, payload map[string]string) error {
	if r == nil || strings.TrimSpace(taskID) == "" || eventType == "" {
		return nil
	}
	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	rec, ok := r.records[taskID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("task %q not found", taskID)
	}
	now := time.Now().UnixMilli()
	r.appendEventLocked(rec, eventType, now, payload)
	newEvents := r.eventsSinceLocked(eventStart)
	r.pruneMutationLocked(now, newEvents)
	err := r.saveLocked()
	r.completeMutationLocked(err, rollbackState)
	r.mu.Unlock()
	return err
}

func (r *Registry) Heartbeat(taskID, progress string) error {
	now := time.Now().UnixMilli()
	return r.Update(taskID, func(rec *Record) {
		if rec.Status != StatusQueued && rec.Status != StatusRunning {
			return
		}
		rec.LastEventAt = now
		if progress = strings.TrimSpace(progress); progress != "" {
			rec.ProgressSummary = progress
		}
	})
}

// MarkWaitingForInput projects a durable human interaction onto a running task.
// Only bounded, user-safe interaction metadata belongs in the task registry.
func (r *Registry) MarkWaitingForInput(
	taskID, interactionID, shortID, summary string,
) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("interaction ID is required")
	}
	return r.updateInteractionProjection(taskID, func(rec *Record) (bool, error) {
		if rec.Status == StatusWaitingForInput && rec.InteractionID != interactionID {
			return false, fmt.Errorf(
				"task %q already waits for interaction %q",
				taskID,
				rec.InteractionID,
			)
		}
		if rec.Status != StatusQueued && rec.Status != StatusRunning &&
			rec.Status != StatusWaitingForInput {
			return false, fmt.Errorf(
				"task %q cannot wait for input from status %q", taskID, rec.Status,
			)
		}
		rec.Status = StatusWaitingForInput
		rec.InteractionID = interactionID
		rec.InteractionShortID = truncateInteractionField(shortID, 64)
		rec.InteractionSummary = truncateInteractionField(summary, 500)
		rec.ProgressSummary = "waiting for human input"
		return true, nil
	})
}

// MarkInteractionRunning returns a matching waiting task to running before its
// suspended continuation starts. The interaction ID is retained for audit and
// correlation while display-only waiting metadata is cleared.
func (r *Registry) MarkInteractionRunning(taskID, interactionID string) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("interaction ID is required")
	}
	return r.updateInteractionProjection(taskID, func(rec *Record) (bool, error) {
		if rec.InteractionID != interactionID {
			return false, fmt.Errorf(
				"task %q interaction mismatch: have %q, got %q",
				taskID,
				rec.InteractionID,
				interactionID,
			)
		}
		if rec.Status != StatusWaitingForInput && rec.Status != StatusRunning &&
			rec.Status != StatusLost {
			return false, fmt.Errorf(
				"task %q cannot resume from status %q", taskID, rec.Status,
			)
		}
		if rec.Status == StatusLost {
			rec.EndedAt = 0
			rec.CleanupAfter = 0
			rec.Error = ""
			if rec.DeliveryStatus == DeliveryNotApplicable {
				rec.DeliveryStatus = DeliveryPending
			}
		}
		rec.Status = StatusRunning
		rec.InteractionShortID = ""
		rec.InteractionSummary = ""
		rec.ProgressSummary = "resuming after human input"
		return true, nil
	})
}

// FinishInteraction projects a terminal interaction failure onto its owning
// task. Successful answers resume the task and are completed by the task owner.
func (r *Registry) FinishInteraction(
	taskID, interactionID string,
	status Status,
	summary string,
) error {
	switch status {
	case StatusFailed, StatusTimedOut, StatusCancelled:
	default:
		return fmt.Errorf("invalid terminal interaction task status %q", status)
	}
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("interaction ID is required")
	}
	return r.updateInteractionProjection(taskID, func(rec *Record) (bool, error) {
		if rec.InteractionID != interactionID {
			return false, fmt.Errorf(
				"task %q interaction mismatch: have %q, got %q",
				taskID,
				rec.InteractionID,
				interactionID,
			)
		}
		undeliveredSucceededResult := rec.Status == StatusSucceeded &&
			rec.DeliveryStatus != DeliveryDelivered &&
			rec.DeliveryStatus != DeliverySessionQueued &&
			rec.DeliveryStatus != DeliveryNotApplicable
		canFailUndeliveredResult := status == StatusFailed && undeliveredSucceededResult
		canCancelUndeliveredResult := status == StatusCancelled && undeliveredSucceededResult
		if isTerminalStatus(rec.Status) && rec.Status != StatusLost &&
			!canFailUndeliveredResult && !canCancelUndeliveredResult {
			return false, nil
		}
		if rec.Status == StatusLost {
			rec.EndedAt = 0
			rec.CleanupAfter = 0
		}
		rec.Status = status
		if canFailUndeliveredResult {
			rec.DeliveryStatus = DeliveryFailed
			rec.DeliveredAt = 0
		} else {
			rec.DeliveryStatus = DeliveryNotApplicable
			rec.DeliveredAt = time.Now().UnixMilli()
		}
		if canCancelUndeliveredResult {
			rec.LastCompletionID = ""
			rec.DeliveryError = ""
			rec.TerminalSummary = ""
			rec.Deliverable = nil
		}
		rec.InteractionShortID = ""
		rec.InteractionSummary = ""
		rec.ProgressSummary = ""
		rec.Error = truncateInteractionField(summary, 1000)
		return true, nil
	})
}

// CompleteInteractionTask terminalizes a task only after its suspended
// continuation has produced and delivered the final result.
func (r *Registry) CompleteInteractionTask(
	taskID, interactionID, content string,
	delivery DeliveryStatus,
) error {
	var deliverable *taskresult.Deliverable
	if strings.TrimSpace(content) != "" {
		deliverable = &taskresult.Deliverable{Text: content}
	}
	return r.CompleteInteractionTaskResult(taskID, interactionID, content, deliverable, delivery)
}

// CompleteInteractionTaskResult terminalizes a suspended task and preserves
// its canonical deliverable across restart and delivery recovery.
func (r *Registry) CompleteInteractionTaskResult(
	taskID, interactionID, summary string,
	deliverable *taskresult.Deliverable,
	delivery DeliveryStatus,
) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("interaction ID is required")
	}
	if delivery == "" {
		delivery = DeliveryNotApplicable
	}
	return r.updateInteractionProjection(taskID, func(rec *Record) (bool, error) {
		if rec.InteractionID != interactionID {
			return false, fmt.Errorf(
				"task %q interaction mismatch: have %q, got %q",
				taskID, rec.InteractionID, interactionID,
			)
		}
		if isTerminalStatus(rec.Status) && rec.Status != StatusLost {
			return false, nil
		}
		if rec.Status == StatusLost {
			rec.EndedAt = 0
			rec.CleanupAfter = 0
		}
		terminalSummary := truncateInteractionField(summary, 1000)
		var objectiveOutcome *taskresult.Outcome
		if deliverable != nil {
			objectiveOutcome = deliverable.ObjectiveOutcome
		}
		rec.Status = TerminalStatusForObjectiveOutcome(objectiveOutcome)
		rec.DeliveryStatus = delivery
		rec.InteractionShortID = ""
		rec.InteractionSummary = ""
		rec.ProgressSummary = ""
		rec.TerminalSummary = terminalSummary
		rec.Error = ""
		if rec.Status == StatusFailed {
			rec.Error = terminalSummary
		}
		rec.Deliverable = taskresult.CloneDeliverable(deliverable)
		if delivery == DeliveryDelivered || delivery == DeliveryNotApplicable {
			rec.DeliveredAt = time.Now().UnixMilli()
		}
		return true, nil
	})
}

// TerminalStatusForObjectiveOutcome keeps the durable task state aligned with
// the runtime-verified objective contract. A partial or blocked objective is a
// completed execution, but it is not a successfully completed task.
func TerminalStatusForObjectiveOutcome(outcome *taskresult.Outcome) Status {
	if outcome == nil || strings.TrimSpace(string(outcome.Status)) == "" ||
		outcome.Status == taskresult.OutcomeSucceeded {
		return StatusSucceeded
	}
	return StatusFailed
}

func (r *Registry) updateInteractionProjection(
	taskID string,
	mutate func(*Record) (bool, error),
) error {
	if r == nil || strings.TrimSpace(taskID) == "" || mutate == nil {
		return nil
	}
	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	rec, ok := r.records[taskID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("task %q not found", taskID)
	}
	before := rec
	changed, err := mutate(&rec)
	if err != nil || !changed {
		r.mu.Unlock()
		return err
	}
	rec.GenerationID = before.GenerationID
	rec.LastEventSeq = before.LastEventSeq
	now := time.Now().UnixMilli()
	rec.LastEventAt = now
	rec = r.normalizeRecord(rec, now)
	r.records[taskID] = rec
	r.appendUpdateEventsLocked(before, rec, now)
	newEvents := r.eventsSinceLocked(eventStart)
	r.pruneMutationLocked(now, newEvents)
	err = r.saveLocked()
	r.completeMutationLocked(err, rollbackState)
	r.mu.Unlock()
	return err
}
