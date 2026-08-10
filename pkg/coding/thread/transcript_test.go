package thread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestAppendUserMessageRequiresLiveMatchingLeaseAndSurvivesRestart(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	if err := store.AppendUserMessage(t.Context(), lease, metadata, "first prompt\nwith detail"); err != nil {
		t.Fatalf("AppendUserMessage() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := store.AppendUserMessage(t.Context(), lease, metadata, "after release"); err == nil ||
		!strings.Contains(err.Error(), "released") {
		t.Fatalf("AppendUserMessage(released) error = %v", err)
	}

	reopenedStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatalf("NewStore(restart) error = %v", err)
	}
	reopenedMetadata, err := reopenedStore.Load(metadata.ThreadID)
	if err != nil {
		t.Fatalf("Load(restart) error = %v", err)
	}
	canonical, err := memory.NewJSONLStore(
		filepath.Join(store.Root(), "threads", metadata.ThreadID, "sessions"),
	)
	if err != nil {
		t.Fatalf("NewJSONLStore(restart) error = %v", err)
	}
	backend := session.NewJSONLBackend(canonical)
	t.Cleanup(func() { _ = backend.Close() })
	history, err := backend.ReadTurnHistory(t.Context(), reopenedMetadata.SessionKey)
	if err != nil {
		t.Fatalf("ReadTurnHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].Role != "user" || history[0].Content != "first prompt\nwith detail" {
		t.Fatalf("restarted history = %#v", history)
	}
}

func TestAppendUserMessageKeepsThreadsIsolated(t *testing.T) {
	store, first := newLeaseTestThread(t)
	project := first.Project
	second, err := NewMetadata(NewThreadID(), project, "second thread", first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	firstLease, err := store.AcquireLease(first.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstLease.Release() })
	secondLease, err := store.AcquireLease(second.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondLease.Release() })
	if err := store.AppendUserMessage(t.Context(), firstLease, first, "first only"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUserMessage(t.Context(), secondLease, second, "second only"); err != nil {
		t.Fatal(err)
	}

	firstRoot, _ := store.ThreadRoot(first.ThreadID)
	secondRoot, _ := store.ThreadRoot(second.ThreadID)
	if firstRoot == secondRoot {
		t.Fatal("thread roots are shared")
	}
	for root, want := range map[string]string{firstRoot: "first only", secondRoot: "second only"} {
		canonical, openErr := memory.NewJSONLStore(filepath.Join(root, "sessions"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		backend := session.NewJSONLBackend(canonical)
		history, readErr := backend.ReadTurnHistory(t.Context(), SessionKey(filepath.Base(root)))
		_ = backend.Close()
		if readErr != nil || len(history) != 1 || history[0].Content != want {
			t.Fatalf("history below %q = %#v / %v", root, history, readErr)
		}
	}
}

func TestAppendUserMessageRejectsInvalidInputBeforeWrite(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	other, err := NewMetadata(NewThreadID(), metadata.Project, "other thread", metadata.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if err := store.AppendUserMessage(t.Context(), lease, other, "wrong thread"); err == nil ||
		!strings.Contains(err.Error(), "cannot write") {
		t.Fatalf("AppendUserMessage(wrong lease) error = %v", err)
	}
	if err := store.AppendUserMessage(context.Background(), lease, metadata, " \n\t "); err == nil {
		t.Fatal("AppendUserMessage(empty) succeeded")
	}
	if err := store.AppendUserMessage(
		context.Background(),
		lease,
		metadata,
		strings.Repeat("x", MaxPromptBytes+1),
	); err == nil {
		t.Fatal("AppendUserMessage(oversized) succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.AppendUserMessage(canceled, lease, metadata, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendUserMessage(canceled) error = %v", err)
	}
}

func TestAppendUserMessageRejectsLeaseFromDifferentStoreWithSameThreadID(t *testing.T) {
	storeA, metadata := newLeaseTestThread(t)
	storeB, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storeB.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := storeA.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if err := storeB.AppendUserMessage(t.Context(), lease, metadata, "wrong store"); err == nil ||
		!strings.Contains(err.Error(), "different store") {
		t.Fatalf("AppendUserMessage(cross-store lease) error = %v", err)
	}
	threadRoot, err := storeB.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(threadRoot, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-store append created transcript state: %v", err)
	}
}

func TestClassifyPromptAppendPreservesCommittedState(t *testing.T) {
	committedCause := &memory.CommittedAppendError{Err: errors.New("metadata finalization failed")}
	err := classifyPromptAppend("thread-id", committedCause, nil)
	if !IsCommittedPromptError(err) || !errors.Is(err, committedCause) ||
		!strings.Contains(err.Error(), "do not blindly retry") {
		t.Fatalf("classifyPromptAppend() error = %v", err)
	}
	ordinary := errors.New("append failed")
	if err := classifyPromptAppend("thread-id", ordinary, nil); IsCommittedPromptError(err) {
		t.Fatalf("ordinary append classified as committed: %v", err)
	}
	indeterminateCause := &memory.IndeterminateAppendError{Err: errors.New("fsync failed")}
	err = classifyPromptAppend("thread-id", indeterminateCause, nil)
	if !IsIndeterminatePromptError(err) || IsCommittedPromptError(err) ||
		!errors.Is(err, indeterminateCause) || !strings.Contains(err.Error(), "do not blindly retry") {
		t.Fatalf("indeterminate classifyPromptAppend() error = %v", err)
	}
}
