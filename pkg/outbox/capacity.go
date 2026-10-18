package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CapacityBudget struct {
	MaxBytes   int64
	MaxRecords int64
}

// CapacityPolicy supplies an explicit candidate age and optional logical-store
// budgets. A zero budget is unconfigured. It never authorizes identity removal.
type CapacityPolicy struct {
	MinAge     time.Duration
	MaxBytes   int64
	MaxRecords int64
}

type CapacityGroup struct {
	Count           int64     `json:"count"`
	Bytes           int64     `json:"bytes"`
	OldestUpdatedAt time.Time `json:"oldest_updated_at,omitzero"`
}

// CapacityReport counts the whole directory while retaining at most one batch
// of candidate keys. Byte values are logical file sizes, not filesystem blocks,
// snapshots or external backups. Groups partition observed data entries; active
// payloads remain protected even when their count exceeds an operator budget.
type CapacityReport struct {
	SchemaVersion          string           `json:"schema_version"`
	GeneratedAt            time.Time        `json:"generated_at"`
	ScanComplete           bool             `json:"scan_complete"`
	TotalEntries           int64            `json:"total_entries"`
	TotalRecords           int64            `json:"total_records"`
	MinimumAge             string           `json:"minimum_age"`
	TotalBytes             int64            `json:"total_bytes"`
	Active                 CapacityGroup    `json:"active_payloads"`
	Protected              CapacityGroup    `json:"protected_payloads"`
	Eligible               CapacityGroup    `json:"eligible_payloads"`
	Receipts               CapacityGroup    `json:"retained_receipts"`
	Unavailable            CapacityGroup    `json:"unavailable_entries"`
	Reasons                map[string]int64 `json:"reasons"`
	Outcomes               map[Status]int64 `json:"outcomes"`
	Candidates             []string         `json:"candidates"`
	CandidateTruncated     bool             `json:"candidate_truncated"`
	MaxBytes               int64            `json:"max_bytes"`
	MaxRecords             int64            `json:"max_records"`
	BudgetExceeded         bool             `json:"budget_exceeded"`
	OperatorActionRequired bool             `json:"operator_action_required"`
}

func (g *CapacityGroup) add(bytes int64, updated time.Time) {
	g.Count++
	g.Bytes += bytes
	if !updated.IsZero() && (g.OldestUpdatedAt.IsZero() || updated.Before(g.OldestUpdatedAt)) {
		g.OldestUpdatedAt = updated
	}
}

// InspectCapacityOffline takes the same cooperating-writer lock as retirement.
// It neither creates a missing store nor changes any payload/status/identity.
// Every observed entry is counted; decoding and returned samples stay bounded.
func InspectCapacityOffline(
	ctx context.Context,
	instanceRoot string,
	policy CapacityPolicy,
) (report CapacityReport, err error) {
	if policy.MinAge <= 0 || policy.MaxBytes < 0 || policy.MaxRecords < 0 {
		return report, errors.New("outbox capacity requires a positive age and nonnegative budgets")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	info, statErr := os.Stat(Path(instanceRoot))
	if statErr != nil || !info.IsDir() {
		return report, errors.New("outbox capacity requires an existing store directory")
	}
	c, err := OpenCoordinator(instanceRoot)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	return inspectCapacityOwned(ctx, c, policy)
}

func inspectCapacityOwned(
	ctx context.Context,
	c *Coordinator,
	policy CapacityPolicy,
) (report CapacityReport, err error) {
	dir, err := os.Open(c.store.dir)
	if err != nil {
		return report, errors.New("outbox capacity directory unavailable")
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	now := time.Now().UTC()
	report = CapacityReport{
		SchemaVersion: "mintclaw.outbox.capacity.v1",
		GeneratedAt:   now,
		MinimumAge:    policy.MinAge.String(),
		Reasons: make(
			map[string]int64,
		),
		Outcomes:   make(map[Status]int64),
		Candidates: make([]string, 0, MaxRetirementBatch),
		MaxBytes:   policy.MaxBytes,
		MaxRecords: policy.MaxRecords,
	}
	defer func() {
		report.BudgetExceeded = policy.MaxBytes > 0 && report.TotalBytes > policy.MaxBytes ||
			policy.MaxRecords > 0 && report.TotalRecords > policy.MaxRecords
		report.OperatorActionRequired = report.BudgetExceeded || report.Unavailable.Count > 0 || !report.ScanComplete
	}()
	before := now.Add(-policy.MinAge)
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			inspectCapacityEntry(c.store.dir, entry, &report, before, now)
		}
		if errors.Is(readErr, io.EOF) {
			report.ScanComplete = true
			return report, nil
		}
		if readErr != nil {
			return report, errors.New("outbox capacity directory scan incomplete")
		}
	}
}

// inspectCapacityEntry accounts one namespace entry without following links or
// retaining its payload. The caller owns the complete offline scan lifetime.
func inspectCapacityEntry(dir string, entry os.DirEntry, report *CapacityReport, before, now time.Time) {
	info, infoErr := entry.Info()
	if infoErr == nil && entry.Name() == ".writer.lock" && info.Mode().IsRegular() && info.Size() == 0 {
		return
	}
	report.TotalEntries++
	id := strings.TrimSuffix(entry.Name(), ".json")
	canonical := validateID(id) == nil && entry.Name() == id+".json"
	if canonical {
		report.TotalRecords++
	}
	if infoErr != nil {
		report.Unavailable.add(0, time.Time{})
		report.Reasons["entry_unavailable"]++
		return
	}
	report.TotalBytes += info.Size()
	if !canonical {
		report.Unavailable.add(info.Size(), time.Time{})
		report.Reasons["unexpected_entry"]++
		return
	}
	if !info.Mode().IsRegular() || info.Size() > MaxInspectionBytes {
		reason := "protected_nonregular_record"
		if info.Mode().IsRegular() {
			reason = "protected_large_record"
		}
		report.Unavailable.add(info.Size(), time.Time{})
		report.Reasons[reason]++
		return
	}
	data, recordErr := readCapacityRecord(filepath.Join(dir, entry.Name()))
	if recordErr != nil {
		report.Unavailable.add(info.Size(), time.Time{})
		report.Reasons["record_unavailable"]++
		return
	}
	intent, inspectErr := InspectIntent(data, id)
	if inspectErr != nil {
		report.Unavailable.add(info.Size(), time.Time{})
		report.Reasons["invalid_record"]++
		return
	}
	report.Outcomes[intent.Status]++
	if intent.PayloadRetired {
		report.Receipts.add(info.Size(), intent.UpdatedAt)
		return
	}
	reason := payloadRetirementBlocker(intent, before, now)
	if reason == "" {
		encoded, encodeErr := json.MarshalIndent(compactDeliveredIntent(intent, now), "", "  ")
		if encodeErr != nil || len(encoded)+1 > maxRetiredReceiptBytes {
			reason = "protected_large_receipt"
		}
	}
	if reason == "" {
		report.Eligible.add(info.Size(), intent.UpdatedAt)
		if len(report.Candidates) < MaxRetirementBatch {
			report.Candidates = append(report.Candidates, id)
		} else {
			report.CandidateTruncated = true
		}
		return
	}
	report.Reasons[reason]++
	if intent.Status == StatusPending || intent.Status == StatusAttempting ||
		intent.Status == StatusDefinitelyFailed && intent.Attempts < MaxDeliveryAttempts ||
		intent.TranscriptProjectionPending() || intent.RecoverySettlementPending() {
		report.Active.add(info.Size(), intent.UpdatedAt)
	} else {
		report.Protected.add(info.Size(), intent.UpdatedAt)
	}
}

func readCapacityRecord(path string) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return io.ReadAll(io.LimitReader(f, MaxInspectionBytes+1))
}
