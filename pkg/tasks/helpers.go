package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
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
		rec.Deliverable = normalizeDeliverablePayload(rec.Deliverable, now)
	}
	rec.InteractionID = strings.TrimSpace(rec.InteractionID)
	rec.InteractionShortID = truncateInteractionField(rec.InteractionShortID, 64)
	rec.InteractionSummary = truncateInteractionField(rec.InteractionSummary, 500)
	return rec
}

func truncateInteractionField(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func normalizeDeliverablePayload(payload *DeliverablePayload, generatedAt int64) *DeliverablePayload {
	if payload == nil {
		return nil
	}
	out := *payload
	out.Artifacts = append([]DeliverableItem(nil), payload.Artifacts...)
	out.Metadata = copyStringMap(payload.Metadata)
	out.ObjectiveOutcome = cloneObjectiveOutcome(payload.ObjectiveOutcome)
	if payload.Report != nil {
		report := cloneDeliverableReport(payload.Report)
		if report.SchemaVersion == "" {
			report.SchemaVersion = DeliverableReportV1
		}
		if report.GeneratedAt == 0 {
			report.GeneratedAt = generatedAt
		}
		if report.ContentHash == "" {
			report.ContentHash = deliverableContentHash(&out)
		}
		if report.ReportID == "" {
			report.ReportID = "deliverable:" + report.ContentHash
		}
		out.Report = report
		return &out
	}
	if strings.TrimSpace(out.Text) == "" && len(out.Artifacts) == 0 && len(out.Metadata) == 0 {
		return &out
	}
	contentHash := deliverableContentHash(&out)
	report := &DeliverableReport{
		SchemaVersion: DeliverableReportV1,
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
		report.Claims = append(report.Claims, ReportClaim{
			Kind:       "fact",
			Text:       summary,
			Confidence: "producer_reported",
		})
	}
	out.Report = report
	return &out
}

func deliverableContentHash(payload *DeliverablePayload) string {
	if payload == nil {
		return ""
	}
	type hashPayload struct {
		Text             string            `json:"text,omitempty"`
		Artifacts        []DeliverableItem `json:"artifacts,omitempty"`
		Metadata         map[string]string `json:"metadata,omitempty"`
		ObjectiveOutcome *ObjectiveOutcome `json:"objective_outcome,omitempty"`
	}
	data, _ := json.Marshal(hashPayload{
		Text:             strings.TrimSpace(payload.Text),
		Artifacts:        append([]DeliverableItem(nil), payload.Artifacts...),
		Metadata:         copyStringMap(payload.Metadata),
		ObjectiveOutcome: cloneObjectiveOutcome(payload.ObjectiveOutcome),
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneDeliverableReport(report *DeliverableReport) *DeliverableReport {
	if report == nil {
		return nil
	}
	cloned := &DeliverableReport{
		SchemaVersion: report.SchemaVersion,
		ReportID:      report.ReportID,
		ContentHash:   report.ContentHash,
		GeneratedAt:   report.GeneratedAt,
		Summary:       report.Summary,
		Provenance:    copyStringMap(report.Provenance),
		Metadata:      copyStringMap(report.Metadata),
		Extra:         copyAnyMap(report.Extra),
	}
	for _, claim := range report.Claims {
		cloned.Claims = append(cloned.Claims, ReportClaim{
			Kind:       claim.Kind,
			Text:       claim.Text,
			Confidence: claim.Confidence,
			SourceRefs: append([]string(nil), claim.SourceRefs...),
			Metadata:   copyStringMap(claim.Metadata),
		})
	}
	cloned.FieldDeltas = append([]ReportFieldDelta(nil), report.FieldDeltas...)
	return cloned
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

func copyAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out, err := canonicalAnyMap(in)
	if err == nil {
		return out
	}
	// Public mutations reject invalid Extra values before accepting state.
	// Keep the outer map detached while the invalid candidate is validated.
	out = make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func canonicalizeRecordExtra(record *Record) error {
	if record == nil || record.Deliverable == nil || record.Deliverable.Report == nil {
		return nil
	}
	extra, err := canonicalAnyMap(record.Deliverable.Report.Extra)
	if err != nil {
		return err
	}
	record.Deliverable.Report.Extra = extra
	return nil
}

func canonicalAnyMap(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
