package tasks

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func (r *Registry) pruneLocked(now int64) bool {
	if r == nil {
		return false
	}
	changed := false
	for id, rec := range r.records {
		if shouldPruneExpired(rec, now) {
			delete(r.records, id)
			changed = true
		}
	}
	if r.options.MaxRecords > 0 && len(r.records) > r.options.MaxRecords {
		terminal := make([]Record, 0, len(r.records))
		for _, rec := range r.records {
			if canPruneRecord(rec) {
				terminal = append(terminal, rec)
			}
		}
		slices.SortFunc(terminal, func(a, b Record) int {
			return cmp.Compare(recordReferenceAt(a), recordReferenceAt(b))
		})
		for len(r.records) > r.options.MaxRecords && len(terminal) > 0 {
			victim := terminal[0]
			terminal = terminal[1:]
			delete(r.records, victim.TaskID)
			changed = true
		}
	}
	if r.pruneEventsLocked() {
		changed = true
	}
	if r.pruneSnapshotBytesLocked() {
		changed = true
	}
	return changed
}

func (r *Registry) pruneLoadedState(now int64) {
	rollback := r.captureStateLocked()
	if !r.pruneLocked(now) {
		return
	}
	if err := r.saveLocked(); err != nil && !fileutil.IsCommittedWriteError(err) {
		r.restoreStateLocked(rollback)
		r.lastLoad = fmt.Errorf("persist pruned task registry: %w", err)
	}
}

func (r *Registry) pruneMutationLocked(now int64, candidates []TaskEvent) {
	r.pruneLocked(now)
	if len(candidates) == 0 {
		return
	}
	type streamKey struct {
		taskID       string
		generationID string
	}
	retainedStreams := make(map[streamKey]struct{}, len(candidates))
	candidateIDs := make(map[string]struct{}, len(candidates))
	for _, event := range candidates {
		candidateIDs[event.EventID] = struct{}{}
	}
	retainedCandidates := make(map[string]struct{}, len(candidates))
	nonCandidates := make([]TaskEvent, 0, len(r.events))
	for _, event := range r.events {
		if _, candidate := candidateIDs[event.EventID]; candidate {
			retainedCandidates[event.EventID] = struct{}{}
			retainedStreams[streamKey{event.TaskID, event.GenerationID}] = struct{}{}
		} else {
			nonCandidates = append(nonCandidates, event)
		}
	}
	floorByStream := make(map[streamKey]TaskEvent)
	for _, event := range candidates {
		key := streamKey{event.TaskID, event.GenerationID}
		if _, retained := retainedStreams[key]; retained {
			continue
		}
		record, exists := r.records[event.TaskID]
		if exists && record.GenerationID == event.GenerationID {
			floorByStream[key] = event
		}
	}
	mutationEvents := make([]TaskEvent, 0, len(candidates))
	for _, event := range candidates {
		key := streamKey{event.TaskID, event.GenerationID}
		floor, isFloor := floorByStream[key]
		_, retained := retainedCandidates[event.EventID]
		if retained || isFloor && floor.EventID == event.EventID {
			mutationEvents = append(mutationEvents, event)
		}
	}
	r.events = append(nonCandidates, mutationEvents...)
}

func (r *Registry) pruneSnapshotBytesLocked() bool {
	if r == nil || r.options.MaxSnapshotBytes <= 0 || r.snapshotSizeLocked() <= r.options.MaxSnapshotBytes {
		return false
	}
	changed := r.trimEventsForSnapshotBudgetLocked()

	if r.snapshotSizeLocked() <= r.options.MaxSnapshotBytes {
		return changed
	}
	candidates := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		if canPruneRecord(rec) {
			candidates = append(candidates, rec)
		}
	}
	slices.SortFunc(candidates, func(a, b Record) int {
		return cmp.Compare(recordReferenceAt(a), recordReferenceAt(b))
	})
	for _, rec := range candidates {
		if r.snapshotSizeLocked() <= r.options.MaxSnapshotBytes {
			break
		}
		delete(r.records, rec.TaskID)
		r.pruneEventsLocked()
		changed = true
	}
	return changed
}

func (r *Registry) trimEventsForSnapshotBudgetLocked() bool {
	if r == nil || len(r.events) == 0 || r.snapshotSizeLocked() <= r.options.MaxSnapshotBytes {
		return false
	}
	original := r.events
	low, high := 0, len(original)
	for low < high {
		mid := low + (high-low)/2
		r.events = original[mid:]
		if r.snapshotSizeLocked() <= r.options.MaxSnapshotBytes {
			high = mid
		} else {
			low = mid + 1
		}
	}
	r.events = original[low:]
	return low > 0
}

func (r *Registry) pruneEventsLocked() bool {
	if r == nil || len(r.events) == 0 {
		return false
	}
	changed := false
	kept := r.events[:0]
	for _, evt := range r.events {
		if _, ok := r.records[evt.TaskID]; ok {
			kept = append(kept, evt)
		} else {
			changed = true
		}
	}
	r.events = kept
	if r.options.MaxEvents > 0 && len(r.events) > r.options.MaxEvents {
		r.events = append([]TaskEvent(nil), r.events[len(r.events)-r.options.MaxEvents:]...)
		changed = true
	}
	return changed
}

func shouldPruneExpired(rec Record, now int64) bool {
	return canPruneRecord(rec) && rec.CleanupAfter > 0 && now >= rec.CleanupAfter
}

func canPruneRecord(rec Record) bool {
	return taskRecordIsRetentionTerminal(rec)
}

func taskRecordIsRetentionTerminal(rec Record) bool {
	return isTerminalStatus(rec.Status) &&
		isFinalDeliveryStatus(rec.DeliveryStatus)
}

func isTerminalStatus(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusLost:
		return true
	default:
		return false
	}
}

func validTaskStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusLost:
		return true
	default:
		return false
	}
}

func isFinalDeliveryStatus(status DeliveryStatus) bool {
	switch status {
	case DeliveryDelivered, DeliverySessionQueued, DeliveryFailed, DeliveryParentMissing, DeliveryNotApplicable:
		return true
	default:
		return false
	}
}

func recordReferenceAt(rec Record) int64 {
	for _, value := range []int64{rec.EndedAt, rec.LastEventAt, rec.StartedAt, rec.CreatedAt} {
		if value > 0 {
			return value
		}
	}
	return 0
}
