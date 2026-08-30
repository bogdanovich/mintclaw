package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func newTestModel(controller frontend.Controller) (*Model, error) {
	return NewModel(context.Background(), controller)
}

func TestComposerInheritsTerminalBackground(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}

	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{name: "focused cursor line", style: model.composer.FocusedStyle.CursorLine},
		{name: "focused text", style: model.composer.FocusedStyle.Text},
		{name: "blurred cursor line", style: model.composer.BlurredStyle.CursorLine},
		{name: "blurred text", style: model.composer.BlurredStyle.Text},
	}
	for _, tc := range styles {
		if background := tc.style.GetBackground(); background != (lipgloss.NoColor{}) {
			t.Errorf("%s forces background color %v", tc.name, background)
		}
	}
}

type fakeController struct {
	*frontend.Projector
	interrupts   atomic.Int32
	hardCancels  atomic.Int32
	closes       atomic.Int32
	submits      atomic.Int32
	mu           sync.Mutex
	prompts      []string
	submitErr    error
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
	return f.submitErr
}

func (f *fakeController) submittedPrompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
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

func TestComposerKeepsLargePastedDraftWhenSubmissionFails(t *testing.T) {
	controller := newController(t)
	controller.submitErr = errors.New("admission rejected")
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	draft := strings.Repeat("λ界👩🏽‍💻", 4_000) + "\nlast line"
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(draft), Paste: true})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	message := command()
	model = updateModel(t, model, message)
	if model.ComposerValue() != draft || model.err == nil || model.submitting || model.initialTurnPending {
		t.Fatalf(
			"failed submit state: draft bytes=%d want=%d err=%v submitting=%v pending=%v",
			len(model.ComposerValue()),
			len(draft),
			model.err,
			model.submitting,
			model.initialTurnPending,
		)
	}
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

func TestTranscriptRenderingIsCellBoundedAndSanitizesControls(t *testing.T) {
	entries := []frontend.TranscriptEntry{{
		ID:   "unicode",
		Kind: frontend.EntryAssistant,
		Text: "界 e\u0301 👩🏽‍💻 אבג \x1b[31mred\x1b[0m\x07 " + strings.Repeat("界", 20) +
			strings.Repeat("a", 30),
	}}
	tools := []frontend.ToolState{{
		CallID: "call-1", Name: "exec", Arguments: "SECRET-ARG", Output: "SECRET-OUTPUT", Status: frontend.ToolRunning,
	}}
	content, layout := renderTranscript(
		buildTranscriptView(entries, tools, nil, nil, "view:tool::call-1", ""),
		12,
		false,
		false,
		false,
	)
	if len(layout.blocks) != 2 || !strings.Contains(content, "▶ Tool") || !strings.Contains(content, "[running]") {
		t.Fatalf("semantic transcript = %q layout=%+v", content, layout)
	}
	if strings.Contains(content, "SECRET") || strings.Contains(content, "\x1b") || strings.Contains(content, "\x07") {
		t.Fatalf("unsafe terminal/tool content leaked: %q", content)
	}
	for _, line := range strings.Split(content, "\n") {
		if width := ansi.StringWidth(line); width > 12 {
			t.Fatalf("rendered line width=%d > 12: %q", width, line)
		}
	}
}

func TestTranscriptKeepsFinalAssistantAnswerAfterToolsAndRepositoryState(t *testing.T) {
	entries := []frontend.TranscriptEntry{
		{ID: "user", TurnID: "turn-1", Kind: frontend.EntryUser, Text: "inspect", Complete: true},
		{ID: "assistant", TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: "final answer", Complete: true},
	}
	tools := []frontend.ToolState{
		{TurnID: "turn-1", CallID: "call-1", Name: "exec", Status: frontend.ToolSucceeded},
	}
	workspace := &codingworkspace.Snapshot{ProjectRoot: "/work/project", CWD: "/work/project"}
	display := buildTranscriptView(entries, tools, nil, workspace, "", "")
	if len(display) != 4 {
		t.Fatalf("display entries = %+v", display)
	}
	wantIDs := []string{"user", "view:tool:turn-1:call-1", "view:workspace", "assistant"}
	for index, want := range wantIDs {
		if display[index].id != want {
			t.Fatalf("display[%d].id = %q, want %q; display=%+v", index, display[index].id, want, display)
		}
	}
}

func TestTranscriptKeepsCompletedAssistantAfterLaterTurnWarning(t *testing.T) {
	entries := []frontend.TranscriptEntry{
		{ID: "user", TurnID: "turn-1", Kind: frontend.EntryUser, Text: "inspect", Complete: true},
		{ID: "assistant", TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: "final answer", Complete: true},
		{ID: "fallback", TurnID: "turn-1", Kind: frontend.EntryWarning, Text: "provider fallback", Complete: true},
	}
	display := buildTranscriptView(entries, nil, nil, nil, "", "")
	wantIDs := []string{"user", "fallback", "assistant"}
	assertTranscriptViewIDs(t, display, wantIDs)
}

func TestTranscriptKeepsIncompleteAssistantBeforeLaterTurnError(t *testing.T) {
	entries := []frontend.TranscriptEntry{
		{ID: "user", TurnID: "turn-1", Kind: frontend.EntryUser, Text: "inspect", Complete: true},
		{ID: "assistant", TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: "partial answer"},
		{ID: "error", TurnID: "turn-1", Kind: frontend.EntryError, Text: "provider failed", Complete: true},
	}
	display := buildTranscriptView(entries, nil, nil, nil, "", "")
	wantIDs := []string{"user", "assistant", "error"}
	assertTranscriptViewIDs(t, display, wantIDs)
}

func TestTranscriptKeepsRepositoryStateWithNewestLiveTurn(t *testing.T) {
	entries := []frontend.TranscriptEntry{
		{ID: "user-1", TurnID: "turn-1", Kind: frontend.EntryUser, Text: "first", Complete: true},
		{ID: "assistant-1", TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: "done", Complete: true},
		{ID: "user-2", TurnID: "turn-2", Kind: frontend.EntryUser, Text: "second", Complete: true},
	}
	tools := []frontend.ToolState{
		{TurnID: "turn-2", CallID: "call-2", Name: "exec", Status: frontend.ToolRunning},
	}
	workspace := &codingworkspace.Snapshot{ProjectRoot: "/work/project", CWD: "/work/project"}
	display := buildTranscriptView(entries, tools, nil, workspace, "", "")
	wantIDs := []string{"user-1", "assistant-1", "user-2", "view:tool:turn-2:call-2", "view:workspace"}
	assertTranscriptViewIDs(t, display, wantIDs)
}

func assertTranscriptViewIDs(t *testing.T, display []transcriptViewEntry, want []string) {
	t.Helper()
	if len(display) != len(want) {
		t.Fatalf("display entries = %+v, want IDs %v", display, want)
	}
	for index, wantID := range want {
		if display[index].id != wantID {
			t.Fatalf("display[%d].id = %q, want %q; display=%+v", index, display[index].id, wantID, display)
		}
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
	if len(state.Entries) != 2 || state.Entries[1].Text != "working" {
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
	if view := model.View(); view == "" || len(snapshot.Entries) != 128 || !snapshot.HasOlderEntries {
		t.Fatalf(
			"long-history model is not bounded and renderable: entries=%d older=%v",
			len(snapshot.Entries),
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
	if len(update.Snapshot.Entries) != 1 || update.Snapshot.Entries[0].Text != "inspect" {
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
