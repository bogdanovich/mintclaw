package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type commandPanel uint8

const (
	commandPanelNone commandPanel = iota
	commandPanelHelp
	commandPanelStatus
	commandPanelModel
	commandPanelDiff
)

type parsedSlashCommand struct {
	name string
	args string
}

func parseSlashCommand(value string) (parsedSlashCommand, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return parsedSlashCommand{}, false
	}
	fields := strings.Fields(value)
	name := strings.ToLower(fields[0])
	args := strings.TrimSpace(value[len(fields[0]):])
	return parsedSlashCommand{name: name, args: args}, true
}

func unescapeSlashPrompt(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if !strings.HasPrefix(trimmed, "//") {
		return value
	}
	prefixBytes := len(value) - len(trimmed)
	return value[:prefixBytes] + trimmed[1:]
}

func (m *Model) handleSlashCommand(value string) (bool, tea.Cmd) {
	command, ok := parseSlashCommand(value)
	if !ok {
		return false, nil
	}
	noArgs := func() bool {
		if command.args == "" {
			return true
		}
		m.err = fmt.Errorf("%s does not accept arguments", command.name)
		return false
	}
	show := func(panel commandPanel) (bool, tea.Cmd) {
		if !noArgs() {
			return true, nil
		}
		m.commandPanel = panel
		m.err = nil
		m.clearCommandDraft()
		return true, nil
	}

	switch command.name {
	case "/help", "/?":
		return show(commandPanelHelp)
	case "/status":
		return show(commandPanelStatus)
	case "/model":
		return show(commandPanelModel)
	case "/diff":
		return show(commandPanelDiff)
	case "/compact":
		if !noArgs() {
			return true, nil
		}
		m.commandPanel = commandPanelNone
		m.err = nil
		m.clearCommandDraft()
		m.pendingSlashCommand = "compact"
		return true, typedCommandCmd(m.ctx, "compact", m.controller.Compact)
	case "/rename":
		if command.args == "" {
			m.err = errors.New("/rename requires a title")
			return true, nil
		}
		m.commandPanel = commandPanelNone
		m.err = nil
		m.clearCommandDraft()
		m.pendingSlashCommand = "rename"
		return true, typedCommandCmd(m.ctx, "rename", func(ctx context.Context) error {
			return m.controller.Rename(ctx, command.args)
		})
	case "/new":
		if !noArgs() {
			return true, nil
		}
		m.commandPanel = commandPanelNone
		m.err = nil
		m.clearCommandDraft()
		m.pendingSlashCommand = "new"
		return true, typedCommandCmd(m.ctx, "new", m.controller.NewThread)
	case "/exit", "/quit", "/q":
		if !noArgs() {
			return true, nil
		}
		m.clearCommandDraft()
		return true, tea.Quit
	default:
		m.err = fmt.Errorf("unknown coding command %q; use /help", command.name)
		return true, nil
	}
}

func (m *Model) clearCommandDraft() {
	m.composer.Reset()
	m.historyIndex = -1
	m.historyDraft = ""
}

func slashCommandError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, frontend.ErrCommandUnsupported) {
		switch operation {
		case "new":
			return errors.New("new thread is unavailable in this screen; use /exit, then mintclaw code <prompt>")
		case "rename":
			return errors.New("thread rename is not implemented yet; the current title is unchanged")
		}
	}
	return fmt.Errorf("%s command: %w", operation, err)
}

func (m *Model) commandPanelView() string {
	content := commandPanelContent(m.commandPanel, m.snapshot)
	content = sanitizeTerminalText(content)
	wrapped := ansi.Wrap(content, max(1, m.width), "")
	lines := strings.Split(strings.TrimSpace(wrapped), "\n")
	limit := max(1, m.viewport.Height)
	if len(lines) > limit {
		hidden := len(lines) - limit + 1
		lines = append(lines[:limit-1], fmt.Sprintf("… %d more lines; Esc closes", hidden))
	}
	for index := range lines {
		lines[index] = clipLine(lines[index], m.width)
	}
	return strings.Join(lines, "\n")
}

func commandPanelContent(panel commandPanel, snapshot frontend.ThreadSnapshot) string {
	switch panel {
	case commandPanelHelp:
		return strings.Join([]string{
			"MintClaw coding commands",
			"/help              show commands and keyboard bindings",
			"/status            show live thread and workspace status",
			"/model             show the current model and provider",
			"/diff              show the current bounded repository change summary",
			"/compact           start real context compaction when idle",
			"/rename <title>    request a thread title change",
			"/new               request a new coding thread",
			"/exit              close the controller and exit",
			"",
			"Keyboard",
			"Enter submit · Ctrl+J newline · Ctrl+C interrupt/exit",
			"PgUp history · Alt+End latest · Ctrl+R refresh repository",
			"Alt+J/Alt+K select tool · Ctrl+O expand tool · Esc close panel",
			"Start a prompt with // when its text must begin with a slash.",
		}, "\n")
	case commandPanelStatus:
		return statusPanelContent(snapshot)
	case commandPanelModel:
		return strings.Join([]string{
			"Current coding model",
			"model: " + modelStatus(snapshot.Metadata),
			"In-session model switching is not admitted yet. Use mintclaw resume <thread-id> --model <name>.",
		}, "\n")
	case commandPanelDiff:
		return diffPanelContent(snapshot)
	default:
		return ""
	}
}

func statusPanelContent(snapshot frontend.ThreadSnapshot) string {
	lines := []string{
		"Current coding thread status",
		"thread: " + snapshot.ThreadID,
		"title: " + fallbackStatusValue(snapshot.Metadata.Title),
		"activity: " + activityStatus(snapshot),
		"project: " + fallbackStatusValue(snapshot.Metadata.ProjectRoot),
		"cwd: " + fallbackStatusValue(snapshot.Metadata.CWD),
		"model: " + modelStatus(snapshot.Metadata),
		"context: " + strings.TrimPrefix(contextStatus(snapshot.ContextUsage), "context "),
	}
	if workspace := snapshot.Workspace; workspace != nil {
		lines = append(
			lines,
			"branch: "+branchStatus(workspace),
			"repository: "+repositoryStatus(
				workspace.Git.Available,
				workspace.Git.StatusAvailable,
				workspace.Git.Dirty,
			),
		)
	}
	if compaction := snapshot.LastCompaction; compaction != nil {
		lines = append(lines, fmt.Sprintf(
			"last compaction: %s, %d tokens saved",
			compaction.Status,
			compaction.TokensSaved,
		))
	}
	return strings.Join(lines, "\n")
}

func diffPanelContent(snapshot frontend.ThreadSnapshot) string {
	lines := []string{"Current bounded repository changes"}
	workspace := snapshot.Workspace
	if workspace == nil {
		return strings.Join(append(lines, "workspace observation unavailable; use Ctrl+R to refresh"), "\n")
	}
	if !workspace.Git.Available {
		reason := fallbackStatusValue(workspace.Git.UnavailableReason)
		return strings.Join(append(lines, "Git unavailable: "+reason), "\n")
	}
	lines = append(lines,
		"root: "+fallbackStatusValue(workspace.ProjectRoot),
		"branch: "+branchStatus(workspace),
		"repository: "+repositoryStatus(true, workspace.Git.StatusAvailable, workspace.Git.Dirty),
	)
	if workspace.DiffStatAvailable {
		lines = append(lines, fmt.Sprintf(
			"diff stat: %d files, +%d -%d, %d binary",
			workspace.DiffStat.Files,
			workspace.DiffStat.Additions,
			workspace.DiffStat.Deletions,
			workspace.DiffStat.BinaryFiles,
		))
	} else {
		lines = append(lines, "diff stat unavailable")
	}
	for _, changed := range workspace.ChangedPaths {
		path := changed.Path
		if changed.OriginalPath != "" {
			path = changed.OriginalPath + " -> " + changed.Path
		}
		lines = append(lines, changed.Status+" "+path)
	}
	if len(workspace.ChangedPaths) == 0 && workspace.Git.StatusAvailable {
		lines = append(lines, "no changed paths")
	}
	if workspace.Truncated {
		lines = append(lines, "[repository observation truncated]")
	}
	if workspace.Warning != "" {
		lines = append(lines, "warning: "+workspace.Warning)
	}
	return strings.Join(lines, "\n")
}

func repositoryStatus(gitAvailable, statusAvailable, dirty bool) string {
	switch {
	case !gitAvailable:
		return "not a Git repository"
	case !statusAvailable:
		return "status unavailable"
	case dirty:
		return "dirty"
	default:
		return "clean"
	}
}

func fallbackStatusValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}
