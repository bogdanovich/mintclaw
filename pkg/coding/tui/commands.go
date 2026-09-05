package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

type commandPanel uint8

const (
	commandPanelNone commandPanel = iota
	commandPanelHelp
	commandPanelStatus
	commandPanelModel
	commandPanelDiff
	commandPanelReview
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
		m.commandPanelOffset = 0
		m.err = nil
		m.clearCommandDraft()
		return true, nil
	}

	switch command.name {
	case "/attach":
		if command.args == "" {
			m.err = errors.New("/attach requires a local file path")
			return true, nil
		}
		paths, err := normalizeAttachmentPaths(command.args)
		if err != nil {
			m.err = err
			return true, nil
		}
		if len(m.composerAttachments)+len(paths) > frontend.MaxTurnAttachments {
			m.err = fmt.Errorf("a turn supports at most %d attachments", frontend.MaxTurnAttachments)
			return true, nil
		}
		originalDraft := m.composer.Value()
		originalAttachments := append([]composerAttachment(nil), m.composerAttachments...)
		originalPasteNumber := m.nextPasteNumber
		originalImageNumber := m.nextImageNumber
		m.clearCommandDraft()
		for index, path := range paths {
			if index > 0 {
				m.composer.InsertString("\n")
			}
			contentType, image := supportedImageContentType(path)
			if err = m.addComposerAttachment(path, "", contentType, false, image); err != nil {
				m.composerAttachments = originalAttachments
				m.nextPasteNumber = originalPasteNumber
				m.nextImageNumber = originalImageNumber
				m.composer.SetValue(originalDraft)
				m.err = err
				return true, nil
			}
		}
		m.err = nil
		return true, textarea.Blink
	case "/help", "/?":
		return show(commandPanelHelp)
	case "/status":
		if !noArgs() {
			return true, nil
		}
		m.commandPanel = commandPanelStatus
		m.commandPanelOffset = 0
		m.err = nil
		m.clearCommandDraft()
		reader, ok := m.controller.(frontend.RepositoryEvidenceReader)
		if !ok {
			return true, nil
		}
		m.workspaceNotice = "repository status loading"
		return true, repositoryStatusCmd(m.ctx, reader, m.beginEvidenceRequest())
	case "/model":
		return show(commandPanelModel)
	case "/diff":
		target, err := slashDiffTarget(command.args)
		if err != nil {
			m.err = err
			return true, nil
		}
		m.commandPanel = commandPanelDiff
		m.commandPanelOffset = 0
		m.err = nil
		m.clearCommandDraft()
		reader, ok := m.controller.(frontend.RepositoryEvidenceReader)
		if !ok {
			return true, nil
		}
		m.workspaceNotice = "repository diff loading"
		return true, repositoryDiffCmd(m.ctx, reader, target, m.beginEvidenceRequest())
	case "/review":
		target, err := slashReviewTarget(command.args)
		if err != nil {
			m.err = err
			return true, nil
		}
		reviewer, ok := m.controller.(frontend.Reviewer)
		if !ok {
			m.err = errors.New("native code review is unavailable for this controller")
			return true, nil
		}
		m.commandPanel = commandPanelReview
		m.commandPanelOffset = 0
		m.err = nil
		m.clearCommandDraft()
		m.pendingSlashCommand = "review"
		return true, typedCommandCmd(m.ctx, "review", func(ctx context.Context) error {
			return reviewer.Review(ctx, target)
		})
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
	case "/archive", "/unarchive":
		if !noArgs() {
			return true, nil
		}
		operation := strings.TrimPrefix(command.name, "/")
		archived := command.name == "/archive"
		m.commandPanel = commandPanelNone
		m.err = nil
		m.clearCommandDraft()
		m.pendingSlashCommand = operation
		return true, typedCommandCmd(m.ctx, operation, func(ctx context.Context) error {
			return m.controller.SetArchived(ctx, archived)
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

func slashDiffTarget(args string) (codingworkspace.DiffTarget, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent}, nil
	}
	target := codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetKind(strings.ToLower(fields[0]))}
	switch target.Kind {
	case codingworkspace.DiffTargetCurrent:
		if len(fields) != 1 {
			return codingworkspace.DiffTarget{}, fmt.Errorf("/diff %s does not accept a ref", target.Kind)
		}
	case codingworkspace.DiffTargetBase, codingworkspace.DiffTargetCommit:
		if len(fields) != 2 {
			return codingworkspace.DiffTarget{}, fmt.Errorf("/diff %s requires one local ref", target.Kind)
		}
		target.Ref = fields[1]
	default:
		return codingworkspace.DiffTarget{}, errors.New("/diff target must be current, base, or commit")
	}
	return target, nil
}

func slashReviewTarget(args string) (codingreview.Target, error) {
	fields := strings.Fields(args)
	separator := slices.Index(fields, "--")
	scopeFields := fields
	instructions := ""
	if separator >= 0 {
		scopeFields = fields[:separator]
		instructions = strings.Join(fields[separator+1:], " ")
	}
	target := codingreview.Target{Kind: codingreview.TargetCurrent, Instructions: strings.TrimSpace(instructions)}
	if len(scopeFields) > 0 {
		target.Kind = codingreview.TargetKind(strings.ToLower(scopeFields[0]))
	}
	switch target.Kind {
	case codingreview.TargetCurrent:
		if len(scopeFields) > 1 {
			return codingreview.Target{}, errors.New("/review current does not accept a ref")
		}
	case codingreview.TargetBase, codingreview.TargetCommit:
		if len(scopeFields) != 2 {
			return codingreview.Target{}, fmt.Errorf("/review %s requires one local ref", target.Kind)
		}
		target.Ref = scopeFields[1]
	default:
		return codingreview.Target{}, errors.New("/review target must be current, base, or commit")
	}
	if err := target.Validate(); err != nil {
		return codingreview.Target{}, fmt.Errorf("/review: %w", err)
	}
	return target, nil
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
			return errors.New("thread rename is unavailable; the current title is unchanged")
		case "archive", "unarchive":
			return fmt.Errorf("thread %s is unavailable; lifecycle state is unchanged", operation)
		case "review":
			return errors.New("native code review is unavailable for the current provider")
		}
	}
	return fmt.Errorf("%s command: %w", operation, err)
}

func (m *Model) commandPanelView() string {
	lines := m.commandPanelLines()
	pageSize := m.commandPanelPageSize(len(lines))
	offset := min(max(0, m.commandPanelOffset), max(0, len(lines)-pageSize))
	end := min(len(lines), offset+pageSize)
	visible := append([]string(nil), lines[offset:end]...)
	if len(lines) > pageSize {
		footer := fmt.Sprintf(
			"lines %d-%d of %d · PgUp/PgDown scroll · Esc closes",
			offset+1,
			end,
			len(lines),
		)
		visible = append(visible, clipLine(footer, m.width))
	}
	return strings.Join(visible, "\n")
}

func (m *Model) commandPanelLines() []string {
	content := sanitizeTerminalText(commandPanelContent(m.commandPanel, m.snapshot))
	wrapped := ansi.Wrap(content, max(1, m.width), "")
	lines := strings.Split(strings.TrimSpace(wrapped), "\n")
	for index := range lines {
		lines[index] = clipLine(lines[index], m.width)
	}
	return lines
}

func (m *Model) commandPanelPageSize(lineCount int) int {
	height := max(1, m.viewport.Height)
	if lineCount <= height {
		return height
	}
	return max(1, height-1)
}

func (m *Model) scrollCommandPanel(direction int) {
	lineCount := len(m.commandPanelLines())
	pageSize := m.commandPanelPageSize(lineCount)
	maximum := max(0, lineCount-pageSize)
	m.commandPanelOffset = min(max(0, m.commandPanelOffset+direction*pageSize), maximum)
}

func commandPanelContent(panel commandPanel, snapshot frontend.ThreadSnapshot) string {
	switch panel {
	case commandPanelHelp:
		return strings.Join([]string{
			"MintClaw coding commands",
			"/help              show commands and keyboard bindings",
			"/status            show live thread and workspace status",
			"/model             show the current model and provider",
			"/diff [target]     show bounded hunks for current, base, or commit",
			"/review [target] [-- instructions]  run a read-only local review",
			"/attach <paths…>   attach local files to the draft",
			"/compact           start real context compaction when idle",
			"/rename <title>    request a thread title change",
			"/archive | /unarchive  hide or restore this thread in the active catalog",
			"/new               request a new coding thread",
			"/exit              close the controller and exit",
			"",
			"Keyboard",
			"Enter submit · Ctrl+J newline · Ctrl+V paste clipboard image · Ctrl+C interrupt/exit",
			"PgUp/PgDown scroll panel or transcript · Alt+End latest · Ctrl+R refresh repository",
			"Alt+J/Alt+K select tool · Ctrl+O expand tool · Esc close panel",
			"Start a prompt with // when its text must begin with a slash.",
		}, "\n")
	case commandPanelStatus:
		return statusPanelContent(snapshot)
	case commandPanelModel:
		return strings.Join([]string{
			"Current coding model",
			"model: " + boundedSingleLine(modelStatus(snapshot.Metadata), 512),
			"In-session model switching is not admitted yet. Use mintclaw resume <thread-id> --model <name>.",
		}, "\n")
	case commandPanelDiff:
		return diffPanelContent(snapshot)
	case commandPanelReview:
		if snapshot.Review == nil {
			return "Local code review\nphase: waiting for admission"
		}
		return codingreview.RenderStatePlain(*snapshot.Review)
	default:
		return ""
	}
}

func statusPanelContent(snapshot frontend.ThreadSnapshot) string {
	lines := []string{
		"Current coding thread status",
		"thread: " + boundedSingleLine(snapshot.ThreadID, 256),
		"title: " + fallbackStatusValue(snapshot.Metadata.Title),
		"lifecycle: " + threadLifecycleStatus(snapshot.Metadata.Archived),
		"activity: " + boundedSingleLine(activityStatus(snapshot), 512),
		"project: " + fallbackStatusValue(snapshot.Metadata.ProjectRoot),
		"cwd: " + fallbackStatusValue(snapshot.Metadata.CWD),
		"model: " + boundedSingleLine(modelStatus(snapshot.Metadata), 512),
		"context: " + strings.TrimPrefix(contextStatus(snapshot.ContextUsage), "context "),
	}
	if snapshot.RepositoryStatus != nil {
		lines = append(lines, "", codingworkspace.RenderStatusPlain(*snapshot.RepositoryStatus))
	} else if workspace := snapshot.Workspace; workspace != nil {
		lines = append(
			lines,
			"branch: "+boundedSingleLine(branchStatus(workspace), 512),
			"repository: "+repositoryStatus(
				workspace.Git.Available,
				workspace.Git.StatusAvailable,
				workspace.Git.Dirty,
			),
		)
	}
	if compaction := snapshot.LastCompaction; compaction != nil {
		lines = append(lines, compactionStatusLines(compaction)...)
	}
	return strings.Join(lines, "\n")
}

func threadLifecycleStatus(archived bool) string {
	if archived {
		return "archived"
	}
	return "active"
}

func compactionStatusLines(compaction *frontend.CompactionState) []string {
	lines := []string{
		fmt.Sprintf(
			"last compaction: %s (%s)",
			boundedSingleLine(string(compaction.Status), 128),
			compactionMode(compaction),
		),
		"compaction trigger: " + compactionTrigger(compaction.Reason),
	}
	if compaction.TokenCountsObserved {
		lines = append(lines, fmt.Sprintf(
			"compaction context: %s → %s tokens",
			formatTokenCount(compaction.TokensBefore),
			formatTokenCount(compaction.TokensAfter),
		))
	} else {
		lines = append(lines, "compaction context: unavailable")
	}
	lines = append(
		lines,
		"compaction tokens saved: "+formatTokenCount(compaction.TokensSaved),
		fmt.Sprintf(
			"compaction summaries: %d total (%d leaf, %d condensed)",
			compaction.SummariesCreated,
			compaction.LeafSummaries,
			compaction.CondensedSummaries,
		),
	)
	if compaction.Duration > 0 {
		lines = append(lines, "compaction duration: "+formatToolDuration(compaction.Duration))
	} else if compaction.Status == frontend.CompactionRunning || compaction.Status == frontend.CompactionProgress {
		lines = append(lines, "compaction duration: in progress")
	} else {
		lines = append(lines, "compaction duration: unavailable")
	}
	lines = append(
		lines,
		"compaction continuation: "+compactionContinuation(compaction),
		"compaction guidance: after repeated compactions or a changed objective, use /new for a focused thread",
	)
	return lines
}

func compactionContinuation(compaction *frontend.CompactionState) string {
	switch compaction.Status {
	case frontend.CompactionRunning, frontend.CompactionProgress:
		if compaction.Background {
			return "composer remains available while compaction runs"
		}
		return "the current turn is waiting for compaction"
	case frontend.CompactionFailed, frontend.CompactionInterrupted:
		if compaction.Background {
			return "work can continue; retry compaction later if context pressure remains"
		}
		return "the current turn may stop; retry /compact or start a focused thread"
	default:
		return "work can continue"
	}
}

func diffPanelContent(snapshot frontend.ThreadSnapshot) string {
	if snapshot.RepositoryDiff != nil {
		return codingworkspace.RenderDiffPlain(*snapshot.RepositoryDiff)
	}
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
		"branch: "+boundedSingleLine(branchStatus(workspace), 512),
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
		path := boundedSingleLine(changed.Path, 512)
		if changed.OriginalPath != "" {
			path = boundedSingleLine(changed.OriginalPath, 512) + " -> " + path
		}
		lines = append(lines, boundedSingleLine(changed.Status, 32)+" "+path)
	}
	if len(workspace.ChangedPaths) == 0 && workspace.Git.StatusAvailable {
		lines = append(lines, "no changed paths")
	}
	if workspace.Truncated {
		lines = append(lines, "[repository observation truncated]")
	}
	if workspace.Warning != "" {
		lines = append(lines, "warning: "+boundedSingleLine(workspace.Warning, 512))
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
	return boundedSingleLine(value, 512)
}
