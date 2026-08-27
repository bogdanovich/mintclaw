package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestCodingInstructionsSelectOneFilePerDirectoryAndOrderByScope(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "pkg", "service")
	global := filepath.Join(root, "global")
	state := filepath.Join(root, "state")
	for _, directory := range []string{cwd, global} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCodingInstructionTestFile(t, filepath.Join(global, "AGENTS.md"), "global agents")
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), "root agents")
	writeCodingInstructionTestFile(t, filepath.Join(project, "CLAUDE.md"), "root claude must not merge")
	writeCodingInstructionTestFile(t, filepath.Join(cwd, "AGENTS.override.md"), "nested override")
	writeCodingInstructionTestFile(t, filepath.Join(cwd, "AGENTS.md"), "nested agents must not merge")
	writeCodingInstructionTestFile(t, filepath.Join(cwd, "CLAUDE.md"), "nested claude must not merge")

	loader := newCodingInstructionTestLoader(t, project, state, []string{global, project, cwd})
	bundle := loader.initial()
	if len(bundle.Documents) != 3 {
		t.Fatalf("documents = %#v, want global, root, and nested", bundle.Documents)
	}
	contents := []string{
		bundle.Documents[0].Content,
		bundle.Documents[1].Content,
		bundle.Documents[2].Content,
	}
	want := []string{"global agents", "root agents", "nested override"}
	for index := range want {
		if contents[index] != want[index] {
			t.Fatalf("document %d = %q, want %q", index, contents[index], want[index])
		}
	}
	rendered := renderCodingInstructionBundle(bundle, false)
	for _, forbidden := range []string{"root claude must not merge", "nested agents must not merge", "nested claude"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("same-directory fallback was merged (%q):\n%s", forbidden, rendered)
		}
	}
	wantCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if loader.workingDirectory() != wantCWD {
		t.Fatalf("working directory = %q, want %q", loader.workingDirectory(), wantCWD)
	}
}

func TestCodingInstructionsUseClaudeOnlyAsFallback(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "CLAUDE.md"), "root claude fallback")
	writeCodingInstructionTestFile(t, filepath.Join(nested, "AGENTS.md"), "nested agents")
	writeCodingInstructionTestFile(t, filepath.Join(nested, "CLAUDE.md"), "nested claude must not merge")

	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project, nested},
	)
	bundle := loader.initial()
	if len(bundle.Documents) != 2 || bundle.Documents[0].Label != "CLAUDE.md" ||
		bundle.Documents[1].Label != "AGENTS.md" {
		t.Fatalf("fallback selection = %#v", bundle.Documents)
	}
	if strings.Contains(renderCodingInstructionBundle(bundle, false), "nested claude must not merge") {
		t.Fatal("CLAUDE.md merged despite same-directory AGENTS.md")
	}
}

func TestCodingInstructionTurnStateScopesSiblingsAndDeduplicatesHistory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	for _, directory := range []string{filepath.Join(project, "a"), filepath.Join(project, "b")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "a", "AGENTS.md"), "rules only for a")
	writeCodingInstructionTestFile(t, filepath.Join(project, "b", "CLAUDE.md"), "rules only for b")
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	state := newCodingInstructionTurnState(loader, nil)

	aBundle, discovered := state.discover("read_file", map[string]any{"path": "a/file.go"})
	if !discovered || len(aBundle.Documents) != 1 || aBundle.Documents[0].Content != "rules only for a" {
		t.Fatalf("a discovery = %#v, %v", aBundle, discovered)
	}
	if _, discovered = state.discover("write_file", map[string]any{"path": "a/other.go"}); discovered {
		t.Fatal("same scoped instruction was delivered twice in one turn")
	}
	bBundle, discovered := state.discover("read_file", map[string]any{"path": "b/file.go"})
	if !discovered || len(bBundle.Documents) != 1 || bBundle.Documents[0].Content != "rules only for b" {
		t.Fatalf("b discovery = %#v, %v", bBundle, discovered)
	}
	if strings.Contains(renderCodingInstructionBundle(aBundle, true), "rules only for b") ||
		strings.Contains(renderCodingInstructionBundle(bBundle, true), "rules only for a") {
		t.Fatal("sibling instructions leaked across scopes")
	}

	history := []providers.Message{{Role: "tool", Content: renderCodingInstructionBundle(aBundle, true)}}
	resumed := newCodingInstructionTurnState(loader, history)
	if _, discovered = resumed.discover("read_file", map[string]any{"path": "a/file.go"}); discovered {
		t.Fatal("instruction marker in active history was not deduplicated")
	}
}

func TestCodingInstructionSearchLoadsOnlyTheTargetDirectoryChain(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	for _, subtree := range []string{"a", "b"} {
		directory := filepath.Join(project, subtree)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		writeCodingInstructionTestFile(t, filepath.Join(directory, "AGENTS.md"), "rules "+subtree)
	}
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	state := newCodingInstructionTurnState(loader, nil)
	bundle, discovered := state.discover("search_files", map[string]any{"path": ".", "pattern": "TODO"})
	if discovered || len(bundle.Documents) != 0 || len(bundle.Diagnostics) != 0 {
		t.Fatalf("root search loaded descendant instructions = %#v, %v", bundle, discovered)
	}

	bundle, discovered = state.discover("search_files", map[string]any{"path": "a", "pattern": "TODO"})
	if !discovered || len(bundle.Documents) != 1 || bundle.Documents[0].Content != "rules a" {
		t.Fatalf("targeted search discovery = %#v, %v", bundle, discovered)
	}
}

func TestCodingInstructionSearchFileUsesContainingScope(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(nested, "AGENTS.md"), "nested file rules")
	writeCodingInstructionTestFile(t, filepath.Join(nested, "file.go"), "package nested")
	loader := newCodingInstructionTestLoader(t, project, filepath.Join(root, "state"), []string{project})
	state := newCodingInstructionTurnState(loader, nil)

	bundle, discovered := state.discover("search_files", map[string]any{
		"path": "nested/file.go", "pattern": "package",
	})
	if !discovered || len(bundle.Documents) != 1 || bundle.Documents[0].Content != "nested file rules" {
		t.Fatalf("file search discovery = %#v, %v", bundle, discovered)
	}
}

func TestCodingInstructionTargetsResolveSymlinksAndKeepLogicalScopeIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	sibling := filepath.Join(project, "sibling")
	outside := filepath.Join(root, "outside")
	for _, directory := range []string{nested, sibling, outside} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(project, "shared.md")
	writeCodingInstructionTestFile(t, shared, "shared scoped rules")
	if err := os.Symlink(shared, filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(sibling, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(nested, "file.go"), "package nested")
	writeCodingInstructionTestFile(t, filepath.Join(outside, "file.go"), "package outside")
	if err := os.Symlink(filepath.Join(nested, "file.go"), filepath.Join(project, "linked.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "file.go"), filepath.Join(project, "outside.go")); err != nil {
		t.Fatal(err)
	}

	loader := newCodingInstructionTestLoader(t, project, filepath.Join(root, "state"), []string{project})
	state := newCodingInstructionTurnState(loader, nil)
	linked, discovered := state.discover("read_file", map[string]any{"path": "linked.go"})
	canonicalNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !discovered || len(linked.Documents) != 1 || linked.Documents[0].Scope != canonicalNested {
		t.Fatalf("symlink target discovery = %#v, %v", linked, discovered)
	}
	siblingBundle, discovered := state.discover("read_file", map[string]any{"path": "sibling/new.go"})
	if !discovered || len(siblingBundle.Documents) != 1 ||
		siblingBundle.Documents[0].Key == linked.Documents[0].Key {
		t.Fatalf("logical scope identity collapsed = %#v, %v", siblingBundle, discovered)
	}
	escape, discovered := state.discover("read_file", map[string]any{"path": "outside.go"})
	if !discovered || len(escape.Documents) != 0 || len(escape.Diagnostics) != 1 ||
		!strings.Contains(escape.Diagnostics[0].Message, "outside") {
		t.Fatalf("outside symlink discovery = %#v, %v", escape, discovered)
	}
}

func TestCodingInstructionsBoundBytesAndPreferSpecificScope(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), strings.Repeat("r", 20))
	writeCodingInstructionTestFile(t, filepath.Join(nested, "AGENTS.md"), strings.Repeat("n", 20))
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project, nested},
	)
	loader.maxFileBytes = 12
	loader.maxTotalBytes = 16
	bundle := loader.initial()
	wantNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Documents) == 0 || bundle.Documents[len(bundle.Documents)-1].Scope != wantNested {
		t.Fatalf("specific instruction was not retained: %#v", bundle.Documents)
	}
	contentBytes := 0
	for _, document := range bundle.Documents {
		contentBytes += len(document.Content)
	}
	if contentBytes > loader.maxTotalBytes {
		t.Fatalf("instruction bytes = %d, limit %d", contentBytes, loader.maxTotalBytes)
	}
	if len(bundle.Diagnostics) == 0 || !strings.Contains(renderCodingInstructionBundle(bundle, false), "truncated") {
		t.Fatalf("truncation was not reported: %#v", bundle)
	}
}

func TestCodingInstructionsRejectUnsafeAndUnreadableSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	outside := filepath.Join(root, "outside.md")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, outside, "outside rules")
	if err := os.Symlink(outside, filepath.Join(project, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), "must not bypass override")
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	bundle := loader.initial()
	if len(bundle.Documents) != 0 || len(bundle.Diagnostics) != 1 ||
		!strings.Contains(bundle.Diagnostics[0].Message, "outside") {
		t.Fatalf("unsafe override result = %#v", bundle)
	}

	if err := os.Remove(filepath.Join(project, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing.md"), filepath.Join(project, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	bundle = loader.initial()
	if len(bundle.Documents) != 0 || len(bundle.Diagnostics) != 1 {
		t.Fatalf("broken override result = %#v", bundle)
	}
}

func TestCodingInstructionCacheInvalidatesOnChange(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "AGENTS.md")
	writeCodingInstructionTestFile(t, path, "first")
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	first := loader.initial().Documents[0]
	writeCodingInstructionTestFile(t, path, "second version")
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	second := loader.initial().Documents[0]
	if second.Content != "second version" || second.Key == first.Key {
		t.Fatalf("cache did not invalidate: first=%#v second=%#v", first, second)
	}
}

func TestCodingInstructionSelectionInvalidatesOnOverrideCreateAndDelete(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(project, "AGENTS.md")
	overridePath := filepath.Join(project, "AGENTS.override.md")
	writeCodingInstructionTestFile(t, agentsPath, "base agents")
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	if got := loader.initial().Documents[0].Content; got != "base agents" {
		t.Fatalf("initial content = %q", got)
	}
	writeCodingInstructionTestFile(t, overridePath, "new override")
	if got := loader.initial().Documents[0].Content; got != "new override" {
		t.Fatalf("content after override creation = %q", got)
	}
	if err := os.Remove(overridePath); err != nil {
		t.Fatal(err)
	}
	if got := loader.initial().Documents[0].Content; got != "base agents" {
		t.Fatalf("content after override deletion = %q", got)
	}
}

func TestCodingPromptRefreshesGlobalRepositoryAndCWDInstructions(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "pkg")
	global := filepath.Join(root, "global")
	for _, directory := range []string{cwd, global} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCodingInstructionTestFile(t, filepath.Join(global, "CLAUDE.md"), "global fallback")
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), "repository rules")
	nestedPath := filepath.Join(cwd, "AGENTS.override.md")
	writeCodingInstructionTestFile(t, nestedPath, "cwd override v1")
	layout, err := NewCodingRuntimeLayout(
		"thread-prompt-instructions",
		project,
		filepath.Join(root, "state"),
		[]string{global, project, cwd},
	)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newCodingContextBuilder(layout)
	if err != nil {
		t.Fatal(err)
	}

	first := builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "inspect"})
	if len(first) != 2 || len(first[0].SystemParts) != 2 {
		t.Fatalf("prompt shape = %#v", first)
	}
	system := first[0].Content
	globalIndex := strings.Index(system, "global fallback")
	rootIndex := strings.Index(system, "repository rules")
	nestedIndex := strings.Index(system, "cwd override v1")
	if globalIndex < 0 || rootIndex <= globalIndex || nestedIndex <= rootIndex {
		t.Fatalf("instruction precedence order is wrong:\n%s", system)
	}
	if !strings.Contains(system, "Working directory: "+layout.InstructionRoots()[2]) {
		t.Fatalf("prompt omitted invocation cwd:\n%s", system)
	}

	writeCodingInstructionTestFile(t, nestedPath, "cwd override version two")
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(nestedPath, now, now); err != nil {
		t.Fatal(err)
	}
	second := builder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "continue"})
	if !strings.Contains(second[0].Content, "cwd override version two") ||
		strings.Contains(second[0].Content, "cwd override v1") {
		t.Fatalf("coding prompt retained stale instructions:\n%s", second[0].Content)
	}
}

func TestCodingInstructionTargetsCoverPathAwareCodingTools(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a/file.go\n@@\n-old\n+new\n*** Add File: b/file.go\n+new\n*** End Patch"
	tests := []struct {
		name      string
		tool      string
		arguments map[string]any
		want      []codingInstructionTarget
	}{
		{
			name:      "read",
			tool:      "read_file",
			arguments: map[string]any{"path": "a/file.go"},
			want:      []codingInstructionTarget{{Path: "a/file.go"}},
		},
		{
			name:      "list",
			tool:      "list_dir",
			arguments: map[string]any{"path": "a"},
			want:      []codingInstructionTarget{{Path: "a", Directory: true}},
		},
		{
			name:      "search",
			tool:      "search_files",
			arguments: map[string]any{"path": "a"},
			want:      []codingInstructionTarget{{Path: "a", Directory: true}},
		},
		{
			name:      "exec",
			tool:      "exec",
			arguments: map[string]any{"action": "run", "cwd": "a"},
			want:      []codingInstructionTarget{{Path: "a", Directory: true}},
		},
		{
			name:      "patch",
			tool:      "apply_patch",
			arguments: map[string]any{"input": patch},
			want:      []codingInstructionTarget{{Path: "a/file.go"}, {Path: "b/file.go"}},
		},
		{name: "exec poll ignored", tool: "exec", arguments: map[string]any{"action": "poll"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := codingInstructionTargets(test.tool, test.arguments)
			if len(got) != len(test.want) {
				t.Fatalf("targets = %#v, want %#v", got, test.want)
			}
			for index := range test.want {
				if got[index] != test.want[index] {
					t.Fatalf("target %d = %#v, want %#v", index, got[index], test.want[index])
				}
			}
		})
	}
}

func TestCodingInstructionArgumentsResolveFromInvocationCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "pkg")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	loader := newCodingInstructionTestLoader(
		t,
		project,
		filepath.Join(root, "state"),
		[]string{project, cwd},
	)
	state := newCodingInstructionTurnState(loader, nil)

	writeArgs := state.normalizeArguments("write_file", map[string]any{"path": "nested/out.txt"})
	canonicalCWD := loader.workingDirectory()
	if got := writeArgs["path"]; got != filepath.Join(canonicalCWD, "nested", "out.txt") {
		t.Fatalf("write path = %v", got)
	}
	execArgs := state.normalizeArguments("exec", map[string]any{"action": "run", "command": "pwd"})
	if got := execArgs["cwd"]; got != canonicalCWD {
		t.Fatalf("exec cwd = %v, want %s", got, canonicalCWD)
	}
	patchArgs := state.normalizeArguments("apply_patch", map[string]any{
		"input": "*** Begin Patch\n*** Add File: nested/out.txt\n+done\n*** End Patch",
	})
	if got, _ := patchArgs["input"].(string); !strings.Contains(
		got,
		"*** Add File: "+filepath.Join(canonicalCWD, "nested", "out.txt"),
	) {
		t.Fatalf("normalized patch = %q", got)
	}
}

func TestCodingExecDefaultsToInvocationCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "pkg")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	layout, err := NewCodingRuntimeLayout(
		"thread-exec-cwd",
		project,
		filepath.Join(root, "state"),
		[]string{project, cwd},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewCodingAgentLoop(t.Context(), cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)
	execTool, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("exec")
	if !ok {
		t.Fatal("coding exec tool is missing")
	}
	ctx := toolshared.WithToolInboundContext(context.Background(), "cli", "thread-exec-cwd", "", "")
	result := execTool.Execute(ctx, map[string]any{"action": "run", "command": "pwd"})
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.ContentForLLM(), canonicalCWD) {
		t.Fatalf("exec result = %#v, want cwd %s", result, canonicalCWD)
	}
}

func TestCodingInstructionBarrierDefersWriteUntilModelReviewsNestedScope(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "pkg")
	nested := filepath.Join(cwd, "nested")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), "root rules")
	writeCodingInstructionTestFile(t, filepath.Join(nested, "CLAUDE.md"), "nested fallback rules")
	canonicalNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(canonicalNested, "result.txt")
	relativeTarget := filepath.Join("nested", "result.txt")

	provider := llmscenario.NewScriptedProvider(
		"coding-instruction-model",
		llmscenario.ProviderStep{
			Name: "request nested write",
			Assert: func(call llmscenario.ProviderCall) error {
				if len(call.Messages) == 0 || !strings.Contains(call.Messages[0].Content, "root rules") ||
					strings.Contains(call.Messages[0].Content, "nested fallback rules") {
					return fmt.Errorf("initial instructions = %#v", call.Messages)
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I will write the file.",
				llmscenario.ToolCall("write-1", "write_file", map[string]any{
					"path": relativeTarget, "content": "done",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "review nested instructions before retry",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", "nested fallback rules")(call); err != nil {
					return err
				}
				if _, err := os.Stat(target); err == nil {
					return fmt.Errorf("write ran before instruction review: %s exists", target)
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("inspect deferred write: %w", err)
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I reviewed the scoped rules and will retry.",
				llmscenario.ToolCall("write-2", "write_file", map[string]any{
					"path": relativeTarget, "content": "done",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "finish after write",
			Assert: func(call llmscenario.ProviderCall) error {
				return llmscenario.RequireLastMessage("tool", "File written")(call)
			},
			Response: llmscenario.TextResponse("done"),
		},
	)
	layout, err := NewCodingRuntimeLayout(
		"thread-barrier",
		project,
		stateRoot,
		[]string{project, cwd},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.Provider = "test-provider"
	cfg.Agents.Defaults.ModelName = "coding-instruction-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	loop, err := NewCodingAgentLoop(t.Context(), cfg, bus.NewMessageBus(), provider, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)

	response, err := loop.ProcessDirect(context.Background(), "write nested result", "coding:thread-barrier")
	if err != nil {
		t.Fatalf("ProcessDirect() error = %v", err)
	}
	if response != "done" {
		t.Fatalf("response = %q, want done", response)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "done" {
		t.Fatalf("written file = %q, %v", data, err)
	}
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
}

func newCodingInstructionTestLoader(
	t *testing.T,
	project string,
	state string,
	instructionRoots []string,
) *codingInstructionLoader {
	t.Helper()
	layout, err := NewCodingRuntimeLayout(
		"thread-instructions",
		project,
		state,
		instructionRoots,
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	return newCodingInstructionLoader(layout)
}

func writeCodingInstructionTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
