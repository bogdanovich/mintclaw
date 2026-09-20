package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func newTestModel(controller frontend.Controller) (*Model, error) {
	return NewModel(context.Background(), controller)
}

func TestComposerInheritsTerminalColors(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}

	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{name: "focused base", style: model.composer.FocusedStyle.Base},
		{name: "focused cursor line", style: model.composer.FocusedStyle.CursorLine},
		{name: "focused cursor line number", style: model.composer.FocusedStyle.CursorLineNumber},
		{name: "focused line number", style: model.composer.FocusedStyle.LineNumber},
		{name: "focused text", style: model.composer.FocusedStyle.Text},
		{name: "focused placeholder", style: model.composer.FocusedStyle.Placeholder},
		{name: "focused prompt", style: model.composer.FocusedStyle.Prompt},
		{name: "focused end of buffer", style: model.composer.FocusedStyle.EndOfBuffer},
		{name: "blurred base", style: model.composer.BlurredStyle.Base},
		{name: "blurred cursor line", style: model.composer.BlurredStyle.CursorLine},
		{name: "blurred cursor line number", style: model.composer.BlurredStyle.CursorLineNumber},
		{name: "blurred line number", style: model.composer.BlurredStyle.LineNumber},
		{name: "blurred text", style: model.composer.BlurredStyle.Text},
		{name: "blurred placeholder", style: model.composer.BlurredStyle.Placeholder},
		{name: "blurred prompt", style: model.composer.BlurredStyle.Prompt},
		{name: "blurred end of buffer", style: model.composer.BlurredStyle.EndOfBuffer},
	}
	for _, tc := range styles {
		if foreground := tc.style.GetForeground(); foreground != (lipgloss.NoColor{}) {
			t.Errorf("%s forces foreground color %v", tc.name, foreground)
		}
		if background := tc.style.GetBackground(); background != (lipgloss.NoColor{}) {
			t.Errorf("%s forces background color %v", tc.name, background)
		}
	}
}

func TestComposerInvitesAnyTask(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}

	const want = "Ask MintClaw to do anything"
	if model.composer.Placeholder != want {
		t.Fatalf("composer placeholder = %q, want %q", model.composer.Placeholder, want)
	}
	if model.composer.Prompt != "› " {
		t.Fatalf("composer prompt = %q, want %q", model.composer.Prompt, "› ")
	}
	if model.composer.Height() != 1 {
		t.Fatalf("idle composer height = %d, want 1", model.composer.Height())
	}
}

func TestAdaptiveHeightIdleSurfaceShowsCompactStartupStatus(t *testing.T) {
	controller := newController(t)
	controller.ThreadMetadataUpdated(frontend.ThreadMetadata{
		ProjectRoot: "/home/server/src/mintclaw",
		CWD:         "/home/server/src/mintclaw",
		Model:       "gpt-5.6-sol",
		Provider:    "openai",
	})
	controller.RuntimeStatusUpdated(frontend.RuntimeStatus{
		Version: "mintclaw v0.1.0-test (git: abcdef12)", Permission: frontend.PermissionFullAccess,
		Autonomy: frontend.AutonomyYolo,
	})
	model, err := newModel(
		t.Context(),
		controller,
		modelOptions{adaptiveHeight: true, home: "/home/server"},
	)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 24)

	view := model.View()
	if model.viewport.Height != 1 {
		t.Fatalf("empty adaptive viewport height = %d, want internal minimum 1", model.viewport.Height)
	}
	lines := strings.Split(view, "\n")
	if strings.HasPrefix(view, "\n") || len(lines) != 11 || lines[7] != "" || lines[9] != "" {
		t.Fatalf("idle adaptive view should contain status card, composer gaps, and footer, got %q", view)
	}
	for _, want := range []string{
		">_ MintClaw (v0.1.0-test)", "model:       gpt-5.6-sol", "directory:   ~/src/mintclaw",
		"permissions: YOLO mode", "Ask MintClaw to do anything",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("idle adaptive view omits %q: %q", want, view)
		}
	}
	for _, unwanted := range []string{"Repository changes", "Session", "thread-1"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("startup status includes full status detail %q: %q", unwanted, view)
		}
	}
}

func TestStartupStatusDismissesForWorkEscapeAndResumedThreads(t *testing.T) {
	newModelForTest := func(t *testing.T) *Model {
		t.Helper()
		controller := newController(t)
		controller.ThreadMetadataUpdated(frontend.ThreadMetadata{
			CWD: "/work/project", Model: "coding-model", Provider: "openai",
		})
		controller.RuntimeStatusUpdated(frontend.RuntimeStatus{
			Version: "mintclaw v1.2.3", Permission: frontend.PermissionFullAccess,
			Autonomy: frontend.AutonomyYolo,
		})
		model, err := newModel(t.Context(), controller, modelOptions{adaptiveHeight: true})
		if err != nil {
			t.Fatal(err)
		}
		model.resize(80, 24)
		return model
	}

	model := newModelForTest(t)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("inspect")})
	if !strings.Contains(model.View(), ">_ MintClaw") {
		t.Fatal("typing a draft hid the startup status before submission")
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || strings.Contains(model.View(), ">_ MintClaw") {
		t.Fatalf("submitted work retained startup status: command=%v view=%q", command, model.View())
	}

	model = newModelForTest(t)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(model.View(), ">_ MintClaw") {
		t.Fatalf("Esc did not dismiss startup status: %q", model.View())
	}

	controller := newController(t)
	controller.RuntimeStatusUpdated(frontend.RuntimeStatus{Resumed: true})
	resumed, err := newModel(t.Context(), controller, modelOptions{adaptiveHeight: true})
	if err != nil {
		t.Fatal(err)
	}
	resumed.resize(80, 24)
	if strings.Contains(resumed.View(), ">_ MintClaw") {
		t.Fatalf("resumed thread rendered a fresh-session card: %q", resumed.View())
	}
}

func TestStartupStatusYieldsToComposerOnConstrainedTerminals(t *testing.T) {
	model, err := newModel(t.Context(), newController(t), modelOptions{adaptiveHeight: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, dimensions := range [][2]int{{80, 10}, {23, 24}, {8, 4}} {
		model.resize(dimensions[0], dimensions[1])
		view := model.View()
		if strings.Contains(view, ">_ MintClaw") || !strings.Contains(view, "›") ||
			len(strings.Split(view, "\n")) > dimensions[1] {
			t.Fatalf("constrained %dx%d startup view = %q", dimensions[0], dimensions[1], view)
		}
	}
}

func TestAdaptiveHeightGrowsThenBoundsTranscript(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted("turn-short", "inspect")
	controller.AssistantAccumulated("turn-short", "Short answer.", true)
	controller.TurnCompleted("turn-short", "completed")
	model, err := newModel(t.Context(), controller, modelOptions{adaptiveHeight: true})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 24)

	if model.document.lineCount <= 1 || model.document.lineCount >= model.maximumViewportHeight() {
		t.Fatalf("short transcript lines = %d", model.document.lineCount)
	}
	if model.viewport.Height != model.document.lineCount {
		t.Fatalf(
			"short adaptive viewport height = %d, want content height %d",
			model.viewport.Height,
			model.document.lineCount,
		)
	}

	controller.TurnStarted("turn-long", "continue")
	controller.AssistantAccumulated("turn-long", strings.Repeat("additional output line\n", 64), true)
	controller.TurnCompleted("turn-long", "completed")
	snapshot, snapshotErr := controller.Snapshot(t.Context())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	if model.viewport.Height != model.maximumViewportHeight() {
		t.Fatalf(
			"long adaptive viewport height = %d, want bound %d",
			model.viewport.Height,
			model.maximumViewportHeight(),
		)
	}
	if rows := len(strings.Split(model.View(), "\n")); rows > model.height {
		t.Fatalf("bounded adaptive view emitted %d rows for terminal height %d", rows, model.height)
	}
}

type fakeController struct {
	*frontend.Projector
	interrupts   atomic.Int32
	hardCancels  atomic.Int32
	closes       atomic.Int32
	submits      atomic.Int32
	steerCalls   atomic.Int32
	mu           sync.Mutex
	prompts      []string
	inputs       []frontend.TurnInput
	steers       []frontend.SteerInput
	submitErr    error
	steerErr     error
	refreshes    atomic.Int32
	refreshErr   error
	refreshState *codingworkspace.Snapshot
	compacts     atomic.Int32
	renames      atomic.Int32
	archives     atomic.Int32
	archived     atomic.Bool
	newThreads   atomic.Int32
	compactErr   error
	compactStart chan struct{}
	compactWait  <-chan struct{}
	renameErr    error
	newThreadErr error
	renameTitles []string
}

func (f *fakeController) Submit(_ context.Context, input frontend.TurnInput) error {
	f.submits.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, input.Text)
	f.inputs = append(f.inputs, input.Clone())
	return f.submitErr
}

func (f *fakeController) submittedInputs() []frontend.TurnInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	inputs := make([]frontend.TurnInput, len(f.inputs))
	for index := range f.inputs {
		inputs[index] = f.inputs[index].Clone()
	}
	return inputs
}

func (f *fakeController) submittedPrompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
}

func (f *fakeController) Steer(_ context.Context, input frontend.SteerInput) error {
	f.steerCalls.Add(1)
	f.mu.Lock()
	f.steers = append(f.steers, input)
	err := f.steerErr
	f.mu.Unlock()
	if err == nil {
		snapshot, snapshotErr := f.Snapshot(context.Background())
		if snapshotErr == nil {
			f.SteeringAccepted(snapshot.ActiveTurnID, input)
		}
	}
	return err
}

func (f *fakeController) steeredInputs() []frontend.SteerInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.steers)
}

func (f *fakeController) RefreshWorkspace(context.Context) error {
	f.refreshes.Add(1)
	if f.refreshErr != nil {
		return f.refreshErr
	}
	if f.refreshState != nil {
		f.WorkspaceUpdated(*f.refreshState)
	}
	return nil
}

func (f *fakeController) Interrupt(context.Context) error {
	f.interrupts.Add(1)
	return nil
}

func (f *fakeController) HardCancel(context.Context) error {
	f.hardCancels.Add(1)
	return nil
}

func (f *fakeController) Compact(ctx context.Context) error {
	f.compacts.Add(1)
	if f.compactStart != nil {
		close(f.compactStart)
	}
	if f.compactWait != nil {
		select {
		case <-f.compactWait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.compactErr != nil {
		return f.compactErr
	}
	f.CompactionUpdate(frontend.CompactionState{
		Reason: "manual", Status: frontend.CompactionRunning,
	})
	f.CompactionUpdate(frontend.CompactionState{
		Reason: "manual", Status: frontend.CompactionCompleted, TokensSaved: 256,
	})
	return nil
}

func (f *fakeController) Rename(_ context.Context, title string) error {
	f.renames.Add(1)
	f.mu.Lock()
	f.renameTitles = append(f.renameTitles, title)
	f.mu.Unlock()
	return f.renameErr
}

func (f *fakeController) SetArchived(_ context.Context, archived bool) error {
	f.archives.Add(1)
	f.archived.Store(archived)
	return nil
}

func (f *fakeController) NewThread(context.Context) error {
	f.newThreads.Add(1)
	return f.newThreadErr
}

func (f *fakeController) Close(context.Context) error {
	f.closes.Add(1)
	return nil
}

func TestModelHandlesResizeAndMultilineBracketedPaste(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 43, Height: 11})
	if width, height := model.Dimensions(); width != 43 || height != 11 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
	model = updateModel(t, model, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("first line\n第二行 👩🏽‍💻"),
		Paste: true,
	})
	if got := model.ComposerValue(); got != "first line\n第二行 👩🏽‍💻" {
		t.Fatalf("composer value = %q", got)
	}
}

func TestComposerSubmitsMultilineUnicodeAndNavigatesHistory(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updateModel(t, model, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("第二行 👩🏽‍💻 e\u0301 שלום"),
	})
	prompt := "first\n第二行 👩🏽‍💻 e\u0301 שלום"
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || !model.submitting {
		t.Fatal("Enter did not start composer submission")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDelete})
	if model.ComposerValue() != prompt {
		t.Fatalf("pending submission mutated composer to %q", model.ComposerValue())
	}
	message, ok := command().(SubmitResultMsg)
	if !ok {
		t.Fatalf("submit command message = %T", message)
	}
	model = updateModel(t, model, message)
	if got := controller.submittedPrompts(); !slices.Equal(got, []string{prompt}) {
		t.Fatalf("submitted prompts = %#v", got)
	}
	if model.ComposerValue() != "" || model.submitting {
		t.Fatalf("successful submit left composer=%q submitting=%v", model.ComposerValue(), model.submitting)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new draft")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if got := model.ComposerValue(); got != prompt {
		t.Fatalf("history previous = %q", got)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	if got := model.ComposerValue(); got != "new draft" {
		t.Fatalf("history restored draft = %q", got)
	}
}

func TestComposerRemainsUsableDuringBackgroundCompaction(t *testing.T) {
	controller := newController(t)
	controller.CompactionUpdate(frontend.CompactionState{
		Reason: "proactive_budget", Status: frontend.CompactionRunning, Background: true,
	})
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	if model.snapshot.Activity != frontend.ActivityIdle {
		t.Fatalf("background compaction changed activity to %q", model.snapshot.Activity)
	}
	model.composer.SetValue("continue while context is compacted")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("background compaction blocked composer submission")
	}
	message, ok := command().(SubmitResultMsg)
	if !ok {
		t.Fatalf("submit command message = %T", message)
	}
	updateModel(t, model, message)
	if got := controller.submittedPrompts(); !slices.Equal(got, []string{"continue while context is compacted"}) {
		t.Fatalf("submitted prompts = %#v", got)
	}
}

func TestComposerSpoolsLargePasteAndKeepsItWhenSubmissionFails(t *testing.T) {
	controller := newController(t)
	controller.submitErr = errors.New("admission rejected")
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	draft := strings.Repeat("λ界👩🏽‍💻", 4_000) + "\nlast line"
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(draft), Paste: true})
	placeholder := fmt.Sprintf("[Pasted Content %d chars]", utf8.RuneCountInString(draft))
	if model.ComposerValue() != placeholder || len(model.composerAttachments) != 1 {
		t.Fatalf("rich paste composer=%q attachments=%d", model.ComposerValue(), len(model.composerAttachments))
	}
	attachment := model.composerAttachments[0]
	if attachment.input.ContentType != "text/plain; charset=utf-8" || !attachment.owned {
		t.Fatalf("rich paste attachment = %+v", attachment)
	}
	info, statErr := os.Stat(attachment.input.Path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("paste permissions=%v", info.Mode().Perm())
	}
	info, statErr = os.Stat(filepath.Dir(attachment.input.Path))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("paste directory permissions=%v", info.Mode().Perm())
	}
	stored, err := os.ReadFile(attachment.input.Path)
	if err != nil || string(stored) != draft {
		t.Fatalf("spooled paste bytes=%d err=%v", len(stored), err)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	message := command()
	model = updateModel(t, model, message)
	if model.ComposerValue() != placeholder || model.err == nil || model.submitting || model.initialTurnPending {
		t.Fatalf(
			"failed submit state: draft=%q err=%v submitting=%v pending=%v",
			model.ComposerValue(),
			model.err,
			model.submitting,
			model.initialTurnPending,
		)
	}
	if _, err = os.Stat(attachment.input.Path); err != nil {
		t.Fatalf("failed submission removed paste: %v", err)
	}
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || inputs[0].Text != "" || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].Path != attachment.input.Path {
		t.Fatalf("submitted rich input = %+v", inputs)
	}

	controller.submitErr = nil
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	_ = updateModel(t, model, command())
	if model.ComposerValue() != "" || len(model.composerAttachments) != 0 {
		t.Fatalf(
			"successful retry retained draft=%q attachments=%d",
			model.ComposerValue(),
			len(model.composerAttachments),
		)
	}
	if _, err = os.Stat(attachment.input.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful submission retained paste: %v", err)
	}
}

func TestComposerRemovingPlaceholderDropsOwnedPaste(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	paste := strings.Repeat("x", largePasteRuneThreshold+1)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	path := model.composerAttachments[0].input.Path
	model.composer.SetValue("")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("replacement")})
	if len(model.composerAttachments) != 0 {
		t.Fatalf("detached attachments = %+v", model.composerAttachments)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached paste still exists: %v", err)
	}
}

func TestComposerHistoryNavigationCannotDetachPendingAttachment(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model.composerHistory = []string{"older text prompt"}
	path := filepath.Join(t.TempDir(), "pending.log")
	if err = os.WriteFile(path, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = model.addComposerAttachment(path, "", "text/plain", false, false); err != nil {
		t.Fatal(err)
	}
	draft := model.ComposerValue()
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if model.ComposerValue() != draft || len(model.composerAttachments) != 1 ||
		model.composerAttachments[0].input.Path != path || model.historyIndex != -1 || model.err == nil {
		t.Fatalf(
			"history changed rich draft: draft=%q attachments=%+v index=%d err=%v",
			model.ComposerValue(),
			model.composerAttachments,
			model.historyIndex,
			model.err,
		)
	}
}

func TestComposerPastedImagePathBecomesStructuredAttachment(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "screen shot.png")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	if err = os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	pasted := fmt.Sprintf("%q", path)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	if model.ComposerValue() != "[Image #1]" {
		t.Fatalf("image placeholder = %q", model.ComposerValue())
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	_ = updateModel(t, model, command())
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || inputs[0].Text != "" || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].Path != path || inputs[0].Attachments[0].ContentType != "image/png" {
		t.Fatalf("submitted image input = %+v", inputs)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("caller-owned image was removed: %v", err)
	}
}

func TestComposerCtrlVPastesClipboardImageAsOwnedAttachment(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	imageBytes := testClipboardPNG(t)
	model.readClipboardImage = func(context.Context) ([]byte, error) {
		return append([]byte(nil), imageBytes...), nil
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(*Model)
	if command == nil || !model.clipboardPasteBusy || !strings.Contains(model.View(), "reading clipboard image") {
		t.Fatalf(
			"clipboard read did not start: command=%v busy=%t view=%q",
			command,
			model.clipboardPasteBusy,
			model.View(),
		)
	}
	updated, blocked := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if blocked != nil || !model.clipboardPasteBusy || model.err == nil {
		t.Fatalf(
			"submission was not blocked during clipboard read: command=%v busy=%t err=%v",
			blocked,
			model.clipboardPasteBusy,
			model.err,
		)
	}
	rawMessage := command()
	message, ok := rawMessage.(ClipboardImageMsg)
	if !ok {
		t.Fatalf("clipboard command message = %T", rawMessage)
	}
	model = updateModel(t, model, message)
	if model.clipboardPasteBusy || model.err != nil || model.ComposerValue() != "[Image #1]" ||
		len(model.composerAttachments) != 1 {
		t.Fatalf(
			"clipboard result: busy=%t err=%v draft=%q attachments=%+v",
			model.clipboardPasteBusy,
			model.err,
			model.ComposerValue(),
			model.composerAttachments,
		)
	}
	attachment := model.composerAttachments[0]
	if !attachment.owned || attachment.input.Filename != "pasted-image-1.png" ||
		attachment.input.ContentType != "image/png" {
		t.Fatalf("clipboard attachment = %+v", attachment)
	}
	stored, err := os.ReadFile(attachment.input.Path)
	if err != nil || !bytes.Equal(stored, imageBytes) {
		t.Fatalf("stored clipboard image bytes=%d err=%v", len(stored), err)
	}
	info, err := os.Stat(attachment.input.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("clipboard image permissions=%v", info.Mode().Perm())
	}
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	_ = updateModel(t, model, command())
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].ContentType != "image/png" {
		t.Fatalf("submitted clipboard input = %+v", inputs)
	}
	if _, err = os.Stat(attachment.input.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("submitted clipboard temporary file remains: %v", err)
	}
}

func TestComposerCtrlVReportsClipboardAndImageValidationErrorsWithoutMutation(t *testing.T) {
	truncatedPNG := testClipboardPNG(t)[:33]
	tests := []struct {
		name string
		read func(context.Context) ([]byte, error)
	}{
		{name: "clipboard unavailable", read: func(context.Context) ([]byte, error) {
			return nil, errors.New("clipboard unavailable")
		}},
		{name: "invalid PNG", read: func(context.Context) ([]byte, error) {
			return []byte("not a PNG"), nil
		}},
		{name: "truncated PNG body", read: func(context.Context) ([]byte, error) {
			return append([]byte(nil), truncatedPNG...), nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := newTestModel(newController(t))
			if err != nil {
				t.Fatal(err)
			}
			model.composer.SetValue("keep draft")
			model.readClipboardImage = test.read
			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
			model = updated.(*Model)
			model = updateModel(t, model, command())
			if model.clipboardPasteBusy || model.err == nil || model.ComposerValue() != "keep draft" ||
				len(model.composerAttachments) != 0 || model.pasteDirectory != "" {
				t.Fatalf(
					"failed clipboard read mutated composer: busy=%t err=%v draft=%q attachments=%+v dir=%q",
					model.clipboardPasteBusy,
					model.err,
					model.ComposerValue(),
					model.composerAttachments,
					model.pasteDirectory,
				)
			}
		})
	}
}

func TestComposerCtrlVRemovesPartialFileAfterWriteFailure(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	imageBytes := testClipboardPNG(t)
	model.readClipboardImage = func(context.Context) ([]byte, error) {
		return append([]byte(nil), imageBytes...), nil
	}
	injected := errors.New("injected partial write")
	var partialPath string
	model.writePasteFile = func(path string, data []byte, mode os.FileMode) error {
		partialPath = path
		if err := os.WriteFile(path, data[:len(data)/2], mode); err != nil {
			t.Fatal(err)
		}
		return injected
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(*Model)
	model = updateModel(t, model, command())
	if !errors.Is(model.err, injected) || model.ComposerValue() != "" || len(model.composerAttachments) != 0 {
		t.Fatalf(
			"partial write result: err=%v draft=%q attachments=%+v",
			model.err,
			model.ComposerValue(),
			model.composerAttachments,
		)
	}
	if _, err := os.Stat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial clipboard image remains: %v", err)
	}
	directory := model.pasteDirectory
	if err := model.closeRichInput(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private clipboard directory remains after close: %v", err)
	}
}

func TestComposerCtrlVPreservesLiteralImageLabelOnSubmission(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	const literal = "keep literal [Image #1] text"
	model.composer.SetValue(literal)
	model.readClipboardImage = func(context.Context) ([]byte, error) {
		return testClipboardPNG(t), nil
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(*Model)
	model = updateModel(t, model, command())
	if !strings.Contains(model.ComposerValue(), "[Image #2]") {
		t.Fatalf("collision-safe clipboard draft = %q", model.ComposerValue())
	}
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	_ = updateModel(t, model, command())
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || inputs[0].Text != literal || len(inputs[0].Attachments) != 1 {
		t.Fatalf("submitted collision-safe input = %+v", inputs)
	}
}

func testClipboardPNG(t *testing.T) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, 2, 2))
	value.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestComposerUnicodeCursorStaysWithinNarrowCellBounds(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 12, Height: 8})
	input := "界e\u0301👩🏽‍💻אבג界e\u0301"
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)})
	info := model.composer.LineInfo()
	if model.ComposerValue() != input {
		t.Fatalf("composer round trip = %q", model.ComposerValue())
	}
	if info.ColumnOffset < 0 || info.ColumnOffset > info.Width || info.Width > model.composer.Width() {
		t.Fatalf("cursor line info = %+v, composer width=%d", info, model.composer.Width())
	}
}

func TestUnsupportedOrChangedHistoryDisablesPagingWithoutFrontendError(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.transcript.loading = true
	model = updateModel(t, model, TranscriptPageMsg{Err: frontend.ErrTranscriptPagingUnsupported})
	if !model.transcript.disabled || model.transcript.loading || model.err != nil {
		t.Fatalf("unsupported paging state = %+v err=%v", model.transcript, model.err)
	}
	model.transcript = transcriptWindow{loading: true}
	model = updateModel(t, model, TranscriptPageMsg{Err: frontend.ErrTranscriptHistoryChanged})
	if !model.transcript.disabled || model.err != nil {
		t.Fatalf("changed history state = %+v err=%v", model.transcript, model.err)
	}
}

func TestModelUsesGracefulThenHardCancellation(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted("turn-1", "fix it")
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)

	updated, first := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if first == nil {
		t.Fatal("first Ctrl+C produced no interrupt command")
	}
	first()
	if controller.interrupts.Load() != 1 || controller.hardCancels.Load() != 0 {
		t.Fatalf(
			"after first Ctrl+C: interrupts=%d hard=%d",
			controller.interrupts.Load(),
			controller.hardCancels.Load(),
		)
	}
	controller.AssistantAccumulated("turn-1", "still streaming", false)
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	_, second := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if second == nil {
		t.Fatal("second Ctrl+C produced no hard-cancel command")
	}
	second()
	if controller.hardCancels.Load() != 1 {
		t.Fatalf("hard cancels = %d", controller.hardCancels.Load())
	}
}

func TestCtrlCClearsDraftBeforeIdleQuit(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("discard this draft")

	updated, first := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if model.ComposerValue() != "" {
		t.Fatalf("first Ctrl+C left draft %q", model.ComposerValue())
	}
	if controller.interrupts.Load() != 0 || controller.hardCancels.Load() != 0 {
		t.Fatalf(
			"draft clear touched controller: interrupts=%d hard=%d",
			controller.interrupts.Load(),
			controller.hardCancels.Load(),
		)
	}
	if first == nil {
		t.Fatal("first Ctrl+C did not restart the composer cursor")
	}

	_, second := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if second == nil {
		t.Fatal("second Ctrl+C produced no quit command")
	}
	if _, ok := second().(tea.QuitMsg); !ok {
		t.Fatal("second Ctrl+C did not quit the idle application")
	}
}

func TestScrollKeysNeverEnterComposer(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 12)
	model.viewport.setDocument(semanticViewportDocument{lineCount: 100})
	model.viewport.GotoBottom()
	model.composer.SetValue("keep this draft")

	bottom := model.viewport.YOffset
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyPgUp})
	if model.viewport.YOffset >= bottom || model.ComposerValue() != "keep this draft" {
		t.Fatalf("PageUp offset=%d draft=%q", model.viewport.YOffset, model.ComposerValue())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyPgDown})
	if model.viewport.YOffset != bottom || model.ComposerValue() != "keep this draft" {
		t.Fatalf("PageDown offset=%d/%d draft=%q", model.viewport.YOffset, bottom, model.ComposerValue())
	}

	rawWheel := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("[<64;35;23M[<66;35;23M"),
	}
	model = updateModel(t, model, rawWheel)
	if model.viewport.YOffset >= bottom || model.ComposerValue() != "keep this draft" {
		t.Fatalf("raw mouse offset=%d draft=%q", model.viewport.YOffset, model.ComposerValue())
	}
}

func TestModelQuitsBeforeControllerCleanupWhileIdle(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	_, quit := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("idle Ctrl+C produced no quit command")
	}
	message := quit()
	if _, ok := message.(tea.QuitMsg); !ok {
		t.Fatalf("idle Ctrl+C command returned %T, want tea.QuitMsg", message)
	}
	if controller.closes.Load() != 0 {
		t.Fatalf("model closed controller before terminal restoration: closes=%d", controller.closes.Load())
	}
}

func TestModelInterruptsAdmittedInitialTurnBeforeFirstView(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	model.admitInitialTurn()

	_, interrupt := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if interrupt == nil {
		t.Fatal("pending initial turn produced no interrupt command")
	}
	interrupt()
	if controller.interrupts.Load() != 1 || controller.closes.Load() != 0 {
		t.Fatalf("interrupts=%d closes=%d", controller.interrupts.Load(), controller.closes.Load())
	}
	controller.TurnStarted("turn-1", "fix it")
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	if model.initialTurnPending {
		t.Fatal("authoritative turn lifecycle did not clear pending admission")
	}
}

func TestSubscriptionReconcilesInitialTurnCompletedBeforeInit(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.admitInitialTurn()
	controller.TurnStarted("turn-1", "fix it")
	controller.TurnCompleted("turn-1", "completed")

	model = startModelSubscription(t, model)
	if model.initialTurnPending || model.Snapshot().LastTurn == nil ||
		model.Snapshot().LastTurn.Outcome != frontend.TurnOutcomeCompleted {
		t.Fatalf("completed-before-subscribe state = %+v pending=%v", model.Snapshot(), model.initialTurnPending)
	}
	_, quit := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("idle model did not quit after initial turn completed")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("idle Ctrl+C command returned unexpected message")
	}
	if controller.interrupts.Load() != 0 || controller.hardCancels.Load() != 0 {
		t.Fatalf(
			"completed turn was interrupted: interrupts=%d hard=%d",
			controller.interrupts.Load(),
			controller.hardCancels.Load(),
		)
	}
}

func TestModelConsumesLatestCoalescedView(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	controller.TurnStarted("turn-1", "fix it")
	controller.AssistantAccumulated("turn-1", "working", false)
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	state := model.Snapshot()
	if len(state.Messages()) != 2 || state.Messages()[1].Text != "working" {
		t.Fatalf("coalesced model view = %+v", state)
	}
}

func TestViewUpdateDoesNotClearCommandError(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	commandErr := errors.New("command failed")
	model = updateModel(t, model, CommandErrorMsg{Operation: "compact", Err: commandErr})
	controller.TurnStarted("turn-1", "fix it")
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	if !errors.Is(model.err, commandErr) {
		t.Fatalf("view update replaced command error with %v", model.err)
	}
}

func TestStreamingPreservesManualScrollAndFollowsBottom(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		turnID := fmt.Sprintf("turn-%d", i)
		projector.TurnStarted(turnID, strings.Repeat(fmt.Sprintf("question-%d ", i), 3))
		projector.AssistantAccumulated(turnID, strings.Repeat("answer ", 5), true)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	model.resize(24, 9)
	model.viewport.SetYOffset(5)
	anchor := model.layout.anchorAt(model.viewport.YOffset)
	projector.TurnStarted("streaming", "new question")
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	if got := model.layout.anchorAt(model.viewport.YOffset); got.id != anchor.id || got.offset != anchor.offset {
		t.Fatalf("manual scroll anchor moved from %+v to %+v", anchor, got)
	}

	model.viewport.GotoBottom()
	projector.AssistantAccumulated("streaming", strings.Repeat("streaming text ", 6), false)
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	if !model.viewport.AtBottom() {
		t.Fatalf("streaming while following bottom left offset=%d", model.viewport.YOffset)
	}
}

func TestMouseWheelRevealsEarlierActivityWithoutReplacingCompletedCells(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		turnID := fmt.Sprintf("turn-%d", i)
		projector.TurnStarted(turnID, fmt.Sprintf("question-%02d", i))
		projector.AssistantAccumulated(turnID, fmt.Sprintf("answer-%02d", i), true)
		projector.TurnCompleted(turnID, "completed")
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(40, 10)
	if !model.viewport.AtBottom() {
		t.Fatal("completed transcript did not initially follow the latest activity")
	}
	bottom := model.viewport.YOffset
	model = updateModel(t, model, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	if model.viewport.YOffset >= bottom {
		t.Fatalf("wheel up left viewport offset at %d, started at %d", model.viewport.YOffset, bottom)
	}
	if !strings.Contains(model.document.text(), "question-00") ||
		!strings.Contains(model.document.text(), "answer-11") {
		t.Fatalf("completed cells were replaced instead of retained: %q", model.document.text())
	}
	if !strings.Contains(model.statusLine(), "PgUp history") {
		t.Fatalf("scrollable transcript omitted keyboard fallback: %q", model.statusLine())
	}
}

func TestSnapshotUpdatePreservesComposerAndReferencedScrollAnchor(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		turnID := fmt.Sprintf("turn-%d", i)
		projector.TurnStarted(turnID, strings.Repeat("question ", 4))
		projector.AssistantAccumulated(turnID, strings.Repeat("answer ", 4), true)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(22, 9)
	model.composer.SetValue("unsubmitted 界 draft")
	model.viewport.SetYOffset(6)
	anchor := model.layout.anchorAt(model.viewport.YOffset)
	projector.TurnStarted("new-turn", "later")
	latest, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: latest})
	if model.ComposerValue() != "unsubmitted 界 draft" {
		t.Fatalf("snapshot replaced composer: %q", model.ComposerValue())
	}
	if got := model.layout.anchorAt(model.viewport.YOffset); got.id != anchor.id || got.offset != anchor.offset {
		t.Fatalf("snapshot scroll anchor moved from %+v to %+v", anchor, got)
	}
}

type pagedController struct {
	*fakeController
	pages    map[int]frontend.TranscriptPage
	requests []frontend.TranscriptPageRequest
}

func TestMouseWheelAtTopHydratesOlderTranscript(t *testing.T) {
	controller := &pagedController{
		fakeController: newController(t),
		pages: map[int]frontend.TranscriptPage{
			10: {
				Entries: []frontend.TranscriptEntry{{
					ID: "older", Kind: frontend.EntryWarning, Text: "older retained evidence",
				}},
				Start: 0, End: 10, Total: 11, HasNewer: true,
			},
		},
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(64, 10)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: []frontend.TranscriptEntry{{
			ID: "latest", Kind: frontend.EntryError, Text: "latest retained evidence",
		}},
		Start: 10, End: 11, Total: 11, HasOlder: true,
	}, Mode: transcriptPageInitial})
	model.viewport.GotoTop()
	updated, command := model.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	model = updated.(*Model)
	if command == nil || !model.transcript.loading {
		t.Fatalf("wheel did not start older hydration: command=%v loading=%t", command, model.transcript.loading)
	}
	model = updateModel(t, model, command())
	if len(controller.requests) != 1 || controller.requests[0].Before != 10 ||
		!strings.Contains(model.document.text(), "older retained evidence") {
		t.Fatalf("wheel hydration requests=%+v transcript=%q", controller.requests, model.document.text())
	}
}

func (p *pagedController) TranscriptPage(
	_ context.Context,
	request frontend.TranscriptPageRequest,
) (frontend.TranscriptPage, error) {
	p.requests = append(p.requests, request)
	return p.pages[request.Before], nil
}

func TestTranscriptHydrationPagesRemainBoundedAndPreserveLiveEntries(t *testing.T) {
	controller := newController(t)
	paged := &pagedController{fakeController: controller}
	model, err := newTestModel(paged)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	latest := makeTranscriptEntries("latest", 200)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: latest, Start: 100, End: 300, Total: 300, HasOlder: true,
	}, Mode: transcriptPageInitial})
	older := makeTranscriptEntries("older", 100)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: older, Start: 0, End: 100, Total: 300, HasNewer: true,
	}, Mode: transcriptPageOlder})
	if got := len(model.transcript.historical); got != maxHydratedTranscriptEntries {
		t.Fatalf("hydrated entries=%d, want %d", got, maxHydratedTranscriptEntries)
	}
	if !model.transcript.hasNewer || model.transcript.hasOlder {
		t.Fatalf("page flags older=%v newer=%v", model.transcript.hasOlder, model.transcript.hasNewer)
	}

	paged.TurnStarted("live-turn", "live prompt")
	model = updateModel(t, model, nextSnapshotCmd(t.Context(), model.updates)())
	entries := model.TranscriptEntries()
	if len(entries) != maxHydratedTranscriptEntries+1 || entries[len(entries)-1].Text != "live prompt" {
		t.Fatalf("merged transcript len=%d last=%+v", len(entries), entries[len(entries)-1])
	}
}

func TestTranscriptPageCommandRequestsBoundedWindow(t *testing.T) {
	controller := newController(t)
	paged := &pagedController{
		fakeController: controller,
		pages: map[int]frontend.TranscriptPage{
			42: {Start: 0, End: 42, Total: 42},
		},
	}
	message, ok := transcriptPageCmd(t.Context(), paged, 42, transcriptPageOlder)().(TranscriptPageMsg)
	if !ok {
		t.Fatalf("page command message has unexpected type")
	}
	if message.Mode != transcriptPageOlder || message.Page.End != 42 {
		t.Fatalf("page message = %+v", message)
	}
	if !slices.Equal(paged.requests, []frontend.TranscriptPageRequest{{Before: 42, Limit: transcriptPageSize}}) {
		t.Fatalf("page requests = %+v", paged.requests)
	}
}

func makeTranscriptEntries(prefix string, count int) []frontend.TranscriptEntry {
	entries := make([]frontend.TranscriptEntry, count)
	for i := range entries {
		entries[i] = frontend.TranscriptEntry{
			ID: prefix + "-" + fmt.Sprint(i), Kind: frontend.EntryAssistant, Text: fmt.Sprint(i), Complete: true,
		}
	}
	return entries
}

func TestModelKeepsLongBoundedHistoryUsableAtNarrowSize(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{Entries: 128})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		turnID := fmt.Sprintf("turn-%d", i)
		projector.TurnStarted(turnID, "question")
		projector.AssistantAccumulated(turnID, "answer", true)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 12, Height: 7})
	if view := model.View(); view == "" || len(snapshot.Messages()) != 128 || !snapshot.HasOlderEntries {
		t.Fatalf(
			"long-history model is not bounded and renderable: entries=%d older=%v",
			len(snapshot.Messages()),
			snapshot.HasOlderEntries,
		)
	}
}

func TestModelUsesActualTinyTerminalDimensions(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 1, Height: 1})
	if width, height := model.Dimensions(); width != 1 || height != 1 {
		t.Fatalf("dimensions = %dx%d, want 1x1", width, height)
	}
	if view := model.View(); view == "" || strings.Contains(view, "\n") {
		t.Fatalf("tiny view = %q", view)
	}
}

func TestNextSnapshotCommandUsesExistingSubscription(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	controller.TurnStarted("turn-1", "inspect")
	message := nextSnapshotCmd(t.Context(), model.updates)()
	update, ok := message.(SnapshotMsg)
	if !ok {
		t.Fatalf("subscription message = %T", message)
	}
	if len(update.Snapshot.Messages()) != 1 || update.Snapshot.Messages()[0].Text != "inspect" {
		t.Fatalf("subscription view = %+v", update.Snapshot)
	}
}

func TestModelTracksTerminalFocusWithoutChangingComposer(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
	model = updateModel(t, model, tea.BlurMsg{})
	if !strings.Contains(model.View(), "terminal unfocused") || model.ComposerValue() != "draft" {
		t.Fatalf("blurred view=%q composer=%q", model.View(), model.ComposerValue())
	}
	model = updateModel(t, model, tea.FocusMsg{})
	if strings.Contains(model.View(), "terminal unfocused") || model.ComposerValue() != "draft" {
		t.Fatalf("focused view=%q composer=%q", model.View(), model.ComposerValue())
	}
}

func newController(t *testing.T) *fakeController {
	t.Helper()
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeController{Projector: projector}
}

func updateModel(t *testing.T, model *Model, message tea.Msg) *Model {
	t.Helper()
	updated, _ := model.Update(message)
	result, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model = %T", updated)
	}
	return result
}

func startModelSubscription(t *testing.T, model *Model) *Model {
	t.Helper()
	message, ok := subscribeCmd(t.Context(), model.controller)().(SubscriptionMsg)
	if !ok {
		t.Fatalf("subscription command returned unexpected message")
	}
	return updateModel(t, model, message)
}
