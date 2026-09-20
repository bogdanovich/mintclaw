package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSGRMouseScrollDeltaAcceptsLeakedTerminalPacketsOnly(t *testing.T) {
	tests := []struct {
		name    string
		message tea.KeyMsg
		delta   int
		handled bool
	}{
		{
			name: "missing escape and mixed trackpad axes",
			message: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(
				"[<66;35;23M[<65;35;23M[<64;35;23M[<67;37;20M[<65;37;20M",
			)},
			delta: 1, handled: true,
		},
		{
			name: "complete escape sequence",
			message: tea.KeyMsg{
				Type: tea.KeyRunes, Runes: []rune("\x1b[<64;10;4M"),
			},
			delta: -1, handled: true,
		},
		{
			name: "ordinary text",
			message: tea.KeyMsg{
				Type: tea.KeyRunes, Runes: []rune("[not a mouse report]"),
			},
		},
		{
			name: "intentional paste",
			message: tea.KeyMsg{
				Type: tea.KeyRunes, Runes: []rune("[<65;35;23M"), Paste: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delta, handled := sgrMouseScrollDelta(test.message)
			if delta != test.delta || handled != test.handled {
				t.Fatalf("delta=%d handled=%t, want %d/%t", delta, handled, test.delta, test.handled)
			}
		})
	}
}
