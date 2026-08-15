// Package tui contains the interactive terminal frontend for coding threads.
package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const composerHeight = 4

type SnapshotMsg struct {
	Snapshot frontend.ThreadSnapshot
	Err      error
}

type DeltaMsg struct {
	Delta frontend.Delta
}

// WatchErrorMsg reports a frontend subscription failure to the update loop.
type WatchErrorMsg struct {
	Err error
}

type CommandErrorMsg struct {
	Operation string
	Err       error
}

// SubmitResultMsg completes one composer submission without discarding a
// draft when controller admission fails.
type SubmitResultMsg struct {
	Prompt string
	Err    error
}

// TranscriptPageMsg delivers optional canonical transcript hydration.
type TranscriptPageMsg struct {
	Page frontend.TranscriptPage
	Mode transcriptPageMode
	Err  error
}

// Model is the bounded terminal view of one frontend controller. It never owns
// an agent runtime or canonical transcript state.
type Model struct {
	controller         frontend.Controller
	ctx                context.Context
	reducer            *frontend.Reducer
	viewport           viewport.Model
	composer           textarea.Model
	transcript         transcriptWindow
	layout             transcriptLayout
	width              int
	height             int
	interruptPending   bool
	initialTurnPending bool
	admittedRevision   frontend.Revision
	admittedLastTurn   *frontend.LastTurnOutcome
	resyncing          bool
	focused            bool
	err                error
	submitting         bool
	composerHistory    []string
	historyIndex       int
	historyDraft       string
}

var _ tea.Model = (*Model)(nil)

func NewModel(controller frontend.Controller, snapshot frontend.ThreadSnapshot) (*Model, error) {
	return NewModelWithContext(context.Background(), controller, snapshot)
}

// NewModelWithContext constructs a terminal model whose background frontend
// watches stop with the application context.
func NewModelWithContext(
	ctx context.Context,
	controller frontend.Controller,
	snapshot frontend.ThreadSnapshot,
) (*Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reducer, err := frontend.NewReducer(snapshot)
	if err != nil {
		return nil, err
	}
	composer := textarea.New()
	composer.ShowLineNumbers = false
	composer.Placeholder = "Describe the coding task…"
	composer.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "shift+enter"),
		key.WithHelp("ctrl+j", "new line"),
	)
	composer.SetHeight(composerHeight)
	composer.Focus()
	return &Model{
		controller:   controller,
		ctx:          ctx,
		reducer:      reducer,
		viewport:     viewport.New(80, 18),
		composer:     composer,
		width:        80,
		height:       24,
		focused:      true,
		historyIndex: -1,
	}, nil
}

func (m *Model) Init() tea.Cmd {
	commands := []tea.Cmd{
		textarea.Blink,
		nextDeltaCmd(m.ctx, m.controller, m.reducer.State().Revision),
	}
	if pager, ok := m.controller.(frontend.TranscriptPager); ok {
		m.transcript.loading = true
		commands = append(commands, transcriptPageCmd(m.ctx, pager, -1, transcriptPageInitial))
	}
	return tea.Batch(commands...)
}

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		return m, nil
	case SnapshotMsg:
		m.resyncing = false
		m.err = message.Err
		if message.Err == nil {
			m.err = m.reducer.ApplySnapshot(message.Snapshot)
			if m.initialTurnResolvedBy(message.Snapshot) {
				m.initialTurnPending = false
			}
			if m.err == nil && !activeWork(m.reducer.State().Activity) {
				m.interruptPending = false
			}
			m.refreshViewport()
		}
		if message.Err == nil {
			return m, nextDeltaCmd(m.ctx, m.controller, m.reducer.State().Revision)
		}
		return m, nil
	case DeltaMsg:
		if err := m.reducer.Apply(message.Delta); err != nil {
			if errors.Is(err, frontend.ErrRevisionGap) && m.controller != nil {
				m.resyncing = true
				return m, snapshotCmd(m.controller)
			}
			m.err = err
			return m, nil
		}
		if !activeWork(m.reducer.State().Activity) {
			m.interruptPending = false
		}
		if turnLifecycleDelta(message.Delta.Kind) {
			m.initialTurnPending = false
		}
		m.refreshViewport()
		return m, nextDeltaCmd(m.ctx, m.controller, m.reducer.State().Revision)
	case TranscriptPageMsg:
		m.transcript.loading = false
		if message.Err != nil {
			m.err = message.Err
			return m, nil
		}
		m.transcript.apply(message.Page, message.Mode)
		m.refreshViewport()
		return m, nil
	case WatchErrorMsg:
		if message.Err != nil && !errors.Is(message.Err, context.Canceled) {
			m.err = message.Err
		}
		return m, nil
	case CommandErrorMsg:
		m.err = message.Err
		return m, nil
	case SubmitResultMsg:
		m.submitting = false
		if message.Err != nil {
			m.initialTurnPending = false
			m.interruptPending = false
			m.err = message.Err
			return m, nil
		}
		m.err = nil
		m.rememberPrompt(message.Prompt)
		m.composer.Reset()
		m.historyIndex = -1
		m.historyDraft = ""
		return m, nil
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			return m.handleInterrupt()
		}
		if handled, command := m.handleComposerKey(message); handled {
			return m, command
		}
	case tea.InterruptMsg:
		return m.handleInterrupt()
	case tea.FocusMsg:
		m.focused = true
		m.composer.Focus()
		return m, textarea.Blink
	case tea.BlurMsg:
		m.focused = false
		m.composer.Blur()
		return m, nil
	}

	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(message)
	commands = append(commands, command)
	m.composer, command = m.composer.Update(message)
	commands = append(commands, command)
	return m, tea.Batch(commands...)
}

func (m *Model) View() string {
	status := m.reducer.State().Status
	if strings.TrimSpace(status) == "" {
		status = string(m.reducer.State().Activity)
	}
	if strings.TrimSpace(status) == "" {
		status = "idle"
	}
	if m.resyncing {
		status = "resynchronizing frontend…"
	}
	if m.submitting {
		status = "submitting prompt…"
	}
	if m.err != nil {
		status = "frontend error: " + m.err.Error()
	}
	if !m.focused {
		status += " · terminal unfocused"
	}
	if m.height <= 2 {
		return clipLine(status, m.width)
	}
	if m.height <= 4 {
		return m.composer.View() + "\n" + clipLine(status, m.width)
	}
	return m.viewport.View() + "\n" + m.composer.View() + "\n" + clipLine(status, m.width)
}

func (m *Model) ComposerValue() string {
	return m.composer.Value()
}

// TranscriptEntries exposes semantic view state for deterministic frontend
// tests without requiring full-screen golden snapshots.
func (m *Model) TranscriptEntries() []frontend.TranscriptEntry {
	return m.transcript.entries(m.reducer.State().Entries)
}

// ViewportOffset reports the semantic transcript scroll position.
func (m *Model) ViewportOffset() int {
	return m.viewport.YOffset
}

func (m *Model) Snapshot() frontend.ThreadSnapshot {
	return m.reducer.State()
}

func (m *Model) Dimensions() (int, int) {
	return m.width, m.height
}

func (m *Model) admitInitialTurn() {
	m.initialTurnPending = true
	state := m.reducer.State()
	m.admittedRevision = state.Revision
	m.admittedLastTurn = cloneLastTurn(state.LastTurn)
}

func (m *Model) initialTurnResolvedBy(snapshot frontend.ThreadSnapshot) bool {
	if !m.initialTurnPending {
		return false
	}
	if activeWork(snapshot.Activity) {
		return true
	}
	if snapshot.Revision <= m.admittedRevision {
		return false
	}
	return !sameLastTurn(snapshot.LastTurn, m.admittedLastTurn)
}

func cloneLastTurn(last *frontend.LastTurnOutcome) *frontend.LastTurnOutcome {
	if last == nil {
		return nil
	}
	cloned := *last
	return &cloned
}

func sameLastTurn(left, right *frontend.LastTurnOutcome) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (m *Model) resize(width, height int) {
	m.width = max(1, width)
	m.height = max(1, height)
	composerRows := min(composerHeight, max(1, m.height/3))
	m.viewport.Width = m.width
	m.viewport.Height = max(1, m.height-composerRows-2)
	m.composer.SetWidth(m.width)
	m.composer.SetHeight(composerRows)
	m.refreshViewport()
}

func clipLine(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if width <= 0 || len([]rune(value)) <= width {
		return value
	}
	runes := []rune(value)
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func (m *Model) refreshViewport() {
	state := m.reducer.State()
	wasAtBottom := m.viewport.AtBottom()
	anchor := m.layout.anchorAt(m.viewport.YOffset)
	content, layout := renderTranscript(
		transcriptDisplayEntries(m.transcript.entries(state.Entries), state.Tools),
		m.viewport.Width,
		m.transcript.hasOlder || state.HasOlderEntries,
		m.transcript.hasNewer,
		m.transcript.loading,
	)
	m.viewport.SetContent(strings.TrimSpace(content))
	m.layout = layout
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else if line, ok := layout.lineFor(anchor); ok {
		m.viewport.SetYOffset(line)
	}
}

func (m *Model) handleComposerKey(message tea.KeyMsg) (bool, tea.Cmd) {
	switch message.String() {
	case "enter":
		if message.Paste || m.submitting {
			return true, nil
		}
		prompt := m.composer.Value()
		if strings.TrimSpace(prompt) == "" {
			m.err = errors.New("enter a coding prompt; use Ctrl+J for a new line")
			return true, nil
		}
		m.submitting = true
		m.err = nil
		m.admitInitialTurn()
		return true, submitCmd(m.ctx, m.controller, prompt)
	case "alt+up":
		if m.submitting {
			return true, nil
		}
		m.navigateHistory(-1)
		return true, textarea.Blink
	case "alt+down":
		if m.submitting {
			return true, nil
		}
		m.navigateHistory(1)
		return true, textarea.Blink
	case "alt+end":
		if m.transcript.hasNewer && !m.transcript.loading {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				return true, transcriptPageCmd(m.ctx, pager, -1, transcriptPageLatest)
			}
		}
	case "pgup":
		if m.viewport.AtTop() && m.transcript.hasOlder && !m.transcript.loading {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				return true, tea.Batch(
					transcriptPageCmd(m.ctx, pager, m.transcript.start, transcriptPageOlder),
					func() tea.Msg { return message },
				)
			}
		}
	}
	if m.submitting && message.Type == tea.KeyRunes {
		return true, nil
	}
	return false, nil
}

func (m *Model) rememberPrompt(prompt string) {
	if len(m.composerHistory) == 0 || m.composerHistory[len(m.composerHistory)-1] != prompt {
		m.composerHistory = append(m.composerHistory, prompt)
	}
	const maxComposerHistory = 100
	if len(m.composerHistory) > maxComposerHistory {
		m.composerHistory = append([]string(nil), m.composerHistory[len(m.composerHistory)-maxComposerHistory:]...)
	}
}

func (m *Model) navigateHistory(direction int) {
	if len(m.composerHistory) == 0 {
		return
	}
	if m.historyIndex < 0 {
		if direction > 0 {
			return
		}
		m.historyDraft = m.composer.Value()
		m.historyIndex = len(m.composerHistory) - 1
	} else {
		next := m.historyIndex + direction
		switch {
		case next >= len(m.composerHistory):
			m.historyIndex = -1
		case next < 0:
			m.historyIndex = 0
		default:
			m.historyIndex = next
		}
	}
	if m.historyIndex < 0 {
		m.composer.SetValue(m.historyDraft)
		return
	}
	m.composer.SetValue(m.composerHistory[m.historyIndex])
}

func (m *Model) handleInterrupt() (tea.Model, tea.Cmd) {
	if m.controller == nil {
		return m, tea.Quit
	}
	activity := m.reducer.State().Activity
	if activeWork(activity) || m.initialTurnPending {
		if m.interruptPending || activity == frontend.ActivityInterrupting {
			return m, commandCmd("hard_cancel", m.controller.HardCancel)
		}
		m.interruptPending = true
		return m, commandCmd("interrupt", m.controller.Interrupt)
	}
	return m, tea.Quit
}

func activeWork(activity frontend.Activity) bool {
	return activity == frontend.ActivityRunning || activity == frontend.ActivityCompacting ||
		activity == frontend.ActivityInterrupting
}

func turnLifecycleDelta(kind frontend.DeltaKind) bool {
	switch kind {
	case frontend.DeltaTurnStarted,
		frontend.DeltaTurnCompleted,
		frontend.DeltaTurnSuspended,
		frontend.DeltaTurnFailed,
		frontend.DeltaTurnInterrupted:
		return true
	default:
		return false
	}
}

func snapshotCmd(controller frontend.Controller) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := controller.Snapshot(context.Background())
		return SnapshotMsg{Snapshot: snapshot, Err: err}
	}
}

func nextDeltaCmd(
	ctx context.Context,
	controller frontend.Controller,
	revision frontend.Revision,
) tea.Cmd {
	if controller == nil {
		return nil
	}
	return func() tea.Msg {
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		updates, err := controller.Watch(watchCtx, revision)
		if err != nil {
			if errors.Is(err, frontend.ErrRevisionUnavailable) {
				snapshot, snapshotErr := controller.Snapshot(ctx)
				return SnapshotMsg{Snapshot: snapshot, Err: snapshotErr}
			}
			return WatchErrorMsg{Err: err}
		}
		select {
		case delta, ok := <-updates:
			if !ok {
				return WatchErrorMsg{Err: watchCtx.Err()}
			}
			return DeltaMsg{Delta: delta}
		case <-ctx.Done():
			return WatchErrorMsg{Err: ctx.Err()}
		}
	}
}

func commandCmd(operation string, command func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		if err := command(context.Background()); err != nil {
			return CommandErrorMsg{Operation: operation, Err: err}
		}
		return nil
	}
}

func submitCmd(ctx context.Context, controller frontend.Controller, prompt string) tea.Cmd {
	return func() tea.Msg {
		if controller == nil {
			return SubmitResultMsg{Prompt: prompt, Err: errors.New("coding controller is unavailable")}
		}
		return SubmitResultMsg{Prompt: prompt, Err: controller.Submit(ctx, prompt)}
	}
}

func transcriptPageCmd(
	ctx context.Context,
	pager frontend.TranscriptPager,
	before int,
	mode transcriptPageMode,
) tea.Cmd {
	return func() tea.Msg {
		page, err := pager.TranscriptPage(ctx, frontend.TranscriptPageRequest{
			Before: before,
			Limit:  transcriptPageSize,
		})
		return TranscriptPageMsg{Page: page, Mode: mode, Err: err}
	}
}
