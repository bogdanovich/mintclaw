// Package tui contains the interactive terminal frontend for coding threads.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const maxComposerHeight = 4

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
	Submission composerSubmission
	Err        error
}

// SteerResultMsg completes same-turn guidance admission. A failed admission
// keeps the draft available for retry or submission as the next turn.
type SteerResultMsg struct {
	Input frontend.SteerInput
	Draft string
	Err   error
}

// TranscriptPageMsg delivers optional canonical transcript hydration.
type TranscriptPageMsg struct {
	Page frontend.TranscriptPage
	Mode transcriptPageMode
	Err  error
}

// WorkspaceRefreshMsg completes an explicit repository observation.
type WorkspaceRefreshMsg struct {
	RequestID uint64
	Err       error
}

type RepositoryStatusMsg struct {
	RequestID uint64
	Err       error
}

type RepositoryDiffMsg struct {
	RequestID uint64
	Err       error
}

// ClipboardImageMsg completes one asynchronous system-clipboard image read.
type ClipboardImageMsg struct {
	Data []byte
	Err  error
}

// TranscriptCopyMsg reports one plain-text clipboard operation.
type TranscriptCopyMsg struct {
	RequestID uint64
	Scope     string
	Err       error
}

// Model is the bounded terminal view of one frontend controller. It never owns
// an agent runtime or canonical transcript state.
type Model struct {
	controller          frontend.Controller
	ctx                 context.Context
	snapshot            frontend.ThreadSnapshot
	cells               semanticCellStore
	hydratedCells       semanticCellStore
	staticCells         map[string]*staticSemanticCell
	document            semanticViewportDocument
	updates             <-chan frontend.ThreadSnapshot
	viewport            semanticViewport
	composer            textarea.Model
	transcript          transcriptWindow
	transcriptOverlay   transcriptOverlayState
	layout              cellLayout
	theme               cellTheme
	colorLevel          cellColorLevel
	working             workingIndicator
	keys                keyMap
	width               int
	height              int
	interruptPending    bool
	initialTurnPending  bool
	admittedLastTurn    *frontend.LastTurnOutcome
	focused             bool
	err                 error
	submitting          bool
	steering            bool
	pendingSlashCommand string
	composerHistory     []string
	historyIndex        int
	historyDraft        string
	refreshingWorkspace bool
	workspaceNotice     string
	commandPanel        commandPanel
	commandPanelOffset  int
	nextEvidenceRequest uint64
	activeEvidenceReq   uint64
	composerAttachments []composerAttachment
	pasteDirectory      string
	nextPasteNumber     int
	nextImageNumber     int
	nextSteerNumber     uint64
	readClipboardImage  clipboardImageReader
	writePasteFile      pasteFileWriter
	writeClipboardText  clipboardTextWriter
	clipboardPasteBusy  bool
	home                string
	diagnosticNow       func() time.Time
	firstPaintStarted   time.Time
	firstPaintRecorded  bool
	diagnostics         presentationDiagnosticsState
}

var _ tea.Model = (*Model)(nil)

// NewModel constructs a terminal model whose background frontend watches stop
// with the application context.
func NewModel(
	ctx context.Context,
	controller frontend.Controller,
) (*Model, error) {
	return newModel(ctx, controller, modelOptions{})
}

type modelOptions struct {
	motionMode    MotionMode
	interruptKeys []string
	now           func() time.Time
	diagnosticNow func() time.Time
	home          string
	theme         cellTheme
	copyText      clipboardTextWriter
}

func newModel(
	ctx context.Context,
	controller frontend.Controller,
	options modelOptions,
) (*Model, error) {
	diagnosticNow := options.diagnosticNow
	if diagnosticNow == nil {
		diagnosticNow = time.Now
	}
	firstPaintStarted := diagnosticNow()
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
	cells, err := newSemanticCellStore(snapshot.Items)
	if err != nil {
		return nil, fmt.Errorf("build semantic cell store: %w", err)
	}
	composer := textarea.New()
	configureComposerStyles(&composer)
	composer.ShowLineNumbers = false
	composer.Prompt = "› "
	composer.Placeholder = "Ask MintClaw to do anything"
	composer.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "shift+enter"),
		key.WithHelp("ctrl+j", "new line"),
	)
	composer.SetWidth(80)
	composer.SetHeight(1)
	composer.Focus()
	theme := options.theme
	if theme == cellThemeUnknown {
		theme = cellThemeDark
	}
	model := &Model{
		controller:         controller,
		ctx:                ctx,
		snapshot:           snapshot,
		cells:              cells,
		staticCells:        make(map[string]*staticSemanticCell),
		viewport:           newSemanticViewport(80, 18),
		composer:           composer,
		transcriptOverlay:  newTranscriptOverlayState(),
		width:              80,
		height:             24,
		theme:              theme,
		colorLevel:         currentCellColorLevel(),
		working:            newWorkingIndicator(options.motionMode, options.now),
		keys:               newKeyMap(options.interruptKeys),
		focused:            true,
		historyIndex:       -1,
		commandPanel:       initialCommandPanel(snapshot),
		readClipboardImage: readSystemClipboardImage,
		writePasteFile:     writePrivatePasteFile,
		writeClipboardText: options.copyText,
		home:               options.home,
		diagnosticNow:      diagnosticNow,
		firstPaintStarted:  firstPaintStarted,
	}
	if model.writeClipboardText == nil {
		model.writeClipboardText = writeSystemClipboardText
	}
	model.syncWorkingIndicator()
	model.updateSurfaceDimensions()
	model.refreshViewport()
	model.diagnostics.observeSnapshot(
		snapshot,
		elapsedDiagnosticTime(firstPaintStarted, model.diagnosticTime()),
		0,
	)
	return model, nil
}

func initialCommandPanel(snapshot frontend.ThreadSnapshot) commandPanel {
	if snapshot.Review == nil || snapshot.Review.Result == nil {
		return commandPanelNone
	}
	switch snapshot.Review.Phase {
	case codingreview.PhaseCompleted, codingreview.PhaseStale:
		return commandPanelReview
	default:
		return commandPanelNone
	}
}

func configureComposerStyles(composer *textarea.Model) {
	terminalDefault := lipgloss.NewStyle()
	prompt := terminalDefault.Bold(true)

	// bubbles/textarea defaults the focused cursor line to a forced white or
	// black background. Do not guess the terminal theme for the foreground,
	// either: inherit both colors so contrast follows the user's terminal.
	composer.FocusedStyle.Base = terminalDefault
	composer.FocusedStyle.CursorLine = terminalDefault
	composer.FocusedStyle.CursorLineNumber = terminalDefault
	composer.FocusedStyle.LineNumber = terminalDefault
	composer.FocusedStyle.Text = terminalDefault
	composer.FocusedStyle.Placeholder = terminalDefault
	composer.FocusedStyle.Prompt = prompt
	composer.FocusedStyle.EndOfBuffer = terminalDefault
	composer.BlurredStyle.Base = terminalDefault
	composer.BlurredStyle.CursorLine = terminalDefault
	composer.BlurredStyle.CursorLineNumber = terminalDefault
	composer.BlurredStyle.LineNumber = terminalDefault
	composer.BlurredStyle.Text = terminalDefault
	composer.BlurredStyle.Placeholder = terminalDefault
	composer.BlurredStyle.Prompt = prompt
	composer.BlurredStyle.EndOfBuffer = terminalDefault
}

func (m *Model) Init() tea.Cmd {
	commands := []tea.Cmd{
		textarea.Blink,
		subscribeCmd(m.ctx, m.controller),
	}
	if command := m.scheduleWorkingTick(); command != nil {
		commands = append(commands, command)
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
		return m, m.scheduleWorkingTick()
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
		return m, tea.Batch(nextSnapshotCmd(m.ctx, m.updates), m.scheduleWorkingTick())
	case SnapshotMsg:
		if message.Err != nil {
			m.err = message.Err
			return m, nil
		}
		if err := m.installSnapshot(message.Snapshot); err != nil {
			m.err = err
			return m, nil
		}
		return m, tea.Batch(nextSnapshotCmd(m.ctx, m.updates), m.scheduleWorkingTick())
	case workingTickMsg:
		if !m.working.acceptTick(message) {
			return m, nil
		}
		return m, m.scheduleWorkingTick()
	case TranscriptPageMsg:
		hydrationStarted := m.diagnosticTime()
		m.transcript.loading = false
		if message.Err != nil {
			m.diagnostics.observeHydration(
				elapsedDiagnosticTime(hydrationStarted, m.diagnosticTime()),
				message.Page.Entries,
				true,
			)
			if errors.Is(message.Err, frontend.ErrTranscriptPagingUnsupported) ||
				errors.Is(message.Err, frontend.ErrTranscriptHistoryChanged) {
				m.transcript = transcriptWindow{disabled: true}
				m.hydratedCells = semanticCellStore{}
				m.refreshViewport()
				m.syncTranscriptOverlay()
				return m, nil
			}
			m.err = message.Err
			m.syncTranscriptOverlay()
			return m, nil
		}
		m.transcript.apply(message.Page, message.Mode)
		hydrated, err := newHydratedSemanticCellStore(m.transcript.historical)
		if err != nil {
			m.diagnostics.observeHydration(
				elapsedDiagnosticTime(hydrationStarted, m.diagnosticTime()),
				message.Page.Entries,
				true,
			)
			m.err = fmt.Errorf("hydrate semantic transcript cells: %w", err)
			return m, nil
		}
		m.hydratedCells = hydrated
		m.refreshViewport()
		m.syncTranscriptOverlay()
		m.diagnostics.observeHydration(
			elapsedDiagnosticTime(hydrationStarted, m.diagnosticTime()),
			message.Page.Entries,
			false,
		)
		return m, nil
	case WorkspaceRefreshMsg:
		if message.RequestID == 0 || message.RequestID != m.activeEvidenceReq {
			return m, nil
		}
		m.activeEvidenceReq = 0
		refreshing := m.refreshingWorkspace
		m.refreshingWorkspace = false
		if !refreshing || m.err != nil {
			return m, nil
		}
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
	case RepositoryStatusMsg:
		if message.RequestID == 0 || message.RequestID != m.activeEvidenceReq {
			return m, nil
		}
		m.activeEvidenceReq = 0
		loading := m.workspaceNotice == "repository status loading"
		if loading {
			m.workspaceNotice = ""
		}
		if m.err != nil || !loading {
			return m, nil
		}
		if message.Err != nil {
			m.err = message.Err
			m.workspaceNotice = "repository status unavailable"
			return m, nil
		}
		m.workspaceNotice = "repository status refreshed"
		return m, nil
	case RepositoryDiffMsg:
		if message.RequestID == 0 || message.RequestID != m.activeEvidenceReq {
			return m, nil
		}
		m.activeEvidenceReq = 0
		loading := m.workspaceNotice == "repository diff loading"
		if loading {
			m.workspaceNotice = ""
		}
		if m.err != nil || !loading {
			return m, nil
		}
		if message.Err != nil {
			m.err = message.Err
			m.workspaceNotice = "repository diff unavailable"
			return m, nil
		}
		m.workspaceNotice = "repository diff refreshed"
		return m, nil
	case ClipboardImageMsg:
		m.clipboardPasteBusy = false
		if message.Err != nil {
			m.err = message.Err
			return m, nil
		}
		if err := m.addClipboardImage(message.Data); err != nil {
			m.err = err
			return m, nil
		}
		m.err = nil
		m.reflowComposer()
		return m, textarea.Blink
	case TranscriptCopyMsg:
		if !m.transcriptOverlay.active || message.RequestID != m.transcriptOverlay.copyRequestID {
			return m, nil
		}
		if message.Err != nil {
			m.transcriptOverlay.notice = "Copy failed: " + message.Err.Error()
		} else {
			m.transcriptOverlay.notice = "Copied " + message.Scope
		}
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
		m.pendingSlashCommand = ""
		if message.Err != nil {
			if message.Operation == "review" {
				m.commandPanel = commandPanelNone
				m.commandPanelOffset = 0
			}
			m.err = slashCommandError(message.Operation, message.Err)
		} else {
			m.err = nil
		}
		return m, nil
	case SubmitResultMsg:
		m.submitting = false
		if message.Err != nil {
			position := m.captureViewportPosition()
			m.initialTurnPending = false
			m.interruptPending = false
			m.err = message.Err
			m.syncWorkingIndicator()
			m.updateSurfaceDimensions()
			m.refreshViewportAt(position)
			return m, nil
		}
		m.err = nil
		if message.Submission.rememberDraft {
			m.rememberPrompt(message.Submission.draft)
		}
		m.clearSubmittedAttachments()
		m.composer.Reset()
		m.historyIndex = -1
		m.historyDraft = ""
		m.reflowComposer()
		return m, m.scheduleWorkingTick()
	case SteerResultMsg:
		m.submitting = false
		m.steering = false
		if message.Err != nil {
			m.err = fmt.Errorf("queue guidance: %w", message.Err)
			return m, nil
		}
		m.err = nil
		m.rememberPrompt(message.Draft)
		m.composer.Reset()
		m.historyIndex = -1
		m.historyDraft = ""
		m.reflowComposer()
		return m, m.scheduleWorkingTick()
	case tea.KeyMsg:
		if key.Matches(message, m.keys.interrupt) {
			return m.handleInterrupt()
		}
		if m.transcriptOverlay.active {
			if handled, command := m.handleTranscriptOverlayKey(message); handled {
				return m, command
			}
		}
		if handled, command := m.handleComposerKey(message); handled {
			return m, command
		}
	case tea.InterruptMsg:
		return m.handleInterrupt()
	case tea.FocusMsg:
		m.focused = true
		if m.transcriptOverlay.active {
			return m, m.scheduleWorkingTick()
		}
		m.composer.Focus()
		return m, tea.Batch(textarea.Blink, m.scheduleWorkingTick())
	case tea.BlurMsg:
		m.focused = false
		m.composer.Blur()
		m.working.stopTicks()
		return m, nil
	}

	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(message)
	commands = append(commands, command)
	m.composer, command = m.composer.Update(message)
	commands = append(commands, command)
	m.pruneDetachedAttachments()
	m.reflowComposer()
	return m, tea.Batch(commands...)
}

func (m *Model) View() string {
	if !m.firstPaintRecorded {
		defer m.observeFirstPaint()
	}
	if m.transcriptOverlay.active {
		return m.transcriptOverlayView()
	}
	status := m.statusLine()
	if m.clipboardPasteBusy {
		status = "reading clipboard image…"
	}
	if m.submitting {
		status = "submitting prompt…"
	}
	if m.steering {
		status = "queueing guidance…"
	}
	if m.pendingSlashCommand != "" {
		status = m.pendingSlashCommand + " command…"
	}
	if m.err != nil {
		status = "frontend error: " + m.err.Error()
	}
	if !m.focused {
		status = "terminal unfocused · " + status
	}
	if m.height <= 4 {
		return m.tinyView(status)
	}
	sections := []string{m.viewport.View()}
	if m.commandPanel != commandPanelNone {
		sections[0] = m.commandPanelView()
	}
	if working := m.workingLine(); working != "" {
		sections = append(sections, clipLine(working, m.width))
	}
	if pending := m.pendingGuidanceView(); pending != "" {
		sections = append(sections, pending)
	}
	sections = append(sections, m.composer.View(), clipLine(status, m.width))
	return strings.Join(sections, "\n")
}

func (m *Model) observeFirstPaint() {
	if m.firstPaintRecorded {
		return
	}
	m.diagnostics.FirstPaint = elapsedDiagnosticTime(m.firstPaintStarted, m.diagnosticTime())
	m.firstPaintRecorded = true
}

func (m *Model) ComposerValue() string {
	return m.composer.Value()
}

// TranscriptEntries exposes semantic view state for deterministic frontend
// tests without requiring full-screen golden snapshots.
func (m *Model) TranscriptEntries() []frontend.TranscriptEntry {
	return m.transcript.entries(m.snapshot.Messages())
}

// ViewportOffset reports the semantic transcript scroll position.
func (m *Model) ViewportOffset() int {
	return m.viewport.YOffset
}

func (m *Model) Snapshot() frontend.ThreadSnapshot {
	return m.snapshot.Clone()
}

// Diagnostics returns content-free presentation counters for debugging and
// performance regression evidence.
func (m *Model) Diagnostics() PresentationDiagnostics {
	return m.diagnostics.PresentationDiagnostics
}

func (m *Model) flushPresentationForShutdown() int {
	return m.cells.flushActiveForShutdown()
}

func (m *Model) installSnapshot(snapshot frontend.ThreadSnapshot) error {
	presentationStarted := m.diagnosticTime()
	if snapshot.ThreadID != m.snapshot.ThreadID {
		return errors.New("coding frontend snapshot changed thread ID")
	}
	position := m.captureViewportPosition()
	cells, stats, err := reconcileSemanticCellStoreWithStats(m.cells, snapshot.Items, true)
	if err != nil {
		return fmt.Errorf("update semantic cell store: %w", err)
	}
	if m.initialTurnResolvedBy(snapshot) {
		m.initialTurnPending = false
	}
	m.snapshot = snapshot
	m.cells = cells
	if !activeWork(snapshot.Activity) {
		m.interruptPending = false
	}
	m.syncWorkingIndicator()
	m.updateSurfaceDimensions()
	m.refreshViewportAt(position)
	m.syncTranscriptOverlay()
	m.diagnostics.observeSnapshot(
		snapshot,
		elapsedDiagnosticTime(presentationStarted, m.diagnosticTime()),
		stats.coalescedRevisions,
	)
	return nil
}

func (m *Model) Dimensions() (int, int) {
	return m.width, m.height
}

func (m *Model) admitInitialTurn() {
	position := m.captureViewportPosition()
	m.initialTurnPending = true
	m.admittedLastTurn = cloneLastTurn(m.snapshot.LastTurn)
	m.syncWorkingIndicator()
	m.updateSurfaceDimensions()
	m.refreshViewportAt(position)
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
	position := m.captureViewportPosition()
	m.width = max(1, width)
	m.height = max(1, height)
	m.composer.SetWidth(m.width)
	m.syncComposerDimensions()
	m.updateSurfaceDimensions()
	m.refreshViewportAt(position)
	m.syncTranscriptOverlay()
}

func (m *Model) updateSurfaceDimensions() {
	composerRows := m.composer.Height()
	workingRows := 0
	if m.workingSurfaceVisible() {
		workingRows = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(1, m.height-composerRows-workingRows-m.pendingGuidanceRows()-2)
}

func clipLine(value string, width int) string {
	value = strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
	if width <= 0 || ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func (m *Model) refreshViewport() {
	m.refreshViewportAt(m.captureViewportPosition())
}

type viewportPosition struct {
	followBottom bool
	anchor       transcriptAnchor
}

func (m *Model) captureViewportPosition() viewportPosition {
	return viewportPosition{
		followBottom: m.viewport.AtBottom(),
		anchor:       m.layout.anchorAt(m.viewport.YOffset),
	}
}

func (m *Model) refreshViewportAt(position viewportPosition) {
	started := m.diagnosticTime()
	state := m.snapshot
	m.reconcileStaticCells(state)
	m.document = reconcileSemanticViewportDocument(
		m.document,
		m.visibleSemanticCellSpecs(state),
		cellRenderContext{Width: m.viewport.Width, Theme: m.theme, ColorLevel: m.colorLevel},
	)
	m.viewport.setDocument(m.document)
	m.layout = m.document.layout
	if position.followBottom {
		m.viewport.GotoBottom()
	} else if line, ok := m.layout.lineFor(position.anchor); ok {
		m.viewport.SetYOffset(line)
	}
	m.diagnostics.observeRender(
		elapsedDiagnosticTime(started, m.diagnosticTime()),
		m.document,
		len(m.transcript.historical),
	)
}

func (m *Model) diagnosticTime() time.Time {
	if m != nil && m.diagnosticNow != nil {
		return m.diagnosticNow()
	}
	return time.Now()
}

func elapsedDiagnosticTime(started, finished time.Time) time.Duration {
	if finished.Before(started) {
		return 0
	}
	return finished.Sub(started)
}

func (m *Model) handleComposerKey(message tea.KeyMsg) (bool, tea.Cmd) {
	if m.submitting {
		return true, nil
	}
	if message.Type == tea.KeyCtrlV {
		if m.clipboardPasteBusy {
			return true, nil
		}
		m.clipboardPasteBusy = true
		m.err = nil
		return true, clipboardImageCmd(m.ctx, m.readClipboardImage)
	}
	if m.clipboardPasteBusy && message.Type == tea.KeyEnter {
		m.err = errors.New("wait for the clipboard image to finish loading")
		return true, nil
	}
	if message.Paste {
		handled, err := m.handleRichPaste(string(message.Runes))
		if err != nil {
			m.err = err
			return true, nil
		}
		if handled {
			m.err = nil
			m.reflowComposer()
			return true, textarea.Blink
		}
	}
	switch message.String() {
	case "esc":
		if m.commandPanel != commandPanelNone {
			m.commandPanel = commandPanelNone
			m.commandPanelOffset = 0
			m.err = nil
			return true, nil
		}
	case "pgdown":
		if m.commandPanel != commandPanelNone {
			m.scrollCommandPanel(1)
			return true, nil
		}
	case "ctrl+r":
		m.supersedeEvidenceRequest()
		if activeWork(m.snapshot.Activity) || m.initialTurnPending {
			m.workspaceNotice = "repository refresh is available when idle"
			return true, nil
		}
		refresher, ok := m.controller.(frontend.WorkspaceRefresher)
		if !ok {
			m.workspaceNotice = "repository refresh unavailable"
			return true, nil
		}
		m.err = nil
		m.refreshingWorkspace = true
		m.workspaceNotice = ""
		return true, workspaceRefreshCmd(m.ctx, refresher, m.beginEvidenceRequest())
	case "ctrl+t":
		m.err = nil
		return true, m.openTranscriptOverlay()
	case "enter":
		m.supersedeEvidenceRequest()
		if message.Paste {
			return true, nil
		}
		if m.pendingSlashCommand != "" {
			m.err = fmt.Errorf("%s command is still running", m.pendingSlashCommand)
			return true, nil
		}
		draft := m.composer.Value()
		m.pruneDetachedAttachments()
		if strings.TrimSpace(draft) == "" && len(m.composerAttachments) == 0 {
			m.err = errors.New("enter a coding prompt; use Ctrl+J for a new line")
			return true, nil
		}
		if handled, command := m.handleSlashCommand(draft); handled {
			m.reflowComposer()
			return true, command
		}
		if m.acceptsSteeringInput() {
			if len(m.composerAttachments) > 0 {
				m.err = errors.New("attachments cannot be queued as same-turn guidance; wait for the active turn")
				return true, nil
			}
			steerer, ok := m.controller.(frontend.Steerer)
			if !ok {
				m.err = errors.New("same-turn guidance is unavailable; wait for the active turn")
				return true, nil
			}
			m.nextSteerNumber++
			input := frontend.SteerInput{ID: fmt.Sprintf("tui-steer-%d", m.nextSteerNumber), Text: draft}
			m.submitting = true
			m.steering = true
			m.err = nil
			return true, steerCmd(m.ctx, steerer, input, draft)
		}
		submission := m.prepareSubmission(draft)
		m.submitting = true
		m.err = nil
		m.admitInitialTurn()
		return true, submitCmd(m.ctx, m.controller, submission)
	case "alt+up":
		if len(m.composerAttachments) > 0 {
			m.err = errors.New("history navigation is unavailable while attachments are pending")
			return true, nil
		}
		m.navigateHistory(-1)
		m.reflowComposer()
		return true, textarea.Blink
	case "alt+down":
		if len(m.composerAttachments) > 0 {
			m.err = errors.New("history navigation is unavailable while attachments are pending")
			return true, nil
		}
		m.navigateHistory(1)
		m.reflowComposer()
		return true, textarea.Blink
	case "alt+end":
		if m.transcript.hasNewer && !m.transcript.loading {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				return true, transcriptPageCmd(m.ctx, pager, -1, transcriptPageLatest)
			}
		}
	case "pgup":
		if m.commandPanel != commandPanelNone {
			m.scrollCommandPanel(-1)
			return true, nil
		}
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

func (m *Model) acceptsSteeringInput() bool {
	if strings.TrimSpace(m.snapshot.ActiveTurnID) == "" {
		return false
	}
	return m.snapshot.Activity == frontend.ActivityRunning ||
		m.snapshot.Activity == frontend.ActivityCompacting
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
		activity == frontend.ActivityReviewing || activity == frontend.ActivityInterrupting
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

func submitCmd(
	ctx context.Context,
	controller frontend.Controller,
	submission composerSubmission,
) tea.Cmd {
	return func() tea.Msg {
		if controller == nil {
			return SubmitResultMsg{
				Submission: submission,
				Err:        errors.New("coding controller is unavailable"),
			}
		}
		return SubmitResultMsg{
			Submission: submission,
			Err:        controller.Submit(ctx, submission.input),
		}
	}
}

func steerCmd(
	ctx context.Context,
	steerer frontend.Steerer,
	input frontend.SteerInput,
	draft string,
) tea.Cmd {
	return func() tea.Msg {
		if steerer == nil {
			return SteerResultMsg{
				Input: input,
				Draft: draft,
				Err:   errors.New("coding steering is unavailable"),
			}
		}
		return SteerResultMsg{Input: input, Draft: draft, Err: steerer.Steer(ctx, input)}
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

func workspaceRefreshCmd(
	ctx context.Context,
	refresher frontend.WorkspaceRefresher,
	requestID uint64,
) tea.Cmd {
	return func() tea.Msg {
		return WorkspaceRefreshMsg{RequestID: requestID, Err: refresher.RefreshWorkspace(ctx)}
	}
}

func repositoryStatusCmd(
	ctx context.Context,
	reader frontend.RepositoryEvidenceReader,
	requestID uint64,
) tea.Cmd {
	return func() tea.Msg {
		_, err := reader.RepositoryStatus(ctx)
		return RepositoryStatusMsg{RequestID: requestID, Err: err}
	}
}

func repositoryDiffCmd(
	ctx context.Context,
	reader frontend.RepositoryEvidenceReader,
	target codingworkspace.DiffTarget,
	requestID uint64,
) tea.Cmd {
	return func() tea.Msg {
		_, err := reader.RepositoryDiff(ctx, target)
		return RepositoryDiffMsg{RequestID: requestID, Err: err}
	}
}

func (m *Model) beginEvidenceRequest() uint64 {
	m.nextEvidenceRequest++
	if m.nextEvidenceRequest == 0 {
		m.nextEvidenceRequest++
	}
	m.activeEvidenceReq = m.nextEvidenceRequest
	return m.activeEvidenceReq
}

func (m *Model) supersedeEvidenceRequest() {
	if m.activeEvidenceReq == 0 {
		return
	}
	m.activeEvidenceReq = 0
	m.refreshingWorkspace = false
	if m.workspaceNotice == "repository status loading" || m.workspaceNotice == "repository diff loading" {
		m.workspaceNotice = ""
	}
}
