//go:build (linux || darwin) && integration

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	remoteWorkspaceAlias   = "project"
	remoteWorkspaceTarget  = "workspace-node"
	remoteWorkspaceProfile = "project-files"
	remoteWorkspaceJob     = "project-jobs"
)

func TestRemoteWorkspaceVerticalSliceRealProcess(t *testing.T) {
	gatewayWorkspace := t.TempDir()
	remoteRoot := canonicalVerticalSlicePath(t, filepath.Join(t.TempDir(), "project"))
	if err := os.Mkdir(remoteRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gatewayWorkspace, "same.txt"), []byte("gateway-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRoot, "same.txt"), []byte("remote-original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := filepath.Join(t.TempDir(), "workspace-fixture")
	writeRemoteWorkspaceFixture(t, fixture)
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg := remoteWorkspaceVerticalSliceConfig(gatewayWorkspace)
	registry, admission, runtimeState := newNodeJobVerticalSliceRuntime(t, gatewayWorkspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeNodeJobVerticalSliceRuntime(t, runtimeState)

	binaryPath := buildVerticalSliceCompanion(t, t.TempDir())
	companionConfig := remoteWorkspaceVerticalSliceCompanionConfig(
		t,
		server,
		stateDir,
		remoteRoot,
		fixture,
	)
	configPath := filepath.Join(t.TempDir(), "companion.json")
	writeVerticalSliceConfig(t, configPath, companionConfig)
	process := startVerticalSliceCompanion(t, binaryPath, configPath)
	defer func() { process.stop(t) }()

	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{remoteWorkspaceTarget},
		AllowedCommands: []string{
			nodes.WorkspaceCommandRead,
			nodes.WorkspaceCommandSearch,
			nodes.WorkspaceCommandWrite,
			nodes.WorkspaceCommandPatch,
			"system.exec.v1",
			nodes.JobCommandStart,
		},
		At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	provider := &remoteWorkspaceEvidenceProvider{}
	loop := agent.NewAgentLoop(
		cfg,
		bus.NewMessageBus(),
		provider,
		agent.WithIsolatedToolBootstrap(),
	)
	defer loop.Close()
	if err := setupNodeTools(cfg, loop, runtimeState); err != nil {
		t.Fatal(err)
	}
	response, err := loop.ProcessDirect(
		t.Context(),
		"Exercise the configured local default and complete remote workspace vertical slice.",
		"remote-workspace-e2e-session",
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "Remote workspace vertical slice completed." {
		t.Fatalf("model-facing response = %q", response)
	}
	waitForNodeJobFile(t, filepath.Join(remoteRoot, "job.completed"))
	if _, statErr := os.Stat(filepath.Join(gatewayWorkspace, "created.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("remote mutation appeared in gateway-local workspace: %v", statErr)
	}
	instance, ok := loop.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("main agent is unavailable")
	}

	uncertainCtx := remoteWorkspaceToolContext(gatewayWorkspace, "remote-uncertain")
	firstCtx, cancelFirst := context.WithTimeout(uncertainCtx, 15*time.Second)
	defer cancelFirst()
	workspaceExec, ok := instance.Tools.Get("workspace_exec")
	if !ok {
		t.Fatal("workspace_exec is unavailable")
	}
	uncertainArgs := map[string]any{
		"remote_workspace": remoteWorkspaceAlias, "executable": "fixture",
		"args": []any{"uncertain"}, "mode": "foreground", "timeout_seconds": float64(10),
	}
	resultChannel := make(chan *toolshared.ToolResult, 1)
	go func() {
		resultChannel <- workspaceExec.Execute(firstCtx, uncertainArgs)
	}()
	waitForNodeJobFile(t, filepath.Join(remoteRoot, "uncertain.started"))
	process.stop(t)
	disconnected := waitForVerticalSliceNodeState(t, registry, nodes.StateDisconnected)
	if disconnected.ID != pending.ID {
		t.Fatalf("disconnected node ID = %q, want %q", disconnected.ID, pending.ID)
	}
	var first *toolshared.ToolResult
	select {
	case first = <-resultChannel:
	case <-firstCtx.Done():
		t.Fatalf("disconnected invocation did not return: %v", context.Cause(firstCtx))
	}
	if !first.IsError || !strings.Contains(first.ContentForLLM(), "DISPATCH_UNCERTAIN") {
		t.Fatalf("disconnected mutation = %#v", first)
	}

	process = startVerticalSliceCompanion(t, binaryPath, configPath)
	reconnected := waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	if reconnected.ID != pending.ID {
		t.Fatalf("reconnected node ID = %q, want %q", reconnected.ID, pending.ID)
	}
	secondCtx, cancelSecond := context.WithTimeout(uncertainCtx, 5*time.Second)
	defer cancelSecond()
	second := workspaceExec.Execute(secondCtx, uncertainArgs)
	if !second.IsError || !strings.Contains(second.ContentForLLM(), "DISPATCH_UNCERTAIN") {
		t.Fatalf("repeated uncertain mutation = %#v", second)
	}
	launches, err := os.ReadFile(filepath.Join(remoteRoot, "uncertain.launches"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(launches), "launch\n") != 1 {
		t.Fatalf("uncertain mutation launches = %q, want exactly one", launches)
	}
}

func remoteWorkspaceVerticalSliceConfig(workspace string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Tools.Approval.Mode = config.ToolApprovalModeAllowAll
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"remote": {
			Type: "node", Node: remoteWorkspaceTarget, Executor: companion.LocalExecutor,
			FileProfile: remoteWorkspaceProfile, JobProfile: remoteWorkspaceJob,
		},
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		remoteWorkspaceAlias: {
			Target: "remote", WorkingScope: remoteWorkspaceAlias, Revision: "project-workspace-v1",
			Tools: []string{"read_file", "search_files", "write_file", "apply_patch", "workspace_exec", "jobs"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"remote"}}
	return cfg
}

func remoteWorkspaceVerticalSliceCompanionConfig(
	t *testing.T,
	server *httptest.Server,
	stateDir string,
	root string,
	fixture string,
) companion.Config {
	t.Helper()
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	return companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir:   stateDir,
		TLS: companion.TLSConfig{
			CertificateSHA256: hex.EncodeToString(fingerprint[:]),
		},
		Reconnect: companion.ReconnectConfig{
			MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1,
		},
		Policy: nodes.LocalCommandPolicy{
			Revision: "remote-workspace-e2e-policy",
			AllowedCommands: []string{
				nodes.WorkspaceCommandRead,
				nodes.WorkspaceCommandSearch,
				nodes.WorkspaceCommandWrite,
				nodes.WorkspaceCommandPatch,
				"system.exec.v1",
				nodes.JobCommandStart,
			},
			MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 30, MaxOutputBytes: 64 * 1024,
		},
		FilePolicies: companion.FilePolicies{
			remoteWorkspaceProfile: {
				Enabled: true, Revision: "project-files-v1",
				ReadableRoots: []string{root}, WritableRoots: []string{root},
				AllowCreate: true, AllowOverwrite: true, MaxFileBytes: protocol.MaxTransferFileBytes,
				Approval: companion.FileApprovalPolicy{
					Metadata: companion.FileApprovalNone,
					Read:     companion.FileApprovalNone,
					Write:    companion.FileApprovalNone,
				},
			},
		},
		SystemExec: &companion.SystemExecPolicy{
			WorkingRoots: []string{root}, Executables: []string{fixture},
			Discovery: &companion.SystemExecDiscovery{
				ExecutableAliases:   map[string]string{"fixture": fixture},
				WorkingScopeAliases: map[string]string{remoteWorkspaceAlias: root},
			},
		},
		JobProfiles: companion.JobProfiles{
			remoteWorkspaceJob: {
				Enabled: true, Revision: "project-jobs-v1", Executor: "system_exec",
				TimeoutSecondsMax: 30, ConcurrentJobs: 1,
				StdoutBytesMax: 4096, StderrBytesMax: 4096,
				ArtifactCountMax: 1, ArtifactBytesMax: 4096, ArtifactsTotalBytesMax: 4096,
				RetentionSeconds: 300, CancelGuarantee: companion.JobCancelProcessGroup,
				Approval: companion.JobProfileApproval{Start: "none", Read: "none", Cancel: "none"},
			},
		},
	}
}

func canonicalVerticalSlicePath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(canonical, filepath.Base(path))
}

func writeRemoteWorkspaceFixture(t *testing.T, path string) {
	t.Helper()
	const script = `#!/bin/sh
set -eu
case "$1" in
  foreground)
    printf 'foreground-ok\n'
    ;;
  job)
    printf 'done\n' > job.completed
    ;;
  uncertain)
    printf 'launch\n' >> uncertain.launches
    : > uncertain.started
    while :; do /bin/sleep 1; done
    ;;
  *)
    exit 64
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func remoteWorkspaceToolContext(workspace string, callID string) context.Context {
	ctx := toolshared.WithToolSessionContext(context.Background(), "main", "history-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "route-session")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "remote-workspace-e2e")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "owner", ActorID: "owner",
	})
	ctx = toolshared.WithToolCallID(ctx, callID)
	return toolshared.WithToolApprovalBypass(ctx, true)
}

type remoteWorkspaceEvidenceProvider struct {
	step int
}

func (*remoteWorkspaceEvidenceProvider) GetDefaultModel() string { return "remote-workspace-e2e-model" }

func (provider *remoteWorkspaceEvidenceProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	call := llmscenario.ProviderCall{Messages: messages, Tools: toolDefs}
	step := provider.step
	provider.step++
	if step == 0 {
		for _, name := range []string{"read_file", "search_files", "write_file", "apply_patch", "workspace_exec"} {
			if err := llmscenario.RequireToolDefinition(name)(call); err != nil {
				return nil, err
			}
		}
		return remoteWorkspaceToolCall("local-read", "read_file", map[string]any{
			"path": "same.txt", "offset": 0, "length": 1024,
		}), nil
	}
	if step == 1 {
		content, err := remoteWorkspaceLastToolContent(messages)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(content, "gateway-local") || strings.Contains(content, "remote-original") {
			return nil, fmt.Errorf("local default result = %q", content)
		}
		return remoteWorkspaceToolCall("remote-read", "read_file", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "path": "same.txt", "offset": 0, "length": 1024,
		}), nil
	}
	payload, err := nodeP0LastToolPayload(call)
	if err != nil {
		return nil, err
	}
	if payload["placement"] != "remote" || payload["remote_workspace"] != remoteWorkspaceAlias ||
		payload["target"] != "remote" || payload["remote_workspace_revision"] != "project-workspace-v1" {
		return nil, fmt.Errorf("remote workspace envelope = %#v", payload)
	}
	switch step {
	case 2:
		result, _ := payload["result"].(map[string]any)
		if !strings.Contains(fmt.Sprint(result["content"]), "remote-original") {
			return nil, fmt.Errorf("remote read = %#v", payload)
		}
		return remoteWorkspaceToolCall("remote-write", "write_file", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "path": "created.txt",
			"content": "needle before\n", "overwrite": false,
		}), nil
	case 3:
		return remoteWorkspaceToolCall("remote-search", "search_files", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "pattern": "needle",
		}), nil
	case 4:
		result, _ := payload["result"].(map[string]any)
		if result["matches"] != float64(1) {
			return nil, fmt.Errorf("remote search = %#v", payload)
		}
		return remoteWorkspaceToolCall("remote-patch", "apply_patch", map[string]any{
			"remote_workspace": remoteWorkspaceAlias,
			"input": "*** Begin Patch\n*** Update File: created.txt\n@@\n-needle before\n" +
				"+needle after\n*** End Patch",
		}), nil
	case 5:
		result, _ := payload["result"].(map[string]any)
		if result["state"] != "completed" {
			return nil, fmt.Errorf("remote patch = %#v", payload)
		}
		return remoteWorkspaceToolCall("remote-patched-read", "read_file", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "path": "created.txt", "offset": 0, "length": 1024,
		}), nil
	case 6:
		result, _ := payload["result"].(map[string]any)
		if content := fmt.Sprint(result["content"]); !strings.Contains(content, "needle after") ||
			strings.Contains(content, "needle before") {
			return nil, fmt.Errorf("remote patched read = %#v", payload)
		}
		return remoteWorkspaceToolCall("remote-foreground", "workspace_exec", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "executable": "fixture", "args": []any{"foreground"},
			"mode": "foreground", "timeout_seconds": 5,
		}), nil
	case 7:
		result, _ := payload["result"].(map[string]any)
		if payload["mode"] != "foreground" || result["stdout"] != "foreground-ok\n" {
			return nil, fmt.Errorf("remote foreground = %#v", payload)
		}
		return remoteWorkspaceToolCall("remote-job", "workspace_exec", map[string]any{
			"remote_workspace": remoteWorkspaceAlias, "executable": "fixture", "args": []any{"job"},
			"mode": "job", "timeout_seconds": 10,
		}), nil
	case 8:
		if payload["mode"] != "job" || strings.TrimSpace(fmt.Sprint(payload["job_id"])) == "" {
			return nil, fmt.Errorf("remote job = %#v", payload)
		}
		return llmscenario.TextResponse("Remote workspace vertical slice completed."), nil
	default:
		return nil, fmt.Errorf("unexpected remote workspace evidence model call %d", step+1)
	}
}

func remoteWorkspaceToolCall(callID string, name string, arguments map[string]any) *providers.LLMResponse {
	return llmscenario.ToolCallResponse(
		"I will execute the next bounded workspace step.",
		llmscenario.ToolCall(callID, name, arguments),
	)
}

func remoteWorkspaceLastToolContent(messages []providers.Message) (string, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "tool" {
			return messages[index].Content, nil
		}
	}
	return "", errors.New("tool result is missing")
}
