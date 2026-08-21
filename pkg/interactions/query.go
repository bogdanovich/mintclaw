package interactions

import (
	"strings"
	"time"
)

func (r *Registry) Get(id string) (Record, bool) {
	if r == nil {
		return Record{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[strings.TrimSpace(id)]
	return cloneRecord(rec), ok
}

func (r *Registry) List() []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, cloneRecord(rec))
	}
	sortRecords(out)
	return out
}

func (r *Registry) ListNonterminal() []Record {
	all := r.List()
	out := make([]Record, 0, len(all))
	for _, rec := range all {
		if !isTerminal(rec.Status) {
			out = append(out, rec)
		}
	}
	return out
}

// FindNonterminalByTaskID returns the current durable interaction owned by a task.
func (r *Registry) FindNonterminalByTaskID(taskID string) (Record, bool) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return Record{}, false
	}
	records := r.ListNonterminal()
	for index := len(records) - 1; index >= 0; index-- {
		if strings.TrimSpace(records[index].Origin.TaskID) == taskID {
			return records[index], true
		}
	}
	return Record{}, false
}

// NonterminalTaskIDs returns the task side of every current interaction relation.
func (r *Registry) NonterminalTaskIDs() map[string]struct{} {
	ids := make(map[string]struct{})
	for _, record := range r.ListNonterminal() {
		if taskID := strings.TrimSpace(record.Origin.TaskID); taskID != "" {
			ids[taskID] = struct{}{}
		}
	}
	return ids
}

func (r *Registry) FindWaitingBySession(sessionKey string) []Record {
	sessionKey = strings.TrimSpace(sessionKey)
	all := r.List()
	out := make([]Record, 0, 1)
	for _, rec := range all {
		if rec.Status == StatusWaiting && rec.Route.SessionKey == sessionKey {
			out = append(out, rec)
		}
	}
	return out
}

func (r *Registry) FindWaitingByRoute(route Route) []Record {
	route = normalizeRoute(route)
	all := r.List()
	out := make([]Record, 0, 1)
	for _, rec := range all {
		if rec.Status == StatusWaiting && routesEqual(rec.Route, route) {
			out = append(out, rec)
		}
	}
	return out
}

func (r *Registry) ListEvents(id string) []Event {
	if r == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Event, 0, len(r.events))
	for _, event := range r.events {
		if id == "" || event.InteractionID == id {
			out = append(out, cloneEvent(event))
		}
	}
	return out
}

func (r *Registry) Prune(now time.Time) error {
	if r == nil {
		return ErrStoreUnavailable
	}
	nowMillis := now.UnixMilli()
	if now.IsZero() {
		nowMillis = r.nowMillis()
	}
	r.mu.Lock()
	if err := r.availableLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	releaseStore, err := r.lockAndReloadLocked()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	before := r.snapshotLocked()
	if !r.pruneLocked(nowMillis) {
		releaseStore()
		r.mu.Unlock()
		return nil
	}
	if err := r.saveLocked(); err != nil {
		r.restoreSnapshotLocked(before)
		releaseStore()
		r.mu.Unlock()
		return err
	}
	releaseStore()
	r.mu.Unlock()
	return nil
}

func (r *Registry) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stats := Stats{
		RecordCount:      len(r.records),
		EventCount:       len(r.events),
		SnapshotBytes:    r.snapshotSizeLocked(),
		Retention:        r.options.TerminalRetention,
		MaxRecords:       r.options.MaxRecords,
		MaxEvents:        r.options.MaxEvents,
		MaxSnapshotBytes: r.options.MaxSnapshotBytes,
	}
	for _, rec := range r.records {
		if !isTerminal(rec.Status) {
			stats.NonterminalCount++
		}
	}
	stats.OverBudget = stats.SnapshotBytes > stats.MaxSnapshotBytes
	return stats
}
