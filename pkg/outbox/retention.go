package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	MaxRetirementBatch     = 64
	maxRetiredReceiptBytes = 16 * 1024
)

// RetirementPolicy selects a bounded offline batch. Apply defaults to false.
// MinAge is explicit; delivery identities never expire with their payloads.
type RetirementPolicy struct {
	DeliveryIDs    []string
	MinAge         time.Duration
	Apply          bool
	CapacityBudget *CapacityBudget
}

type RetirementItem struct {
	DeliveryID  string `json:"delivery_id"`
	Outcome     string `json:"outcome"`
	BeforeBytes int64  `json:"before_bytes"`
	AfterBytes  int64  `json:"after_bytes"`
}

// RetirementReport describes the selected batch and optional whole-store
// capacity snapshots taken under the same writer ownership.
// Items contain opaque delivery keys and fixed outcome codes, never payloads.
type RetirementReport struct {
	SchemaVersion  string           `json:"schema_version"`
	CapacityBefore *CapacityReport  `json:"capacity_before,omitempty"`
	CapacityAfter  *CapacityReport  `json:"capacity_after,omitempty"`
	DryRun         bool             `json:"dry_run"`
	Items          []RetirementItem `json:"items"`
}

// RetirePayloadsOffline opens a fresh exclusive coordinator for one bounded
// batch. It cannot run beside a cooperating runtime. Operators must establish
// the supported reader/writer floor before activating a migrated store; older
// binaries do not participate in this process-lock contract.
func RetirePayloadsOffline(
	ctx context.Context,
	instanceRoot string,
	policy RetirementPolicy,
) (report RetirementReport, err error) {
	report.SchemaVersion = "mintclaw.outbox.retirement.v1"
	report.DryRun = !policy.Apply
	if policy.CapacityBudget != nil && (policy.CapacityBudget.MaxBytes < 0 || policy.CapacityBudget.MaxRecords < 0) {
		return report, errors.New("outbox retirement requires nonnegative capacity budgets")
	}
	if policy.MinAge <= 0 || len(policy.DeliveryIDs) == 0 || len(policy.DeliveryIDs) > MaxRetirementBatch {
		return report, errors.New("outbox retention requires a positive age and 1..64 delivery IDs")
	}
	seen := make(map[string]bool, len(policy.DeliveryIDs))
	for _, id := range policy.DeliveryIDs {
		if validateID(id) != nil || seen[id] {
			return report, errors.New("invalid or duplicate outbox retirement identity")
		}
		seen[id] = true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	instanceRoot, err = canonicalInstanceRoot(instanceRoot)
	if err != nil {
		return report, err
	}
	// A typo or a dry run must not create a new outbox and imply it was read.
	if info, statErr := os.Stat(Path(instanceRoot)); statErr != nil || !info.IsDir() {
		return report, errors.New("outbox retirement requires an existing store directory")
	}
	coordinator, err := OpenCoordinator(instanceRoot)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, coordinator.Close()) }()
	report.Items = make([]RetirementItem, 0, len(policy.DeliveryIDs))
	var capacityPolicy CapacityPolicy
	if policy.CapacityBudget != nil {
		capacityPolicy = CapacityPolicy{
			MinAge:     policy.MinAge,
			MaxBytes:   policy.CapacityBudget.MaxBytes,
			MaxRecords: policy.CapacityBudget.MaxRecords,
		}
		beforeReport, capacityErr := inspectCapacityOwned(ctx, coordinator, capacityPolicy)
		report.CapacityBefore = &beforeReport
		if capacityErr != nil {
			return report, capacityErr
		}
		// Unknown, corrupt, unreadable and nonregular authorities are an
		// operator preflight failure; no selected payload is changed.
		if !beforeReport.ScanComplete || beforeReport.Unavailable.Count > 0 {
			return report, errors.New("outbox capacity preflight contains unavailable records")
		}
	}
	now := time.Now().UTC()
	before := now.Add(-policy.MinAge)
	for _, id := range policy.DeliveryIDs {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		item := RetirementItem{DeliveryID: id}
		info, statErr := os.Lstat(coordinator.store.recordPath(id))
		if statErr != nil {
			item.Outcome = "record_unavailable"
			report.Items = append(report.Items, item)
			return report, fmt.Errorf("inspect outbox retirement identity %s: unavailable", id)
		}
		item.BeforeBytes, item.AfterBytes = info.Size(), info.Size()
		if !info.Mode().IsRegular() {
			item.Outcome = "protected_nonregular_record"
			report.Items = append(report.Items, item)
			continue
		}
		if info.Size() > MaxInspectionBytes {
			item.Outcome = "protected_large_record"
			report.Items = append(report.Items, item)
			continue
		}
		intent, readErr := coordinator.store.Get(id)
		if readErr != nil {
			item.Outcome = "invalid_or_unreadable"
			report.Items = append(report.Items, item)
			return report, fmt.Errorf("inspect outbox retirement identity %s: invalid_or_unreadable", id)
		}
		if intent.PayloadRetired {
			item.Outcome = "already_retired"
		} else if reason := payloadRetirementBlocker(intent, before, now); reason != "" {
			item.Outcome = reason
			report.Items = append(report.Items, item)
			continue
		} else {
			item.Outcome = "eligible"
		}
		compact := compactDeliveredIntent(intent, now)
		data, encodeErr := json.MarshalIndent(compact, "", "  ")
		if encodeErr != nil {
			return report, encodeErr
		}
		if len(data)+1 > maxRetiredReceiptBytes {
			item.Outcome = "protected_large_receipt"
			report.Items = append(report.Items, item)
			continue
		}
		item.AfterBytes = int64(len(data) + 1)
		if policy.Apply {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			if _, retireErr := coordinator.store.retirePlainPayload(id, before, now); retireErr != nil {
				item.Outcome = "write_unconfirmed"
				report.Items = append(report.Items, item)
				return report, fmt.Errorf("outbox retirement identity %s: write_unconfirmed", id)
			}
			if !intent.PayloadRetired {
				item.Outcome = "retired"
			}
		}
		report.Items = append(report.Items, item)
	}
	if policy.CapacityBudget != nil && policy.Apply {
		afterReport, capacityErr := inspectCapacityOwned(ctx, coordinator, capacityPolicy)
		report.CapacityAfter = &afterReport
		if capacityErr != nil {
			return report, capacityErr
		}
	}
	return report, nil
}

func payloadRetirementBlocker(intent Intent, before, now time.Time) string {
	if intent.RetentionClass == retentionSourcePending {
		return "source_completion_pending"
	}
	if intent.RetentionClass != retentionPlainMessage {
		return "protected_class"
	}
	if intent.Status != StatusDelivered || intent.Attempts < 1 {
		return "protected_outcome"
	}
	if intent.Message == nil || !PlainMessageRetirementAllowed(*intent.Message) {
		return "protected_domain"
	}
	if intent.TranscriptProjectionPending() || intent.RecoverySettlementPending() {
		return "pending_settlement"
	}
	if !intent.UpdatedAt.Before(before) || now.Before(intent.UpdatedAt) {
		return "retention_age"
	}
	return ""
}

func compactDeliveredIntent(intent Intent, now time.Time) Intent {
	if intent.PayloadRetired {
		return intent
	}
	intent.Version = retentionRecordVersion
	intent.PayloadRetired = true
	intent.PayloadRetiredAt = now.UTC()
	intent.Message = nil
	intent.Media = nil
	intent.LastError = ""
	intent.RetryAfter = time.Time{}
	return intent
}
