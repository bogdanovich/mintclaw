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
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

type commandPanel uint8

const (
	commandPanelNone commandPanel = iota
	commandPanelHelp
	commandPanelStatus
	commandPanelModel
	commandPanelSkills
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
	case "/transcript":
		if !noArgs() {
			return true, nil
		}
		m.err = nil
		m.clearCommandDraft()
		return true, m.openTranscriptOverlay()
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
		if command.args != "" {
			return true, m.beginDirectModelSelection(command.args)
		}
		m.commandPanel = commandPanelModel
		m.commandPanelOffset = 0
		m.modelSelection = currentModelOptionIndex(m.snapshot)
		m.modelReasoning = 0
		m.pendingModel = ""
		m.err = nil
		m.clearCommandDraft()
		return true, nil
	case "/skills":
		if command.args == "" {
			return show(commandPanelSkills)
		}
		name, ok := resolveRuntimeSkillName(m.snapshot.Runtime, command.args)
		if !ok {
			m.err = fmt.Errorf("unknown coding skill %q; use /skills to list available skills", command.args)
			return true, nil
		}
		m.commandPanel = commandPanelNone
		m.commandPanelOffset = 0
		m.err = nil
		m.clearCommandDraft()
		m.composer.InsertString("$" + name + " ")
		return true, textarea.Blink
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
		case "model":
			return errors.New("model switching is unavailable for this coding runtime")
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
	if m.commandPanel == commandPanelStatus {
		return renderStatusCard(m.snapshot, m.width, m.home)
	}
	if m.commandPanel == commandPanelModel {
		return m.modelPanelLines()
	}
	content := commandPanelContent(m.commandPanel, m.snapshot)
	content = sanitizeTerminalText(content)
	logical := strings.Split(strings.Trim(content, "\n"), "\n")
	lines := make([]string, 0, len(logical))
	for _, line := range logical {
		prefix := line[:len(line)-len(strings.TrimLeft(line, " "))]
		body := strings.TrimPrefix(line, prefix)
		wrapped := strings.Split(ansi.Wrap(body, max(1, m.width-ansi.StringWidth(prefix)), ""), "\n")
		for _, part := range wrapped {
			lines = append(lines, clipLine(prefix+part, m.width))
		}
	}
	return lines
}

func (m *Model) commandPanelPageSize(lineCount int) int {
	height := m.maximumViewportHeight()
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

func (m *Model) scrollCommandPanelLines(delta int) {
	lineCount := len(m.commandPanelLines())
	pageSize := m.commandPanelPageSize(lineCount)
	maximum := max(0, lineCount-pageSize)
	m.commandPanelOffset = min(max(0, m.commandPanelOffset+delta), maximum)
}

func commandPanelContent(panel commandPanel, snapshot frontend.ThreadSnapshot) string {
	switch panel {
	case commandPanelHelp:
		return strings.Join([]string{
			"MintClaw coding commands",
			"/help              show commands and keyboard bindings",
			"/status            show live thread and workspace status",
			"/model [name [effort]] select a model and reasoning effort",
			"/skills [name]     list skills or insert an exact $skill mention",
			"/transcript        search and copy the retained transcript",
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
			"Ctrl+T transcript overlay · Esc close panel",
			"Transcript: / find · n/N match · c copy line · C copy all · ? help · Esc close",
			"Start a prompt with // when its text must begin with a slash.",
		}, "\n")
	case commandPanelStatus:
		return statusPanelContent(snapshot)
	case commandPanelModel:
		return ""
	case commandPanelSkills:
		return skillsPanelContent(snapshot.Runtime)
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

func availableModelOptions(snapshot frontend.ThreadSnapshot) []frontend.ModelOption {
	if snapshot.Runtime != nil && len(snapshot.Runtime.Models) > 0 {
		return snapshot.Runtime.Models
	}
	if strings.TrimSpace(snapshot.Metadata.Model) == "" {
		return nil
	}
	return []frontend.ModelOption{{
		Name: snapshot.Metadata.Model, Providers: []string{snapshot.Metadata.Provider},
	}}
}

func currentModelOptionIndex(snapshot frontend.ThreadSnapshot) int {
	options := availableModelOptions(snapshot)
	for index, option := range options {
		if option.Name == snapshot.Metadata.Model {
			return index
		}
	}
	return 0
}

func (m *Model) modelPanelLines() []string {
	if m.pendingModel != "" {
		return m.reasoningPanelLines()
	}
	options := availableModelOptions(m.snapshot)
	lines := []string{"Select model", ""}
	if len(options) == 0 {
		return append(lines, "No enabled models are configured.", "Esc closes")
	}
	selection := min(max(0, m.modelSelection), len(options)-1)
	for index, option := range options {
		cursor := "  "
		if index == selection {
			cursor = "› "
		}
		selected := "  "
		if option.Name == m.snapshot.Metadata.Model {
			selected = "✓ "
		}
		providersText := strings.Join(option.Providers, ", ")
		if providersText != "" {
			providersText = "  " + providersText
		}
		lines = append(lines, clipLine(
			cursor+selected+boundedSingleLine(option.Name, 512)+providersText,
			m.width,
		))
	}
	if m.snapshot.Runtime != nil && m.snapshot.Runtime.ModelsTruncated {
		lines = append(lines, "[model list truncated]")
	}
	return append(lines, "", "↑/↓ navigate · Enter select · Esc close")
}

func (m *Model) moveModelPickerSelection(delta int) {
	if m.pendingModel != "" {
		options := availableReasoningOptions(m.snapshot, m.pendingModel)
		if len(options) == 0 {
			return
		}
		m.modelReasoning = min(max(0, m.modelReasoning+delta), len(options)-1)
		line := m.modelReasoning + 2
		m.keepModelPickerLineVisible(line)
		return
	}
	options := availableModelOptions(m.snapshot)
	if len(options) == 0 {
		return
	}
	m.modelSelection = min(max(0, m.modelSelection+delta), len(options)-1)
	m.keepModelPickerLineVisible(m.modelSelection + 2)
}

func (m *Model) keepModelPickerLineVisible(line int) {
	pageSize := m.commandPanelPageSize(len(m.modelPanelLines()))
	if line < m.commandPanelOffset {
		m.commandPanelOffset = line
	} else if line >= m.commandPanelOffset+pageSize {
		m.commandPanelOffset = line - pageSize + 1
	}
}

func (m *Model) selectHighlightedModelOrReasoning() tea.Cmd {
	if m.pendingModel != "" {
		options := availableReasoningOptions(m.snapshot, m.pendingModel)
		if len(options) == 0 {
			m.err = errors.New("no reasoning efforts are available")
			return nil
		}
		m.modelReasoning = min(max(0, m.modelReasoning), len(options)-1)
		return m.selectModel(frontend.ModelSelection{
			Model: m.pendingModel, ReasoningEffort: string(options[m.modelReasoning].ID),
		})
	}
	options := availableModelOptions(m.snapshot)
	if len(options) == 0 {
		m.err = errors.New("no enabled coding models are configured")
		return nil
	}
	m.modelSelection = min(max(0, m.modelSelection), len(options)-1)
	model := options[m.modelSelection].Name
	if len(options[m.modelSelection].ReasoningProfile.Options) == 0 {
		return m.selectModel(frontend.ModelSelection{Model: model})
	}
	m.openReasoningPicker(model)
	return nil
}

func (m *Model) beginDirectModelSelection(args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) == 0 || len(fields) > 2 {
		m.err = errors.New("usage: /model [name [reasoning-effort]]")
		return nil
	}
	name := fields[0]
	if !modelOptionExists(m.snapshot, name) {
		m.err = fmt.Errorf("model %q is not an enabled coding model", name)
		return nil
	}
	if len(fields) == 2 {
		return m.selectModel(frontend.ModelSelection{Model: name, ReasoningEffort: fields[1]})
	}
	if len(availableReasoningOptions(m.snapshot, name)) == 0 {
		return m.selectModel(frontend.ModelSelection{Model: name})
	}
	m.commandPanel = commandPanelModel
	m.commandPanelOffset = 0
	m.openReasoningPicker(name)
	m.clearCommandDraft()
	m.err = nil
	return nil
}

func (m *Model) selectModel(selection frontend.ModelSelection) tea.Cmd {
	selection.Model = strings.TrimSpace(selection.Model)
	selection.ReasoningEffort = strings.ToLower(strings.TrimSpace(selection.ReasoningEffort))
	if selection.Model == "" {
		m.err = errors.New("/model requires a configured model name")
		return nil
	}
	if selection.ReasoningEffort != "" {
		effort, ok := reasoning.Parse(selection.ReasoningEffort)
		if !ok || !slices.ContainsFunc(availableReasoningOptions(m.snapshot, selection.Model), func(
			option reasoning.Option,
		) bool {
			return option.ID == effort
		}) {
			m.err = fmt.Errorf("reasoning effort %q is not supported by model %q",
				selection.ReasoningEffort, selection.Model)
			return nil
		}
	}
	selector, ok := m.controller.(frontend.ModelSelector)
	if !ok {
		m.err = slashCommandError("model", frontend.ErrCommandUnsupported)
		return nil
	}
	m.clearCommandDraft()
	m.err = nil
	m.pendingSlashCommand = "model"
	return typedCommandCmd(m.ctx, "model", func(ctx context.Context) error {
		return selector.SelectModel(ctx, selection)
	})
}

func modelOptionExists(snapshot frontend.ThreadSnapshot, name string) bool {
	for _, option := range availableModelOptions(snapshot) {
		if option.Name == name {
			return true
		}
	}
	return false
}

func availableReasoningOptions(snapshot frontend.ThreadSnapshot, model string) []reasoning.Option {
	for _, option := range availableModelOptions(snapshot) {
		if option.Name == model {
			return option.ReasoningProfile.Options
		}
	}
	return nil
}

func (m *Model) openReasoningPicker(model string) {
	m.pendingModel = model
	m.commandPanelOffset = 0
	m.modelReasoning = reasoningEffortIndex(m.snapshot, model)
}

func reasoningEffortIndex(snapshot frontend.ThreadSnapshot, model string) int {
	options := availableReasoningOptions(snapshot, model)
	target := ""
	if model == snapshot.Metadata.Model && snapshot.Runtime != nil {
		target = strings.ToLower(strings.TrimSpace(snapshot.Runtime.ReasoningEffort))
	}
	if target == "" {
		for _, option := range availableModelOptions(snapshot) {
			if option.Name == model {
				target = string(option.ReasoningProfile.Default)
				break
			}
		}
	}
	for index, option := range options {
		if string(option.ID) == target {
			return index
		}
	}
	return 0
}

func (m *Model) reasoningPanelLines() []string {
	options := availableReasoningOptions(m.snapshot, m.pendingModel)
	lines := []string{"Select reasoning level for " + boundedSingleLine(m.pendingModel, 512), ""}
	if len(options) == 0 {
		return append(lines, "No reasoning efforts are available.", "Esc goes back")
	}
	selection := min(max(0, m.modelReasoning), len(options)-1)
	currentReasoning := ""
	if m.pendingModel == m.snapshot.Metadata.Model && m.snapshot.Runtime != nil {
		currentReasoning = strings.ToLower(strings.TrimSpace(m.snapshot.Runtime.ReasoningEffort))
	}
	for index, option := range options {
		cursor := "  "
		if index == selection {
			cursor = "› "
		}
		selected := "  "
		if string(option.ID) == currentReasoning {
			selected = "✓ "
		}
		label := option.Label
		if label == "" {
			label = reasoning.Label(option.ID)
		}
		lines = append(lines, clipLine(cursor+selected+label, m.width))
	}
	return append(lines, "", "↑/↓ navigate · Enter select · Esc back")
}

func resolveRuntimeSkillName(runtimeStatus *frontend.RuntimeStatus, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if runtimeStatus == nil || requested == "" || strings.ContainsAny(requested, " \t\r\n") {
		return "", false
	}
	for _, skill := range runtimeStatus.Skills {
		if strings.EqualFold(skill.Name, requested) {
			return skill.Name, true
		}
	}
	return "", false
}

func skillsPanelContent(runtimeStatus *frontend.RuntimeStatus) string {
	lines := []string{
		"Available coding skills",
		"Use /skills <name> to insert an exact $skill mention into the composer.",
		"A selected skill supplies instructions for one turn and never grants tools or permissions.",
	}
	if runtimeStatus == nil || len(runtimeStatus.Skills) == 0 {
		return strings.Join(append(lines, "", "No compatible skills are available."), "\n")
	}
	lines = append(lines, "")
	for _, skill := range runtimeStatus.Skills {
		line := "$" + boundedSingleLine(skill.Name, 128)
		if scope := boundedSingleLine(skill.Scope, 128); scope != "" {
			line += " [" + scope + "]"
		}
		if description := boundedSingleLine(skill.Description, 512); description != "" {
			line += " — " + description
		}
		lines = append(lines, line)
	}
	if runtimeStatus.SkillsTruncated {
		lines = append(lines, "", "Additional skills were omitted from this bounded view.")
	}
	return strings.Join(lines, "\n")
}

func statusPanelContent(snapshot frontend.ThreadSnapshot) string {
	return RenderStatusPlain(snapshot, "")
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
