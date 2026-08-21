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

// Fail records a terminal failure chosen by the task's owning runtime.
func (r *Registry) Fail(
	taskID string,
	status Status,
	summary string,
) error {
	switch status {
	case StatusFailed, StatusTimedOut, StatusCancelled:
	default:
		return fmt.Errorf("invalid terminal task status %q", status)
	}
	return r.updateTask(taskID, func(rec *Record) (bool, error) {
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
		rec.ProgressSummary = ""
		rec.Error = truncateTaskSummary(summary)
		return true, nil
	})
}

// Complete records the canonical result produced by the task's owning runtime.
func (r *Registry) Complete(
	taskID, summary string,
	deliverable *taskresult.Deliverable,
	delivery DeliveryStatus,
) error {
	if delivery == "" {
		delivery = DeliveryNotApplicable
	}
	return r.updateTask(taskID, func(rec *Record) (bool, error) {
		if isTerminalStatus(rec.Status) && rec.Status != StatusLost {
			return false, nil
		}
		if rec.Status == StatusLost {
			rec.EndedAt = 0
			rec.CleanupAfter = 0
		}
		terminalSummary := truncateTaskSummary(summary)
		var objectiveOutcome *taskresult.Outcome
		if deliverable != nil {
			objectiveOutcome = deliverable.ObjectiveOutcome
		}
		rec.Status = TerminalStatusForObjectiveOutcome(objectiveOutcome)
		rec.DeliveryStatus = delivery
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

func (r *Registry) updateTask(
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

func truncateTaskSummary(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 1000 {
		return string(runes[:1000])
	}
	return value
}
