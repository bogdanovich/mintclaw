//go:build linux || darwin

package companion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestWorkspaceReadUsesConfiguredScopeAndFileProfile(t *testing.T) {
	root := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileRuntime, err := NewFileTransferRuntime(testFilePolicies(t, root), newMemoryFileTransferLedger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fileRuntime.Close)
	fileRouter, err := NewFileTransferRouter(fileRuntime)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	execPolicy, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root}, Executables: []string{executable},
		Discovery: &SystemExecDiscovery{
			WorkingScopeAliases: map[string]string{"project": root},
			ExecutableAliases:   map[string]string{"test": executable},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	policy := testRuntimePolicy([]string{nodes.WorkspaceCommandRead})
	runtime, err := NewRuntime(
		"node_test",
		"test",
		policy,
		newMemoryInvocationLedger(),
		WithWorkspaceRead(fileRouter, execPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{
		"profile_revision":"project-v1",
		"workspace_revision":"workspace-v1",
		"working_scope":"project",
		"path":"hello.txt",
		"start_line":2,
		"max_lines":1
	}`)
	resultJSON, err := runtime.Invoke(t.Context(), testRuntimePlan(t, runtime, nodes.WorkspaceCommandRead, input))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result["path"] != "hello.txt" || result["content"] != "2|second" || result["size"] != float64(19) ||
		len(result["sha256"].(string)) != 64 || result["truncated"] != true {
		t.Fatalf("workspace read result = %#v", result)
	}
}

func TestWorkspaceReadRejectsEscapeAndWrongProfile(t *testing.T) {
	runtime := newWorkspaceReadTestRuntime(t)
	for _, input := range []string{
		`{"profile_revision":"project-v1","workspace_revision":"v1","working_scope":"project","path":"../secret"}`,
		`{"profile_revision":"other-v1","workspace_revision":"v1","working_scope":"project","path":"hello.txt"}`,
		`{"profile_revision":"project-v1","workspace_revision":"v1","working_scope":"other","path":"hello.txt"}`,
	} {
		handler := runtime.handlers[nodes.WorkspaceCommandRead]
		_, err := handler.execute(t.Context(), commandInvocation{Input: json.RawMessage(input)})
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("Invoke(%s) error = %v", input, err)
		}
	}
}

func TestWorkspaceReadLineModeBoundsRenderedOutput(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	longLine := strings.Repeat("x", nodes.MaxWorkspaceReadBytes)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(longLine+"\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.handlers[nodes.WorkspaceCommandRead].execute(
		t.Context(),
		commandInvocation{Input: json.RawMessage(`{
			"profile_revision":"project-v1",
			"workspace_revision":"workspace-v1",
			"working_scope":"project",
			"path":"large.txt",
			"start_line":1,
			"max_lines":2
		}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	read := result.(WorkspaceReadResult)
	if len(read.Content) > nodes.MaxWorkspaceReadBytes || !read.Truncated {
		t.Fatalf("workspace read content bytes = %d, truncated = %v", len(read.Content), read.Truncated)
	}
}

func TestWorkspaceSearchIsBoundedAndRespectsIgnoreFiles(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		".gitignore":     "ignored.txt\n",
		"README.md":      "MintClaw remote workspace\n",
		"ignored.txt":    "MintClaw secret\n",
		"pkg/runtime.go": "package pkg\n// MintClaw search hit\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input := json.RawMessage(`{
		"profile_revision":"project-v1",
		"workspace_revision":"workspace-v1",
		"working_scope":"project",
		"pattern":"MintClaw",
		"target":"content",
		"output_mode":"content",
		"limit":10
	}`)
	resultJSON, err := runtime.Invoke(t.Context(), testRuntimePlan(t, runtime, nodes.WorkspaceCommandSearch, input))
	if err != nil {
		t.Fatal(err)
	}
	var result WorkspaceSearchResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.Matches != 2 || result.FilesVisited != 4 || result.Truncated ||
		!strings.Contains(result.Result, "README.md:1:") ||
		!strings.Contains(result.Result, "pkg/runtime.go:2:") ||
		strings.Contains(result.Result, "ignored.txt") {
		t.Fatalf("workspace search result = %#v", result)
	}
	fileInput := json.RawMessage(`{
		"profile_revision":"project-v1",
		"workspace_revision":"workspace-v1",
		"working_scope":"project",
		"pattern":"*.go",
		"target":"files",
		"limit":10
	}`)
	fileResult, err := runtime.handlers[nodes.WorkspaceCommandSearch].execute(
		t.Context(),
		commandInvocation{Input: fileInput},
	)
	if err != nil {
		t.Fatal(err)
	}
	files := fileResult.(WorkspaceSearchResult)
	if files.Matches != 1 || files.Result != "pkg/runtime.go" {
		t.Fatalf("workspace file search = %#v", files)
	}
}

func TestWorkspaceSearchCountModeEmitsEntryAtLimit(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("match\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resultJSON, err := runtime.Invoke(
		t.Context(),
		testRuntimePlan(t, runtime, nodes.WorkspaceCommandSearch, json.RawMessage(`{
			"profile_revision":"project-v1",
			"workspace_revision":"workspace-v1",
			"working_scope":"project",
			"pattern":"match",
			"output_mode":"count",
			"limit":1
		}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var result WorkspaceSearchResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.Matches != 1 || result.Result != "a.txt:1" || !result.Truncated {
		t.Fatalf("workspace count result = %#v", result)
	}
}

func TestWorkspaceSearchChargesFilteredEntriesToGlobalBudget(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	for _, name := range []string{"a.skip", "b.skip"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("ignored\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := runtime.handlers[nodes.WorkspaceCommandSearch].(workspaceSearchHandler)
	capability := handler.runtime.files.workspace["project-v1"].(*FileTransferRuntime)
	profile := capability.profiles["project-v1"]
	state := &workspaceSearchState{
		ctx: t.Context(), profile: profile, root: profile.workspaceReadableRoot(root), workspace: root,
		options: WorkspaceSearchOptions{
			Pattern: "never", Target: "content", FileGlob: "*.go", OutputMode: "content", Limit: 100,
		},
		regex: regexp.MustCompile("never"), examined: nodes.MaxWorkspaceSearchFiles - 1,
	}
	if err := state.walk(root, nil, 0); err != nil {
		t.Fatal(err)
	}
	if state.examined != nodes.MaxWorkspaceSearchFiles || !state.truncated || state.visited != 0 {
		t.Fatalf("examined = %d, visited = %d, truncated = %v", state.examined, state.visited, state.truncated)
	}
}

func newWorkspaceReadTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	runtime, _ := newWorkspaceReadSearchTestRuntime(t)
	return runtime
}

func newWorkspaceReadSearchTestRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	root := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileRuntime, err := NewFileTransferRuntime(testFilePolicies(t, root), newMemoryFileTransferLedger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fileRuntime.Close)
	router, err := NewFileTransferRouter(fileRuntime)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	execPolicy, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root}, Executables: []string{executable},
		Discovery: &SystemExecDiscovery{
			WorkingScopeAliases: map[string]string{"project": root},
			ExecutableAliases:   map[string]string{"test": executable},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(
		"node_test", "test", testRuntimePolicy([]string{
			nodes.WorkspaceCommandRead,
			nodes.WorkspaceCommandSearch,
		}),
		newMemoryInvocationLedger(), WithWorkspaceRead(router, execPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, root
}
