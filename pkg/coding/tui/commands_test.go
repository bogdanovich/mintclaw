package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

type evidenceController struct {
	*fakeController
	status codingworkspace.StatusResult
	diff   codingworkspace.DiffResult
	target codingworkspace.DiffTarget
}

type reviewController struct {
	*fakeController
	target codingreview.Target
	err    error
}

func (controller *reviewController) Review(_ context.Context, target codingreview.Target) error {
	controller.target = target
	return controller.err
}

func (controller *evidenceController) RepositoryStatus(
	context.Context,
) (codingworkspace.StatusResult, error) {
	controller.RepositoryStatusUpdated(controller.status)
	return controller.status, nil
}

func (controller *evidenceController) RepositoryDiff(
	_ context.Context,
	target codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	controller.target = target
	controller.RepositoryDiffUpdated(controller.diff)
	return controller.diff, nil
}

func TestSlashHelpAndUnknownCommandState(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(90, 24)

	model.composer.SetValue("/help")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command != nil || model.commandPanel != commandPanelHelp || model.ComposerValue() != "" {
		t.Fatalf("help transition = panel=%v draft=%q command=%v", model.commandPanel, model.ComposerValue(), command)
	}
	for _, want := range []string{
		"MintClaw coding commands", "/compact", "/attach <paths…>", "/rename <title>", "/new", "/exit",
		"Ctrl+J newline", "Ctrl+V paste clipboard image", "Ctrl+R refresh repository", "Esc close panel",
	} {
		if !strings.Contains(model.View(), want) {
			t.Fatalf("help omits %q: %q", want, model.View())
		}
	}
	for _, line := range strings.Split(model.View(), "\n") {
		if width := ansi.StringWidth(line); width > 90 {
			t.Fatalf("help line width = %d, want <= 90: %q", width, line)
		}
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.commandPanel != commandPanelNone {
		t.Fatalf("Esc left command panel %v", model.commandPanel)
	}

	model.composer.SetValue("/mystery keep this")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command != nil || model.ComposerValue() != "/mystery keep this" || model.err == nil ||
		!strings.Contains(model.err.Error(), "use /help") || controller.submits.Load() != 0 {
		t.Fatalf(
			"unknown command state: draft=%q err=%v submits=%d",
			model.ComposerValue(),
			model.err,
			controller.submits.Load(),
		)
	}

	model.composer.SetValue("/rename")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command != nil || model.ComposerValue() != "/rename" || model.err == nil ||
		!strings.Contains(model.err.Error(), "requires a title") {
		t.Fatalf("malformed rename state: draft=%q err=%v command=%v", model.ComposerValue(), model.err, command)
	}

	model.composer.SetValue("/status unexpected")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command != nil || model.ComposerValue() != "/status unexpected" || model.err == nil ||
		!strings.Contains(model.err.Error(), "does not accept arguments") {
		t.Fatalf("malformed status state: draft=%q err=%v command=%v", model.ComposerValue(), model.err, command)
	}
}

func TestSlashReviewUsesTypedTargetAndRendersCurrentState(t *testing.T) {
	controller := &reviewController{fakeController: newController(t)}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("/review base main -- focus on cancellation")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || model.commandPanel != commandPanelReview || model.pendingSlashCommand != "review" {
		t.Fatalf(
			"review admission = command=%v panel=%v pending=%q",
			command,
			model.commandPanel,
			model.pendingSlashCommand,
		)
	}
	model = updateModel(t, model, command())
	want := codingreview.Target{
		Kind: codingreview.TargetBase, Ref: "main", Instructions: "focus on cancellation",
	}
	if controller.target != want || model.pendingSlashCommand != "" {
		t.Fatalf("review target = %#v, pending=%q", controller.target, model.pendingSlashCommand)
	}
	state := codingreview.State{Target: want, Phase: codingreview.PhaseProgress, Progress: "inspecting changes"}
	model.snapshot.Review = &state
	for _, text := range []string{"Local code review", "target: base main", "phase: progress", "inspecting changes"} {
		if !strings.Contains(model.commandPanelView(), text) {
			t.Fatalf("review panel omits %q: %s", text, model.commandPanelView())
		}
	}
}

func TestSlashReviewAdmissionFailureClosesWaitingPanel(t *testing.T) {
	controller := &reviewController{fakeController: newController(t), err: frontend.ErrCommandUnsupported}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("/review")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || model.commandPanel != commandPanelReview {
		t.Fatalf("review waiting panel = command=%v panel=%v", command, model.commandPanel)
	}
	model = updateModel(t, model, command())
	if model.commandPanel != commandPanelNone || model.err == nil ||
		!strings.Contains(model.err.Error(), "current provider") {
		t.Fatalf("failed review panel = %v, error=%v", model.commandPanel, model.err)
	}
}

func TestSlashReviewTargetValidation(t *testing.T) {
	tests := []struct {
		input string
		want  codingreview.Target
		err   string
	}{
		{input: "", want: codingreview.Target{Kind: codingreview.TargetCurrent}},
		{
			input: "current -- security",
			want:  codingreview.Target{Kind: codingreview.TargetCurrent, Instructions: "security"},
		},
		{input: "commit HEAD~1", want: codingreview.Target{Kind: codingreview.TargetCommit, Ref: "HEAD~1"}},
		{
			input: "base feature--branch",
			want:  codingreview.Target{Kind: codingreview.TargetBase, Ref: "feature--branch"},
		},
		{input: "base", err: "requires one local ref"},
		{input: "current main", err: "does not accept a ref"},
		{input: "mystery", err: "target must be"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := slashReviewTarget(test.input)
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("slashReviewTarget(%q) error = %v", test.input, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("slashReviewTarget(%q) = %#v, %v", test.input, got, err)
			}
		})
	}
}

func TestReviewActivityIsInterruptible(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.snapshot.Activity = frontend.ActivityReviewing
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if command == nil || !model.interruptPending {
		t.Fatalf("review interrupt = command=%v pending=%v", command, model.interruptPending)
	}
	_ = command()
	if controller.interrupts.Load() != 1 {
		t.Fatalf("review interrupt calls = %d", controller.interrupts.Load())
	}
}

func TestSlashAttachAddsFileWithoutSubmittingCommandText(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trace output.log")
	if err = os.WriteFile(path, []byte("failure details"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "screenshot.txt")
	if err = os.WriteFile(secondPath, []byte("not actually an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue(
		"/attach " + strings.ReplaceAll(path, " ", "\\ ") + " " + strings.ReplaceAll(secondPath, " ", "\\ "),
	)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("attach did not return a cursor command")
	}
	if model.ComposerValue() != "[File: trace output.log]\n[File: screenshot.txt]" ||
		len(model.composerAttachments) != 2 {
		t.Fatalf("attach draft=%q attachments=%+v", model.ComposerValue(), model.composerAttachments)
	}
	model.composer.InsertString(" inspect this")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	_ = updateModel(t, model, command())
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || inputs[0].Text != "inspect this" || len(inputs[0].Attachments) != 2 ||
		inputs[0].Attachments[0].Path != path || inputs[0].Attachments[1].Path != secondPath {
		t.Fatalf("attached turn = %+v", inputs)
	}
}

func TestSlashAttachBatchFailureKeepsCommandDraftAndAddsNothing(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(t.TempDir(), "valid.log")
	if err = os.WriteFile(valid, []byte("valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	draft := "/attach " + valid + " " + filepath.Join(t.TempDir(), "missing.log")
	model.composer.SetValue(draft)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if model.err == nil || model.ComposerValue() != draft || len(model.composerAttachments) != 0 {
		t.Fatalf(
			"failed batch state: err=%v draft=%q attachments=%+v",
			model.err,
			model.ComposerValue(),
			model.composerAttachments,
		)
	}
}

func TestReadOnlyCommandPanelsFollowCurrentSnapshot(t *testing.T) {
	controller := newController(t)
	controller.ThreadMetadataUpdated(frontend.ThreadMetadata{
		Title: "Parser work", ProjectRoot: "/work/mintclaw", CWD: "/work/mintclaw/sub",
		Model: "coding-model", Provider: "openai",
	})
	controller.ContextUsage(2_000, 10_000)
	controller.WorkspaceUpdated(codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw", CWD: "/work/mintclaw/sub",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "main", Head: "1234567890", Dirty: true,
		},
		ChangedPaths: []codingworkspace.ChangedPath{{Path: "parser.go", Status: " M"}},
		DiffStat: codingworkspace.DiffStat{
			Files: 1, Additions: 12, Deletions: 3,
		},
		DiffStatAvailable: true,
	})
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(100, 30)

	enterPanelCommand(t, model, "/status")
	for _, want := range []string{
		"Current coding thread status", "Parser work", "branch: main", "repository: dirty",
		"model: coding-model/openai", "context: 20%",
	} {
		if !strings.Contains(model.View(), want) {
			t.Fatalf("status panel omits %q: %q", want, model.View())
		}
	}
	controller.WorkspaceUpdated(codingworkspace.Snapshot{
		ProjectRoot: "/work/mintclaw", CWD: "/work/mintclaw/sub",
		Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "feature/live", Head: "abcdef1234",
		},
		DiffStatAvailable: true,
	})
	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	if !strings.Contains(model.View(), "branch: feature/live") || !strings.Contains(model.View(), "repository: clean") {
		t.Fatalf("status panel did not converge to current view: %q", model.View())
	}

	enterPanelCommand(t, model, "/model")
	if !strings.Contains(model.View(), "coding-model/openai") || !strings.Contains(model.View(), "--model <name>") {
		t.Fatalf("model panel = %q", model.View())
	}
	enterPanelCommand(t, model, "/diff")
	if !strings.Contains(model.View(), "Current bounded repository changes") ||
		!strings.Contains(model.View(), "branch: feature/live") || !strings.Contains(model.View(), "no changed paths") {
		t.Fatalf("diff panel = %q", model.View())
	}
}

func TestRepositoryPanelsRefreshThroughTypedEvidenceReader(t *testing.T) {
	controller := &evidenceController{fakeController: newController(t)}
	controller.status = codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Snapshot: codingworkspace.Snapshot{Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "typed-status",
		}},
	}
	controller.diff = codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"},
		Files: []codingworkspace.DiffFile{{
			Path: "typed.go", Status: "M", Provenance: codingworkspace.ProvenanceFirstObservedDuringThread,
		}},
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	model.composer.SetValue("/status")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("/status did not request typed repository evidence")
	}
	completion := command()
	controller.ThreadMetadataUpdated(frontend.ThreadMetadata{Title: "newer canonical state"})
	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	model = updateModel(t, model, completion)
	if !strings.Contains(model.View(), "typed-status") || model.snapshot.RepositoryStatus == nil ||
		model.snapshot.Metadata.Title != "newer canonical state" {
		t.Fatalf("typed status panel = %q", model.View())
	}

	model.composer.SetValue("/diff base main")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("/diff did not request typed repository evidence")
	}
	completion = command()
	snapshot, err = controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	model = updateModel(t, model, completion)
	if controller.target.Kind != codingworkspace.DiffTargetBase || controller.target.Ref != "main" ||
		!strings.Contains(model.View(), "Repository diff (base main)") ||
		!strings.Contains(model.View(), "typed.go") || !strings.Contains(model.View(), "first_observed_during_thread") {
		t.Fatalf("typed diff target/panel = %#v / %q", controller.target, model.View())
	}

	controller.status.Snapshot.Git.Branch = "new-status"
	model.composer.SetValue("/status")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	completion = command()
	snapshot, err = controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	model = updateModel(t, model, completion)
	if model.snapshot.RepositoryDiff != nil || !strings.Contains(model.View(), "new-status") {
		t.Fatalf(
			"canonical status refresh retained obsolete diff: %#v / %q",
			model.snapshot.RepositoryDiff,
			model.View(),
		)
	}
}

func TestCommandPanelScrollMakesTailDiffDiagnosticsReachable(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	diff := codingworkspace.DiffResult{
		Target: codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files: []codingworkspace.DiffFile{{
			Path: "long\t.go", Status: " M",
			Hunks: []codingworkspace.DiffHunk{{
				OldStart: 1, OldLines: 8, NewStart: 1, NewLines: 8,
				Lines: []codingworkspace.DiffLine{
					{Kind: "context", OldLine: 1, NewLine: 1, Text: "line\t1"},
					{Kind: "context", OldLine: 2, NewLine: 2, Text: "line 2"},
					{Kind: "context", OldLine: 3, NewLine: 3, Text: "line 3"},
					{Kind: "context", OldLine: 4, NewLine: 4, Text: "line 4"},
					{Kind: "context", OldLine: 5, NewLine: 5, Text: "line 5"},
					{Kind: "context", OldLine: 6, NewLine: 6, Text: "line 6"},
					{Kind: "context", OldLine: 7, NewLine: 7, Text: "line 7"},
					{Kind: "context", OldLine: 8, NewLine: 8, Text: "line 8"},
				},
			}},
		}},
		Warning: "tail diagnostic",
	}
	snapshot := model.snapshot.Clone()
	snapshot.RepositoryDiff = &diff
	if err = model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	model.commandPanel = commandPanelDiff
	model.resize(60, 8)
	if strings.Contains(model.View(), "tail diagnostic") || !strings.Contains(model.View(), "PgUp/PgDown scroll") {
		t.Fatalf("initial diff page = %q", model.View())
	}
	for range 8 {
		model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if model.commandPanelOffset == 0 || !strings.Contains(model.View(), "warning: tail diagnostic") {
		t.Fatalf("scrolled diff page = offset %d / %q", model.commandPanelOffset, model.View())
	}
	model.resize(12, 8)
	for _, line := range strings.Split(model.commandPanelView(), "\n") {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("narrow command panel retained a terminal-dependent tab: %q", line)
		}
		if width := ansi.StringWidth(line); width > 12 {
			t.Fatalf("narrow command panel line width = %d: %q", width, line)
		}
	}
}

func TestSupersededEvidenceCompletionPreservesNewerError(t *testing.T) {
	controller := &evidenceController{fakeController: newController(t)}
	controller.status = codingworkspace.StatusResult{
		Snapshot: codingworkspace.Snapshot{Git: codingworkspace.GitState{
			Available: true, StatusAvailable: true, Branch: "main",
		}},
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	model.composer.SetValue("/status")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("/status did not start evidence request")
	}
	staleCompletion := command()

	model.composer.SetValue("/diff unsupported")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if model.err == nil || !strings.Contains(model.err.Error(), "target must be") {
		t.Fatalf("newer diff error = %v", model.err)
	}
	model = updateModel(t, model, staleCompletion)
	if model.err == nil || !strings.Contains(model.err.Error(), "target must be") || model.workspaceNotice != "" {
		t.Fatalf("stale status completion changed newer UI state: err=%v notice=%q", model.err, model.workspaceNotice)
	}
}

func TestRepositoryCompletionsRetireLoadingStateAroundErrors(t *testing.T) {
	newModel := func() *Model {
		model, err := newTestModel(&evidenceController{fakeController: newController(t)})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}

	model := newModel()
	model.err = errors.New("old error")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(*Model)
	if command == nil || model.err != nil || !model.refreshingWorkspace {
		t.Fatalf(
			"accepted refresh retained old state: command=%v err=%v refreshing=%v",
			command,
			model.err,
			model.refreshingWorkspace,
		)
	}
	model = updateModel(t, model, command())
	if model.refreshingWorkspace || model.workspaceNotice != "repository refreshed" {
		t.Fatalf("refresh completion = refreshing:%v notice:%q", model.refreshingWorkspace, model.workspaceNotice)
	}

	for _, test := range []struct {
		name  string
		start func(*Model) (tea.Model, tea.Cmd)
	}{
		{
			name: "refresh",
			start: func(model *Model) (tea.Model, tea.Cmd) {
				return model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
			},
		},
		{
			name: "status",
			start: func(model *Model) (tea.Model, tea.Cmd) {
				model.composer.SetValue("/status")
				return model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "diff",
			start: func(model *Model) (tea.Model, tea.Cmd) {
				model.composer.SetValue("/diff")
				return model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newModel()
			updated, command := test.start(model)
			model = updated.(*Model)
			if command == nil {
				t.Fatal("repository operation did not start")
			}
			completion := command()
			newerErr := errors.New("newer UI error")
			model.err = newerErr
			model = updateModel(t, model, completion)
			if !errors.Is(model.err, newerErr) || model.activeEvidenceReq != 0 ||
				model.refreshingWorkspace || model.workspaceNotice != "" {
				t.Fatalf(
					"completion retained operation state: err=%v request=%d refreshing=%v notice=%q",
					model.err,
					model.activeEvidenceReq,
					model.refreshingWorkspace,
					model.workspaceNotice,
				)
			}
		})
	}
}

func TestRepositoryOperationsIgnoreCrossedCompletions(t *testing.T) {
	newModel := func() (*Model, *evidenceController) {
		controller := &evidenceController{fakeController: newController(t)}
		controller.status = codingworkspace.StatusResult{
			Snapshot: codingworkspace.Snapshot{Git: codingworkspace.GitState{
				Available: true, StatusAvailable: true, Branch: "main",
			}},
		}
		model, err := newTestModel(controller)
		if err != nil {
			t.Fatal(err)
		}
		return model, controller
	}

	model, _ := newModel()
	updated, refreshCommand := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(*Model)
	staleRefresh := refreshCommand()
	model.composer.SetValue("/status")
	updated, statusCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	statusCompletion := statusCommand()
	model = updateModel(t, model, staleRefresh)
	if model.workspaceNotice != "repository status loading" {
		t.Fatalf("old refresh replaced status request: %q", model.workspaceNotice)
	}
	model = updateModel(t, model, statusCompletion)
	if model.workspaceNotice != "repository status refreshed" {
		t.Fatalf("status completion after old refresh = %q", model.workspaceNotice)
	}

	model, _ = newModel()
	model.composer.SetValue("/status")
	updated, statusCommand = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	staleStatus := statusCommand()
	updated, refreshCommand = model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(*Model)
	refreshCompletion := refreshCommand()
	model = updateModel(t, model, staleStatus)
	if !model.refreshingWorkspace || model.workspaceNotice != "" {
		t.Fatalf(
			"old status replaced refresh request: refreshing=%v notice=%q",
			model.refreshingWorkspace,
			model.workspaceNotice,
		)
	}
	model = updateModel(t, model, refreshCompletion)
	if model.refreshingWorkspace || model.workspaceNotice != "repository refreshed" {
		t.Fatalf(
			"refresh completion after old status: refreshing=%v notice=%q",
			model.refreshingWorkspace,
			model.workspaceNotice,
		)
	}

	model, _ = newModel()
	updated, refreshCommand = model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(*Model)
	staleRefresh = refreshCommand()
	model.composer.SetValue("/diff unsupported")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	model = updateModel(t, model, staleRefresh)
	if model.err == nil || !strings.Contains(model.err.Error(), "target must be") || model.workspaceNotice != "" {
		t.Fatalf("old refresh changed newer error: err=%v notice=%q", model.err, model.workspaceNotice)
	}
}

func TestCommandPanelsEscapeStructuredSnapshotFields(t *testing.T) {
	snapshot := frontend.ThreadSnapshot{
		ThreadID: "thread\nforged-thread\tcell",
		Metadata: frontend.ThreadMetadata{
			Title:       "title\nforged-title\tcell",
			ProjectRoot: "/project\nforged-project",
			CWD:         "/cwd\tforged-cwd",
			Model:       "model\nforged-model",
			Provider:    "provider\tforged-provider",
		},
		Activity: frontend.ActivityRunning,
		Status:   "working\nforged-status\tcell",
		Workspace: &codingworkspace.Snapshot{
			ProjectRoot: "/root\nforged-root",
			Git: codingworkspace.GitState{
				Available: true, StatusAvailable: true, Branch: "branch\nforged-branch",
			},
			ChangedPaths: []codingworkspace.ChangedPath{{
				Status:       " M\nforged-status",
				Path:         "new\nforged-path\tcell",
				OriginalPath: "old\tforged-original",
			}},
			Warning: "warning\nforged-warning\tcell",
		},
	}

	status := statusPanelContent(snapshot)
	for _, want := range []string{
		`thread: thread\nforged-thread\tcell`,
		`title: title\nforged-title\tcell`,
		`activity: running/working\nforged-status\tcell`,
		`project: /project\nforged-project`,
		`cwd: /cwd\tforged-cwd`,
		`model: model\nforged-model/provider\tforged-provider`,
		`branch: branch\nforged-branch`,
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("escaped status panel omits %q: %q", want, status)
		}
	}

	diff := diffPanelContent(snapshot)
	for _, want := range []string{
		`root: /root\nforged-root`,
		` M\nforged-status old\tforged-original -> new\nforged-path\tcell`,
		`warning: warning\nforged-warning\tcell`,
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("escaped diff panel omits %q: %q", want, diff)
		}
	}

	snapshot.Workspace.Git = codingworkspace.GitState{
		UnavailableReason: "reason\nforged-reason\tcell",
	}
	diff = diffPanelContent(snapshot)
	if !strings.Contains(diff, `Git unavailable: reason\nforged-reason\tcell`) {
		t.Fatalf("escaped unavailable reason = %q", diff)
	}
}

func TestTypedSlashCommandsAndLiteralSlashPrompt(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	result := runTypedSlashCommand(t, model, "/compact")
	model = updateModel(t, model, result)
	if controller.compacts.Load() != 1 || model.err != nil {
		t.Fatalf("compact result: calls=%d err=%v", controller.compacts.Load(), model.err)
	}
	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	enterPanelCommand(t, model, "/status")
	if !strings.Contains(model.View(), "last compaction: completed (blocking)") ||
		!strings.Contains(model.View(), "compaction tokens saved: 256") ||
		!strings.Contains(model.View(), "compaction continuation: work can continue") {
		t.Fatalf("compact did not converge through current view: %q", model.View())
	}

	result = runTypedSlashCommand(t, model, "/rename Parser cleanup")
	model = updateModel(t, model, result)
	controller.mu.Lock()
	renames := append([]string(nil), controller.renameTitles...)
	controller.mu.Unlock()
	if controller.renames.Load() != 1 || len(renames) != 1 || renames[0] != "Parser cleanup" {
		t.Fatalf("rename calls=%d titles=%v", controller.renames.Load(), renames)
	}
	model = updateModel(t, model, runTypedSlashCommand(t, model, "/archive"))
	if controller.archives.Load() != 1 || !controller.archived.Load() {
		t.Fatalf("archive calls=%d archived=%t", controller.archives.Load(), controller.archived.Load())
	}
	model = updateModel(t, model, runTypedSlashCommand(t, model, "/unarchive"))
	if controller.archives.Load() != 2 || controller.archived.Load() {
		t.Fatalf("unarchive calls=%d archived=%t", controller.archives.Load(), controller.archived.Load())
	}
	result = runTypedSlashCommand(t, model, "/new")
	model = updateModel(t, model, result)
	if controller.newThreads.Load() != 1 {
		t.Fatalf("new-thread calls=%d", controller.newThreads.Load())
	}

	model.composer.SetValue("//status is prompt text")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("escaped slash prompt was not submitted")
	}
	message, ok := command().(SubmitResultMsg)
	if !ok {
		t.Fatalf("escaped slash command result = %T", message)
	}
	model = updateModel(t, model, message)
	if prompts := controller.submittedPrompts(); len(prompts) != 1 || prompts[0] != "/status is prompt text" {
		t.Fatalf("escaped slash prompts = %v", prompts)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if model.ComposerValue() != "//status is prompt text" {
		t.Fatalf("escaped history prompt = %q", model.ComposerValue())
	}
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || model.commandPanel != commandPanelNone {
		t.Fatalf("recalled escaped prompt became a command: panel=%v command=%v", model.commandPanel, command)
	}
	message, ok = command().(SubmitResultMsg)
	if !ok {
		t.Fatalf("recalled escaped prompt result = %T", message)
	}
	model = updateModel(t, model, message)
	if prompts := controller.submittedPrompts(); len(prompts) != 2 || prompts[1] != "/status is prompt text" {
		t.Fatalf("recalled escaped prompts = %v", prompts)
	}

	model.composer.SetValue("/exit")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(*Model) != model {
		t.Fatal("/exit replaced model")
	}
	if command == nil {
		t.Fatal("/exit did not quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("/exit command = %T", command())
	}
}

func TestSlashMutationBlocksLaterSubmissionUntilAdmissionCompletes(t *testing.T) {
	controller := newController(t)
	controller.compactStart = make(chan struct{})
	compactRelease := make(chan struct{})
	controller.compactWait = compactRelease
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	model.composer.SetValue("/compact")
	updated, compactCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if compactCommand == nil || model.pendingSlashCommand != "compact" {
		t.Fatalf("compact admission state: command=%v pending=%q", compactCommand, model.pendingSlashCommand)
	}
	compactResult := make(chan tea.Msg, 1)
	go func() { compactResult <- compactCommand() }()
	select {
	case <-controller.compactStart:
	case <-time.After(time.Second):
		t.Fatal("compact command did not reach controller")
	}

	model.composer.SetValue("later prompt")
	updated, laterCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if laterCommand != nil || controller.submits.Load() != 0 || model.ComposerValue() != "later prompt" ||
		model.err == nil || !strings.Contains(model.err.Error(), "compact command is still running") {
		t.Fatalf(
			"later admission overtook compact: command=%v submits=%d draft=%q err=%v",
			laterCommand,
			controller.submits.Load(),
			model.ComposerValue(),
			model.err,
		)
	}

	close(compactRelease)
	select {
	case result := <-compactResult:
		model = updateModel(t, model, result)
	case <-time.After(time.Second):
		t.Fatal("compact command did not complete")
	}
	if model.pendingSlashCommand != "" {
		t.Fatalf("completed compact left pending command %q", model.pendingSlashCommand)
	}
	updated, laterCommand = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if laterCommand == nil || !model.submitting {
		t.Fatal("later prompt was not admitted after compact completion")
	}
	model = updateModel(t, model, laterCommand())
	if controller.submits.Load() != 1 || model.submitting || model.ComposerValue() != "" {
		t.Fatalf(
			"later submit result: calls=%d submitting=%v draft=%q",
			controller.submits.Load(),
			model.submitting,
			model.ComposerValue(),
		)
	}
}

func TestUnsupportedTypedCommandsAreActionable(t *testing.T) {
	controller := newController(t)
	controller.renameErr = frontend.ErrCommandUnsupported
	controller.newThreadErr = frontend.ErrCommandUnsupported
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	model = updateModel(t, model, runTypedSlashCommand(t, model, "/rename New title"))
	if model.err == nil || !strings.Contains(model.err.Error(), "current title is unchanged") {
		t.Fatalf("unsupported rename error = %v", model.err)
	}
	model = updateModel(t, model, runTypedSlashCommand(t, model, "/new"))
	if model.err == nil || !strings.Contains(model.err.Error(), "mintclaw code <prompt>") {
		t.Fatalf("unsupported new-thread error = %v", model.err)
	}
}

func enterPanelCommand(t *testing.T, model *Model, value string) {
	t.Helper()
	model.composer.SetValue(value)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil {
		t.Fatalf("panel command %q returned async command", value)
	}
	if updated.(*Model) != model {
		t.Fatal("panel command replaced model")
	}
}

func runTypedSlashCommand(t *testing.T, model *Model, value string) tea.Msg {
	t.Helper()
	model.composer.SetValue(value)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(*Model) != model || command == nil {
		t.Fatalf("typed command %q did not start", value)
	}
	return command()
}
