package tasks

import (
	"cmp"
	"slices"
	"strings"
)

func (r *Registry) Get(taskID string) (Record, bool) {
	if r == nil {
		return Record{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[taskID]
	return cloneTaskRecord(rec), ok
}

func (r *Registry) List() []Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, cloneTaskRecord(rec))
	}
	slices.SortFunc(out, func(a, b Record) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	return out
}

func (r *Registry) ListEvents(taskID string) []TaskEvent {
	if r == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TaskEvent, 0, len(r.events))
	for _, evt := range r.events {
		if taskID == "" || evt.TaskID == taskID {
			out = append(out, cloneTaskEvent(evt))
		}
	}
	slices.SortFunc(out, func(a, b TaskEvent) int {
		if c := cmp.Compare(a.EmittedAt, b.EmittedAt); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Seq, b.Seq); c != 0 {
			return c
		}
		return cmp.Compare(a.EventID, b.EventID)
	})
	return out
}

func (r *Registry) ListActive() []Record {
	records := r.List()
	out := make([]Record, 0)
	for _, rec := range records {
		if rec.Status == StatusQueued || rec.Status == StatusRunning ||
			rec.Status == StatusWaitingForInput {
			out = append(out, rec)
		}
	}
	return out
}
