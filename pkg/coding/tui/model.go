// Package tui contains the bounded Bubble Tea feasibility model admitted by
// P0.5. Command wiring and the polished application shell remain P4 work.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

type CommandErrorMsg struct {
	Operation string
	Err       error
}

// Model proves the selected framework against the admitted controller
// boundary. It intentionally has no runtime pointer and no command entrypoint.
type Model struct {
	controller       frontend.Controller
	reducer          *frontend.Reducer
	viewport         viewport.Model
	composer         textarea.Model
	width            int
	height           int
	interruptPending bool
	resyncing        bool
	err              error
}

var _ tea.Model = (*Model)(nil)

func NewModel(controller frontend.Controller, snapshot frontend.ThreadSnapshot) (*Model, error) {
	reducer, err := frontend.NewReducer(snapshot)
	if err != nil {
		return nil, err
	}
	composer := textarea.New()
	composer.ShowLineNumbers = false
	composer.Placeholder = "Describe the coding task…"
	composer.SetHeight(composerHeight)
	composer.Focus()
	return &Model{
		controller: controller,
		reducer:    reducer,
		viewport:   viewport.New(80, 18),
		composer:   composer,
		width:      80,
		height:     24,
	}, nil
}

func (m *Model) Init() tea.Cmd {
	return textarea.Blink
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
			m.refreshViewport()
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
		m.interruptPending = false
		m.refreshViewport()
		return m, nil
	case CommandErrorMsg:
		m.err = message.Err
		return m, nil
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			return m.handleInterrupt()
		}
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
	if m.resyncing {
		status = "resynchronizing frontend…"
	}
	if m.err != nil {
		status = "frontend error: " + m.err.Error()
	}
	return m.viewport.View() + "\n" + m.composer.View() + "\n" + status
}

func (m *Model) ComposerValue() string {
	return m.composer.Value()
}

func (m *Model) Snapshot() frontend.ThreadSnapshot {
	return m.reducer.State()
}

func (m *Model) Dimensions() (int, int) {
	return m.width, m.height
}

func (m *Model) resize(width, height int) {
	m.width = max(1, width)
	m.height = max(composerHeight+2, height)
	m.viewport.Width = m.width
	m.viewport.Height = max(1, m.height-composerHeight-2)
	m.composer.SetWidth(m.width)
	m.composer.SetHeight(composerHeight)
	m.refreshViewport()
}

func (m *Model) refreshViewport() {
	state := m.reducer.State()
	wasAtBottom := m.viewport.AtBottom()
	var content strings.Builder
	for _, entry := range state.Entries {
		fmt.Fprintf(&content, "%s: %s\n\n", entry.Kind, entry.Text)
	}
	for _, tool := range state.Tools {
		fmt.Fprintf(&content, "tool %s [%s]\n", tool.Name, tool.Status)
	}
	m.viewport.SetContent(strings.TrimSpace(content.String()))
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

func (m *Model) handleInterrupt() (tea.Model, tea.Cmd) {
	if m.controller == nil {
		return m, tea.Quit
	}
	activity := m.reducer.State().Activity
	if activity == frontend.ActivityRunning || activity == frontend.ActivityCompacting ||
		activity == frontend.ActivityInterrupting {
		if m.interruptPending || activity == frontend.ActivityInterrupting {
			return m, commandCmd("hard_cancel", m.controller.HardCancel)
		}
		m.interruptPending = true
		return m, commandCmd("interrupt", m.controller.Interrupt)
	}
	return m, tea.Sequence(commandCmd("close", m.controller.Close), tea.Quit)
}

func snapshotCmd(controller frontend.Controller) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := controller.Snapshot(context.Background())
		return SnapshotMsg{Snapshot: snapshot, Err: err}
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
