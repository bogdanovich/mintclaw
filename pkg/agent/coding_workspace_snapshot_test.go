package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestCodingWorkspaceSnapshotRefreshesPromptAndEmitsFrontendObservation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingInstructionTestFile(t, filepath.Join(project, "AGENTS.md"), "repository rules")
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "tracked.txt"), "baseline\n")
	runCodingWorkspaceGit(t, project, "init", "-b", "main")
	runCodingWorkspaceGit(t, project, "config", "user.email", "mintclaw-tests@example.invalid")
	runCodingWorkspaceGit(t, project, "config", "user.name", "MintClaw Tests")
	runCodingWorkspaceGit(t, project, "add", "AGENTS.md", "tracked.txt")
	runCodingWorkspaceGit(t, project, "commit", "-m", "initial")

	target := filepath.Join(project, "new.txt")
	provider := llmscenario.NewScriptedProvider(
		"coding-workspace-model",
		llmscenario.ProviderStep{
			Name: "observe clean workspace",
			Assert: func(call llmscenario.ProviderCall) error {
				if len(call.Messages) == 0 ||
					!strings.Contains(call.Messages[0].Content, "# Live workspace snapshot") ||
					!strings.Contains(call.Messages[0].Content, "Branch: main") ||
					!strings.Contains(call.Messages[0].Content, "Status: clean") {
					return fmt.Errorf("initial workspace prompt = %#v", call.Messages)
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I will add the file.",
				llmscenario.ToolCall("write-1", "write_file", map[string]any{
					"path": "new.txt", "content": "model-only secret body\n",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "observe refreshed dirty workspace",
			Assert: func(call llmscenario.ProviderCall) error {
				if len(call.Messages) == 0 {
					return fmt.Errorf("refreshed workspace prompt is missing")
				}
				system := call.Messages[0].Content
				if !strings.Contains(system, "Status: dirty") || !strings.Contains(system, `?? "new.txt"`) {
					return fmt.Errorf("refreshed workspace prompt = %q", system)
				}
				if strings.Contains(system, "model-only secret body") {
					return fmt.Errorf("workspace prompt included file contents: %q", system)
				}
				return llmscenario.RequireLastMessage("tool", "File written")(call)
			},
			Response: llmscenario.TextResponse("done"),
		},
	)

	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-workspace"},
		project,
		stateRoot,
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	subscription, workspaceEvents, err := eventBus.Channel().OfKind(
		runtimeevents.KindAgentWorkspaceSnapshot,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "workspace-test", Buffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = subscription.Close()
		_ = eventBus.Close()
	})

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.Provider = "test-provider"
	cfg.Agents.Defaults.ModelName = "coding-workspace-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	loop, err := NewAgentLoopWithRuntimeProfile(
		cfg,
		bus.NewMessageBus(),
		provider,
		profile,
		WithRuntimeEvents(eventBus),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)

	response, err := loop.ProcessDirect(context.Background(), "add a file", "coding:thread-workspace")
	if err != nil {
		t.Fatalf("ProcessDirect() error = %v", err)
	}
	if response != "done" {
		t.Fatalf("response = %q, want done", response)
	}
	if _, err = os.Stat(target); err != nil {
		t.Fatalf("written target: %v", err)
	}
	if err = provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}

	first := <-workspaceEvents
	second := <-workspaceEvents
	firstPayload, firstOK := first.Payload.(WorkspaceSnapshotPayload)
	secondPayload, secondOK := second.Payload.(WorkspaceSnapshotPayload)
	if !firstOK || !secondOK || firstPayload.Snapshot.Git.Dirty || !secondPayload.Snapshot.Git.Dirty ||
		len(secondPayload.Snapshot.ChangedPaths) != 1 || secondPayload.Snapshot.ChangedPaths[0].Path != "new.txt" {
		t.Fatalf("workspace events = first %#v, second %#v", first.Payload, second.Payload)
	}

	runCodingWorkspaceGit(t, project, "switch", "-c", "refreshed-branch")
	builder := loop.GetRegistry().GetDefaultAgent().ContextBuilder
	refreshed, changed := builder.RefreshCodingWorkspace(t.Context())
	if !changed || refreshed.Git.Branch != "refreshed-branch" {
		t.Fatalf("explicit workspace refresh = changed:%v snapshot:%+v", changed, refreshed)
	}
	if _, changed = builder.RefreshCodingWorkspace(t.Context()); changed {
		t.Fatal("unchanged explicit workspace refresh emitted a duplicate snapshot")
	}
}

func writeCodingWorkspaceTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCodingWorkspaceGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
