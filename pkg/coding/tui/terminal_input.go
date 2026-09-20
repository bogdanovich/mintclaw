package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

// Some terminal/multiplexer combinations can deliver SGR mouse packets as
// rune keys after consuming the leading escape byte. Treat complete packets
// as mouse input so their printable tail can never enter the composer.
func sgrMouseScrollDelta(message tea.KeyMsg) (int, bool) {
	if message.Type != tea.KeyRunes || message.Paste || len(message.Runes) == 0 {
		return 0, false
	}
	value := []byte(string(message.Runes))
	delta := 0
	parsed := false
	for offset := 0; offset < len(value); {
		if value[offset] == '\x1b' {
			offset++
		}
		if offset < len(value) && value[offset] == '[' {
			offset++
		}
		if offset >= len(value) || value[offset] != '<' {
			return 0, false
		}
		offset++

		button, next, ok := sgrMouseNumber(value, offset)
		if !ok || next >= len(value) || value[next] != ';' {
			return 0, false
		}
		offset = next + 1
		_, next, ok = sgrMouseNumber(value, offset)
		if !ok || next >= len(value) || value[next] != ';' {
			return 0, false
		}
		offset = next + 1
		_, next, ok = sgrMouseNumber(value, offset)
		if !ok || next >= len(value) || (value[next] != 'M' && value[next] != 'm') {
			return 0, false
		}
		pressed := value[next] == 'M'
		offset = next + 1
		parsed = true
		if !pressed || button&64 == 0 {
			continue
		}
		switch button & 3 {
		case 0:
			delta--
		case 1:
			delta++
		}
	}
	return delta, parsed
}

func sgrMouseNumber(value []byte, offset int) (int, int, bool) {
	start := offset
	number := 0
	for offset < len(value) && value[offset] >= '0' && value[offset] <= '9' {
		number = number*10 + int(value[offset]-'0')
		offset++
	}
	return number, offset, offset > start
}

func (m *Model) handleLeakedMouseScroll(delta int) (tea.Model, tea.Cmd) {
	if delta == 0 {
		return m, nil
	}
	step := max(1, m.viewport.MouseWheelDelta)
	lines := delta * step
	if m.transcriptOverlay.active {
		state := &m.transcriptOverlay
		if state.help {
			state.helpOffset = max(0, state.helpOffset+lines)
			return m, nil
		}
		if lines < 0 && state.selected == 0 && !m.transcript.loading && !m.transcript.disabled &&
			(m.transcript.hasOlder || m.snapshot.HasOlderEntries) {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				m.syncTranscriptOverlay()
				return m, transcriptPageCmd(m.ctx, pager, m.transcript.start, transcriptPageOlder)
			}
		}
		state.selectLine(state.lines, state.selected+lines)
		return m, nil
	}
	if m.commandPanel != commandPanelNone {
		m.scrollCommandPanelLines(lines)
		return m, nil
	}
	if lines < 0 {
		if m.viewport.AtTop() && m.transcript.hasOlder && !m.transcript.loading {
			if pager, ok := m.controller.(frontend.TranscriptPager); ok {
				m.transcript.loading = true
				return m, transcriptPageCmd(m.ctx, pager, m.transcript.start, transcriptPageOlder)
			}
		}
		m.viewport.ScrollUp(-lines)
		return m, nil
	}
	m.viewport.ScrollDown(lines)
	return m, nil
}
