package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (r *Registry) sortedRecordIDsLocked() []string {
	ids := make([]string, 0, len(r.records))
	for id := range r.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Registry) appendUpdateEventsLocked(before, after Record, emittedAt int64) {
	if before.Status != after.Status {
		r.appendEventLocked(after, EventTaskStatusChanged, emittedAt, map[string]string{
			"from": string(before.Status),
			"to":   string(after.Status),
		})
	}
	if before.DeliveryStatus != after.DeliveryStatus {
		payload := map[string]string{
			"from": string(before.DeliveryStatus),
			"to":   string(after.DeliveryStatus),
		}
		if completionID := strings.TrimSpace(after.LastCompletionID); completionID != "" {
			payload["completion_id"] = completionID
		}
		r.appendEventLocked(after, EventTaskDeliveryChanged, emittedAt, payload)
	}
	if before.ProgressSummary != after.ProgressSummary && strings.TrimSpace(after.ProgressSummary) != "" {
		r.appendEventLocked(after, EventTaskProgress, emittedAt, map[string]string{
			"summary": after.ProgressSummary,
		})
	}
	if before.Status == after.Status &&
		before.DeliveryStatus == after.DeliveryStatus &&
		before.ProgressSummary == after.ProgressSummary &&
		recordChanged(before, after) {
		r.appendEventLocked(after, EventTaskUpdated, emittedAt, nil)
	}
}

func (r *Registry) appendEventLocked(rec Record, eventType EventType, emittedAt int64, payload map[string]string) {
	if r == nil || strings.TrimSpace(rec.TaskID) == "" || eventType == "" {
		return
	}
	if emittedAt == 0 {
		emittedAt = time.Now().UnixMilli()
	}
	stored, ok := r.records[rec.TaskID]
	if !ok || stored.GenerationID != rec.GenerationID {
		return
	}
	stored.LastEventSeq++
	r.records[rec.TaskID] = stored
	seq := stored.LastEventSeq
	evt := TaskEvent{
		SchemaVersion:  TaskEventSchemaVersion,
		TaskID:         rec.TaskID,
		GenerationID:   rec.GenerationID,
		Runtime:        rec.Runtime,
		ParentTaskID:   rec.ParentTaskID,
		Type:           eventType,
		Status:         rec.Status,
		DeliveryStatus: rec.DeliveryStatus,
		Seq:            seq,
		EmittedAt:      emittedAt,
		Source:         "task_registry",
		Producer:       firstNonEmpty(rec.AgentID, string(rec.Runtime)),
		Payload:        cleanPayload(payload),
	}
	evt.EventID = fmt.Sprintf("%s:%s:%06d:%s", rec.TaskID, rec.GenerationID, seq, eventType)
	evt.Fingerprint = taskEventFingerprint(evt)
	r.events = append(r.events, evt)
}

func taskEventFingerprint(evt TaskEvent) string {
	type immutableEvent struct {
		SchemaVersion  string            `json:"schema_version"`
		EventID        string            `json:"event_id"`
		TaskID         string            `json:"task_id"`
		GenerationID   string            `json:"generation_id"`
		Runtime        Runtime           `json:"runtime"`
		ParentTaskID   string            `json:"parent_task_id"`
		Type           EventType         `json:"type"`
		Status         Status            `json:"status"`
		DeliveryStatus DeliveryStatus    `json:"delivery_status"`
		Seq            int64             `json:"seq"`
		EmittedAt      int64             `json:"emitted_at"`
		Source         string            `json:"source"`
		Producer       string            `json:"producer"`
		Payload        map[string]string `json:"payload"`
	}
	payload, _ := json.Marshal(immutableEvent{
		SchemaVersion: evt.SchemaVersion, EventID: evt.EventID,
		TaskID: evt.TaskID, GenerationID: evt.GenerationID,
		Runtime: evt.Runtime, ParentTaskID: evt.ParentTaskID,
		Type: evt.Type, Status: evt.Status,
		DeliveryStatus: evt.DeliveryStatus, Seq: evt.Seq,
		EmittedAt: evt.EmittedAt, Source: evt.Source,
		Producer: evt.Producer, Payload: evt.Payload,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
