package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const transcriptSearchRunes = 256

type transcriptOverlayLine struct {
	key  string
	text string
}

type transcriptOverlayState struct {
	active                  bool
	searching               bool
	help                    bool
	queryInput              textinput.Model
	query                   string
	matches                 []int
	matchIndex              int
	selected                int
	selectedKey             string
	offset                  int
	helpOffset              int
	followBottom            bool
	notice                  string
	copyRequestID           uint64
	savedViewportPosition   viewportPosition
	savedComposerFocus      bool
	savedToolSelection      bool
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
	m.transcriptOverlay.savedToolSelection = m.toolSelectionActive
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
	savedToolSelection := m.transcriptOverlay.savedToolSelection
	savedPanel := m.transcriptOverlay.savedCommandPanel
	savedPanelOffset := m.transcriptOverlay.savedCommandPanelOffset
	m.transcriptOverlay.active = false
	m.transcriptOverlay.searching = false
	m.transcriptOverlay.help = false
	m.transcriptOverlay.copyRequestID++
	m.transcriptOverlay.queryInput.Blur()
	m.toolSelectionActive = savedToolSelection
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
	lines := m.transcriptOverlayLines()
	m.transcriptOverlay.sync(lines)
	m.transcriptOverlay.queryInput.Width = max(1, m.width)
}

func (state *transcriptOverlayState) sync(lines []transcriptOverlayLine) {
	if len(lines) == 0 {
		state.selected = -1
		state.selectedKey = ""
		state.offset = 0
		state.matches = nil
		state.matchIndex = -1
		return
	}
	selected := -1
	if !state.followBottom && state.selectedKey != "" {
		selected = slices.IndexFunc(lines, func(line transcriptOverlayLine) bool {
			return line.key == state.selectedKey
		})
	}
	if selected < 0 {
		if state.followBottom {
			selected = len(lines) - 1
		} else {
			selected = min(max(0, state.selected), len(lines)-1)
		}
	}
	state.selected = selected
	state.selectedKey = lines[selected].key
	state.recomputeMatches(lines)
}

func (state *transcriptOverlayState) recomputeMatches(lines []transcriptOverlayLine) {
	state.matches = nil
	query := strings.ToLower(strings.TrimSpace(sanitizeTerminalText(state.query)))
	if query == "" {
		state.matchIndex = -1
		return
	}
	for index, line := range lines {
		if strings.Contains(strings.ToLower(line.text), query) {
			state.matches = append(state.matches, index)
		}
	}
	if len(state.matches) == 0 {
		state.matchIndex = -1
		return
	}
	state.matchIndex = slices.Index(state.matches, state.selected)
}

func (state *transcriptOverlayState) selectLine(lines []transcriptOverlayLine, index int) {
	if len(lines) == 0 {
		return
	}
	state.selected = min(max(0, index), len(lines)-1)
	state.selectedKey = lines[state.selected].key
	state.followBottom = false
	if match := slices.Index(state.matches, state.selected); match >= 0 {
		state.matchIndex = match
	}
}

func (state *transcriptOverlayState) moveMatch(lines []transcriptOverlayLine, direction int) {
	if len(state.matches) == 0 {
		state.notice = "No transcript matches"
		return
	}
	next := -1
	if direction > 0 {
		next = slices.IndexFunc(state.matches, func(line int) bool { return line > state.selected })
		if next < 0 {
			next = 0
		}
	} else {
		for index := len(state.matches) - 1; index >= 0; index-- {
			if state.matches[index] < state.selected {
				next = index
				break
			}
		}
		if next < 0 {
			next = len(state.matches) - 1
		}
	}
	state.matchIndex = next
	state.selectLine(lines, state.matches[next])
	state.followBottom = false
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
	next := slices.IndexFunc(state.matches, func(line int) bool { return line >= state.selected })
	if next < 0 {
		next = 0
	}
	state.matchIndex = next
	state.selectLine(lines, state.matches[next])
	state.followBottom = false
	state.notice = ""
}

func (m *Model) handleTranscriptOverlayKey(message tea.KeyMsg) (bool, tea.Cmd) {
	state := &m.transcriptOverlay
	if !state.active {
		return false, nil
	}
	lines := m.transcriptOverlayLines()
	state.sync(lines)
	if state.searching {
		switch message.String() {
		case "esc":
			state.searching = false
			state.queryInput.SetValue(state.query)
			state.queryInput.Blur()
			return true, nil
		case "enter":
			state.applySearch(lines)
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
			lines[state.selected].text,
		)
	case "C":
		if state.help {
			state.notice = "Close help before copying"
			break
		}
		plain := make([]string, 0, len(lines))
		for _, line := range lines {
			plain = append(plain, line.text)
		}
		state.copyRequestID++
		state.notice = "Copying full transcript…"
		return true, transcriptCopyCmd(
			m.ctx,
			m.writeClipboardText,
			state.copyRequestID,
			"full transcript",
			strings.Join(plain, "\n"),
		)
	}
	return true, nil
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
	lines := m.transcriptOverlayLines()
	state.sync(lines)
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
			matchSet[match] = struct{}{}
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
