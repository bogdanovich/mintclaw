package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/cases"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const transcriptSearchRunes = 256

type transcriptOverlayLine struct {
	key         string
	text        string
	logicalText string
	start       int
	end         int
}

type transcriptOverlayMatch struct {
	line   int
	key    string
	offset int
}

type transcriptOverlayState struct {
	active                  bool
	searching               bool
	help                    bool
	queryInput              textinput.Model
	query                   string
	matches                 []transcriptOverlayMatch
	matchIndex              int
	selected                int
	selectedKey             string
	selectedOffset          int
	offset                  int
	helpOffset              int
	followBottom            bool
	notice                  string
	copyRequestID           uint64
	lines                   []transcriptOverlayLine
	savedViewportPosition   viewportPosition
	savedComposerFocus      bool
	savedCommandPanel       commandPanel
	savedCommandPanelOffset int
}

func newTranscriptOverlayState() transcriptOverlayState {
	search := textinput.New()
	search.Prompt = "Find: "
	search.Placeholder = "text in transcript"
	search.CharLimit = transcriptSearchRunes
	terminalDefault := lipgloss.NewStyle()
	search.PromptStyle = terminalDefault.Bold(true)
	search.TextStyle = terminalDefault
	search.PlaceholderStyle = terminalDefault
	search.CompletionStyle = terminalDefault
	search.Cursor.Style = terminalDefault
	return transcriptOverlayState{
		queryInput: search,
		matchIndex: -1,
		selected:   -1,
	}
}

func (m *Model) openTranscriptOverlay() tea.Cmd {
	if m.transcriptOverlay.active {
		return nil
	}
	m.transcriptOverlay.active = true
	m.transcriptOverlay.searching = false
	m.transcriptOverlay.help = false
	m.transcriptOverlay.notice = ""
	m.transcriptOverlay.savedViewportPosition = m.captureViewportPosition()
	m.transcriptOverlay.savedComposerFocus = m.composer.Focused()
	m.transcriptOverlay.savedCommandPanel = m.commandPanel
	m.transcriptOverlay.savedCommandPanelOffset = m.commandPanelOffset
	m.transcriptOverlay.followBottom = true
	m.transcriptOverlay.queryInput.Blur()
	m.composer.Blur()
	m.syncTranscriptOverlay()
	return nil
}

func (m *Model) closeTranscriptOverlay() tea.Cmd {
	if !m.transcriptOverlay.active {
		return nil
	}
	savedPosition := m.transcriptOverlay.savedViewportPosition
	savedFocus := m.transcriptOverlay.savedComposerFocus
	savedPanel := m.transcriptOverlay.savedCommandPanel
	savedPanelOffset := m.transcriptOverlay.savedCommandPanelOffset
	m.transcriptOverlay.active = false
	m.transcriptOverlay.searching = false
	m.transcriptOverlay.help = false
	m.transcriptOverlay.copyRequestID++
	m.transcriptOverlay.lines = nil
	m.transcriptOverlay.matches = nil
	m.transcriptOverlay.matchIndex = -1
	m.transcriptOverlay.queryInput.Blur()
	m.commandPanel = savedPanel
	m.commandPanelOffset = savedPanelOffset
	m.refreshViewportAt(savedPosition)
	if savedFocus && m.focused {
		return m.composer.Focus()
	}
	return nil
}

func (m *Model) syncTranscriptOverlay() {
	if !m.transcriptOverlay.active {
		return
	}
	started := m.diagnosticTime()
	lines := m.transcriptOverlayLines()
	m.transcriptOverlay.lines = lines
	m.transcriptOverlay.sync(lines)
	m.transcriptOverlay.queryInput.Width = max(1, m.width)
	m.diagnostics.observeOverlayBuild(elapsedDiagnosticTime(started, m.diagnosticTime()))
}

func (state *transcriptOverlayState) sync(lines []transcriptOverlayLine) {
	if len(lines) == 0 {
		state.selected = -1
		state.selectedKey = ""
		state.selectedOffset = 0
		state.offset = 0
		state.matches = nil
		state.matchIndex = -1
		return
	}
	selected := -1
	if !state.followBottom && state.selectedKey != "" {
		selected = transcriptOverlayLineForAnchor(lines, state.selectedKey, state.selectedOffset)
	}
	anchorFound := selected >= 0
	if selected < 0 {
		if state.followBottom {
			selected = len(lines) - 1
		} else {
			selected = min(max(0, state.selected), len(lines)-1)
		}
	}
	state.selected = selected
	state.selectedKey = lines[selected].key
	if state.followBottom || !anchorFound {
		state.selectedOffset = lines[selected].start
	}
	state.recomputeMatches(lines)
}

func (state *transcriptOverlayState) recomputeMatches(lines []transcriptOverlayLine) {
	state.matches = nil
	query := cases.Fold().String(strings.TrimSpace(sanitizeTerminalText(state.query)))
	if query == "" {
		state.matchIndex = -1
		return
	}
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if _, duplicate := seen[line.key]; duplicate {
			continue
		}
		seen[line.key] = struct{}{}
		logicalText := transcriptOverlayLogicalLineText(line)
		offset, matched := transcriptFoldedMatchOffset(logicalText, query)
		if !matched {
			continue
		}
		matchLine := transcriptOverlayLineForAnchor(lines, line.key, offset)
		if matchLine >= 0 {
			state.matches = append(state.matches, transcriptOverlayMatch{
				line: matchLine, key: line.key, offset: offset,
			})
		}
	}
	if len(state.matches) == 0 {
		state.matchIndex = -1
		return
	}
	state.matchIndex = slices.IndexFunc(state.matches, func(match transcriptOverlayMatch) bool {
		return match.key == state.selectedKey && match.offset == state.selectedOffset
	})
}

func (state *transcriptOverlayState) selectLine(lines []transcriptOverlayLine, index int) {
	if len(lines) == 0 {
		return
	}
	state.selected = min(max(0, index), len(lines)-1)
	state.selectedKey = lines[state.selected].key
	state.selectedOffset = lines[state.selected].start
	state.followBottom = false
	if match := slices.IndexFunc(state.matches, func(match transcriptOverlayMatch) bool {
		return match.key == state.selectedKey && match.offset == state.selectedOffset
	}); match >= 0 {
		state.matchIndex = match
	}
}

func (state *transcriptOverlayState) selectMatch(lines []transcriptOverlayLine, match transcriptOverlayMatch) {
	selected := transcriptOverlayLineForAnchor(lines, match.key, match.offset)
	if selected < 0 {
		return
	}
	state.selected = selected
	state.selectedKey = match.key
	state.selectedOffset = match.offset
	state.followBottom = false
}

func transcriptOverlayLineForAnchor(lines []transcriptOverlayLine, key string, offset int) int {
	fallback := -1
	for index, line := range lines {
		if line.key != key {
			continue
		}
		fallback = index
		if line.start == line.end {
			if offset == line.start {
				return index
			}
			continue
		}
		if offset >= line.start && offset < line.end {
			return index
		}
	}
	return fallback
}

func transcriptFoldedMatchOffset(value, foldedQuery string) (int, bool) {
	if foldedQuery == "" {
		return 0, false
	}
	if asciiText(value) && asciiText(foldedQuery) {
		match := strings.Index(strings.ToLower(value), foldedQuery)
		return match, match >= 0
	}
	fold := cases.Fold()
	match := strings.Index(fold.String(value), foldedQuery)
	if match < 0 {
		return 0, false
	}
	foldedOffset := 0
	for sourceOffset, char := range value {
		if foldedOffset == match {
			return sourceOffset, true
		}
		foldedOffset += len(fold.String(string(char)))
		if foldedOffset > match {
			return sourceOffset, true
		}
	}
	return len(value), true
}

func asciiText(value string) bool {
	for index := range len(value) {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (state *transcriptOverlayState) moveMatch(lines []transcriptOverlayLine, direction int) {
	if len(state.matches) == 0 {
		state.notice = "No transcript matches"
		return
	}
	next := -1
	if direction > 0 {
		next = slices.IndexFunc(state.matches, func(match transcriptOverlayMatch) bool {
			return match.line > state.selected
		})
		if next < 0 {
			next = 0
		}
	} else {
		for index := len(state.matches) - 1; index >= 0; index-- {
			if state.matches[index].line < state.selected {
				next = index
				break
			}
		}
		if next < 0 {
			next = len(state.matches) - 1
		}
	}
	state.matchIndex = next
	state.selectMatch(lines, state.matches[next])
	state.notice = ""
}

func (state *transcriptOverlayState) applySearch(lines []transcriptOverlayLine) {
	state.query = strings.TrimSpace(sanitizeTerminalText(state.queryInput.Value()))
	state.searching = false
	state.queryInput.Blur()
	state.recomputeMatches(lines)
	if state.query == "" {
		state.notice = "Search cleared"
		return
	}
	if len(state.matches) == 0 {
		state.notice = fmt.Sprintf("No matches for %q", state.query)
		return
	}
	next := slices.IndexFunc(state.matches, func(match transcriptOverlayMatch) bool {
		return match.line >= state.selected
	})
	if next < 0 {
		next = 0
	}
	state.matchIndex = next
	state.selectMatch(lines, state.matches[next])
	state.notice = ""
}

func (m *Model) handleTranscriptOverlayKey(message tea.KeyMsg) (bool, tea.Cmd) {
	state := &m.transcriptOverlay
	if !state.active {
		return false, nil
	}
	lines := state.lines
	if state.searching {
		switch message.String() {
		case "esc":
			state.searching = false
			state.queryInput.SetValue(state.query)
			state.queryInput.Blur()
			return true, nil
		case "enter":
			started := m.diagnosticTime()
			state.applySearch(lines)
			m.diagnostics.observeTranscriptSearch(elapsedDiagnosticTime(started, m.diagnosticTime()))
			return true, nil
		}
		var command tea.Cmd
		state.queryInput, command = state.queryInput.Update(message)
		return true, command
	}

	pageSize := m.transcriptOverlayContentHeight()
	switch message.String() {
	case "ctrl+t", "esc", "q":
		return true, m.closeTranscriptOverlay()
	case "?":
		state.help = !state.help
		state.helpOffset = 0
		state.notice = ""
	case "/":
		state.help = false
		state.searching = true
		state.queryInput.SetValue(state.query)
		state.queryInput.CursorEnd()
		state.notice = ""
		return true, state.queryInput.Focus()
	case "ctrl+l":
		state.query = ""
		state.queryInput.SetValue("")
		state.matches = nil
		state.matchIndex = -1
		state.notice = "Search cleared"
	case "n":
		state.moveMatch(lines, 1)
	case "N":
		state.moveMatch(lines, -1)
	case "up", "k":
		if state.help {
			state.helpOffset = max(0, state.helpOffset-1)
		} else {
			state.selectLine(lines, state.selected-1)
		}
	case "down", "j":
		if state.help {
			state.helpOffset++
		} else {
			state.selectLine(lines, state.selected+1)
		}
	case "pgup":
		if state.help {
			state.helpOffset = max(0, state.helpOffset-pageSize)
		} else if state.selected == 0 && !m.transcript.loading &&
			!m.transcript.disabled && (m.transcript.hasOlder || m.snapshot.HasOlderEntries) {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				m.syncTranscriptOverlay()
				return true, transcriptPageCmd(m.ctx, pager, m.transcript.start, transcriptPageOlder)
			}
		} else {
			state.selectLine(lines, state.selected-pageSize)
		}
	case "pgdown":
		if state.help {
			state.helpOffset += pageSize
		} else {
			state.selectLine(lines, state.selected+pageSize)
		}
	case "home", "g":
		if state.help {
			state.helpOffset = 0
		} else {
			state.selectLine(lines, 0)
		}
	case "end", "G":
		if !state.help && m.transcript.hasNewer && !m.transcript.loading {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				state.followBottom = true
				m.syncTranscriptOverlay()
				return true, transcriptPageCmd(m.ctx, pager, -1, transcriptPageLatest)
			}
		}
		if state.help {
			state.helpOffset = len(transcriptOverlayHelpLines())
		} else {
			state.selectLine(lines, len(lines)-1)
			state.followBottom = true
		}
	case "c":
		if state.help || state.selected < 0 || state.selected >= len(lines) {
			state.notice = "No transcript line selected"
			break
		}
		state.copyRequestID++
		state.notice = "Copying selected line…"
		return true, transcriptCopyCmd(
			m.ctx,
			m.writeClipboardText,
			state.copyRequestID,
			"line",
			transcriptOverlayLogicalLineText(lines[state.selected]),
		)
	case "C":
		if state.help {
			state.notice = "Close help before copying"
			break
		}
		state.copyRequestID++
		state.notice = "Copying full transcript…"
		return true, transcriptCopyCmd(
			m.ctx,
			m.writeClipboardText,
			state.copyRequestID,
			"full transcript",
			strings.Join(transcriptOverlayLogicalLines(lines), "\n"),
		)
	}
	return true, nil
}

func transcriptOverlayLogicalLineText(line transcriptOverlayLine) string {
	if line.logicalText != "" || line.text == "" {
		return line.logicalText
	}
	return line.text
}

func transcriptOverlayLogicalLines(lines []transcriptOverlayLine) []string {
	logical := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if _, duplicate := seen[line.key]; duplicate {
			continue
		}
		seen[line.key] = struct{}{}
		logical = append(logical, transcriptOverlayLogicalLineText(line))
	}
	return logical
}

func transcriptCopyCmd(
	ctx context.Context,
	writer clipboardTextWriter,
	requestID uint64,
	scope string,
	text string,
) tea.Cmd {
	return func() tea.Msg {
		if writer == nil {
			return TranscriptCopyMsg{
				RequestID: requestID,
				Scope:     scope,
				Err:       errors.New("clipboard writer is unavailable"),
			}
		}
		return TranscriptCopyMsg{
			RequestID: requestID,
			Scope:     scope,
			Err:       writer(ctx, sanitizeTerminalText(text)),
		}
	}
}

func (m *Model) transcriptOverlayContentHeight() int {
	return max(1, m.height-2)
}

func (m *Model) transcriptOverlayView() string {
	state := &m.transcriptOverlay
	lines := state.lines
	if m.height <= 0 {
		return ""
	}
	header := fmt.Sprintf("Full transcript · line %d/%d", max(0, state.selected+1), len(lines))
	if state.query != "" {
		match := 0
		if state.matchIndex >= 0 {
			match = state.matchIndex + 1
		}
		header += fmt.Sprintf(" · find %q · match %d/%d", state.query, match, len(state.matches))
	}
	if state.help {
		header = "Full transcript help"
	}
	if state.searching {
		header = "Find transcript · Enter apply · Esc cancel"
	}
	if m.height == 1 {
		switch {
		case m.width <= 2:
			return clipLine("q", m.width)
		case m.width < 10:
			return clipLine("Esc", m.width)
		default:
			return clipLine("Esc close · Full transcript", m.width)
		}
	}
	if m.height == 2 {
		return strings.Join([]string{clipLine(header, m.width), clipLine(state.footer(m.width), m.width)}, "\n")
	}

	contentHeight := m.transcriptOverlayContentHeight()
	content := make([]string, 0, contentHeight)
	if state.help {
		help := transcriptOverlayHelpLines()
		maximum := max(0, len(help)-contentHeight)
		state.helpOffset = min(max(0, state.helpOffset), maximum)
		end := min(len(help), state.helpOffset+contentHeight)
		for _, line := range help[state.helpOffset:end] {
			content = append(content, clipLine(line, m.width))
		}
	} else {
		state.ensureSelectedVisible(contentHeight, len(lines))
		matchSet := make(map[int]struct{}, len(state.matches))
		for _, match := range state.matches {
			matchSet[match.line] = struct{}{}
		}
		end := min(len(lines), state.offset+contentHeight)
		for index := state.offset; index < end; index++ {
			prefix := "  "
			if index == state.selected {
				prefix = "› "
			} else if _, match := matchSet[index]; match {
				prefix = "• "
			}
			content = append(content, clipLine(prefix+lines[index].text, m.width))
		}
	}
	for len(content) < contentHeight {
		content = append(content, "")
	}

	footer := state.footer(m.width)
	view := append([]string{clipLine(header, m.width)}, content...)
	view = append(view, clipLine(footer, m.width))
	return strings.Join(view[:min(len(view), m.height)], "\n")
}

func (state *transcriptOverlayState) ensureSelectedVisible(height, lineCount int) {
	maximum := max(0, lineCount-height)
	state.offset = min(max(0, state.offset), maximum)
	if state.selected < state.offset {
		state.offset = state.selected
	}
	if state.selected >= state.offset+height {
		state.offset = state.selected - height + 1
	}
	state.offset = min(max(0, state.offset), maximum)
}

func (state *transcriptOverlayState) footer(width int) string {
	if state.searching {
		return state.queryInput.View()
	}
	if state.notice != "" {
		return "Esc close · ? help · " + state.notice
	}
	if state.help {
		return "Esc close · ↑/↓ or PgUp/PgDown scroll help · ? transcript"
	}
	if width < 64 {
		return "Esc close · / find · c copy · ? help"
	}
	return "Esc close · ↑/↓ line · PgUp/PgDown page · / find · n/N match · c line · C all · ? help"
}

func transcriptOverlayHelpLines() []string {
	return []string{
		"Transcript keyboard commands (no mouse or color required)",
		"↑/↓ or j/k       select one plain-text line",
		"PgUp/PgDown      move one page; PgUp at top loads earlier history",
		"Home/End or g/G  jump to the first or last retained line",
		"/                 edit a Unicode-aware case-insensitive search",
		"Enter/Esc         apply or cancel search editing",
		"n/N               select the next or previous matching line",
		"Ctrl+L            clear the active search",
		"c                 copy the selected plain-text line",
		"C                 copy all currently retained plain-text lines",
		"?                 toggle this help",
		"Esc, q, or Ctrl+T close and restore the prior panel, focus, and scroll",
	}
}
