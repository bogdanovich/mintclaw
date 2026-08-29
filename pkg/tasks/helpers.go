package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func (r *Registry) normalizeRecord(rec Record, now int64) Record {
	if r == nil {
		return rec
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	if isTerminalStatus(rec.Status) && rec.EndedAt == 0 {
		rec.EndedAt = rec.LastEventAt
		if rec.EndedAt == 0 {
			rec.EndedAt = now
		}
	}
	if isTerminalStatus(rec.Status) && rec.CleanupAfter == 0 {
		base := recordReferenceAt(rec)
		if base == 0 {
			base = now
		}
		rec.CleanupAfter = base + int64(r.options.TerminalRetention/time.Millisecond)
	}
	if rec.Deliverable != nil {
		rec.Deliverable = normalizeDeliverable(rec.Deliverable, now)
	}
	return rec
}

func normalizeDeliverable(payload *taskresult.Deliverable, generatedAt int64) *taskresult.Deliverable {
	if payload == nil {
		return nil
	}
	out := taskresult.CloneDeliverable(payload)
	if payload.Report != nil {
		report := taskresult.CloneReport(payload.Report)
		if report.SchemaVersion == "" {
			report.SchemaVersion = taskresult.ReportSchemaV1
		}
		if report.GeneratedAt == 0 {
			report.GeneratedAt = generatedAt
		}
		if strings.TrimSpace(report.ContentHash) == "" {
			report.ContentHash = deliverableContentHash(out)
		}
		if strings.TrimSpace(report.ReportID) == "" {
			report.ReportID = "deliverable:" + report.ContentHash
		}
		out.Report = report
		return out
	}
	if strings.TrimSpace(out.Text) == "" && len(out.Artifacts) == 0 && len(out.Metadata) == 0 {
		return out
	}
	contentHash := deliverableContentHash(out)
	report := &taskresult.Report{
		SchemaVersion: taskresult.ReportSchemaV1,
		ReportID:      "deliverable:" + contentHash,
		ContentHash:   contentHash,
		GeneratedAt:   generatedAt,
		Summary:       strings.TrimSpace(out.Text),
		Metadata:      copyStringMap(out.Metadata),
		Provenance: map[string]string{
			"source":     "task_registry_projection",
			"projection": "deliverable_payload",
		},
	}
	if summary := strings.TrimSpace(out.Text); summary != "" {
		report.Claims = append(report.Claims, taskresult.Claim{
			Kind:       "fact",
			Text:       summary,
			Confidence: "producer_reported",
		})
	}
	out.Report = report
	return out
}

func deliverableContentHash(payload *taskresult.Deliverable) string {
	if payload == nil {
		return ""
	}
	type hashPayload struct {
		Text             string                `json:"text,omitempty"`
		Artifacts        []taskresult.Artifact `json:"artifacts,omitempty"`
		Metadata         map[string]string     `json:"metadata,omitempty"`
		ObjectiveOutcome *taskresult.Outcome   `json:"objective_outcome,omitempty"`
	}
	data, _ := json.Marshal(hashPayload{
		Text:             strings.TrimSpace(payload.Text),
		Artifacts:        append([]taskresult.Artifact(nil), payload.Artifacts...),
		Metadata:         copyStringMap(payload.Metadata),
		ObjectiveOutcome: taskresult.CloneOutcome(payload.ObjectiveOutcome),
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func recordChanged(before, after Record) bool {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	return string(b) != string(a)
}

func cleanPayload(payload map[string]string) map[string]string {
	if len(payload) == 0 {
		return nil
	}
	out := make(map[string]string, len(payload))
	for key, value := range payload {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
