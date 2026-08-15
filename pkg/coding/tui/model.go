// Package tui contains the interactive terminal frontend for coding threads.
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

// WatchErrorMsg reports a frontend subscription failure to the update loop.
type WatchErrorMsg struct {
	Err error
}

type CommandErrorMsg struct {
	Operation string
	Err       error
}

// Model is the bounded terminal view of one frontend controller. It never owns
// an agent runtime or canonical transcript state.
type Model struct {
	controller       frontend.Controller
	ctx              context.Context
	reducer          *frontend.Reducer
	viewport         viewport.Model
	composer         textarea.Model
	width            int
	height           int
	interruptPending bool
	resyncing        bool
	focused          bool
	err              error
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
	composer.SetHeight(composerHeight)
	composer.Focus()
	return &Model{
		controller: controller,
		ctx:        ctx,
		reducer:    reducer,
		viewport:   viewport.New(80, 18),
		composer:   composer,
		width:      80,
		height:     24,
		focused:    true,
	}, nil
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, nextDeltaCmd(m.ctx, m.controller, m.reducer.State().Revision))
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
		m.refreshViewport()
		return m, nextDeltaCmd(m.ctx, m.controller, m.reducer.State().Revision)
	case WatchErrorMsg:
		if message.Err != nil && !errors.Is(message.Err, context.Canceled) {
			m.err = message.Err
		}
		return m, nil
	case CommandErrorMsg:
		m.err = message.Err
		return m, nil
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			return m.handleInterrupt()
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

func (m *Model) Snapshot() frontend.ThreadSnapshot {
	return m.reducer.State()
}

func (m *Model) Dimensions() (int, int) {
	return m.width, m.height
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
	if activeWork(activity) {
		if m.interruptPending || activity == frontend.ActivityInterrupting {
			return m, commandCmd("hard_cancel", m.controller.HardCancel)
		}
		m.interruptPending = true
		return m, commandCmd("interrupt", m.controller.Interrupt)
	}
	return m, tea.Sequence(commandCmd("close", m.controller.Close), tea.Quit)
}

func activeWork(activity frontend.Activity) bool {
	return activity == frontend.ActivityRunning || activity == frontend.ActivityCompacting ||
		activity == frontend.ActivityInterrupting
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
