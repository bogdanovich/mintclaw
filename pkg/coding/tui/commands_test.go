package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestSlashHelpAndUnknownCommandState(t *testing.T) {
	controller := newController(t)
	model, err := NewModel(controller)
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
		"MintClaw coding commands", "/compact", "/rename <title>", "/new", "/exit",
		"Ctrl+J newline", "Ctrl+R refresh repository", "Esc close panel",
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
	model, err := NewModel(controller)
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

func TestTypedSlashCommandsAndLiteralSlashPrompt(t *testing.T) {
	controller := newController(t)
	model, err := NewModel(controller)
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
	if !strings.Contains(model.View(), "last compaction: completed, 256 tokens saved") {
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
	model, err := NewModel(controller)
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
	model, err := NewModel(controller)
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
