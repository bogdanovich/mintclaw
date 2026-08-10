//go:build linux || darwin

package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/patchformat"
)

func TestWorkspaceWriteCreatesAndConditionallyReplaces(t *testing.T) {
	runtime, root, _ := newTestFileTransferRuntime(t)
	created, err := runtime.WriteWorkspace(t.Context(), "project-v1", root, WorkspaceWriteOptions{
		Path: "created.txt", Content: "first\n",
	})
	if err != nil || created.Action != "create" {
		t.Fatalf("create = %#v, err = %v", created, err)
	}
	firstDigest := sha256.Sum256([]byte("first\n"))
	replaced, err := runtime.WriteWorkspace(t.Context(), "project-v1", root, WorkspaceWriteOptions{
		Path: "created.txt", Content: "second\n", Overwrite: true,
		ExpectedSHA256: hex.EncodeToString(firstDigest[:]),
	})
	if err != nil || replaced.Action != "replace" {
		t.Fatalf("replace = %#v, err = %v", replaced, err)
	}
	if content, readErr := os.ReadFile(
		filepath.Join(root, "created.txt"),
	); readErr != nil ||
		string(content) != "second\n" {
		t.Fatalf("content = %q, err = %v", content, readErr)
	}
	if _, err := runtime.WriteWorkspace(t.Context(), "project-v1", root, WorkspaceWriteOptions{
		Path: "created.txt", Content: "stale\n", Overwrite: true,
		ExpectedSHA256: hex.EncodeToString(firstDigest[:]),
	}); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("stale replace error = %v", err)
	}
}

func TestWorkspacePatchPreparesThenPublishesDeterministically(t *testing.T) {
	runtime, root, _ := newTestFileTransferRuntime(t)
	for path, content := range map[string]string{"changed.txt": "old\n", "removed.txt": "gone\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := runtime.PatchWorkspace(t.Context(), "project-v1", root, WorkspacePatchOptions{Input: `*** Begin Patch
*** Delete File: removed.txt
*** Update File: changed.txt
@@
-old
+new
*** Add File: added.txt
+added
*** End Patch`})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || len(result.Committed) != 3 ||
		result.Committed[0].Path != "added.txt" || result.Committed[1].Path != "changed.txt" ||
		result.Committed[2].Path != "removed.txt" {
		t.Fatalf("patch result = %#v", result)
	}
	for path, expected := range map[string]string{"added.txt": "added\n", "changed.txt": "new\n"} {
		content, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || string(content) != expected {
			t.Fatalf("%s = %q, err = %v", path, content, readErr)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "removed.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed file stat error = %v", err)
	}
}

func TestWorkspacePatchUpdateAndDeleteUseWritableAuthority(t *testing.T) {
	root := canonicalTempDir(t)
	for path, content := range map[string]string{"changed.txt": "old\n", "removed.txt": "gone\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policies := testFilePolicies(t, root)
	policy := policies["project"]
	policy.ReadableRoots = nil
	policies["project"] = policy
	runtime, err := NewFileTransferRuntime(policies, newMemoryFileTransferLedger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	result, err := runtime.PatchWorkspace(t.Context(), "project-v1", root, WorkspacePatchOptions{Input: `*** Begin Patch
*** Update File: changed.txt
@@
-old
+new
*** Delete File: removed.txt
*** End Patch`})
	if err != nil || result.State != "completed" || len(result.Committed) != 2 {
		t.Fatalf("writable-only patch = %#v, err = %v", result, err)
	}
	if content, readErr := os.ReadFile(
		filepath.Join(root, "changed.txt"),
	); readErr != nil ||
		string(content) != "new\n" {
		t.Fatalf("updated content = %q, err = %v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "removed.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("deleted file stat error = %v", statErr)
	}
}

func TestWorkspacePatchDoesNotPublishWhenPreparationFails(t *testing.T) {
	runtime, root, _ := newTestFileTransferRuntime(t)
	_, err := runtime.PatchWorkspace(t.Context(), "project-v1", root, WorkspacePatchOptions{Input: `*** Begin Patch
*** Add File: would-have-existed.txt
+content
*** Update File: missing.txt
@@
-old
+new
*** End Patch`})
	if err == nil {
		t.Fatal("invalid patch passed")
	}
	if _, statErr := os.Stat(filepath.Join(root, "would-have-existed.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preparation failure published a prefix: %v", statErr)
	}
}

func TestWorkspacePatchReportsCommittedPrefixAfterLaterConflict(t *testing.T) {
	runtime, root, _ := newTestFileTransferRuntime(t)
	for _, path := range []string{"a.txt", "z.txt"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	operations := []patchformat.Operation{
		{Kind: patchformat.Update, Path: "a.txt", Lines: []string{"@@", "-old", "+new"}},
		{Kind: patchformat.Update, Path: "z.txt", Lines: []string{"@@", "-old", "+new"}},
	}
	prepared, err := runtime.prepareWorkspacePatch(t.Context(), "project-v1", root, operations)
	if err != nil {
		t.Fatal(err)
	}
	defer closePreparedWorkspaceMutations(prepared)
	if err := os.WriteFile(filepath.Join(root, "z.txt"), []byte("concurrent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := publishPreparedWorkspacePatch(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "partial" || result.Code != "FILE_CONFLICT" || len(result.Committed) != 1 ||
		result.Committed[0].Path != "a.txt" {
		t.Fatalf("partial result = %#v", result)
	}
	for path, expected := range map[string]string{"a.txt": "new\n", "z.txt": "concurrent\n"} {
		content, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || string(content) != expected {
			t.Fatalf("%s = %q, err = %v", path, content, readErr)
		}
	}
}

func TestStoppedWorkspacePatchPreservesTimeoutAndCancellationTruth(t *testing.T) {
	committed := WorkspacePatchResult{Committed: []WorkspacePatchEntry{{Path: "done.txt", Action: "add"}}}
	deadlineCtx, deadlineCancel := context.WithTimeout(t.Context(), 0)
	defer deadlineCancel()
	deadlineResult, err := stoppedWorkspacePatch(committed, deadlineCtx)
	if err != nil || deadlineResult.State != "partial" || deadlineResult.Code != "TIMEOUT" {
		t.Fatalf("deadline result = %#v, err = %v", deadlineResult, err)
	}
	cancelCtx, cancel := context.WithCancelCause(t.Context())
	cancel(errCancellationRequested)
	cancelResult, err := stoppedWorkspacePatch(committed, cancelCtx)
	if err != nil || cancelResult.State != "partial" || cancelResult.Code != "CANCELED" {
		t.Fatalf("cancellation result = %#v, err = %v", cancelResult, err)
	}
}

func TestWorkspaceMutationRejectsTraversalPolicyAndCancellation(t *testing.T) {
	runtime, root, _ := newTestFileTransferRuntime(t)
	for _, options := range []WorkspaceWriteOptions{
		{Path: "../escape", Content: "no"},
		{Path: "new.txt", Content: "no", ExpectedSHA256: string(make([]byte, 64))},
	} {
		if _, err := runtime.WriteWorkspace(t.Context(), "project-v1", root, options); err == nil {
			t.Fatalf("write passed: %#v", options)
		}
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errCancellationRequested)
	_, err := runtime.WriteWorkspace(
		ctx,
		"project-v1",
		root,
		WorkspaceWriteOptions{Path: "canceled.txt", Content: "no"},
	)
	if !errors.Is(err, errCommandCancellationConfirmed) {
		t.Fatalf("canceled write error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "canceled.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled write published: %v", statErr)
	}
	deadlineCtx, deadlineCancel := context.WithCancel(t.Context())
	deadlineCancel()
	_, err = runtime.WriteWorkspace(
		deadlineCtx,
		"project-v1",
		root,
		WorkspaceWriteOptions{Path: "timed-out.txt", Content: "no"},
	)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errCommandCancellationConfirmed) {
		t.Fatalf("deadline-like cancellation error = %v", err)
	}
}

func TestWorkspaceMutationRejectsOutputAndInputBoundsBeforeEffect(t *testing.T) {
	runtime, root := newWorkspaceMutationTestRuntime(t)
	_, err := runtime.handlers[nodes.WorkspaceCommandWrite].execute(t.Context(), commandInvocation{
		Input: json.RawMessage(`{
			"profile_revision":"project-v1","workspace_revision":"workspace-v1",
			"working_scope":"project","path":"bounded.txt","content":"new","overwrite":false
		}`),
		OutputLimitBytes: 8,
	})
	if err == nil {
		t.Fatal("tiny output budget passed")
	}
	if _, statErr := os.Stat(filepath.Join(root, "bounded.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output-bound rejection published a file: %v", statErr)
	}
	fileRuntime := runtime.handlers[nodes.WorkspaceCommandPatch].(workspacePatchHandler).runtime.files.
		workspace["project-v1"].(*FileTransferRuntime)
	if _, err := fileRuntime.PatchWorkspace(t.Context(), "project-v1", root, WorkspacePatchOptions{
		Input: strings.Repeat("x", nodes.MaxWorkspacePatchBytes+1),
	}); !errors.Is(err, ErrFileAccessDenied) {
		t.Fatalf("oversized patch error = %v", err)
	}
}

func TestWorkspaceWriteInvocationReusesDurableLedgerResult(t *testing.T) {
	runtime, root := newWorkspaceMutationTestRuntime(t)
	input := []byte(`{"profile_revision":"project-v1","workspace_revision":"workspace-v1",` +
		`"working_scope":"project","path":"once.txt","content":"once\n","overwrite":false}`)
	plan := testRuntimePlan(t, runtime, nodes.WorkspaceCommandWrite, input)
	first, err := runtime.Invoke(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Invoke(t.Context(), plan)
	if err != nil || string(second) != string(first) {
		t.Fatalf("duplicate result = %s, first = %s, err = %v", second, first, err)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, "once.txt")); readErr != nil || string(content) != "once\n" {
		t.Fatalf("durable write content = %q, err = %v", content, readErr)
	}
}

func newWorkspaceMutationTestRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	return runtime, root
}
