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

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
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
	decoder.DisallowUnknownFields()
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
	if err := validateSnapshot(snap); err != nil {
		return err
	}
	records := make(map[string]Record, len(snap.Tasks))
	for _, rec := range snap.Tasks {
		records[rec.TaskID] = rec
	}
	r.records = records
	r.events = append([]TaskEvent(nil), snap.Events...)
	return nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Tasks == nil {
		return fmt.Errorf("task registry is missing tasks array")
	}
	records := make(map[string]Record, len(snapshot.Tasks))
	for _, rec := range snapshot.Tasks {
		if err := validateStoredRecord(rec); err != nil {
			return err
		}
		if _, duplicate := records[rec.TaskID]; duplicate {
			return fmt.Errorf("duplicate task %q", rec.TaskID)
		}
		records[rec.TaskID] = rec
	}
	eventIDs := make(map[string]struct{}, len(snapshot.Events))
	type generationKey struct {
		taskID       string
		generationID string
	}
	lastSequence := make(map[generationKey]int64)
	for _, event := range snapshot.Events {
		if err := validateStoredEvent(event); err != nil {
			return err
		}
		if _, duplicate := eventIDs[event.EventID]; duplicate {
			return fmt.Errorf("duplicate task event %q", event.EventID)
		}
		eventIDs[event.EventID] = struct{}{}
		key := generationKey{taskID: event.TaskID, generationID: event.GenerationID}
		if previous, ok := lastSequence[key]; ok && event.Seq != previous+1 {
			return fmt.Errorf("task event %q has invalid generation sequence", event.EventID)
		}
		lastSequence[key] = event.Seq
		rec, ok := records[event.TaskID]
		if !ok {
			return fmt.Errorf("task event %q references missing task %q", event.EventID, event.TaskID)
		}
		if rec.GenerationID == event.GenerationID && event.Seq > rec.LastEventSeq {
			return fmt.Errorf("task event %q has invalid generation sequence", event.EventID)
		}
	}
	for key, sequence := range lastSequence {
		rec, ok := records[key.taskID]
		if ok && rec.GenerationID == key.generationID && sequence != rec.LastEventSeq {
			return fmt.Errorf("task %q event tail does not match last_event_sequence", key.taskID)
		}
	}
	return nil
}

func validateStoredRecord(rec Record) error {
	if strings.TrimSpace(rec.TaskID) == "" {
		return fmt.Errorf("task is missing task_id")
	}
	if strings.TrimSpace(rec.GenerationID) == "" {
		return fmt.Errorf("task %q is missing generation_id", rec.TaskID)
	}
	if rec.LastEventSeq <= 0 {
		return fmt.Errorf("task %q has invalid last_event_sequence", rec.TaskID)
	}
	if !validRuntime(rec.Runtime) {
		return fmt.Errorf("task %q has invalid runtime %q", rec.TaskID, rec.Runtime)
	}
	if !validTaskStatus(rec.Status) {
		return fmt.Errorf("task %q has invalid status %q", rec.TaskID, rec.Status)
	}
	if !validDeliveryStatus(rec.DeliveryStatus) {
		return fmt.Errorf("task %q has invalid delivery_status %q", rec.TaskID, rec.DeliveryStatus)
	}
	if !validNotifyPolicy(rec.NotifyPolicy) {
		return fmt.Errorf("task %q has invalid notify_policy %q", rec.TaskID, rec.NotifyPolicy)
	}
	if isTerminalStatus(rec.Status) && rec.EndedAt <= 0 {
		return fmt.Errorf("terminal task %q is missing ended_at", rec.TaskID)
	}
	if isTerminalStatus(rec.Status) && rec.CleanupAfter <= 0 {
		return fmt.Errorf("terminal task %q is missing cleanup_after", rec.TaskID)
	}
	if rec.Deliverable == nil {
		return nil
	}
	if rec.Deliverable.Report == nil {
		if strings.TrimSpace(rec.Deliverable.Text) != "" || len(rec.Deliverable.Artifacts) > 0 ||
			len(rec.Deliverable.Metadata) > 0 {
			return fmt.Errorf("task %q deliverable is missing report", rec.TaskID)
		}
		return nil
	}
	report := rec.Deliverable.Report
	if report.SchemaVersion != taskresult.ReportSchemaV1 {
		return fmt.Errorf(
			"task %q deliverable report has schema %q, want %q",
			rec.TaskID,
			report.SchemaVersion,
			taskresult.ReportSchemaV1,
		)
	}
	if strings.TrimSpace(report.ReportID) == "" {
		return fmt.Errorf("task %q deliverable report is missing report_id", rec.TaskID)
	}
	if strings.TrimSpace(report.ContentHash) == "" {
		return fmt.Errorf("task %q deliverable report is missing content_hash", rec.TaskID)
	}
	if report.GeneratedAt <= 0 {
		return fmt.Errorf("task %q deliverable report is missing generated_at", rec.TaskID)
	}
	return nil
}

func validateStoredEvent(event TaskEvent) error {
	if strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("task event is missing event_id")
	}
	if strings.TrimSpace(event.TaskID) == "" {
		return fmt.Errorf("task event %q is missing task_id", event.EventID)
	}
	if strings.TrimSpace(event.GenerationID) == "" {
		return fmt.Errorf("task event %q is missing generation_id", event.EventID)
	}
	if event.SchemaVersion != TaskEventSchemaVersion {
		return fmt.Errorf(
			"task event %q has schema %q, want %q",
			event.EventID,
			event.SchemaVersion,
			TaskEventSchemaVersion,
		)
	}
	if !validEventType(event.Type) {
		return fmt.Errorf("task event %q has invalid type %q", event.EventID, event.Type)
	}
	if event.Seq <= 0 {
		return fmt.Errorf("task event %q has invalid generation sequence", event.EventID)
	}
	if event.EmittedAt <= 0 {
		return fmt.Errorf("task event %q is missing emitted_at", event.EventID)
	}
	if !validRuntime(event.Runtime) {
		return fmt.Errorf("task event %q has invalid runtime %q", event.EventID, event.Runtime)
	}
	if !validTaskStatus(event.Status) {
		return fmt.Errorf("task event %q has invalid status %q", event.EventID, event.Status)
	}
	if !validDeliveryStatus(event.DeliveryStatus) {
		return fmt.Errorf(
			"task event %q has invalid delivery_status %q",
			event.EventID,
			event.DeliveryStatus,
		)
	}
	return nil
}

func validRuntime(runtime Runtime) bool {
	switch runtime {
	case RuntimeSubagent, RuntimeDelegate, RuntimeTool, RuntimeCron:
		return true
	default:
		return false
	}
}

func validNotifyPolicy(policy NotifyPolicy) bool {
	switch policy {
	case NotifyDoneOnly, NotifyStateChanges, NotifySilent:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventTaskUpserted,
		EventTaskStatusChanged,
		EventTaskDeliveryChanged,
		EventTaskDeliveryDecision,
		EventTaskProgress,
		EventTaskUpdated,
		EventTaskReconciled:
		return true
	default:
		return false
	}
}

func (r *Registry) saveLocked() error {
	if err := r.writableErrorLocked(); err != nil {
		return err
	}
	if r.store == "" {
		return nil
	}
	snapshot := r.snapshotLocked()
	if err := validateSnapshot(snapshot); err != nil {
		return fmt.Errorf("validate task registry snapshot: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
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
