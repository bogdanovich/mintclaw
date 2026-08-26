package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type workspaceRetryProvider struct {
	mu    sync.Mutex
	calls [][]providers.Message
}

func (provider *workspaceRetryProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls = append(provider.calls, cloneProviderMessages(messages))
	if len(provider.calls) == 1 {
		return nil, errors.New("status 429: retry workspace snapshot")
	}
	return &providers.LLMResponse{Content: "done", FinishReason: "stop"}, nil
}

func (provider *workspaceRetryProvider) GetDefaultModel() string { return "coding-workspace-model" }

func (provider *workspaceRetryProvider) Calls() [][]providers.Message {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	calls := make([][]providers.Message, len(provider.calls))
	for index := range provider.calls {
		calls[index] = cloneProviderMessages(provider.calls[index])
	}
	return calls
}

type workspaceMutationRetrySleeper struct {
	mutate func()
}

func (sleeper workspaceMutationRetrySleeper) Sleep(ctx context.Context, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sleeper.mutate()
	return nil
}

func TestCodingPromptReanchorsCompactedSummaryToFreshWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "tracked.txt"), "baseline\n")
	runCodingWorkspaceGit(t, project, "init", "-b", "main")
	runCodingWorkspaceGit(t, project, "config", "user.email", "mintclaw-tests@example.invalid")
	runCodingWorkspaceGit(t, project, "config", "user.name", "MintClaw Tests")
	runCodingWorkspaceGit(t, project, "add", "tracked.txt")
	runCodingWorkspaceGit(t, project, "commit", "-m", "initial")

	layout, err := NewCodingRuntimeLayout(
		"thread-reanchor",
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newCodingContextBuilder(layout)
	if err != nil {
		t.Fatal(err)
	}
	staleSummary := "Repository state: Branch: old-summary-branch; HEAD: old-head; Status: clean."
	first := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        staleSummary,
		CurrentMessage: "inspect",
	})
	if len(first) == 0 || !strings.Contains(first[0].Content, "Branch: main") {
		t.Fatalf("initial coding prompt = %#v", first)
	}

	runCodingWorkspaceGit(t, project, "switch", "-c", "external-change")
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "outside.txt"), "changed outside MintClaw\n")
	second := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        staleSummary,
		CurrentMessage: "resume",
	})
	if len(second) == 0 {
		t.Fatal("resumed coding prompt is empty")
	}
	system := second[0].Content
	summaryIndex := strings.Index(system, "CONTEXT_SUMMARY:")
	snapshotIndex := strings.Index(system, "# Live workspace snapshot")
	if summaryIndex < 0 || snapshotIndex <= summaryIndex {
		t.Fatalf("coding context order does not re-anchor the summary:\n%s", system)
	}
	for _, want := range []string{
		"model-generated compacted summary",
		"Branch: external-change",
		"Status: dirty",
		`?? "outside.txt"`,
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("resumed coding prompt missing %q:\n%s", want, system)
		}
	}
	if len(second[0].SystemParts) < 3 {
		t.Fatalf("coding system parts = %#v", second[0].SystemParts)
	}
	lastTwo := second[0].SystemParts[len(second[0].SystemParts)-2:]
	if lastTwo[0].PromptSource != string(PromptSourceSummary) ||
		lastTwo[1].PromptSource != string(PromptSourceRuntime) {
		t.Fatalf("coding context sources = %#v, want model summary then deterministic runtime", lastTwo)
	}
}

func TestCodingPromptSurfacesBoundedWorkspaceCaptureFailure(t *testing.T) {
	root := t.TempDir()
	builder := NewContextBuilder(root)
	builder.codingPrompt = true
	builder.codingWorkspace = codingworkspace.NewObserver(
		root,
		root,
		codingworkspace.Limits{PromptBytes: 320, Timeout: time.Nanosecond},
	)

	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        "The repository was healthy.",
		CurrentMessage: "resume",
	})
	if len(messages) == 0 || len(messages[0].SystemParts) < 2 {
		t.Fatalf("coding prompt = %#v", messages)
	}
	runtimePart := messages[0].SystemParts[len(messages[0].SystemParts)-1]
	snapshotIndex := strings.Index(runtimePart.Text, "# Live workspace snapshot")
	if snapshotIndex < 0 {
		t.Fatalf("failed snapshot is missing: %#v", runtimePart)
	}
	snapshot := runtimePart.Text[snapshotIndex:]
	if runtimePart.PromptSource != string(PromptSourceRuntime) || len(snapshot) > 320 ||
		(!strings.Contains(snapshot, "Git: unavailable") &&
			!strings.Contains(snapshot, "Snapshot warning:") &&
			!strings.Contains(snapshot, "Snapshot status: prompt truncated")) {
		t.Fatalf("bounded failed snapshot = %#v", runtimePart)
	}
}

func TestCodingProviderRetryRefreshesWorkspaceSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "tracked.txt"), "baseline\n")
	runCodingWorkspaceGit(t, project, "init", "-b", "main")
	runCodingWorkspaceGit(t, project, "config", "user.email", "mintclaw-tests@example.invalid")
	runCodingWorkspaceGit(t, project, "config", "user.name", "MintClaw Tests")
	runCodingWorkspaceGit(t, project, "add", "tracked.txt")
	runCodingWorkspaceGit(t, project, "commit", "-m", "initial")

	layout, err := NewCodingRuntimeLayout(
		"thread-retry-reanchor",
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	provider := &workspaceRetryProvider{}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.Provider = "test-provider"
	cfg.Agents.Defaults.ModelName = "coding-workspace-model"
	cfg.Agents.Defaults.MaxLLMRetries = 1
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), provider, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)
	runner := loop.turns.currentRunner()
	if runner == nil || runner.pipeline == nil {
		t.Fatal("coding turn runner is unavailable")
	}
	runner.pipeline.retrySleeper = workspaceMutationRetrySleeper{mutate: func() {
		runCodingWorkspaceGit(t, project, "switch", "-c", "changed-during-retry")
		writeCodingWorkspaceTestFile(t, filepath.Join(project, "retry.txt"), "external retry change\n")
	}}

	response, err := loop.ProcessDirect(t.Context(), "inspect", "coding:thread-retry-reanchor")
	if err != nil {
		t.Fatal(err)
	}
	if response != "done" {
		t.Fatalf("response = %q, want done", response)
	}
	calls := provider.Calls()
	if len(calls) != 2 || len(calls[0]) == 0 || len(calls[1]) == 0 {
		t.Fatalf("provider calls = %#v, want two populated attempts", calls)
	}
	firstSystem := calls[0][0].Content
	secondSystem := calls[1][0].Content
	if !strings.Contains(firstSystem, "Branch: main") || !strings.Contains(firstSystem, "Status: clean") {
		t.Fatalf("first provider attempt workspace = %q", firstSystem)
	}
	for _, want := range []string{"Branch: changed-during-retry", "Status: dirty", `?? "retry.txt"`} {
		if !strings.Contains(secondSystem, want) {
			t.Fatalf("retried provider attempt missing %q: %s", want, secondSystem)
		}
	}
}

func TestPersonalPromptKeepsRuntimeBeforeSummary(t *testing.T) {
	builder := NewContextBuilder(t.TempDir())
	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        "personal summary",
		CurrentMessage: "continue",
	})
	if len(messages) == 0 {
		t.Fatal("personal prompt is empty")
	}
	system := messages[0].Content
	runtimeIndex := strings.Index(system, "## Current Time")
	summaryIndex := strings.Index(system, "CONTEXT_SUMMARY:")
	if runtimeIndex < 0 || summaryIndex <= runtimeIndex ||
		strings.Contains(system, "model-generated compacted summary") {
		t.Fatalf("personal context order or wording changed:\n%s", system)
	}
}
