package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	codingplan "github.com/bogdanovich/mintclaw/pkg/coding/plan"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestStoreCurrentPlanReplacesCheckpointUnderThreadLease(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	before, ok, err := store.LoadCurrentPlan(t.Context(), lease, metadata)
	if err != nil || ok || !reflect.DeepEqual(before, CurrentPlanCheckpoint{}) {
		t.Fatalf("absent checkpoint = %+v, ok=%t, error=%v", before, ok, err)
	}

	firstState, err := codingplan.New("Start here", []codingplan.Step{
		{Step: "Inspect", Status: codingplan.StepInProgress},
		{Step: "Implement", Status: codingplan.StepPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewCurrentPlanCheckpoint(firstState, metadata.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCurrentPlan(t.Context(), lease, metadata, first); err != nil {
		t.Fatal(err)
	}

	secondState, err := codingplan.New("Finish", []codingplan.Step{
		{Step: "Inspect", Status: codingplan.StepCompleted},
		{Step: "Implement", Status: codingplan.StepInProgress},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCurrentPlanCheckpoint(secondState, metadata.CreatedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCurrentPlan(t.Context(), lease, metadata, second); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.LoadCurrentPlan(t.Context(), lease, metadata)
	if err != nil || !ok || !reflect.DeepEqual(loaded, second) {
		t.Fatalf("loaded checkpoint = %+v, ok=%t, error=%v", loaded, ok, err)
	}
	path := filepath.Join(root, "coding", "threads", metadata.ThreadID, presentationDirectory, currentPlanFileName)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint file = %#v, %v", info, err)
	}
}

func TestStoreCurrentPlanRejectsUnsafeOrMalformedCheckpoint(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	state := codingplan.State{
		Explanation: "token sk-123456789abcdef",
		Steps:       []codingplan.Step{{Step: "Inspect", Status: codingplan.StepPending}},
	}
	checkpoint := CurrentPlanCheckpoint{
		SchemaVersion: CurrentPlanSchemaVersion,
		UpdatedAt:     metadata.CreatedAt.Add(time.Minute),
		Plan:          state,
	}
	if err := store.SaveCurrentPlan(t.Context(), lease, metadata, checkpoint); err == nil {
		t.Fatal("unsafe checkpoint was persisted")
	}

	safe, err := codingplan.New("safe", state.Steps)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err = NewCurrentPlanCheckpoint(safe, metadata.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCurrentPlan(t.Context(), lease, metadata, checkpoint); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "coding", "threads", metadata.ThreadID, presentationDirectory, currentPlanFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"schema_version": 1`), []byte(`"unknown": true, "schema_version": 1`), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadCurrentPlan(t.Context(), lease, metadata); err == nil ||
		!strings.Contains(err.Error(), `unknown field "unknown"`) {
		t.Fatalf("malformed checkpoint error = %v", err)
	}
}

func TestCurrentPlanCheckpointRejectsAmbiguousOrDeepJSON(t *testing.T) {
	valid := []byte(`{
  "schema_version": 1,
  "schema_version": 1,
  "updated_at": "2026-08-29T12:00:00Z",
  "plan": {"steps": [{"step": "Inspect", "status": "pending"}]}
}`)
	if _, err := decodeCurrentPlanCheckpoint(valid); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate checkpoint error = %v", err)
	}
	deep := []byte(`[[[[[[[[[["too deep"]]]]]]]]]]`)
	if _, err := decodeCurrentPlanCheckpoint(deep); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deep checkpoint error = %v", err)
	}
}

func TestStoreCurrentPlanRequiresLiveLeaseAndContext(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	state, err := codingplan.New("", []codingplan.Step{{Step: "Inspect", Status: codingplan.StepPending}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewCurrentPlanCheckpoint(state, metadata.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCurrentPlan(t.Context(), lease, metadata, checkpoint); err == nil {
		t.Fatal("released lease saved checkpoint")
	}
	//nolint:staticcheck // This explicitly verifies the public nil-context guard.
	if _, _, err := store.LoadCurrentPlan(nil, lease, metadata); err == nil {
		t.Fatal("nil context loaded checkpoint")
	}
}

func TestCurrentPlanCheckpointCommittedWriteWarningRemainsClassified(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	state, err := codingplan.New("", []codingplan.Step{{Step: "Inspect", Status: codingplan.StepPending}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewCurrentPlanCheckpoint(state, metadata.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("sync failed")
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if err := writeRootFileAtomic(root, name, data, mode); err != nil {
			return err
		}
		return &fileutil.CommittedWriteError{Err: injected}
	}
	err = store.SaveCurrentPlan(t.Context(), lease, metadata, checkpoint)
	if !errors.Is(err, injected) || !fileutil.IsCommittedWriteError(err) {
		t.Fatalf("committed write error = %v", err)
	}
}

func newCurrentPlanTestThread(t *testing.T, root string) (*Store, Metadata, *Lease) {
	t.Helper()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "plan", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	return store, metadata, lease
}
