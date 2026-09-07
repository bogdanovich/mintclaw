package document

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAcquireSnapshotCreatesImmutableIdentityAndCleansUp(t *testing.T) {
	root := directTempDir(t)
	input := filepath.Join(root, "sample.pdf")
	data := []byte("%PDF-1.7\nsynthetic fixture\n%%EOF\n")
	writeFixture(t, input, data)
	report := newReport("document_operation_test", DefaultMaxInputBytes)

	snapshot, got := acquireSnapshot(t.Context(), input, filepath.Join(root, "protected"), report, nil)
	if got.State != StateSucceeded || got.Input == nil {
		t.Fatalf("report = %#v, want succeeded input", got)
	}
	if got.Input.OriginalFilename != "sample.pdf" || got.Input.ContentType != "application/pdf" {
		t.Fatalf("input metadata = %#v", got.Input)
	}
	if got.Input.Size != int64(len(data)) || len(got.Input.SHA256) != 64 {
		t.Fatalf("input identity = %#v", got.Input)
	}
	if snapshot == nil || snapshot.Path() == "" {
		t.Fatal("snapshot path is missing")
	}
	info, err := os.Stat(snapshot.Path())
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("snapshot mode = %o, want 400", info.Mode().Perm())
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{input, root, snapshot.Path(), "synthetic fixture"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
	snapshotDir := filepath.Dir(snapshot.Path())
	if err := snapshot.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("operation scratch survived cleanup: %v", err)
	}
}

func TestAcquireFailsClosedBeforeOpeningInputOnUnsupportedPlatform(t *testing.T) {
	root := directTempDir(t)
	scratch := filepath.Join(root, "must-not-exist")
	snapshot, report := acquireForPlatform(
		t.Context(), filepath.Join(root, "missing.pdf"), AcquireOptions{ScratchRoot: scratch}, "darwin", "arm64",
	)
	if snapshot != nil {
		t.Fatal("unsupported platform returned a snapshot")
	}
	assertFailure(t, report, StateUnavailable, FailureUnsupportedPlatform)
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("unsupported platform touched protected scratch: %v", err)
	}
}

func TestAcquireSnapshotRejectsUnsafeAndUnsupportedInputs(t *testing.T) {
	root := directTempDir(t)
	scratch := filepath.Join(root, "protected")

	t.Run("directory", func(t *testing.T) {
		_, report := acquireSnapshot(
			t.Context(), root, scratch, newReport("document_operation_directory", DefaultMaxInputBytes), nil,
		)
		assertFailure(t, report, StateFailed, FailureInvalidInput)
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink setup requires extra Windows privileges")
		}
		target := filepath.Join(root, "target.pdf")
		writeFixture(t, target, []byte("%PDF-1.7\n%%EOF\n"))
		link := filepath.Join(root, "link.pdf")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		_, report := acquireSnapshot(
			t.Context(), link, scratch, newReport("document_operation_symlink", DefaultMaxInputBytes), nil,
		)
		assertFailure(t, report, StateFailed, FailureInvalidInput)
	})

	t.Run("symlink parent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink setup requires extra Windows privileges")
		}
		realDir := filepath.Join(root, "real-directory")
		target := filepath.Join(realDir, "nested.pdf")
		writeFixture(t, target, []byte("%PDF-1.7\n%%EOF\n"))
		linkDir := filepath.Join(root, "linked-directory")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatalf("create parent symlink: %v", err)
		}
		_, report := acquireSnapshot(
			t.Context(), filepath.Join(linkDir, "nested.pdf"), scratch,
			newReport("document_operation_parent_symlink", DefaultMaxInputBytes), nil,
		)
		assertFailure(t, report, StateFailed, FailureInvalidInput)
	})

	t.Run("not pdf", func(t *testing.T) {
		input := filepath.Join(root, "plain.txt")
		writeFixture(t, input, []byte("not a PDF"))
		_, report := acquireSnapshot(
			t.Context(), input, scratch, newReport("document_operation_plain", DefaultMaxInputBytes), nil,
		)
		assertFailure(t, report, StateUnsupported, FailureUnsupportedType)
	})

	t.Run("oversized", func(t *testing.T) {
		input := filepath.Join(root, "large.pdf")
		writeFixture(t, input, []byte("%PDF-1.7\nlarge\n"))
		_, report := acquireSnapshot(t.Context(), input, scratch, newReport("document_operation_large", 4), nil)
		assertFailure(t, report, StateFailed, FailureLimitExceeded)
	})

	t.Run("canceled", func(t *testing.T) {
		input := filepath.Join(root, "cancel.pdf")
		writeFixture(t, input, []byte("%PDF-1.7\n%%EOF\n"))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, report := acquireSnapshot(ctx, input, scratch, newReport("document_operation_cancel", 1024), nil)
		assertFailure(t, report, StateCanceled, FailureCanceled)
	})
}

func TestAcquireSnapshotDetectsMidCopyMutation(t *testing.T) {
	root := directTempDir(t)
	input := filepath.Join(root, "mutable.pdf")
	writeFixture(t, input, []byte("%PDF-1.7\nversion-one\n"))
	report := newReport("document_operation_mutation", DefaultMaxInputBytes)

	_, got := acquireSnapshot(t.Context(), input, filepath.Join(root, "protected"), report, func() {
		writeFixture(t, input, []byte("%PDF-1.7\nversion-two\n"))
	})
	assertFailure(t, got, StateFailed, FailureSourceChanged)
}

func TestAcquireSnapshotDetectsSourceRemoval(t *testing.T) {
	root := directTempDir(t)
	input := filepath.Join(root, "removed.pdf")
	writeFixture(t, input, []byte("%PDF-1.7\nremoved-during-acquisition\n"))
	report := newReport("document_operation_removal", DefaultMaxInputBytes)

	_, got := acquireSnapshot(t.Context(), input, filepath.Join(root, "protected"), report, func() {
		if err := os.Remove(input); err != nil {
			t.Fatalf("remove source: %v", err)
		}
	})
	assertFailure(t, got, StateFailed, FailureSourceChanged)
}

func TestAcquireSnapshotSeparatesEqualNamesWithDifferentBytes(t *testing.T) {
	root := directTempDir(t)
	first := filepath.Join(root, "first", "same.pdf")
	second := filepath.Join(root, "second", "same.pdf")
	writeFixture(t, first, []byte("%PDF-1.7\nfirst\n"))
	writeFixture(t, second, []byte("%PDF-1.7\nsecond\n"))

	firstSnapshot, firstReport := acquireSnapshot(
		t.Context(), first, filepath.Join(root, "protected"), newReport("document_operation_first", 1024), nil,
	)
	if firstSnapshot == nil {
		t.Fatalf("first acquisition failed: %#v", firstReport)
	}
	t.Cleanup(func() {
		if err := firstSnapshot.Close(); err != nil {
			t.Errorf("close first snapshot: %v", err)
		}
	})
	secondSnapshot, secondReport := acquireSnapshot(
		t.Context(), second, filepath.Join(root, "protected"), newReport("document_operation_second", 1024), nil,
	)
	if secondSnapshot == nil {
		t.Fatalf("second acquisition failed: %#v", secondReport)
	}
	t.Cleanup(func() {
		if err := secondSnapshot.Close(); err != nil {
			t.Errorf("close second snapshot: %v", err)
		}
	})
	if firstReport.Input == nil || secondReport.Input == nil || firstReport.Input.SHA256 == secondReport.Input.SHA256 {
		t.Fatalf(
			"distinct bytes did not retain distinct identities: first=%#v failure=%#v second=%#v failure=%#v",
			firstReport,
			firstReport.Failure,
			secondReport,
			secondReport.Failure,
		)
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func directTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve test temp directory: %v", err)
	}
	return resolved
}

func assertFailure(t *testing.T, report Report, state State, code FailureCode) {
	t.Helper()
	if report.State != state || report.Failure == nil || report.Failure.Code != code || report.Input != nil {
		t.Fatalf("report = %#v, want state %q and code %q", report, state, code)
	}
}
