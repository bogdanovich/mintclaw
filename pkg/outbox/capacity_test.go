package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func TestCapacityAccountsAllStorageWithoutExposingPayloads(t *testing.T) {
	root := t.TempDir()
	eligible := deliveredRetentionFixture(t, root, "eligible", true, false)
	legacy := deliveredRetentionFixture(t, root, "legacy", false, false)
	active := deliveredRetentionFixture(t, root, "transcript", true, true)
	compact := deliveredRetentionFixture(t, root, "compact", true, false)
	result, err := RetirePayloadsOffline(
		t.Context(),
		root,
		RetirementPolicy{DeliveryIDs: []string{compact.ID}, MinAge: time.Hour, Apply: true},
	)
	if err != nil || result.Items[0].Outcome != "retired" {
		t.Fatalf("fixture retirement=%#v,%v", result, err)
	}
	originals := make(map[string][]byte)
	var total int64
	for _, intent := range []Intent{eligible, legacy, active, compact} {
		data, readErr := os.ReadFile(filepath.Join(Path(root), intent.ID+".json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		originals[intent.ID] = data
		total += int64(len(data))
	}
	report, err := InspectCapacityOffline(
		t.Context(),
		root,
		CapacityPolicy{MinAge: time.Hour, MaxBytes: total - 1, MaxRecords: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ScanComplete || report.TotalEntries != 4 || report.TotalRecords != 4 || report.TotalBytes != total ||
		!report.BudgetExceeded ||
		!report.OperatorActionRequired {
		t.Fatalf("whole-store capacity=%#v", report)
	}
	if report.Active.Count != 1 || report.Protected.Count != 1 || report.Eligible.Count != 1 ||
		report.Receipts.Count != 1 ||
		report.Unavailable.Count != 0 {
		t.Fatalf("capacity groups=%#v", report)
	}
	if report.Active.Bytes+report.Protected.Bytes+report.Eligible.Bytes+report.Receipts.Bytes != total ||
		len(
			report.Candidates,
		) != 1 || report.Candidates[0] != eligible.ID || report.CandidateTruncated || report.Outcomes[StatusDelivered] != 4 {
		t.Fatalf("capacity conservation/candidates=%#v", report)
	}
	if report.Eligible.OldestUpdatedAt != eligible.UpdatedAt || report.MinimumAge != "1h0m0s" ||
		report.Reasons["pending_settlement"] != 1 {
		t.Fatalf("age/reasons=%#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || bytes.Contains(encoded, []byte("private-body-sentinel")) ||
		bytes.Contains(encoded, []byte("/agents/main")) ||
		bytes.Contains(encoded, []byte(root)) {
		t.Fatal("capacity report exposed payload or private paths")
	}
	for id, before := range originals {
		after, readErr := os.ReadFile(filepath.Join(Path(root), id+".json"))
		if readErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("inspection changed record %s", id)
		}
	}
	report, err = InspectCapacityOffline(
		t.Context(),
		root,
		CapacityPolicy{MinAge: time.Hour, MaxBytes: total, MaxRecords: 4},
	)
	if err != nil || report.BudgetExceeded || report.OperatorActionRequired {
		t.Fatalf("exact budget=%#v,%v", report, err)
	}
	applied, err := RetirePayloadsOffline(t.Context(), root, RetirementPolicy{
		DeliveryIDs: []string{eligible.ID, legacy.ID, active.ID, compact.ID}, MinAge: time.Hour, Apply: true,
		CapacityBudget: &CapacityBudget{MaxBytes: 1, MaxRecords: 1},
	})
	if err != nil || applied.CapacityAfter == nil || applied.CapacityAfter.TotalRecords != 4 ||
		applied.CapacityAfter.Active.Count != 1 || applied.CapacityAfter.Protected.Count != 1 ||
		applied.CapacityAfter.Receipts.Count != 2 || !applied.CapacityAfter.OperatorActionRequired {
		t.Fatalf("budget discarded obligations/identities=%#v,%v", applied, err)
	}
	for _, id := range []string{legacy.ID, active.ID} {
		after, readErr := os.ReadFile(filepath.Join(Path(root), id+".json"))
		if readErr != nil || !bytes.Equal(originals[id], after) {
			t.Fatal("budget changed protected payload")
		}
	}
}

func TestCapacityStreamsAllEntriesWithBoundedCandidates(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	seeded := make(map[string]bool)
	old := time.Now().Add(-48 * time.Hour)
	for index := range 300 {
		intent, createErr := NewMessageIntent(
			"/synthetic/instance",
			Identity{SourceID: fmt.Sprintf("capacity-%d", index), Channel: "test", ChatID: "synthetic"},
			bus.OutboundMessage{Content: "synthetic body"},
			old,
		)
		if createErr != nil {
			t.Fatal(createErr)
		}
		// Synthetic valid record fixtures exercise streaming/accounting rather
		// than claiming qualification of an additional production owner class.
		intent.Version, intent.RetentionClass, intent.Status, intent.Attempts = retentionRecordVersion, retentionPlainMessage, StatusDelivered, 1
		if err = validateIntent(intent); err != nil {
			t.Fatal(err)
		}
		encoded, encodeErr := json.MarshalIndent(intent, "", "  ")
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if err = os.WriteFile(store.recordPath(intent.ID), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		seeded[intent.ID] = true
	}
	report, err := InspectCapacityOffline(t.Context(), root, CapacityPolicy{MinAge: time.Hour, MaxRecords: 299})
	if err != nil || !report.ScanComplete || report.TotalRecords != 300 || report.Eligible.Count != 300 ||
		len(report.Candidates) != MaxRetirementBatch || !report.CandidateTruncated || !report.OperatorActionRequired {
		t.Fatalf("streamed capacity=%#v,%v", report, err)
	}
	selected := make(map[string]bool)
	for _, id := range report.Candidates {
		if !seeded[id] || selected[id] {
			t.Fatalf("invalid/repeated candidate %s", id)
		}
		selected[id] = true
	}
}

func TestCapacityPreflightPreservesEveryUnknownAuthority(t *testing.T) {
	for _, kind := range []string{"invalid_json", "future_schema", "unexpected_name", "large_record", "directory", "symlink", "nonempty_lock"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			eligible := deliveredRetentionFixture(t, root, "eligible", true, false)
			before, err := os.ReadFile(filepath.Join(Path(root), eligible.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			badID := "out_" + strings.Repeat("a", 32)
			path := filepath.Join(Path(root), badID+".json")
			data := []byte("PRIVATE_UNAVAILABLE_CANARY")
			switch kind {
			case "invalid_json":
				data = []byte("{")
			case "future_schema":
				future := eligible
				future.Identity.SourceID = "future-capacity-source"
				future.ID, err = DeliveryID(future.Identity)
				if err != nil {
					t.Fatal(err)
				}
				body := *future.Message
				body.DeliveryID = future.ID
				future.Message = &body
				future.Version = retentionRecordVersion + 1
				path = filepath.Join(Path(root), future.ID+".json")
				data, err = json.Marshal(future)
				if err != nil {
					t.Fatal(err)
				}
			case "unexpected_name":
				path = filepath.Join(Path(root), "PRIVATE_UNEXPECTED_NAME")
			case "large_record":
				data = bytes.Repeat([]byte("x"), MaxInspectionBytes+1)
			case "directory":
				if err = os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "private-target")
				if err = os.WriteFile(target, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "nonempty_lock":
				path = filepath.Join(Path(root), ".writer.lock")
			}
			if kind != "directory" && kind != "symlink" {
				if err = os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := InspectCapacityOffline(t.Context(), root, CapacityPolicy{MinAge: time.Hour})
			if err != nil || !report.ScanComplete || report.Unavailable.Count != 1 || !report.OperatorActionRequired {
				t.Fatalf("unavailable=%#v,%v", report, err)
			}
			if (kind == "directory" || kind == "symlink") && report.Reasons["protected_nonregular_record"] != 1 {
				t.Fatal("nonregular entry was followed or decoded")
			}
			if kind == "large_record" && report.Reasons["protected_large_record"] != 1 {
				t.Fatal("oversized entry was decoded")
			}

			encoded, _ := json.Marshal(report)
			if bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("capacity exposed unknown content/name")
			}
			result, err := RetirePayloadsOffline(t.Context(), root, RetirementPolicy{
				DeliveryIDs: []string{eligible.ID}, MinAge: time.Hour, Apply: true, CapacityBudget: &CapacityBudget{},
			})
			if err == nil || result.CapacityBefore == nil || len(result.Items) != 0 {
				t.Fatalf("unsafe preflight=%#v,%v", result, err)
			}
			after, readErr := os.ReadFile(filepath.Join(Path(root), eligible.ID+".json"))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("preflight changed eligible record beside unknown authority")
			}
			if _, err = os.Lstat(path); err != nil {
				t.Fatal("unavailable evidence disappeared")
			}
		})
	}
}

func TestCapacityAndRetirementShareExclusiveWriterOwnership(t *testing.T) {
	root := t.TempDir()
	eligible := deliveredRetentionFixture(t, root, "owned", true, false)
	c, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InspectCapacityOffline(
		t.Context(),
		root,
		CapacityPolicy{MinAge: time.Hour},
	); !errors.Is(
		err,
		ErrCoordinatorOwned,
	) {
		t.Fatalf("live inventory=%v", err)
	}
	if _, err = RetirePayloadsOffline(
		t.Context(),
		root,
		RetirementPolicy{
			DeliveryIDs:    []string{eligible.ID},
			MinAge:         time.Hour,
			Apply:          true,
			CapacityBudget: &CapacityBudget{},
		},
	); !errors.Is(
		err,
		ErrCoordinatorOwned,
	) {
		t.Fatalf("live retirement=%v", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := RetirePayloadsOffline(
		t.Context(),
		root,
		RetirementPolicy{
			DeliveryIDs:    []string{eligible.ID},
			MinAge:         time.Hour,
			Apply:          true,
			CapacityBudget: &CapacityBudget{MaxBytes: 1},
		},
	)
	if err != nil || result.CapacityBefore == nil || result.CapacityAfter == nil ||
		result.Items[0].Outcome != "retired" {
		t.Fatalf("offline retirement=%#v,%v", result, err)
	}
	if result.CapacityBefore.Eligible.Count != 1 || result.CapacityAfter.Receipts.Count != 1 ||
		result.CapacityAfter.TotalRecords != 1 ||
		result.CapacityAfter.TotalBytes >= result.CapacityBefore.TotalBytes ||
		!result.CapacityAfter.OperatorActionRequired {
		t.Fatalf("capacity transition=%#v", result)
	}
	// A new runtime can own the store only after both scans and retirement end.
	c, err = OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
}

func TestCapacityInvalidOrCancelledRequestCreatesNoStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, policy := range []CapacityPolicy{{}, {MinAge: time.Hour, MaxBytes: -1}, {MinAge: time.Hour, MaxRecords: -1}} {
		if _, err := InspectCapacityOffline(t.Context(), root, policy); err == nil {
			t.Fatal("invalid capacity policy accepted")
		}
	}
	if _, err := InspectCapacityOffline(
		ctx,
		root,
		CapacityPolicy{MinAge: time.Hour},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled=%v", err)
	}
	if _, err := InspectCapacityOffline(t.Context(), root, CapacityPolicy{MinAge: time.Hour}); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capacity created missing store")
	}
	path := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), MaxInspectionBytes+500), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readCapacityRecord(path)
	if err != nil || len(data) != MaxInspectionBytes+1 {
		t.Fatalf("bounded read=%d,%v", len(data), err)
	}
}
