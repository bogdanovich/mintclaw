package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestToolCardsExposeLifecycleAndExpandedBoundedOutputWithoutArguments(t *testing.T) {
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
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewModel(&fakeController{Projector: projector}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(100, 30)
	collapsed := renderedModelTranscript(model, 100)
	for _, marker := range []string{"[running]", "[suspended]", "[ok]", "[failed]", "[interrupted]", "[unknown]"} {
		if !strings.Contains(collapsed, marker) {
			t.Fatalf("collapsed cards omit %q: %q", marker, collapsed)
		}
	}
	if strings.Contains(collapsed, "SECRET_TOKEN") || strings.Contains(collapsed, "secret command") ||
		strings.Contains(collapsed, "safe stdout") {
		t.Fatalf("collapsed card leaked arguments or output: %q", collapsed)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}, Alt: true})
	if model.selectedToolID != "view:tool:turn-1:interrupted" {
		t.Fatalf("Alt+K selected %q", model.selectedToolID)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlO})
	expanded := renderedModelTranscript(model, 100)
	for _, want := range []string{
		"command canceled", "exit 130", "background", "[output truncated]", "stdout:", "safe stdout", "stderr:",
		"safe stderr", "duration 1.5s",
	} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded card omits %q: %q", want, expanded)
		}
	}
	if strings.Contains(expanded, "SECRET_TOKEN") || strings.Contains(expanded, "secret command") {
		t.Fatalf("expanded card leaked arguments: %q", expanded)
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
	projector.FilesChanged("turn-1", "call-1", []frontend.WriteAudit{{
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
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeController{Projector: projector}
	controller.refreshState = &codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw",
		CWD:         "/work/mintclaw",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "feature/refreshed", Head: "abcdef1234",
		},
		DiffStatAvailable: true,
	}
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(120, 30)
	content := renderedModelTranscript(model, 120)
	for _, want := range []string{
		"Verified writes", "update verified.go", "Repository changes", "repository is dirty",
		"diff stat: 1 files · +12 -3", " M tracked.go", "Ctrl+R refresh repository status",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("repository surface omits %q: %q", want, content)
		}
	}
	status := model.statusLine()
	for _, want := range []string{
		"project mintclaw", "branch main", "model gpt-coding/openai", "context 20% (2.0k/10.0k)",
		"activity idle",
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
	if controller.refreshes.Load() != 1 || !strings.Contains(model.statusLine(), "branch feature/refreshed") ||
		!strings.Contains(renderedModelTranscript(model, 120), "repository is clean") {
		t.Fatalf(
			"refreshed state calls=%d status=%q content=%q",
			controller.refreshes.Load(),
			model.statusLine(),
			renderedModelTranscript(model, 120),
		)
	}
}

func renderedModelTranscript(model *Model, width int) string {
	state := model.reducer.State()
	content, _ := renderTranscript(
		buildTranscriptView(
			model.transcript.entries(state.Entries),
			state.Tools,
			state.ChangedFiles,
			state.Workspace,
			model.selectedToolID,
			model.expandedToolID,
		),
		width,
		false,
		false,
		false,
	)
	return content
}
