// Package tui contains the interactive terminal frontend for coding threads.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const composerHeight = 4

type SnapshotMsg struct {
	Snapshot frontend.ThreadSnapshot
	Err      error
}

type SubscriptionMsg struct {
	Snapshot frontend.ThreadSnapshot
	Updates  <-chan frontend.ThreadSnapshot
	Err      error
}

// SubscriptionErrorMsg reports a frontend subscription failure to the update
// loop.
type SubscriptionErrorMsg struct {
	Err error
}

type CommandErrorMsg struct {
	Operation string
	Err       error
}

// CommandResultMsg completes one typed slash command. Authoritative success
// state arrives through the ordinary current-view subscription.
type CommandResultMsg struct {
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

// WorkspaceRefreshMsg completes an explicit repository observation.
type WorkspaceRefreshMsg struct {
	Err error
}

// Model is the bounded terminal view of one frontend controller. It never owns
// an agent runtime or canonical transcript state.
type Model struct {
	controller          frontend.Controller
	ctx                 context.Context
	snapshot            frontend.ThreadSnapshot
	updates             <-chan frontend.ThreadSnapshot
	viewport            viewport.Model
	composer            textarea.Model
	transcript          transcriptWindow
	layout              transcriptLayout
	width               int
	height              int
	interruptPending    bool
	initialTurnPending  bool
	admittedLastTurn    *frontend.LastTurnOutcome
	focused             bool
	err                 error
	submitting          bool
	composerHistory     []string
	historyIndex        int
	historyDraft        string
	selectedToolID      string
	expandedToolID      string
	refreshingWorkspace bool
	workspaceNotice     string
	commandPanel        commandPanel
}

var _ tea.Model = (*Model)(nil)

func NewModel(controller frontend.Controller) (*Model, error) {
	return NewModelWithContext(context.Background(), controller)
}

// NewModelWithContext constructs a terminal model whose background frontend
// watches stop with the application context.
func NewModelWithContext(
	ctx context.Context,
	controller frontend.Controller,
) (*Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if controller == nil {
		return nil, errors.New("coding frontend controller is required")
	}
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(snapshot.ThreadID) == "" {
		return nil, errors.New("coding frontend snapshot has no thread ID")
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
		snapshot:     snapshot,
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
		subscribeCmd(m.ctx, m.controller),
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
	case SubscriptionMsg:
		if message.Err != nil {
			m.err = message.Err
			return m, nil
		}
		if err := m.installSnapshot(message.Snapshot); err != nil {
			m.err = err
			return m, nil
		}
		m.updates = message.Updates
		return m, nextSnapshotCmd(m.ctx, m.updates)
	case SnapshotMsg:
		if message.Err != nil {
			m.err = message.Err
			return m, nil
		}
		if err := m.installSnapshot(message.Snapshot); err != nil {
			m.err = err
			return m, nil
		}
		return m, nextSnapshotCmd(m.ctx, m.updates)
	case TranscriptPageMsg:
		m.transcript.loading = false
		if message.Err != nil {
			if errors.Is(message.Err, frontend.ErrTranscriptPagingUnsupported) ||
				errors.Is(message.Err, frontend.ErrTranscriptHistoryChanged) {
				m.transcript = transcriptWindow{disabled: true}
				m.refreshViewport()
				return m, nil
			}
			m.err = message.Err
			return m, nil
		}
		m.transcript.apply(message.Page, message.Mode)
		m.refreshViewport()
		return m, nil
	case WorkspaceRefreshMsg:
		m.refreshingWorkspace = false
		if message.Err != nil {
			if errors.Is(message.Err, frontend.ErrWorkspaceRefreshUnsupported) {
				m.workspaceNotice = "repository refresh unavailable"
				return m, nil
			}
			m.err = message.Err
			return m, nil
		}
		m.err = nil
		m.workspaceNotice = "repository refreshed"
		return m, nil
	case SubscriptionErrorMsg:
		if message.Err != nil && !errors.Is(message.Err, context.Canceled) {
			m.err = message.Err
		}
		return m, nil
	case CommandErrorMsg:
		m.err = message.Err
		return m, nil
	case CommandResultMsg:
		if message.Err != nil {
			m.err = slashCommandError(message.Operation, message.Err)
		} else {
			m.err = nil
		}
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
	status := m.statusLine()
	if m.submitting {
		status = "submitting prompt…"
	}
	if m.err != nil {
		status = "frontend error: " + m.err.Error()
	}
	if !m.focused {
		status = "terminal unfocused · " + status
	}
	if m.height <= 2 {
		return clipLine(status, m.width)
	}
	if m.height <= 4 {
		return m.composer.View() + "\n" + clipLine(status, m.width)
	}
	body := m.viewport.View()
	if m.commandPanel != commandPanelNone {
		body = m.commandPanelView()
	}
	return body + "\n" + m.composer.View() + "\n" + clipLine(status, m.width)
}

func (m *Model) ComposerValue() string {
	return m.composer.Value()
}

// TranscriptEntries exposes semantic view state for deterministic frontend
// tests without requiring full-screen golden snapshots.
func (m *Model) TranscriptEntries() []frontend.TranscriptEntry {
	return m.transcript.entries(m.snapshot.Entries)
}

// ViewportOffset reports the semantic transcript scroll position.
func (m *Model) ViewportOffset() int {
	return m.viewport.YOffset
}

func (m *Model) Snapshot() frontend.ThreadSnapshot {
	return m.snapshot.Clone()
}

func (m *Model) installSnapshot(snapshot frontend.ThreadSnapshot) error {
	if snapshot.ThreadID != m.snapshot.ThreadID {
		return errors.New("coding frontend snapshot changed thread ID")
	}
	if m.initialTurnResolvedBy(snapshot) {
		m.initialTurnPending = false
	}
	m.snapshot = snapshot
	if !activeWork(snapshot.Activity) {
		m.interruptPending = false
	}
	m.refreshViewport()
	return nil
}

func (m *Model) Dimensions() (int, int) {
	return m.width, m.height
}

func (m *Model) admitInitialTurn() {
	m.initialTurnPending = true
	m.admittedLastTurn = cloneLastTurn(m.snapshot.LastTurn)
}

func (m *Model) initialTurnResolvedBy(snapshot frontend.ThreadSnapshot) bool {
	if !m.initialTurnPending {
		return false
	}
	if activeWork(snapshot.Activity) {
		return true
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
	value = strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
	if width <= 0 || ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func (m *Model) refreshViewport() {
	state := m.snapshot
	m.normalizeToolSelection(state.Tools)
	wasAtBottom := m.viewport.AtBottom()
	anchor := m.layout.anchorAt(m.viewport.YOffset)
	content, layout := renderTranscript(
		buildTranscriptView(
			m.transcript.entries(state.Entries),
			state.Tools,
			state.ChangedFiles,
			state.Workspace,
			m.selectedToolID,
			m.expandedToolID,
		),
		m.viewport.Width,
		!m.transcript.disabled && (m.transcript.hasOlder || state.HasOlderEntries),
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
	if m.submitting {
		return true, nil
	}
	switch message.String() {
	case "esc":
		if m.commandPanel != commandPanelNone {
			m.commandPanel = commandPanelNone
			m.err = nil
			return true, nil
		}
	case "ctrl+r":
		if activeWork(m.snapshot.Activity) || m.initialTurnPending {
			m.workspaceNotice = "repository refresh is available when idle"
			return true, nil
		}
		refresher, ok := m.controller.(frontend.WorkspaceRefresher)
		if !ok {
			m.workspaceNotice = "repository refresh unavailable"
			return true, nil
		}
		m.refreshingWorkspace = true
		m.workspaceNotice = ""
		return true, workspaceRefreshCmd(m.ctx, refresher)
	case "alt+j":
		m.navigateTools(1)
		return true, nil
	case "alt+k":
		m.navigateTools(-1)
		return true, nil
	case "ctrl+o":
		m.toggleSelectedTool()
		return true, nil
	case "enter":
		if message.Paste {
			return true, nil
		}
		prompt := m.composer.Value()
		if strings.TrimSpace(prompt) == "" {
			m.err = errors.New("enter a coding prompt; use Ctrl+J for a new line")
			return true, nil
		}
		if handled, command := m.handleSlashCommand(prompt); handled {
			return true, command
		}
		prompt = unescapeSlashPrompt(prompt)
		m.submitting = true
		m.err = nil
		m.admitInitialTurn()
		return true, submitCmd(m.ctx, m.controller, prompt)
	case "alt+up":
		m.navigateHistory(-1)
		return true, textarea.Blink
	case "alt+down":
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
	return false, nil
}

func (m *Model) normalizeToolSelection(tools []frontend.ToolState) {
	if len(tools) == 0 {
		m.selectedToolID = ""
		m.expandedToolID = ""
		return
	}
	for _, tool := range tools {
		if toolViewID(tool) == m.selectedToolID {
			return
		}
	}
	m.selectedToolID = toolViewID(tools[len(tools)-1])
	if m.expandedToolID != m.selectedToolID {
		m.expandedToolID = ""
	}
}

func (m *Model) navigateTools(direction int) {
	tools := m.snapshot.Tools
	if len(tools) == 0 {
		m.workspaceNotice = "no tool cards"
		return
	}
	m.normalizeToolSelection(tools)
	selected := 0
	for index, tool := range tools {
		if toolViewID(tool) == m.selectedToolID {
			selected = index
			break
		}
	}
	selected = (selected + direction + len(tools)) % len(tools)
	m.selectedToolID = toolViewID(tools[selected])
	m.expandedToolID = ""
	m.refreshViewport()
	m.focusSelectedTool()
}

func (m *Model) toggleSelectedTool() {
	tools := m.snapshot.Tools
	if len(tools) == 0 {
		m.workspaceNotice = "no tool cards"
		return
	}
	m.normalizeToolSelection(tools)
	selected := frontend.ToolState{}
	for _, tool := range tools {
		if toolViewID(tool) == m.selectedToolID {
			selected = tool
			break
		}
	}
	if !toolHasDisplayOutput(selected) {
		m.expandedToolID = ""
		m.workspaceNotice = "bounded tool output unavailable"
		m.refreshViewport()
		m.focusSelectedTool()
		return
	}
	if m.expandedToolID == m.selectedToolID {
		m.expandedToolID = ""
	} else {
		m.expandedToolID = m.selectedToolID
	}
	m.refreshViewport()
	m.focusSelectedTool()
}

func (m *Model) focusSelectedTool() {
	if line, ok := m.layout.lineFor(transcriptAnchor{id: m.selectedToolID, valid: true}); ok {
		m.viewport.SetYOffset(line)
	}
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
	activity := m.snapshot.Activity
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

func nextSnapshotCmd(
	ctx context.Context,
	updates <-chan frontend.ThreadSnapshot,
) tea.Cmd {
	if updates == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case snapshot, ok := <-updates:
			if !ok {
				return SubscriptionErrorMsg{Err: ctx.Err()}
			}
			return SnapshotMsg{Snapshot: snapshot}
		case <-ctx.Done():
			return SubscriptionErrorMsg{Err: ctx.Err()}
		}
	}
}

func subscribeCmd(ctx context.Context, controller frontend.Controller) tea.Cmd {
	if controller == nil {
		return nil
	}
	return func() tea.Msg {
		snapshot, updates, err := controller.Subscribe(ctx)
		return SubscriptionMsg{Snapshot: snapshot, Updates: updates, Err: err}
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

func typedCommandCmd(
	ctx context.Context,
	operation string,
	command func(context.Context) error,
) tea.Cmd {
	return func() tea.Msg {
		if command == nil {
			return CommandResultMsg{Operation: operation, Err: fmt.Errorf("coding controller is unavailable")}
		}
		return CommandResultMsg{Operation: operation, Err: command(ctx)}
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

func workspaceRefreshCmd(ctx context.Context, refresher frontend.WorkspaceRefresher) tea.Cmd {
	return func() tea.Msg {
		return WorkspaceRefreshMsg{Err: refresher.RefreshWorkspace(ctx)}
	}
}
