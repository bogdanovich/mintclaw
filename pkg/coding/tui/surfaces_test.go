package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend/agentadapter"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestToolCellsExposeLifecycleAndFullTranscriptWithoutArguments(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.ToolStarted("turn-1", "running", "exec", `SECRET_TOKEN=do-not-render`)
	projector.ToolSuspended("turn-1", "suspended", "ask_user", 2*time.Second)
	projector.ToolCompleted("turn-1", "success", "read_file", "result", 120*time.Millisecond, false, nil)
	projector.ToolCompleted("turn-1", "failed", "write_file", "failed", time.Second, true, nil)
	exitCode := 130
	projector.ToolStarted("turn-1", "interrupted", "exec", "secret command")
	projector.ToolCommandOutput("turn-1", "interrupted", frontend.CommandState{
		Stdout: "safe stdout", Stderr: "safe stderr", Status: frontend.CommandCanceled,
		ExitCode: &exitCode, Truncated: true, Background: true, Canceled: true,
	})
	projector.ToolCompleted("turn-1", "interrupted", "exec", "", 1500*time.Millisecond, true, nil)
	projector.ToolOutput("turn-1", "unknown", "orphan output")
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(100, 30)
	collapsed := renderedModelTranscript(model, 100)
	for _, marker := range []string{
		"[running]", "[suspended]", "[succeeded]", "[failed]", "Command interrupted", "[unknown]",
	} {
		if !strings.Contains(collapsed, marker) {
			t.Fatalf("collapsed cards omit %q: %q", marker, collapsed)
		}
	}
	if strings.Contains(collapsed, "SECRET_TOKEN") || strings.Contains(collapsed, "secret command") ||
		!strings.Contains(collapsed, "safe stdout") || !strings.Contains(collapsed, "ctrl+t") {
		t.Fatalf("collapsed card leaked arguments or omitted bounded command evidence: %q", collapsed)
	}

	full := strings.Join(transcriptOverlayLogicalLines(model.transcriptOverlayLines()), "\n")
	for _, want := range []string{
		"Command interrupted", "exit 130", "execution: background", "[… transcript bounded …]", "stdout>",
		"safe stdout", "stderr>", "safe stderr", "· 1.5s",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full transcript omits %q: %q", want, full)
		}
	}
	if strings.Contains(full, "SECRET_TOKEN") || strings.Contains(full, "secret command") {
		t.Fatalf("full transcript leaked arguments: %q", full)
	}
}

func TestOrdinaryToolAdapterOutputRemainsNonExpandableAndRedacted(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	eventBus := runtimeevents.NewBus()
	wrapped, err := agentadapter.WrapBus(eventBus, projector, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	scope := runtimeevents.Scope{
		SessionKey: "thread-1",
		TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
	}
	publish := func(kind runtimeevents.Kind, payload any) {
		t.Helper()
		wrapped.PublishNonBlocking(runtimeevents.Event{
			Kind: kind, Source: runtimeevents.Source{Component: "agent"}, Scope: scope, Payload: payload,
		})
	}
	publish(runtimeevents.KindAgentToolExecStart, agent.ToolExecStartPayload{
		ToolCallID: "call-1",
		Tool:       "read_file",
		Arguments:  map[string]any{"path": "SECRET-PATH"},
	})
	publish(runtimeevents.KindAgentToolExecEnd, agent.ToolExecEndPayload{
		ToolCallID: "call-1", Tool: "read_file", ForLLMLen: 8_192, ForUserLen: 4_096,
	})
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ToolStates()) != 1 || snapshot.ToolStates()[0].Output != "" {
		t.Fatalf("ordinary tool projection = %+v", snapshot.ToolStates())
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 20)
	rendered := renderedModelTranscript(model, 80)
	for _, forbidden := range []string{"SECRET-PATH", "8192", "4096", "result available", "output available"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("ordinary tool card leaked %q: %q", forbidden, rendered)
		}
	}
	full := strings.Join(transcriptOverlayLogicalLines(model.transcriptOverlayLines()), "\n")
	if strings.Contains(full, "SECRET-PATH") {
		t.Fatalf("ordinary tool evidence exposed redacted arguments: %q", full)
	}
}

func TestCompactionSurfacesDistinguishModeAndReportMetrics(t *testing.T) {
	compaction := &frontend.CompactionState{
		Reason: "llm_retry", Status: frontend.CompactionCompleted,
		TokensBefore: 2400, TokensAfter: 900, TokensSaved: 1500, TokenCountsObserved: true,
		SummariesCreated: 3, LeafSummaries: 2, CondensedSummaries: 1, Duration: 1500 * time.Millisecond,
	}
	panel := strings.Join(compactionStatusLines(compaction), "\n")
	for _, want := range []string{
		"last compaction: completed (blocking)",
		"compaction trigger: context overflow retry",
		"compaction context: 2.4k → 900 tokens",
		"compaction summaries: 3 total (2 leaf, 1 condensed)",
		"compaction duration: 1.5s",
		"compaction continuation: work can continue",
		"use /new for a focused thread",
	} {
		if !strings.Contains(panel, want) {
			t.Fatalf("compaction panel omits %q: %q", want, panel)
		}
	}

	compaction.Status = frontend.CompactionFailed
	compaction.Background = true
	if got := compactionContinuation(compaction); !strings.HasPrefix(got, "work can continue") {
		t.Fatalf("background failure continuation = %q", got)
	}

	compaction.Status = frontend.CompactionRunning
	compaction.Reason = "summarize"
	compaction.Background = false
	panel = strings.Join(compactionStatusLines(compaction), "\n")
	if !strings.Contains(panel, "compaction trigger: session summarization") {
		t.Fatalf("foreground summarize panel = %q", panel)
	}
}

func TestRepositoryAndStatusSurfacesRefreshFromAuthoritativeSnapshot(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.ThreadMetadataUpdated(frontend.ThreadMetadata{
		ProjectRoot: "/work/mintclaw", Model: "gpt-coding", Provider: "openai",
	})
	projector.ContextUsage(2_000, 10_000)
	projector.ToolCompleted("turn-1", "call-1", "write_file", "", 0, false, []frontend.WriteAudit{{
		Kind: "file", Target: "verified.go", Action: "update", Success: true, Tool: "write_file",
	}})
	projector.WorkspaceUpdated(codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw",
		CWD:         "/work/mintclaw",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "main", Head: "1234567890", Dirty: true,
		},
		ChangedPaths:      []codingworkspace.ChangedPath{{Path: "tracked.go", Status: " M"}},
		DiffStat:          codingworkspace.DiffStat{Files: 1, Additions: 12, Deletions: 3},
		DiffStatAvailable: true,
	})
	controller := &fakeController{Projector: projector}
	controller.refreshState = &codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw",
		CWD:         "/work/mintclaw",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "feature/refreshed", Head: "abcdef1234",
		},
		DiffStatAvailable: true,
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(120, 30)
	content := renderedModelTranscript(model, 120)
	for _, want := range []string{
		"Edited 1 file", "update verified.go", "Repository changes", "repository is dirty",
		"diff stat: 1 files · +12 -3", " M tracked.go", "Ctrl+R refresh repository status",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("repository surface omits %q: %q", want, content)
		}
	}
	status := model.statusLine()
	for _, want := range []string{
		"gpt-coding/openai", "/work/mintclaw", "main", "context 20% (2.0k/10.0k)",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status omits %q: %q", want, status)
		}
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(*Model)
	if command == nil || !model.refreshingWorkspace {
		t.Fatal("Ctrl+R did not start workspace refresh")
	}
	model = updateModel(t, model, command())
	latest, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: latest})
	if controller.refreshes.Load() != 1 || !strings.Contains(model.statusLine(), "feature/refreshed") ||
		!strings.Contains(renderedModelTranscript(model, 120), "repository is clean") {
		t.Fatalf(
			"refreshed state calls=%d status=%q content=%q",
			controller.refreshes.Load(),
			model.statusLine(),
			renderedModelTranscript(model, 120),
		)
	}
}

func TestRepositoryStatePrecedesTurnBoundaryAndFinalAnswer(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "fix it")
	projector.ToolCompleted("turn-1", "call-1", "write_file", "", time.Second, false, []frontend.WriteAudit{{
		Kind: "file", Target: "main.go", Action: "update", Success: true,
	}})
	projector.WorkspaceUpdated(codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw",
		CWD:         "/work/mintclaw",
		Git:         codingworkspace.GitState{Available: true, StatusAvailable: true, Dirty: true},
		ChangedPaths: []codingworkspace.ChangedPath{{
			Path: "main.go", Status: " M",
		}},
	})
	projector.AssistantAccumulated("turn-1", "The fix is complete.", true)
	projector.TurnCompleted("turn-1", "completed")

	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	content := renderedModelTranscript(model, 100)
	repositoryIndex := strings.Index(content, "Repository changes")
	boundaryIndex := strings.Index(content, "────────")
	finalIndex := strings.Index(content, "The fix is complete.")
	if repositoryIndex < 0 || boundaryIndex <= repositoryIndex || finalIndex <= boundaryIndex {
		t.Fatalf(
			"terminal transcript order repository=%d boundary=%d final=%d: %q",
			repositoryIndex,
			boundaryIndex,
			finalIndex,
			content,
		)
	}
}

func TestStatusFooterKeepsStableFactsAndLeavesActivityToWorkingLine(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.ThreadMetadataUpdated(frontend.ThreadMetadata{
		ProjectRoot: "/work/representative-project", Model: "representative-coding-model", Provider: "provider",
	})
	projector.ContextUsage(20_000, 100_000)
	projector.RuntimeStatusUpdated(frontend.RuntimeStatus{ReasoningEffort: "medium"})
	projector.WorkspaceUpdated(codingworkspace.Snapshot{
		ProjectRoot: "/work/representative-project",
		CWD:         "/work/representative-project",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "feature/representative-status", Head: "1234567890",
		},
	})
	projector.TurnStarted("turn-1", "work")
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{40, 80} {
		model.resize(width, 20)
		status := model.statusLine()
		if !strings.Contains(status, "representative-coding-model/provider") ||
			strings.Contains(status, "activity") || strings.Contains(status, "running") ||
			ansi.StringWidth(status) > width {
			t.Fatalf("width %d status = %q (%d cells)", width, status, ansi.StringWidth(status))
		}
	}
}

func renderedModelTranscript(model *Model, width int) string {
	if model.viewport.Width != width {
		model.viewport.Width = width
		model.refreshViewport()
	}
	return model.document.text()
}
