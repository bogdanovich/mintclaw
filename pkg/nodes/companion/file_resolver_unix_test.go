//go:build linux || darwin

package companion

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFileResolverPinsSourceDescriptorAcrossRename(t *testing.T) {
	rootPath := canonicalTempDir(t)
	original := filepath.Join(rootPath, "source.txt")
	if err := os.WriteFile(original, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	source, err := root.openRegular(original, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.file.Close() }()
	moved := filepath.Join(rootPath, "moved.txt")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source.file.Name())
	if err == nil {
		t.Fatalf("descriptor name unexpectedly resolved after rename: %q", data)
	}
	if _, err := source.file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 7)
	if _, err := source.file.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "trusted" {
		t.Fatalf("pinned descriptor read = %q", buffer)
	}
}

func TestFileResolverRejectsTraversalSymlinksAndSpecialFiles(t *testing.T) {
	rootPath := canonicalTempDir(t)
	regular := filepath.Join(rootPath, "regular.txt")
	if err := os.WriteFile(regular, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(canonicalTempDir(t), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Dir(outside),
		filepath.Join(rootPath, "nested", "escape"),
	); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(rootPath, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	tests := []string{
		"",
		rootPath,
		rootPath + string(filepath.Separator),
		filepath.Join(rootPath, "line\nbreak"),
		filepath.Join(rootPath, "..", filepath.Base(outside)),
		filepath.Join(rootPath, "linked.txt"),
		filepath.Join(rootPath, "nested", "escape", filepath.Base(outside)),
		fifo,
	}
	for _, path := range tests {
		if opened, openErr := root.openRegular(path, 1024, false); openErr == nil {
			_ = opened.file.Close()
			t.Errorf("openRegular(%q) unexpectedly succeeded", path)
		}
	}
}

func TestFileResolverBindsDestinationParentAcrossRename(t *testing.T) {
	rootPath := canonicalTempDir(t)
	originalParent := filepath.Join(rootPath, "project")
	if err := os.Mkdir(originalParent, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	destination := filepath.Join(originalParent, "config.txt")
	parent, err := root.resolveParent(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	stage, err := parent.createStage("transfer_parent_race")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.file.Write([]byte("trusted")); err != nil {
		t.Fatal(err)
	}
	movedParent := filepath.Join(rootPath, "moved")
	if err := os.Rename(originalParent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(originalParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stage.publish(filePublicationCreate); err != nil {
		t.Fatal(err)
	}
	_ = stage.file.Close()
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement path was mutated: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(movedParent, "config.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "trusted" {
		t.Fatalf("descriptor-bound publication = %q", data)
	}
}

func TestFileResolverCreateAndReplaceAreExplicit(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })

	createParent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	createStage, err := createParent.createStage("transfer_create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createStage.file.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := createStage.publish(filePublicationCreate); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("create publication error = %v", err)
	}
	_ = createStage.file.Close()
	if err := createParent.removeStage(createStage.identity, createStage.name); err != nil {
		t.Fatal(err)
	}
	_ = createParent.close()

	replaceParent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	replaceStage, err := replaceParent.createStage("transfer_replace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replaceStage.file.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := replaceStage.publish(filePublicationReplace); err != nil {
		t.Fatal(err)
	}
	finalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	finalIdentity, err := identityFromInfo(finalInfo)
	if err != nil {
		t.Fatal(err)
	}
	if finalIdentity.Device != replaceStage.identity.Device ||
		finalIdentity.Inode != replaceStage.identity.Inode {
		t.Fatalf(
			"published identity = %#v, staged %#v",
			finalIdentity,
			replaceStage.identity,
		)
	}
	_ = replaceStage.file.Close()
	_ = replaceParent.close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("replace publication = %q", data)
	}
}

func TestFileResolverExpectedReplaceRestoresConcurrentIdentity(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.close() })
	expected, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	_ = expected.file.Close()
	stage, err := parent.createStage("expected_replace")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stage.file.Close() })
	if _, err := stage.file.Write([]byte("published")); err != nil {
		t.Fatal(err)
	}
	concurrent := filepath.Join(rootPath, "concurrent.txt")
	if err := os.WriteFile(concurrent, []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(concurrent, path); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("expected"))
	if err := stage.publishReplacing(
		t.Context(),
		expected.identity,
		int64(len("expected")),
		expectedDigest,
	); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("expected replace error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "concurrent" {
		t.Fatalf("restored concurrent file = %q, err = %v", content, err)
	}
}

func TestFileResolverExpectedReplaceRestoresConcurrentContent(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.close() })
	expected, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	_ = expected.file.Close()
	stage, err := parent.createStage("expected_content")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stage.file.Close() })
	if _, err := stage.file.Write([]byte("published")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("expected"))
	if err := stage.publishReplacing(
		t.Context(),
		expected.identity,
		int64(len("expected")),
		expectedDigest,
	); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("expected replace error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "changed" {
		t.Fatalf("restored changed file = %q, err = %v", content, err)
	}
}

func TestFileResolverExpectedReplaceRestoresOnCancellation(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.close() })
	expected, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	_ = expected.file.Close()
	stage, err := parent.createStage("expected_cancel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stage.file.Close() })
	if _, err := stage.file.Write([]byte("published")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	expectedDigest := sha256.Sum256([]byte("expected"))
	if err := stage.publishReplacing(
		ctx,
		expected.identity,
		int64(len("expected")),
		expectedDigest,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "expected" {
		t.Fatalf("restored expected file = %q, err = %v", content, err)
	}
}

func TestFileResolverDeleteRejectsAndRestoresReplacedIdentity(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	original, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	expected := original.identity
	if err := original.file.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(rootPath, "replacement.txt")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("original"))
	if err := parent.removeFinalRegular(
		t.Context(),
		expected,
		int64(len("original")),
		expectedDigest,
	); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("remove replaced identity error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Fatalf("restored replacement = %q", data)
	}
	staged, err := os.ReadDir(filepath.Join(rootPath, fileStageDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("staged entries after restoration = %v", staged)
	}
}

func TestFileResolverDeleteRestoresNonRegularReplacement(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	original, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	expected := original.identity
	if err := original.file.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(rootPath, "replacement.txt")
	if err := os.Symlink("outside.txt", replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("original"))
	if err := parent.removeFinalRegular(
		t.Context(),
		expected,
		int64(len("original")),
		expectedDigest,
	); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("remove non-regular identity error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("restored mode = %v", info.Mode())
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if target != "outside.txt" {
		t.Fatalf("restored symlink target = %q", target)
	}
}

func TestFileResolverDeleteRestoresConcurrentContentAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		prepare     func(*testing.T, string) context.Context
		wantContent string
		wantErr     error
	}{
		{
			name: "changed_content",
			prepare: func(t *testing.T, path string) context.Context {
				t.Helper()
				if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
				return t.Context()
			},
			wantContent: "changed",
			wantErr:     ErrFileConflict,
		},
		{
			name: "canceled",
			prepare: func(t *testing.T, _ string) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			wantContent: "original",
			wantErr:     context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath := canonicalTempDir(t)
			path := filepath.Join(rootPath, "config.txt")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			root, err := openFileRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.close() })
			parent, err := root.resolveParent(path, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parent.close() })
			original, err := parent.openFinalRegular()
			if err != nil {
				t.Fatal(err)
			}
			expected := original.identity
			_ = original.file.Close()
			ctx := test.prepare(t, path)
			digest := sha256.Sum256([]byte("original"))
			err = parent.removeFinalRegular(ctx, expected, int64(len("original")), digest)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("remove error = %v, want %v", err, test.wantErr)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil || string(content) != test.wantContent {
				t.Fatalf("restored content = %q, err = %v", content, readErr)
			}
		})
	}
}

func TestFileResolverDeleteRemovesExactIdentity(t *testing.T) {
	rootPath := canonicalTempDir(t)
	path := filepath.Join(rootPath, "config.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.close() })
	parent, err := root.resolveParent(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	original, err := parent.openFinalRegular()
	if err != nil {
		t.Fatal(err)
	}
	expected := original.identity
	if err := original.file.Close(); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("original"))
	if err := parent.removeFinalRegular(
		t.Context(),
		expected,
		int64(len("original")),
		expectedDigest,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted path stat error = %v", err)
	}
	staged, err := os.ReadDir(filepath.Join(rootPath, fileStageDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("staged entries after delete = %v", staged)
	}
}

func TestFileResolverRejectsUnprotectedStageParent(t *testing.T) {
	rootPath := canonicalTempDir(t)
	shared := filepath.Join(rootPath, "shared")
	if err := os.Mkdir(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	root, err := openFileRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.close() }()
	parent, err := root.resolveParent(
		filepath.Join(shared, "destination.txt"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	if stage, stageErr := parent.createStage(
		"unprotected_parent",
	); !errors.Is(stageErr, ErrFileAccessDenied) {
		if stage != nil && stage.file != nil {
			_ = stage.file.Close()
		}
		t.Fatalf("group-writable staging parent error = %v", stageErr)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	temporary := t.TempDir()
	if err := os.Chmod(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
