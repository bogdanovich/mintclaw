package channels

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolFeedbackCarrierStorePersistsAndDeletes(t *testing.T) {
	root := t.TempDir()
	store, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Record(
		"telegram:chat-1\x00turn\x00parent",
		"telegram",
		"chat-1",
		"message-1",
		time.Unix(1_700_000_000, 0),
	)
	if err != nil || id == "" {
		t.Fatalf("Record() = %q, %v", id, err)
	}
	info, err := os.Stat(toolFeedbackCarrierStorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o, want 600", info.Mode().Perm())
	}

	reopened, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	records := reopened.Snapshot()
	if len(records) != 1 || records[0].ID != id || records[0].Channel != "telegram" ||
		records[0].ChatID != "chat-1" || records[0].MessageID != "message-1" {
		t.Fatalf("recovered records = %#v", records)
	}
	if err := reopened.Delete(id); err != nil {
		t.Fatal(err)
	}
	finalStore, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if records := finalStore.Snapshot(); len(records) != 0 {
		t.Fatalf("records after delete = %#v", records)
	}
}

func TestToolFeedbackCarrierStoreRejectsCorruptSnapshot(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		toolFeedbackCarrierStorePath(root),
		[]byte(`{"version":1,"records":[{"version":1,"id":"forged"}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := openToolFeedbackCarrierStore(root); err == nil {
		t.Fatal("openToolFeedbackCarrierStore() accepted a corrupt snapshot")
	}
}

func TestToolFeedbackRecoveryReclaimsInterruptedParentAndBrowserCarriersOnce(t *testing.T) {
	root := t.TempDir()
	firstStore, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first := newToolFeedbackCoordinator(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, firstStore,
	)
	operations := toolFeedbackOperations{
		channelName: "telegram",
		edit:        func(context.Context, string, string, string) error { return nil },
		delete:      func(context.Context, string, string) error { return nil },
	}
	carriers := []struct {
		key       string
		messageID string
	}{
		{key: "telegram:chat-1\x00turn\x00parent-delegate", messageID: "parent-feedback"},
		{key: "telegram:chat-1\x00turn\x00browser-subagent", messageID: "browser-feedback"},
	}
	for _, carrier := range carriers {
		carrier := carrier
		if _, err := first.Deliver(
			t.Context(), carrier.key, "chat-1", "Working...", operations,
			func(context.Context, string) ([]string, error) {
				return []string{carrier.messageID}, nil
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	if records := firstStore.Snapshot(); len(records) != 2 {
		t.Fatalf("persisted carriers = %#v, want two", records)
	}
	first.StopAll()

	secondStore, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second := newToolFeedbackCoordinator(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, secondStore,
	)
	t.Cleanup(second.StopAll)
	var mu sync.Mutex
	deleted := make([]string, 0, 2)
	recoveryOperations := toolFeedbackOperations{
		channelName: "telegram",
		delete: func(_ context.Context, _, messageID string) error {
			mu.Lock()
			deleted = append(deleted, messageID)
			mu.Unlock()
			return nil
		},
	}
	second.recoverChannel(t.Context(), "telegram", recoveryOperations)
	waitForToolFeedbackTest(t, func() bool { return len(secondStore.Snapshot()) == 0 })
	second.recoverChannel(t.Context(), "telegram", recoveryOperations)
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	slices.Sort(deleted)
	got := append([]string(nil), deleted...)
	mu.Unlock()
	if want := []string{"browser-feedback", "parent-feedback"}; !slices.Equal(got, want) {
		t.Fatalf("deleted carriers = %v, want %v", got, want)
	}
}

func TestToolFeedbackRecoveryRetriesFailureAndPreservesCurrentRunCarrier(t *testing.T) {
	root := t.TempDir()
	firstStore, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first := newToolFeedbackCoordinator(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, firstStore,
	)
	baseOperations := toolFeedbackOperations{
		channelName: "telegram",
		edit:        func(context.Context, string, string, string) error { return nil },
		delete:      func(context.Context, string, string) error { return nil },
	}
	if _, err := first.Deliver(
		t.Context(), "telegram:old-session", "chat-1", "old", baseOperations,
		func(context.Context, string) ([]string, error) { return []string{"old-feedback"}, nil },
	); err != nil {
		t.Fatal(err)
	}
	first.StopAll()

	secondStore, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second := newToolFeedbackCoordinator(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, secondStore,
	)
	second.recoveryRetryDelay = 5 * time.Millisecond
	t.Cleanup(second.StopAll)
	if _, err := second.Deliver(
		t.Context(), "telegram:new-session", "chat-1", "new", baseOperations,
		func(context.Context, string) ([]string, error) { return []string{"new-feedback"}, nil },
	); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	second.recoverChannel(t.Context(), "telegram", toolFeedbackOperations{
		channelName: "telegram",
		delete: func(_ context.Context, _, messageID string) error {
			if messageID != "old-feedback" {
				t.Errorf("recovery deleted current-run message %q", messageID)
			}
			if attempts.Add(1) == 1 {
				return ErrTemporary
			}
			return nil
		},
	})
	waitForToolFeedbackTest(t, func() bool {
		records := secondStore.Snapshot()
		return attempts.Load() == 2 && len(records) == 1 && records[0].MessageID == "new-feedback"
	})
	if second.ActiveCount() != 1 {
		t.Fatalf("current-run active carriers = %d, want 1", second.ActiveCount())
	}
	second.Dismiss(t.Context(), "telegram:new-session")
	waitForToolFeedbackTest(t, func() bool { return len(secondStore.Snapshot()) == 0 })
}

func TestManagerStartAllReconcilesRecoveredToolFeedbackCarriers(t *testing.T) {
	store, err := openToolFeedbackCarrierStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(
		"telegram:chat-1\x00turn\x00interrupted",
		"telegram",
		"chat-1",
		"interrupted-feedback",
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager()
	manager.stream.initializeToolFeedback(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, store,
	)
	channel := &toolFeedbackTestChannel{}
	manager.lifecycle.storeChannel("telegram", channel)
	if err := manager.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.StopAll(context.Background()) })

	waitForToolFeedbackTest(t, func() bool {
		channel.mu.Lock()
		defer channel.mu.Unlock()
		return slices.Equal(channel.deleted, []string{"chat-1|interrupted-feedback"}) &&
			len(store.Snapshot()) == 0
	})
}

func TestToolFeedbackRecoveryBoundsFailuresAndRetainsDurableRecord(t *testing.T) {
	root := t.TempDir()
	store, err := openToolFeedbackCarrierStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(
		"telegram:chat-1\x00turn\x00failed",
		"telegram",
		"chat-1",
		"failed-feedback",
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	coordinator := newToolFeedbackCoordinator(
		ToolFeedbackAnimatorConfig{AnimationInterval: time.Hour}, false, store,
	)
	coordinator.recoveryRetryDelay = time.Millisecond
	coordinator.recoveryRetryLimit = 2
	t.Cleanup(coordinator.StopAll)
	var attempts atomic.Int32
	operations := toolFeedbackOperations{
		channelName: "telegram",
		delete: func(context.Context, string, string) error {
			attempts.Add(1)
			return ErrTemporary
		},
	}
	coordinator.recoverChannel(t.Context(), "telegram", operations)
	waitForToolFeedbackTest(t, func() bool { return attempts.Load() == 2 })
	coordinator.recoverChannel(t.Context(), "telegram", operations)
	time.Sleep(20 * time.Millisecond)
	if attempts.Load() != 2 {
		t.Fatalf("recovery attempts = %d, want bounded at 2", attempts.Load())
	}
	if records := store.Snapshot(); len(records) != 1 || records[0].MessageID != "failed-feedback" {
		t.Fatalf("records after exhausted recovery = %#v", records)
	}
}

func waitForToolFeedbackTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("timed out waiting for tool feedback state")
	}
}
