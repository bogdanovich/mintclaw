package evalfixture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	toolfs "github.com/bogdanovich/mintclaw/pkg/tools/fs"
)

func approvalRequest(workspace string) *agent.ToolApprovalRequest {
	return &agent.ToolApprovalRequest{
		Meta: agent.HookMeta{
			TraceScope: runtimeevents.NewTraceScope(workspace, "turn-1"),
			AgentID:    "eval", SessionKey: "session-1", ToolCallID: "call-1",
		},
		Tool: "write_file", Arguments: map[string]any{
			"path": ApprovalPath, "content": ApprovalAfter, "overwrite": true,
		},
	}
}

func TestApprovalFixtureUsesNativeHookManagerWithoutGranting(t *testing.T) {
	workspace := t.TempDir()
	manager := agent.NewHookManager(nil)
	t.Cleanup(manager.Close)
	if err := manager.Mount(agent.NamedHook(ApprovalHookName, &approvalHook{workspace: workspace})); err != nil {
		t.Fatal(err)
	}
	request := approvalRequest(workspace)
	decision := manager.ApproveTool(t.Context(), request)
	if decision.Approved || !decision.RequireHuman || decision.TimeoutSeconds != 120 || decision.ActionSummary == "" {
		t.Fatalf("native approval decision = %#v", decision)
	}
	if err := os.Mkdir(filepath.Join(workspace, "eval-fixtures"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ApprovalPath)
	if err := os.WriteFile(path, []byte(ApprovalBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	// This positive control proves the real protected tool can change the file.
	// The fixture itself has no executor and never returns Approved=true.
	tool := toolfs.NewWriteFileTool(workspace, true)
	if result := tool.Execute(t.Context(), request.Arguments); result.IsError {
		t.Fatal("native synthetic write failed")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != ApprovalAfter {
		t.Fatal("native write control did not change the fixture", err)
	}
}

func TestApprovalFixtureDeniesEveryScopeAndArgumentMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*agent.ToolApprovalRequest){
		"workspace": func(r *agent.ToolApprovalRequest) { r.Meta.Workspace += "-foreign" },
		"turn":      func(r *agent.ToolApprovalRequest) { r.Meta.TurnID = "" },
		"agent":     func(r *agent.ToolApprovalRequest) { r.Meta.AgentID = "" },
		"session":   func(r *agent.ToolApprovalRequest) { r.Meta.SessionKey = "" },
		"call":      func(r *agent.ToolApprovalRequest) { r.Meta.ToolCallID = "" },
		"tool":      func(r *agent.ToolApprovalRequest) { r.Tool = "exec" },
		"path":      func(r *agent.ToolApprovalRequest) { r.Arguments["path"] = "../outside" },
		"absolute":  func(r *agent.ToolApprovalRequest) { r.Arguments["path"] = "/eval-fixtures/approval.txt" },
		"content":   func(r *agent.ToolApprovalRequest) { r.Arguments["content"] = "other" },
		"newline":   func(r *agent.ToolApprovalRequest) { r.Arguments["content"] = ApprovalAfter + "\n" },
		"overwrite": func(r *agent.ToolApprovalRequest) { r.Arguments["overwrite"] = false },
		"type":      func(r *agent.ToolApprovalRequest) { r.Arguments["overwrite"] = "true" },
		"extra":     func(r *agent.ToolApprovalRequest) { r.Arguments["extra"] = true },
		"missing":   func(r *agent.ToolApprovalRequest) { delete(r.Arguments, "overwrite") },
	} {
		t.Run(name, func(t *testing.T) {
			hook := &approvalHook{workspace: "/private/eval/workspace"}
			request := approvalRequest(hook.workspace)
			mutate(request)
			decision, err := hook.ApproveTool(t.Context(), request)
			if err != nil || decision.Approved || decision.RequireHuman || decision.Reason == "" {
				t.Fatalf("mismatch was not denied: %#v, %v", decision, err)
			}
		})
	}
	hook := &approvalHook{workspace: "/private/eval/workspace"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, request := range []*agent.ToolApprovalRequest{nil, approvalRequest(hook.workspace)} {
		decision, err := hook.ApproveTool(ctx, request)
		if !errors.Is(err, context.Canceled) || decision.Approved || decision.RequireHuman {
			t.Fatal("canceled/nil request was not denied")
		}
	}
	if decision, err := hook.ApproveTool(t.Context(), nil); err != nil || decision.Approved || decision.RequireHuman {
		t.Fatal("nil request was not denied")
	}
}

func TestApprovalFixtureRejectsConfigurablePolicy(t *testing.T) {
	for _, spec := range []config.BuiltinHookConfig{
		{},
		{Enabled: true, Priority: 1},
		{Enabled: true, Config: []byte(`{}`)},
		{Enabled: true, Config: []byte(`{"tool":"exec"}`)},
	} {
		if ApprovalHookMatches(spec) {
			t.Fatal("custom approval policy was admitted")
		}
	}
	if !ApprovalHookMatches(config.BuiltinHookConfig{Enabled: true}) {
		t.Fatal("fixed approval policy was rejected")
	}
}

func TestApprovalFixtureRequiresPrivateCanonicalWorkspace(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := approvalWorkspace(home); !errors.Is(err, ErrApprovalFixture) {
		t.Fatal("missing workspace was admitted", err)
	}
	workspace := filepath.Join(home, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := approvalWorkspace(home); !errors.Is(err, ErrApprovalFixture) {
		t.Fatal("public workspace was admitted", err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if _, err := approvalWorkspace(link); !errors.Is(err, ErrApprovalFixture) {
		t.Fatal("symlinked home was admitted", err)
	}
	if got, err := approvalWorkspace(home); err != nil || got != workspace {
		t.Fatal("private workspace was not admitted", err)
	}
}
