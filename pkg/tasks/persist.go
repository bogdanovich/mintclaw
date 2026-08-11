package tasks

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func (r *Registry) load() error {
	data, err := os.ReadFile(r.store)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&snap); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("task registry contains a trailing JSON value")
		}
		return fmt.Errorf("task registry contains trailing data: %w", err)
	}
	now := time.Now().UnixMilli()
	records := make(map[string]Record, len(snap.Tasks))
	events := make([]TaskEvent, 0, len(snap.Events))
	for _, rec := range snap.Tasks {
		if strings.TrimSpace(rec.TaskID) == "" {
			continue
		}
		if strings.TrimSpace(rec.GenerationID) == "" {
			return fmt.Errorf("task %q is missing generation_id", rec.TaskID)
		}
		if rec.LastEventSeq <= 0 {
			return fmt.Errorf("task %q has invalid last_event_sequence", rec.TaskID)
		}
		records[rec.TaskID] = r.normalizeRecord(rec, now)
	}
	for _, evt := range snap.Events {
		if strings.TrimSpace(evt.TaskID) == "" || evt.Type == "" {
			continue
		}
		if strings.TrimSpace(evt.GenerationID) == "" {
			return fmt.Errorf("task event %q is missing generation_id", evt.EventID)
		}
		if evt.SchemaVersion != TaskEventSchemaVersion {
			return fmt.Errorf(
				"task event %q has schema %q, want %q",
				evt.EventID, evt.SchemaVersion, TaskEventSchemaVersion,
			)
		}
		if rec, ok := records[evt.TaskID]; ok && rec.GenerationID == evt.GenerationID &&
			(evt.Seq <= 0 || evt.Seq > rec.LastEventSeq) {
			return fmt.Errorf("task event %q has invalid generation sequence", evt.EventID)
		}
		events = append(events, evt)
	}
	r.records = records
	r.events = events
	return nil
}

func (r *Registry) saveLocked() error {
	if err := r.writableErrorLocked(); err != nil {
		return err
	}
	if r.store == "" {
		return nil
	}
	data, err := json.MarshalIndent(r.snapshotLocked(), "", "  ")
	if err != nil {
		return err
	}
	writeAtomic := r.writeAtomic
	if writeAtomic == nil {
		writeAtomic = fileutil.WriteFileAtomic
	}
	err = writeAtomic(r.store, data, 0o600)
	if err == nil {
		r.unsyncedWrite = false
	} else if fileutil.IsCommittedWriteError(err) {
		r.unsyncedWrite = true
	}
	return err
}

func (r *Registry) writableErrorLocked() error {
	if r.lastLoad == nil {
		return nil
	}
	return fmt.Errorf("task registry is read-only after load failure: %w", r.lastLoad)
}

func (r *Registry) snapshotSizeLocked() int {
	data, err := json.MarshalIndent(r.snapshotLocked(), "", "  ")
	if err != nil {
		return 0
	}
	return len(data)
}

func (r *Registry) snapshotLocked() Snapshot {
	tasks := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		tasks = append(tasks, rec)
	}
	slices.SortFunc(tasks, func(a, b Record) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	events := append([]TaskEvent(nil), r.events...)
	return Snapshot{Tasks: tasks, Events: events}
}

func (r *Registry) captureStateLocked() registryState {
	records := make(map[string]Record, len(r.records))
	for id, record := range r.records {
		records[id] = cloneTaskRecord(record)
	}
	events := make([]TaskEvent, len(r.events))
	for i := range r.events {
		events[i] = cloneTaskEvent(r.events[i])
	}
	return registryState{records: records, events: events}
}

func (r *Registry) restoreStateLocked(state registryState) {
	r.records = state.records
	r.events = state.events
}

func (r *Registry) completeMutationLocked(
	saveErr error,
	rollback registryState,
) bool {
	committed := saveErr == nil || fileutil.IsCommittedWriteError(saveErr)
	if !committed {
		r.restoreStateLocked(rollback)
	}
	return committed
}
