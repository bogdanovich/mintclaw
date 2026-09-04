//go:build linux || darwin

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

func TestWorkspaceReadMissingFileIsDurableNotFoundWithoutReplay(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	input := json.RawMessage(`{
		"profile_revision":"project-v1",
		"workspace_revision":"workspace-v1",
		"working_scope":"project",
		"path":"missing.txt",
		"start_line":1,
		"max_lines":10
	}`)
	plan := testRuntimePlan(t, runtime, nodes.WorkspaceCommandRead, input)
	if _, err := runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing workspace read error = %v", err)
	} else {
		var failure *commandFailureError
		if !errors.As(err, &failure) || failure.failure.Code != nodes.InvocationDispatchFileNotFound ||
			failure.failure.Message != "workspace file was not found" {
			t.Fatalf("missing workspace read failure = %#v, %v", failure, err)
		}
	}
	record, found, err := runtime.Invocation(plan.InvocationID)
	if err != nil || !found || record.State != nodes.InvocationFailed || record.Failure == nil ||
		record.Failure.Code != nodes.InvocationDispatchFileNotFound {
		t.Fatalf("durable missing workspace read = %#v, found %v, error %v", record, found, err)
	}
	if err := os.WriteFile(filepath.Join(root, "missing.txt"), []byte("created after failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Invoke(t.Context(), plan); err == nil {
		t.Fatal("duplicate missing-file invocation replayed after the file appeared")
	} else {
		var recorded *recordedInvocationError
		if !errors.As(err, &recorded) || recorded.failure.Code != nodes.InvocationDispatchFileNotFound {
			t.Fatalf("duplicate missing workspace read error = %T %v", err, err)
		}
	}
}

func TestWorkspaceReadTransportPreservesDuplicateFileNotFound(t *testing.T) {
	registry, admission := testGatewayAdmission(t)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	identity := testIdentity(t)
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	runtime.nodeID = identity.ID
	client := testRuntimeClientForServer(t, server, identity, runtime)
	authentication, err := client.Authenticate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Approve(authentication.NodeID, nodes.PairingApproval{
		AllowedCommands: []string{nodes.WorkspaceCommandRead},
		At:              time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runCtx) }()
	waitForNodeState(t, registry, identity.ID, nodes.StateConnected)

	registration, exists, err := registry.Registration(identity.ID)
	if err != nil || !exists {
		t.Fatalf("Registration() = exists %v, error %v", exists, err)
	}
	descriptor, err := registration.ApprovedCommand(nodes.WorkspaceCommandRead)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, available := nodes.ProjectFileDescriptorForProfile(descriptor, "project")
	if !available {
		t.Fatal("workspace read descriptor does not expose the project file profile")
	}
	input := json.RawMessage(`{
		"profile_revision":"project-v1",
		"workspace_revision":"workspace-v1",
		"working_scope":"project",
		"path":"transport-missing.txt",
		"start_line":1,
		"max_lines":10
	}`)
	plan, err := nodes.PrepareExecutionPlanForProtocol(
		registration.Snapshot.ProtocolVersion,
		nodes.InvocationRequest{
			InvocationID: "inv_workspace_not_found", IdempotencyKey: "idem_workspace_not_found",
			NodeID: identity.ID, CatalogHash: registration.Snapshot.CatalogHash,
			Command: descriptor.Name, Input: input,
			AgentID: "agent_test", SessionID: "session_test", ActorID: "actor_test",
			TimeoutSeconds: 5, OutputLimitBytes: 4096,
		},
		descriptor,
		registration.Snapshot.Executor,
		registration.Snapshot.PolicyRevision,
		time.Now(),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.policy.Authorize(plan, runtime.catalog, runtime.nodeID, LocalExecutor, time.Now()); err != nil {
		t.Fatalf("transport test plan is not authorized by the companion runtime: %v", err)
	}
	assertNotFound := func(label string) {
		t.Helper()
		_, dispatched, invokeErr := admission.Invoke(t.Context(), identity.ID, plan, nil, nil)
		code, classified := nodes.InvocationDispatchErrorCode(invokeErr)
		if !dispatched || !classified || code != nodes.InvocationDispatchFileNotFound {
			t.Fatalf("%s Invoke() = dispatched %v, code %q, classified %v, error %v",
				label, dispatched, code, classified, invokeErr)
		}
	}
	assertNotFound("initial")
	if err := os.WriteFile(
		filepath.Join(root, "transport-missing.txt"),
		[]byte("created after durable failure"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	assertNotFound("duplicate")

	cancelRun()
	if err := <-runDone; err != nil {
		t.Fatal(err)
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
		}`), OutputLimitBytes: nodes.MaxWorkspaceReadBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	read := result.(WorkspaceReadResult)
	if len(read.Content) > nodes.MaxWorkspaceReadBytes || !read.Truncated {
		t.Fatalf("workspace read content bytes = %d, truncated = %v", len(read.Content), read.Truncated)
	}
}

func TestWorkspaceReadBoundsFullyEncodedResult(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	const outputLimit = 64 * 1024
	content := strings.Repeat("\"\\\t", nodes.MaxWorkspaceReadBytes/3)
	if err := os.WriteFile(filepath.Join(root, "escaped.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	resultJSON, err := runtime.Invoke(
		t.Context(),
		testRuntimePlanAtWithOutputLimit(t, runtime, nodes.WorkspaceCommandRead, json.RawMessage(`{
			"profile_revision":"project-v1",
			"workspace_revision":"workspace-v1",
			"working_scope":"project",
			"path":"escaped.txt",
			"offset":0,
			"length":524288
		}`), time.Now(), time.Minute, outputLimit),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultJSON) > outputLimit {
		t.Fatalf("encoded workspace result bytes = %d", len(resultJSON))
	}
	var result map[string]any
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	boundedContent, _ := result["content"].(string)
	if result["truncated"] != true || len(boundedContent) >= len(content) {
		t.Fatalf("workspace escaped result = %#v", result)
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
		commandInvocation{Input: fileInput, OutputLimitBytes: nodes.MaxWorkspaceReadBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	files := fileResult.(WorkspaceSearchResult)
	if files.Matches != 1 || files.Result != "pkg/runtime.go" {
		t.Fatalf("workspace file search = %#v", files)
	}
}

func TestWorkspaceSearchHonorsRecursiveGitIgnoreAtRootAndNestedDepth(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	for path, content := range map[string]string{
		".gitignore": "**/.env\n/root-only.txt\nprivate/\n*.secret\n" +
			"!visible.secret\n\\!literal\n\\#hash\n",
		".env":                    "workspace-secret\n",
		"pkg/deep/.env":           "workspace-secret\n",
		"pkg/deep/visible":        "workspace-secret\n",
		"root-only.txt":           "workspace-secret\n",
		"pkg/deep/root-only.txt":  "workspace-secret\n",
		"private/hidden.txt":      "workspace-secret\n",
		"pkg/deep/hidden.secret":  "workspace-secret\n",
		"pkg/deep/visible.secret": "workspace-secret\n",
		"pkg/deep/!literal":       "workspace-secret\n",
		"pkg/deep/#hash":          "workspace-secret\n",
	} {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	search := func(includeIgnored bool, searchPath string) WorkspaceSearchResult {
		t.Helper()
		input, err := json.Marshal(map[string]any{
			"profile_revision": "project-v1", "workspace_revision": "workspace-v1",
			"working_scope": "project", "pattern": "workspace-secret", "target": "content",
			"output_mode": "files_only", "limit": 20, "include_ignored": includeIgnored,
			"path": searchPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runtime.handlers[nodes.WorkspaceCommandSearch].execute(
			t.Context(),
			commandInvocation{Input: input, OutputLimitBytes: nodes.MaxWorkspaceReadBytes},
		)
		if err != nil {
			t.Fatal(err)
		}
		return result.(WorkspaceSearchResult)
	}

	filtered := search(false, "")
	for _, path := range []string{"pkg/deep/visible", "pkg/deep/root-only.txt", "pkg/deep/visible.secret"} {
		if !strings.Contains(filtered.Result, path) {
			t.Fatalf("filtered recursive-ignore result missing %q: %#v", path, filtered)
		}
	}
	if filtered.Matches != 3 || strings.Contains(filtered.Result, ".env") ||
		strings.Contains(filtered.Result, "private/") || strings.Contains(filtered.Result, "hidden.secret") ||
		strings.Contains(filtered.Result, "!literal") || strings.Contains(filtered.Result, "#hash") {
		t.Fatalf("filtered recursive-ignore result = %#v", filtered)
	}
	unfiltered := search(true, "")
	if unfiltered.Matches != 10 || !strings.Contains(unfiltered.Result, ".env") ||
		!strings.Contains(unfiltered.Result, "pkg/deep/.env") {
		t.Fatalf("include-ignored recursive result = %#v", unfiltered)
	}
	if explicit := search(false, ".env"); explicit.Matches != 0 || explicit.FilesVisited != 0 {
		t.Fatalf("explicit ignored-file result = %#v", explicit)
	}
	if explicit := search(true, ".env"); explicit.Matches != 1 || explicit.Result != ".env" {
		t.Fatalf("explicit include-ignored result = %#v", explicit)
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

func TestWorkspaceSearchBoundsFullyEncodedResult(t *testing.T) {
	runtime, root := newWorkspaceReadSearchTestRuntime(t)
	line := "match\t\"\\" + strings.Repeat("x", 80) + "\n"
	if err := os.WriteFile(filepath.Join(root, "escaped.txt"), []byte(strings.Repeat(line, 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	resultJSON, err := runtime.Invoke(
		t.Context(),
		testRuntimePlan(t, runtime, nodes.WorkspaceCommandSearch, json.RawMessage(`{
			"profile_revision":"project-v1",
			"workspace_revision":"workspace-v1",
			"working_scope":"project",
			"pattern":"match",
			"output_mode":"content",
			"limit":100
		}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultJSON) > 4096 {
		t.Fatalf("encoded workspace search bytes = %d", len(resultJSON))
	}
	var result map[string]any
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result["truncated"] != true || result["result"] == "" {
		t.Fatalf("workspace encoded search result = %#v", result)
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
	policy := testRuntimePolicy([]string{
		nodes.WorkspaceCommandRead,
		nodes.WorkspaceCommandSearch,
		nodes.WorkspaceCommandWrite,
		nodes.WorkspaceCommandPatch,
	})
	policy.MaximumRisk = nodes.RiskWrite
	runtime, err := NewRuntime(
		"node_test", "test", policy,
		newMemoryInvocationLedger(), WithWorkspaceRead(router, execPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, root
}
