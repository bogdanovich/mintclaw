package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func TestRecoverIsolatesMalformedRecordWithoutReplayingIt(t *testing.T) {
	store := openTestStore(t)
	damaged := createTestIntentWithOrdinal(t, store, "completed", 0)
	if _, err := store.BeginAttempt(damaged.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDelivered(damaged.ID, Outcome{}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(store.recordPath(damaged.ID))
	if err != nil {
		t.Fatal(err)
	}
	pending := createTestIntentWithOrdinal(t, store, "pending", 1)
	interrupted := createTestIntentWithOrdinal(t, store, "interrupted", 2)
	if _, err := store.BeginAttempt(interrupted.ID); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{private-payload-that-must-not-be-logged")
	if err := os.WriteFile(store.recordPath(damaged.ID), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.Recover()
	if err != nil || len(recovered.Issues) != 1 {
		t.Fatalf("Recover() error = %v, want one isolated record", err)
	}
	if recovered.Issues[0] != (RecoveryIssue{DeliveryID: damaged.ID, Code: RecoveryIssueInvalidJSON}) {
		t.Fatalf("recovery issue = %#v", recovered.Issues[0])
	}
	diagnostic, marshalErr := json.Marshal(recovered.Issues)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(diagnostic), "private-payload") {
		t.Fatal("recovery issue exposed the damaged payload")
	}
	if len(recovered.Intents) != 1 || recovered.Intents[0].ID != pending.ID {
		t.Fatalf("Recover() = %#v, want only independent pending work", recovered)
	}
	if _, err := store.Create(newTestIntent(t, "must not be replayed", 0)); err == nil {
		t.Fatal("Create() replaced the damaged delivery identity")
	}
	retained, err := os.ReadFile(store.recordPath(damaged.ID))
	if err != nil || !bytes.Equal(retained, corrupt) {
		t.Fatalf("damaged evidence changed: %q, %v", retained, err)
	}
	got, err := store.Get(interrupted.ID)
	if err != nil || got.Status != StatusAmbiguous {
		t.Fatalf("interrupted intent = %#v, %v, want ambiguous", got, err)
	}

	if err := os.WriteFile(store.recordPath(damaged.ID), original, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(filepath.Dir(store.dir)))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = reopened.Recover()
	if err != nil || len(recovered.Intents) != 1 || recovered.Intents[0].ID != pending.ID {
		t.Fatalf("Recover() after repair = %#v, %v, want no completed/ambiguous replay", recovered, err)
	}
}

func TestCoordinatorRecoverIsolatesRecordsAndClaimsIndependentWorkOnce(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	damaged := createTestIntentWithOrdinal(t, store, "damaged", 0)
	pending := createTestIntentWithOrdinal(t, store, "pending", 1)
	if err := os.WriteFile(store.recordPath(damaged.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })

	recovered, err := coordinator.Recover()
	if err != nil || len(recovered.Issues) != 1 || len(recovered.Admissions) != 1 ||
		recovered.Admissions[0].Intent.ID != pending.ID || !recovered.Admissions[0].Dispatch {
		t.Fatalf("Recover() = %#v, %v, want one independent dispatch", recovered, err)
	}
	if _, err := coordinator.AdmitMessage(
		"/agents/main",
		testIdentity(),
		bus.OutboundMessage{Content: "retry"},
	); err == nil {
		t.Fatal("AdmitMessage() replaced the blocked delivery")
	}
	recovered, err = coordinator.Recover()
	if err != nil || len(recovered.Admissions) != 0 {
		t.Fatalf("second Recover() = %#v, %v, want no duplicate claims", recovered, err)
	}
}

func TestRecoverKeepsUnrecognizedRecordsFatal(t *testing.T) {
	for _, name := range []string{"unknown filename", "uppercase identity", "unsupported version", "invalid schema", "identity conflict", "missing record"} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t)
			createTestIntentWithOrdinal(t, store, "pending", 1)
			intent := newTestIntent(t, "unknown", 0)
			path := store.recordPath(intent.ID)
			data := []byte("{")
			switch name {
			case "unknown filename":
				path = filepath.Join(store.dir, "unknown.json")
			case "uppercase identity":
				path = store.recordPath("out_" + strings.ToUpper(strings.TrimPrefix(intent.ID, "out_")))
			case "unsupported version":
				intent.Version--
				var err error
				data, err = json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid schema":
				data = []byte(`{"version":"unsupported"}`)
			case "identity conflict":
				intent = newTestIntent(t, "other identity", 2)
				var err error
				data, err = json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
			case "missing record":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Skipf("host cannot create a dangling record link: %v", err)
				}
			}
			if name != "missing record" {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := store.Recover()
			if err == nil || len(recovered.Issues) != 0 || len(recovered.Intents) != 0 {
				t.Fatalf("Recover() = %#v, %v, want fatal recovery failure", recovered, err)
			}
		})
	}
}

func TestRecoverPersistenceFailureOverridesIsolatedIssues(t *testing.T) {
	store := openTestStore(t)
	damaged := createTestIntentWithOrdinal(t, store, "damaged", 0)
	createTestIntentWithOrdinal(t, store, "pending", 1)
	interrupted := createTestIntentWithOrdinal(t, store, "interrupted", 2)
	if _, err := store.BeginAttempt(interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.recordPath(damaged.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("disk unavailable")
	store.writeAtomic = func(string, []byte, os.FileMode) error { return wantErr }
	recovered, err := store.Recover()
	if !errors.Is(err, wantErr) || len(recovered.Issues) != 0 || len(recovered.Intents) != 0 {
		t.Fatalf("Recover() = %#v, %v, want fatal persistence failure", recovered, err)
	}
}

func TestRecoverIsolatesPermissionDeniedRecord(t *testing.T) {
	store := openTestStore(t)
	damaged := createTestIntentWithOrdinal(t, store, "unreadable", 0)
	pending := createTestIntentWithOrdinal(t, store, "pending", 1)
	path := store.recordPath(damaged.ID)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); !errors.Is(err, os.ErrPermission) {
		t.Skipf("host does not enforce this permission denial: %v", err)
	}
	recovered, err := store.Recover()
	if err != nil || len(recovered.Issues) != 1 ||
		recovered.Issues[0].Code != RecoveryIssuePermissionDenied ||
		len(recovered.Intents) != 1 || recovered.Intents[0].ID != pending.ID {
		t.Fatalf("Recover() = %#v, %v, want isolated permission denial", recovered, err)
	}
}

func TestRecoverDirectoryFailureRemainsFatal(t *testing.T) {
	store := openTestStore(t)
	createTestIntent(t, store, "pending")
	if err := os.Rename(store.dir, store.dir+".offline"); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Recover()
	if !errors.Is(err, os.ErrNotExist) || len(recovered.Issues) != 0 || len(recovered.Intents) != 0 {
		t.Fatalf("Recover() = %#v, %v, want fatal directory failure", recovered, err)
	}
}

func TestCoordinatorFatalRecoveryDoesNotClaimIndependentWork(t *testing.T) {
	coordinator, err := OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	store := coordinator.store
	damaged := createTestIntentWithOrdinal(t, store, "damaged", 0)
	pending := createTestIntentWithOrdinal(t, store, "pending", 1)
	interrupted := createTestIntentWithOrdinal(t, store, "interrupted", 2)
	if _, err := store.BeginAttempt(interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.recordPath(damaged.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeAtomic
	wantErr := errors.New("disk unavailable")
	store.writeAtomic = func(string, []byte, os.FileMode) error { return wantErr }
	recovered, err := coordinator.Recover()
	if !errors.Is(err, wantErr) || len(recovered.Issues) != 0 || len(recovered.Admissions) != 0 {
		t.Fatalf("Recover() = %#v, %v, want fatal persistence failure", recovered, err)
	}
	store.writeAtomic = originalWrite
	recovered, err = coordinator.Recover()
	if err != nil || len(recovered.Admissions) != 1 || recovered.Admissions[0].Intent.ID != pending.ID {
		t.Fatalf("Recover() after storage repair = %#v, %v, want an unclaimed pending delivery", recovered, err)
	}
}
