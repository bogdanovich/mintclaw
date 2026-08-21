package tasks

import (
	"strings"
	"time"
)

func (r *Registry) MarkStaleActiveLost(
	maxAge time.Duration,
	reason string,
	protectedTaskIDs map[string]struct{},
) (int, error) {
	if r == nil || maxAge <= 0 {
		return 0, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "active task did not report progress before stale timeout"
	}
	now := time.Now().UnixMilli()
	staleBefore := now - int64(maxAge/time.Millisecond)
	changed := 0

	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return 0, err
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	for _, id := range r.sortedRecordIDsLocked() {
		rec := r.records[id]
		if rec.Status != StatusQueued && rec.Status != StatusRunning {
			continue
		}
		if _, protected := protectedTaskIDs[rec.TaskID]; protected {
			continue
		}
		before := rec
		ref := rec.LastEventAt
		if ref == 0 {
			ref = rec.StartedAt
		}
		if ref == 0 {
			ref = rec.CreatedAt
		}
		if ref > 0 && ref > staleBefore {
			continue
		}
		rec.Status = StatusLost
		if !isFinalDeliveryStatus(rec.DeliveryStatus) {
			rec.DeliveryStatus = DeliveryNotApplicable
		}
		rec.LastEventAt = now
		rec.EndedAt = now
		if strings.TrimSpace(rec.Error) == "" {
			rec.Error = reason
		}
		rec = r.normalizeRecord(rec, now)
		r.records[id] = rec
		r.appendUpdateEventsLocked(before, rec, now)
		r.appendEventLocked(rec, EventTaskReconciled, now, map[string]string{"reason": reason})
		changed++
	}
	err := error(nil)
	newEvents := r.eventsSinceLocked(eventStart)
	if changed > 0 {
		r.pruneMutationLocked(now, newEvents)
		err = r.saveLocked()
		if !r.completeMutationLocked(err, rollbackState) {
			changed = 0
		}
	}
	r.mu.Unlock()
	return changed, err
}

func (r *Registry) MarkActiveLost(reason string, protectedTaskIDs map[string]struct{}) (int, error) {
	if r == nil {
		return 0, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "active task owner is no longer alive"
	}
	now := time.Now().UnixMilli()
	changed := 0

	r.mu.Lock()
	if err := r.writableErrorLocked(); err != nil {
		r.mu.Unlock()
		return 0, err
	}
	rollbackState := r.captureStateLocked()
	eventStart := len(r.events)
	for _, id := range r.sortedRecordIDsLocked() {
		rec := r.records[id]
		if rec.Status != StatusQueued && rec.Status != StatusRunning {
			continue
		}
		if _, protected := protectedTaskIDs[rec.TaskID]; protected {
			continue
		}
		before := rec
		rec.Status = StatusLost
		if !isFinalDeliveryStatus(rec.DeliveryStatus) {
			rec.DeliveryStatus = DeliveryNotApplicable
		}
		rec.LastEventAt = now
		rec.EndedAt = now
		if strings.TrimSpace(rec.Error) == "" {
			rec.Error = reason
		}
		rec = r.normalizeRecord(rec, now)
		r.records[id] = rec
		r.appendUpdateEventsLocked(before, rec, now)
		r.appendEventLocked(rec, EventTaskReconciled, now, map[string]string{"reason": reason})
		changed++
	}
	err := error(nil)
	newEvents := r.eventsSinceLocked(eventStart)
	if changed > 0 {
		r.pruneMutationLocked(now, newEvents)
		err = r.saveLocked()
		if !r.completeMutationLocked(err, rollbackState) {
			changed = 0
		}
	}
	r.mu.Unlock()
	return changed, err
}

func (r *Registry) ListPendingTerminalDelivery() []Record {
	if r == nil {
		return nil
	}
	records := r.List()
	out := make([]Record, 0)
	for _, rec := range records {
		if rec.DeliveryStatus == DeliveryPending && isTerminalStatus(rec.Status) {
			out = append(out, rec)
		}
	}
	return out
}
