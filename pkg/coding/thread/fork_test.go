package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestForkThreadAtHistoricalTurnPublishesIndependentRestartableHistory(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewMetadata(NewThreadID(), project, "inspect the parser", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	source.Model = "gpt-source"
	source.Provider = "openai"
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	sourceHistory := []providers.Message{
		{Role: "user", Content: "first request", RootTurnStart: true},
		{Role: "assistant", Content: "first response"},
		{Role: "user", Content: "second request", RootTurnStart: true},
		{Role: "assistant", Content: "second response"},
	}
	writeForkTestHistory(t, store, source, sourceHistory)
	sourceLease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceLease.Release() })

	targetID := NewThreadID()
	child, result, err := store.ForkThread(t.Context(), sourceLease, ForkOptions{
		TargetThreadID: targetID, Project: project, AtTurn: 1, At: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != targetID || result.SourceTurn != 1 || result.CopiedMessages != 2 ||
		!result.LiveFilesystem {
		t.Fatalf("fork result = %+v", result)
	}
	if child.ParentThread != source.ThreadID || child.Fork == nil || child.Fork.SourceRevision == 0 ||
		child.Fork.SourceMessageIndex != 0 || child.Fork.SourceTurn != 1 || child.Fork.CopiedMessages != 2 ||
		len(child.Fork.SourceMessageID) != 64 {
		t.Fatalf("fork metadata = %+v", child)
	}
	if child.SessionKey == source.SessionKey || child.Model != source.Model || child.Provider != source.Provider ||
		child.Compaction != nil {
		t.Fatalf("fork identity = %+v", child)
	}
	if got := readForkTestHistory(t, store, child); !equalForkHistory(got, sourceHistory[:2]) {
		t.Fatalf("child history = %#v", got)
	}

	if err := sourceLease.Release(); err != nil {
		t.Fatal(err)
	}
	childLease, err := store.AcquireLease(child.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUserMessage(t.Context(), childLease, child, "child only"); err != nil {
		t.Fatal(err)
	}
	if err := childLease.Release(); err != nil {
		t.Fatal(err)
	}
	if got := readForkTestHistory(t, store, source); !equalForkHistory(got, sourceHistory) {
		t.Fatalf("source changed with child = %#v", got)
	}
	if got := readForkTestHistory(t, store, child); len(got) != 3 || got[2].Content != "child only" {
		t.Fatalf("independent child history = %#v", got)
	}

	restarted, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(child.ThreadID)
	if err != nil || loaded.Fork == nil || loaded.ParentThread != source.ThreadID {
		t.Fatalf("restarted child = %+v / %v", loaded, err)
	}
}

func TestForkThreadLatestSupportsLegacyRootMarkersAndRejectsBounds(t *testing.T) {
	store, source := newLeaseTestThread(t)
	legacy := []providers.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "one"},
		{Role: "user", ToolCallID: "tool-result", Content: "not a root"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "two"},
	}
	writeForkTestHistory(t, store, source, legacy)
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	child, result, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceTurn != 2 || result.CopiedMessages != len(legacy) || child.Fork.SourceMessageIndex != 3 {
		t.Fatalf("latest legacy fork = %+v metadata=%+v", result, child.Fork)
	}
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, AtTurn: 3, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "exceeds 2 available") {
		t.Fatalf("out-of-range fork error = %v", err)
	}

	tooLarge := make([]providers.Message, MaxForkMessages+1)
	for index := range tooLarge {
		tooLarge[index] = providers.Message{Role: "user", Content: "bounded", RootTurnStart: true}
	}
	writeForkTestHistory(t, store, source, tooLarge)
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized fork error = %v", err)
	}
}

func TestForkThreadRejectsRecordAboveCanonicalReaderLimitBeforeProvision(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{Role: "user", Content: "seed", RootTurnStart: true}})
	oversized, err := json.Marshal(providers.Message{
		Role: "user", Content: strings.Repeat("x", memory.MaxJSONLRecordBytes), RootTurnStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replaceForkTestJSONL(t, store, source, append(oversized, '\n'))
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || !strings.Contains(forkErr.Error(), "token too long") {
		t.Fatalf("oversized JSONL record error = %v", forkErr)
	}
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		t.Fatalf("oversized record allocated target: %v", err)
	}
}

func TestForkThreadRejectsRecordExpandedByCanonicalWriterBeforeProvision(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{Role: "user", Content: "seed", RootTurnStart: true}})
	message := providers.Message{Role: "user", RootTurnStart: true}
	base, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	message.Content = strings.Repeat("x", memory.MaxJSONLRecordBytes-len(base)-1)
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)+1 > memory.MaxJSONLRecordBytes {
		t.Fatalf("source fixture exceeds canonical reader limit: %d", len(raw)+1)
	}
	normalized := message
	createdAt := time.Now()
	normalized.CreatedAt = &createdAt
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 <= memory.MaxJSONLRecordBytes {
		t.Fatalf("normalized fixture does not exceed writer limit: %d", len(encoded)+1)
	}
	replaceForkTestJSONL(t, store, source, append(raw, '\n'))
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || !strings.Contains(forkErr.Error(), "JSONL record limit") {
		t.Fatalf("expanded JSONL record error = %v", forkErr)
	}
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		t.Fatalf("expanded record allocated target: %v", err)
	}
}

func TestForkThreadRejectsBusySourceAndTargetCollision(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if _, err := store.AcquireLease(source.ThreadID); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("second source lease error = %v", err)
	}
	target, err := NewMetadata(NewThreadID(), source.Project, "existing target", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: target.ThreadID, Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("target collision error = %v", err)
	}
	if _, err := store.Load(target.ThreadID); err != nil {
		t.Fatalf("collision removed existing target: %v", err)
	}
}

func TestForkThreadRejectsLinkedSourceSessionsAndDifferentProject(t *testing.T) {
	store, source := newLeaseTestThread(t)
	threadRoot, err := store.ThreadRoot(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(threadRoot, "sessions")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "open sessions") {
		t.Fatalf("linked sessions error = %v", err)
	}
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		t.Fatalf("linked source allocated target: %v", err)
	}
	if err := os.Remove(filepath.Join(threadRoot, "sessions")); err != nil {
		t.Fatal(err)
	}
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})

	other, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: other, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "belongs to project") {
		t.Fatalf("different project error = %v", err)
	}
}

func TestForkThreadRejectsLinkedSourceTranscriptFile(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	sessionsRoot := filepath.Join(threadRoot, "sessions")
	transcript := filepath.Join(sessionsRoot, "coding_"+source.ThreadID+".jsonl")
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Rename(transcript, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, transcript); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "pin transcript JSONL") {
		t.Fatalf("linked transcript error = %v", err)
	}
}

func TestForkThreadRejectsHardLinkedSourceTranscriptFile(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	sessionsRoot := filepath.Join(threadRoot, "sessions")
	transcript := filepath.Join(sessionsRoot, "coding_"+source.ThreadID+".jsonl")
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Link(transcript, outside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "singly linked") {
		t.Fatalf("hard-linked transcript error = %v", err)
	}
}

func TestForkRootIndexesSupportsLegacyPrefixBeforeMarkedTurns(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "legacy root"},
		{Role: "assistant", Content: "legacy answer"},
		{Role: "user", Content: "marked root", RootTurnStart: true},
		{Role: "user", Content: "in-turn user-shaped message"},
		{Role: "assistant", Content: "marked answer"},
	}
	indexes := forkRootIndexes(history)
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 2 {
		t.Fatalf("mixed root indexes = %v", indexes)
	}
}

func TestForkThreadPublishesMetadataLastAndClassifiesCommittedSave(t *testing.T) {
	for _, test := range []struct {
		name      string
		committed bool
	}{
		{name: "pre-commit failure"},
		{name: "committed durability warning", committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, source := newLeaseTestThread(t)
			writeForkTestHistory(t, store, source, []providers.Message{{
				Role: "user", Content: "source", RootTurnStart: true,
			}})
			lease, err := store.AcquireLease(source.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lease.Release() })
			targetID := NewThreadID()
			injected := errors.New("injected metadata write failure")
			originalWrite := store.writeRoot
			store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
				if name != metadataFileName {
					return originalWrite(root, name, data, mode)
				}
				if !test.committed {
					return injected
				}
				if err := originalWrite(root, name, data, mode); err != nil {
					return err
				}
				return &fileutil.CommittedWriteError{Err: injected}
			}
			child, result, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
				TargetThreadID: targetID, Project: source.Project, At: time.Now(),
			})
			targetRoot, err := store.ThreadRoot(targetID)
			if err != nil {
				t.Fatal(err)
			}
			if test.committed {
				if !IsCommittedForkError(forkErr) || result.ThreadID != targetID || child.ThreadID != targetID {
					t.Fatalf("committed fork = child %+v result %+v error %v", child, result, forkErr)
				}
				if _, err := store.Load(targetID); err != nil {
					t.Fatalf("committed child is not readable: %v", err)
				}
				return
			}
			if !errors.Is(forkErr, injected) || IsCommittedForkError(forkErr) {
				t.Fatalf("pre-commit fork error = %v", forkErr)
			}
			if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
				t.Fatalf("pre-commit target remains: %v", err)
			}
		})
	}
}

func TestForkThreadDoesNotClassifyCommittedProvisionAsPublished(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	injected := errors.New("injected provision sync failure")
	originalSync := store.syncRoot
	injectedOnce := false
	store.syncRoot = func(root *os.Root) error {
		if !injectedOnce {
			injectedOnce = true
			return &fileutil.CommittedWriteError{Err: injected}
		}
		return originalSync(root)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if !errors.Is(forkErr, injected) || IsCommittedForkError(forkErr) {
		t.Fatalf("provision failure classification = %v", forkErr)
	}
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		t.Fatalf("unpublished provision target remains: %v", err)
	}
}

func TestForkThreadDoesNotReserveThroughReplacedThreadsRoot(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	threadsPath := filepath.Join(store.root, "threads")
	movedThreads := filepath.Join(store.root, "threads-moved")
	externalThreads := t.TempDir()
	originalMkdir := store.mkdirDurable
	replaced := false
	store.mkdirDurable = func(root, name string, mode os.FileMode) error {
		if err := originalMkdir(root, name, mode); err != nil {
			return err
		}
		if replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(threadsPath, movedThreads); err != nil {
			return err
		}
		return os.Symlink(externalThreads, threadsPath)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || IsCommittedForkError(forkErr) {
		t.Fatalf("replaced threads root classification = %v", forkErr)
	}
	if _, err := os.Stat(filepath.Join(externalThreads, targetID)); !os.IsNotExist(err) {
		t.Fatalf("target was reserved outside the store: %v", err)
	}
}

func TestForkThreadDoesNotPublishThroughReplacedTargetRoot(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := targetRoot + "-moved"
	injected := errors.New("injected post-save durability warning")
	originalWrite := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if name != metadataFileName {
			return originalWrite(root, name, data, mode)
		}
		if err := originalWrite(root, name, data, mode); err != nil {
			return err
		}
		if err := os.Rename(targetRoot, movedRoot); err != nil {
			return err
		}
		if err := os.Symlink(movedRoot, targetRoot); err != nil {
			return err
		}
		return &fileutil.CommittedWriteError{Err: injected}
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || IsCommittedForkError(forkErr) {
		t.Fatalf("replaced target classification = %v", forkErr)
	}
	if info, err := os.Lstat(targetRoot); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement symlink was not preserved: %v / %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(movedRoot, metadataFileName)); err != nil {
		t.Fatalf("fixture did not preserve moved original: %v", err)
	}
}

func TestForkThreadDoesNotDeleteReplacementTargetDirectory(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := targetRoot + "-moved"
	marker := filepath.Join(targetRoot, "unrelated-marker")
	originalWrite := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if name != metadataFileName {
			return originalWrite(root, name, data, mode)
		}
		if err := originalWrite(root, name, data, mode); err != nil {
			return err
		}
		if err := os.Rename(targetRoot, movedRoot); err != nil {
			return err
		}
		if err := os.Mkdir(targetRoot, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
			return err
		}
		return &fileutil.CommittedWriteError{Err: errors.New("injected post-save durability warning")}
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || IsCommittedForkError(forkErr) {
		t.Fatalf("replacement directory classification = %v", forkErr)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("replacement marker = %q / %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(movedRoot, metadataFileName)); err != nil {
		t.Fatalf("fixture did not preserve moved original: %v", err)
	}
}

func TestForkThreadWritesThroughPinnedTargetAfterPathReplacement(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := targetRoot + "-moved"
	marker := filepath.Join(targetRoot, "unrelated-marker")
	originalWrite := store.writeRoot
	replaced := false
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if !replaced && strings.HasSuffix(name, ".jsonl") {
			replaced = true
			if err := os.Rename(targetRoot, movedRoot); err != nil {
				return err
			}
			if err := os.Mkdir(targetRoot, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				return err
			}
		}
		return originalWrite(root, name, data, mode)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || IsCommittedForkError(forkErr) {
		t.Fatalf("pre-write replacement classification = %v", forkErr)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("replacement marker = %q / %v", data, err)
	}
	if _, err := store.Load(targetID); err == nil {
		t.Fatal("replacement became a catalog-visible child")
	}
	if _, err := os.Stat(filepath.Join(movedRoot, metadataFileName)); err != nil {
		t.Fatalf("pinned original did not receive child metadata: %v", err)
	}
}

func TestForkThreadDoesNotPublishAfterSessionsReplacement(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	targetID := NewThreadID()
	targetRoot, err := store.ThreadRoot(targetID)
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeRoot
	replaced := false
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if name == metadataFileName && !replaced {
			replaced = true
			if err := os.Rename(
				filepath.Join(targetRoot, "sessions"),
				filepath.Join(targetRoot, "sessions-moved"),
			); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(targetRoot, "sessions"), 0o700); err != nil {
				return err
			}
		}
		return originalWrite(root, name, data, mode)
	}
	_, _, forkErr := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: targetID, Project: source.Project, At: time.Now(),
	})
	if forkErr == nil || IsCommittedForkError(forkErr) {
		t.Fatalf("replaced sessions classification = %v", forkErr)
	}
	if _, err := store.Load(targetID); err == nil {
		t.Fatal("sessions replacement became a catalog-visible child")
	}
}

func TestForkThreadRejectsDirtyPinnedHistoryWithoutRecoveringIt(t *testing.T) {
	store, source := newLeaseTestThread(t)
	writeForkTestHistory(t, store, source, []providers.Message{{
		Role: "user", Content: "source", RootTurnStart: true,
	}})
	threadRoot, err := store.ThreadRoot(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(threadRoot, "sessions", "coding_"+source.ThreadID+".meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta memory.SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	meta.HistoryDirty = true
	dirtyData, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, dirtyData, 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(source.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if _, _, err := store.ForkThread(t.Context(), lease, ForkOptions{
		TargetThreadID: NewThreadID(), Project: source.Project, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "unfinished history mutation") {
		t.Fatalf("dirty history error = %v", err)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil || !bytes.Equal(after, dirtyData) {
		t.Fatalf("fork mutated dirty metadata: equal=%t error=%v", bytes.Equal(after, dirtyData), err)
	}
}

func TestReadPinnedForkFileSurvivesPathReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.jsonl")
	want := []byte("pinned\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	moved := filepath.Join(root, "moved.jsonl")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := readPinnedForkFile(t.Context(), file)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("pinned read = %q / %v", got, err)
	}
}

func writeForkTestHistory(
	t testing.TB,
	store *Store,
	metadata Metadata,
	history []providers.Message,
) {
	t.Helper()
	root, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := memory.NewJSONLStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	if err := backend.ReplaceTurnHistory(t.Context(), metadata.SessionKey, history); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func replaceForkTestJSONL(t testing.TB, store *Store, metadata Metadata, data []byte) {
	t.Helper()
	root, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "sessions", strings.ReplaceAll(metadata.SessionKey, ":", "_")+".jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readForkTestHistory(t testing.TB, store *Store, metadata Metadata) []providers.Message {
	t.Helper()
	root, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := memory.NewJSONLStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	history, err := backend.ReadTurnHistory(t.Context(), metadata.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	return history
}

func equalForkHistory(left, right []providers.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role || left[index].Content != right[index].Content ||
			left[index].RootTurnStart != right[index].RootTurnStart ||
			left[index].ToolCallID != right[index].ToolCallID {
			return false
		}
	}
	return true
}
