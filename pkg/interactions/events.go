package interactions

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func (r *Registry) pruneLocked(now int64) bool {
	changed := false
	for id, rec := range r.records {
		if isTerminal(rec.Status) && rec.CleanupAfter > 0 && now >= rec.CleanupAfter {
			delete(r.records, id)
			changed = true
		}
	}
	if len(r.records) > r.options.MaxRecords {
		terminal := make([]Record, 0)
		for _, rec := range r.records {
			if isTerminal(rec.Status) {
				terminal = append(terminal, rec)
			}
		}
		slices.SortFunc(
			terminal,
			func(a, b Record) int { return cmp.Compare(a.ResolvedAt, b.ResolvedAt) },
		)
		for len(r.records) > r.options.MaxRecords && len(terminal) > 0 {
			delete(r.records, terminal[0].ID)
			terminal = terminal[1:]
			changed = true
		}
	}
	if r.trimEventsLocked() {
		changed = true
	}
	return changed
}

func (r *Registry) trimEventsLocked() bool {
	changed := false
	kept := r.events[:0]
	for _, event := range r.events {
		if _, exists := r.records[event.InteractionID]; exists {
			kept = append(kept, event)
		} else {
			changed = true
		}
	}
	r.events = kept
	if len(r.events) > r.options.MaxEvents {
		r.events = append([]Event(nil), r.events[len(r.events)-r.options.MaxEvents:]...)
		changed = true
	}
	if r.trimEventsToSnapshotBudgetLocked() {
		changed = true
	}
	return changed
}

// trimEventsToSnapshotBudgetLocked preserves authoritative records and the
// newest committed event while compacting the oldest diagnostic event prefix.
// Keeping a 25 percent reserve prevents every subsequent transition from
// immediately forcing another compaction.
func (r *Registry) trimEventsToSnapshotBudgetLocked() bool {
	if len(r.events) < 2 || r.options.MaxSnapshotBytes <= 0 {
		return false
	}
	target := r.options.MaxSnapshotBytes * 3 / 4
	if r.snapshotSizeLocked() <= target {
		return false
	}
	original := r.events
	findDrop := func(limit int) (int, bool) {
		low, high, best := 1, len(original)-1, -1
		for low <= high {
			middle := low + (high-low)/2
			r.events = original[middle:]
			if r.snapshotSizeLocked() <= limit {
				best = middle
				high = middle - 1
			} else {
				low = middle + 1
			}
		}
		return best, best >= 0
	}
	drop, ok := findDrop(target)
	if !ok {
		drop, ok = findDrop(r.options.MaxSnapshotBytes)
	}
	if !ok {
		r.events = original
		return false
	}
	r.events = append([]Event(nil), original[drop:]...)
	return true
}

func (r *Registry) appendEventLocked(rec *Record, eventType EventType, code string, success *bool) {
	r.appendEventFromLocked(rec, eventType, "", code, success)
}

func (r *Registry) appendEventFromLocked(
	rec *Record,
	eventType EventType,
	from Status,
	code string,
	success *bool,
) {
	rec.LastEventSeq++
	sequence := rec.LastEventSeq
	r.commitSequence++
	r.events = append(r.events, Event{
		SchemaVersion:  EventSchemaVersion,
		EventID:        fmt.Sprintf("%s:%06d:%s", rec.ID, sequence, eventType),
		CommitSequence: r.commitSequence,
		InteractionID:  rec.ID,
		Type:           eventType,
		From:           from,
		To:             rec.Status,
		Outcome:        rec.Outcome,
		Revision:       rec.Revision,
		Sequence:       sequence,
		EmittedAt:      rec.UpdatedAt,
		Code:           strings.TrimSpace(code),
		Success:        success,
	})
}

func (r *Registry) queueNotificationsLocked(events []Event) bool {
	if len(events) == 0 {
		return false
	}
	observers := make([]*observerDelivery, 0, len(r.observers))
	for _, entry := range r.observers {
		observers = append(observers, entry.delivery)
	}
	for _, event := range events {
		r.notifications = append(r.notifications, queuedObservation{
			observation: EventObservation{
				Event:  cloneEvent(event),
				Record: cloneRecord(r.records[event.InteractionID]),
			},
			observers: observers,
		})
	}
	if r.notifying {
		return false
	}
	r.notifying = true
	return true
}

func (r *Registry) drainNotifications() {
	for {
		r.mu.Lock()
		if len(r.notifications) == 0 {
			r.notifying = false
			r.mu.Unlock()
			return
		}
		queued := r.notifications[0]
		r.notifications[0] = queuedObservation{}
		r.notifications = r.notifications[1:]
		r.mu.Unlock()
		for _, delivery := range queued.observers {
			delivery.enqueue(EventObservation{
				Event:  cloneEvent(queued.observation.Event),
				Record: cloneRecord(queued.observation.Record),
			})
		}
	}
}

func notifyObserver(observer Observer, observation EventObservation) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.WarnCF(
				"interactions",
				"Recovered interaction event observer panic",
				map[string]any{
					"event_id": observation.Event.EventID,
				},
			)
		}
	}()
	observer(observation)
}
