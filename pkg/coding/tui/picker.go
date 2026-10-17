package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
)

const (
	DefaultPickerPageSize = 20
	pickerPageTimeout     = 5 * time.Second
	pickerSearchRunes     = 64
)

// PickerOptions configures the standalone resume picker shown before a thread
// lease or coding controller is created.
type PickerOptions struct {
	Input           io.Reader
	Output          io.Writer
	AlternateScreen bool
	AllProjects     bool
	Archived        bool
	Search          string
	Environment     []string
	NoColor         bool
	Now             func() time.Time
	newProgram      func(tea.Model, ...tea.ProgramOption) program
}

// PickerSelection reports either one selected thread or an explicit cancel.
type PickerSelection struct {
	ThreadID string
	Canceled bool
}

// RunPicker opens a bounded project-scoped catalog picker. Selection is
// discovery only; the caller must still acquire the lease and validate the
// project location from authoritative metadata.
func RunPicker(
	ctx context.Context,
	source codingpicker.Source,
	options PickerOptions,
) (PickerSelection, error) {
	if source == nil {
		return PickerSelection{}, fmt.Errorf("coding resume picker source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configureColorProfile(options.NoColor, options.Environment)
	query := codingpicker.Query{
		AllProjects: options.AllProjects,
		Archived:    options.Archived,
		Search:      strings.TrimSpace(options.Search),
		Limit:       DefaultPickerPageSize,
	}
	pageCtx, cancelPage := context.WithTimeout(ctx, pickerPageTimeout)
	page, err := source.Page(pageCtx, query)
	cancelPage()
	if err != nil {
		return PickerSelection{}, fmt.Errorf("coding resume picker catalog: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	model := newPickerModel(ctx, source, query, page, now)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if options.Input != nil {
		programOptions = append(programOptions, tea.WithInput(options.Input))
	}
	if options.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(options.Output))
	}
	if options.AlternateScreen {
		programOptions = append(programOptions, tea.WithAltScreen())
	}
	environment := append([]string(nil), options.Environment...)
	if options.NoColor {
		environment = append(environment, "NO_COLOR=1")
	}
	if len(environment) > 0 {
		programOptions = append(programOptions, tea.WithEnvironment(environment))
	}
	programFactory := options.newProgram
	if programFactory == nil {
		programFactory = defaultProgram
	}
	final, err := programFactory(model, programOptions...).Run()
	if err != nil {
		return PickerSelection{}, fmt.Errorf("coding resume picker: %w", err)
	}
	result, ok := final.(*pickerModel)
	if !ok {
		return PickerSelection{}, fmt.Errorf("coding resume picker returned an unexpected model")
	}
	return PickerSelection{ThreadID: result.selectedThreadID, Canceled: result.canceled}, nil
}

type pickerModel struct {
	ctx               context.Context
	source            codingpicker.Source
	query             codingpicker.Query
	pageQuery         codingpicker.Query
	page              codingpicker.Page
	selected          int
	selectedThreadID  string
	canceled          bool
	loading           bool
	searching         bool
	search            textinput.Model
	notice            string
	err               error
	width             int
	height            int
	now               func() time.Time
	requestGeneration uint64
	loadCancel        context.CancelFunc
	expanded          bool
}

type pickerPageMsg struct {
	generation uint64
	query      codingpicker.Query
	page       codingpicker.Page
	err        error
}

func newPickerModel(
	ctx context.Context,
	source codingpicker.Source,
	query codingpicker.Query,
	page codingpicker.Page,
	now func() time.Time,
) *pickerModel {
	search := textinput.New()
	search.Prompt = "Search: "
	search.Placeholder = "title, preview, path, branch, or ID"
	search.CharLimit = pickerSearchRunes
	return &pickerModel{
		ctx: ctx, source: source, query: query, pageQuery: query, page: page, search: search,
		width: 80, height: 24, now: now,
	}
}

func (m *pickerModel) Init() tea.Cmd { return nil }

func (m *pickerModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, message.Width)
		m.height = max(1, message.Height)
		m.search.Width = max(1, m.width-len(m.search.Prompt))
		return m, nil
	case pickerPageMsg:
		if message.generation != m.requestGeneration || message.query != m.query {
			return m, nil
		}
		m.loading = false
		m.loadCancel = nil
		m.err = message.err
		if message.err == nil {
			m.page = message.page
			m.pageQuery = message.query
			m.selected = 0
			m.expanded = false
			m.notice = ""
		}
		return m, nil
	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(message)
		}
		return m.updateKeys(message)
	}
	return m, nil
}

func (m *pickerModel) updateSearch(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "ctrl+c", "esc":
		m.searching = false
		m.search.Blur()
		m.search.SetValue(m.query.Search)
		return m, nil
	case "enter":
		m.searching = false
		m.search.Blur()
		query := m.query
		query.Search = strings.TrimSpace(m.search.Value())
		query.Offset = 0
		return m, m.load(query)
	}
	var command tea.Cmd
	m.search, command = m.search.Update(message)
	return m, command
}

func (m *pickerModel) updateKeys(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if message.Type == tea.KeyRunes && len(message.Runes) == 1 {
		switch message.Runes[0] {
		case 'Q':
			m.cancelLoad()
			m.canceled = true
			return m, tea.Quit
		case 'A':
			query := m.query
			query.AllProjects = !query.AllProjects
			query.Offset = 0
			return m, m.load(query)
		case 'Z':
			query := m.query
			query.Archived = !query.Archived
			query.Offset = 0
			return m, m.load(query)
		case 'R':
			query := m.query
			query.Offset = 0
			return m, m.load(query)
		case 'E':
			if len(m.page.Items) > 0 && m.selected < len(m.page.Items) {
				m.expanded = !m.expanded
			}
			return m, nil
		}
	}
	switch message.String() {
	case "ctrl+c", "esc":
		m.cancelLoad()
		m.canceled = true
		return m, tea.Quit
	case "/":
		m.searching = true
		m.search.SetValue(m.query.Search)
		m.search.CursorEnd()
		return m, m.search.Focus()
	case "up":
		if m.selected > 0 {
			m.selected--
			m.expanded = false
		}
	case "down":
		if m.selected+1 < len(m.page.Items) {
			m.selected++
			m.expanded = false
		}
	case "pgup", "left":
		if m.canPage() && m.query.Offset > 0 {
			query := m.query
			query.Offset = max(0, query.Offset-query.Limit)
			return m, m.load(query)
		}
	case "pgdown", "right":
		if m.canPage() && m.page.HasMore {
			query := m.query
			query.Offset = m.page.NextOffset
			return m, m.load(query)
		}
	case "enter":
		return m.selectCurrent()
	}
	if message.Type == tea.KeyRunes && len(message.Runes) > 0 {
		m.searching = true
		m.search.SetValue(m.query.Search)
		m.search.CursorEnd()
		focus := m.search.Focus()
		updated, input := m.updateSearch(message)
		return updated, tea.Batch(focus, input)
	}
	return m, nil
}

func (m *pickerModel) load(query codingpicker.Query) tea.Cmd {
	m.cancelLoad()
	m.query = query
	m.loading = true
	m.requestGeneration++
	generation := m.requestGeneration
	m.notice = ""
	m.err = nil
	requestCtx, cancel := context.WithTimeout(m.ctx, pickerPageTimeout)
	m.loadCancel = cancel
	return func() tea.Msg {
		defer cancel()
		page, err := m.source.Page(requestCtx, query)
		return pickerPageMsg{generation: generation, query: query, page: page, err: err}
	}
}

func (m *pickerModel) cancelLoad() {
	if m.loadCancel == nil {
		return
	}
	m.loadCancel()
	m.loadCancel = nil
}

func (m *pickerModel) canPage() bool {
	return !m.loading && m.err == nil && m.pageQuery == m.query
}

func (m *pickerModel) selectCurrent() (tea.Model, tea.Cmd) {
	if m.loading || m.err != nil || m.pageQuery != m.query || len(m.page.Items) == 0 ||
		m.selected >= len(m.page.Items) {
		return m, nil
	}
	item := m.page.Items[m.selected]
	switch {
	case item.Locked:
		owner := "another process"
		if item.LockOwnerPID > 0 {
			owner = fmt.Sprintf("pid %d", item.LockOwnerPID)
			if item.LockOwnerHost != "" {
				owner += " on " + item.LockOwnerHost
			}
		}
		m.notice = "thread is currently locked by " + owner
	case item.Location == codingpicker.LocationMissing:
		m.notice = "project is missing; explicit relocation is required"
	case item.Location == codingpicker.LocationMoved:
		m.notice = "project identity moved; explicit relocation is required"
	case item.Location != codingpicker.LocationAvailable:
		m.notice = "project state is unavailable; refresh before resuming"
	case !item.CurrentProject:
		m.notice = "thread belongs to " + item.ProjectRoot + "; change directory before resuming"
	default:
		m.selectedThreadID = item.ThreadID
		return m, tea.Quit
	}
	return m, nil
}

func (m *pickerModel) View() string {
	if m.height <= 2 {
		return pickerTitleStyle().Render(clipLine("Resume a previous session", m.width))
	}

	chrome := m.chromeLines()
	footer := m.footerLines()
	available := max(1, m.height-len(chrome)-len(footer))
	lines := append([]string(nil), chrome...)

	var content []string
	if len(m.page.Items) == 0 {
		empty := "No coding threads found. Start one with mintclaw code <prompt>."
		if m.query.Archived {
			empty = "No archived coding threads found. Change Status to Active."
		}
		if m.query.Search != "" {
			empty = "No coding threads match the current search. Press / to change it."
		}
		content = []string{clipLine(empty, m.width)}
	} else {
		content = m.visibleItemLines(available)
	}
	lines = append(lines, content...)
	for len(lines)+len(footer) < m.height {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m *pickerModel) chromeLines() []string {
	lines := []string{
		pickerTitleStyle().Render(clipLine("Resume a previous session", m.width)),
	}
	if m.height >= 8 {
		lines = append(lines, "")
	}
	lines = append(lines, m.toolbarLine())
	if m.height >= 10 {
		lines = append(lines, "")
	}
	lines = append(lines, m.searchLine())

	if !m.searching && m.notice != "" {
		lines = append(lines, pickerWarningStyle().Render(clipLine("Notice: "+m.notice, m.width)))
	} else if !m.searching && m.err != nil {
		lines = append(lines, pickerErrorStyle().Render(clipLine("Error: "+m.err.Error()+" · R retry", m.width)))
	} else if !m.searching {
		warning := m.catalogWarning()
		if warning != "" {
			lines = append(lines, pickerWarningStyle().Render(clipLine(warning, m.width)))
		}
	}
	if m.height >= 12 {
		lines = append(lines, "")
	}
	return lines
}

func (m *pickerModel) toolbarLine() string {
	scopeCurrent := !m.query.AllProjects
	statusActive := !m.query.Archived
	parts := []string{
		pickerSecondaryStyle().Render("Filter: "),
		pickerToolbarValue("Cwd", scopeCurrent),
		pickerToolbarValue("All", !scopeCurrent),
		"   ",
		pickerSecondaryStyle().Render("Status: "),
		pickerToolbarValue("Active", statusActive),
		pickerToolbarValue("Archived", !statusActive),
		"   ",
		pickerSecondaryStyle().Render("Sort: "),
		pickerToolbarValue("Updated", true),
	}
	return clipPickerStyledLine(strings.Join(parts, ""), m.width)
}

func (m *pickerModel) searchLine() string {
	if m.searching {
		return clipPickerStyledLine(m.search.View(), m.width)
	}
	if m.query.Search == "" {
		return pickerSecondaryStyle().Render(clipLine("Type to search", m.width))
	}
	return clipLine("Search: "+m.query.Search, m.width)
}

func (m *pickerModel) footerLines() []string {
	if m.height < 5 {
		return nil
	}
	position := 0
	if len(m.page.Items) > 0 {
		position = m.query.Offset + m.selected + 1
	}
	total := m.page.Matched
	progress := fmt.Sprintf(" %d / %d ", position, total)
	if m.loading {
		progress = fmt.Sprintf(" %d / %d · loading… ", position, total)
	}
	separatorWidth := max(0, m.width-pickerLineWidth(progress))
	separator := strings.Repeat("─", separatorWidth) + progress
	hints := "↑/↓ browse   enter resume   esc cancel   type search   A scope   Z status   E details"
	if m.width < 78 {
		hints = "↑/↓ browse · enter resume · esc cancel · type search"
	}
	return []string{
		pickerSecondaryStyle().Render(clipLine(separator, m.width)),
		pickerSecondaryStyle().Render(clipLine(hints, m.width)),
	}
}

func (m *pickerModel) catalogWarning() string {
	warnings := make([]string, 0, 2)
	if m.page.SkippedTotal > 0 {
		label := "corrupt catalog entries"
		if m.query.Search != "" {
			label = "search candidates with missing or invalid state"
		}
		warnings = append(warnings, fmt.Sprintf("%d %s skipped", m.page.SkippedTotal, label))
	}
	if m.page.ScanTruncated {
		warnings = append(warnings, "catalog scan truncated; narrow scope or search")
	}
	if m.page.ContentScanTruncated {
		warnings = append(warnings, "transcript search truncated; narrow scope or query")
	}
	return strings.Join(warnings, " · ")
}

func (m *pickerModel) visibleItemLines(available int) []string {
	var expanded []string
	if m.expanded && len(m.page.Items) > 0 && m.selected < len(m.page.Items) {
		expanded = m.expandedItemLines(m.page.Items[m.selected])
		maximum := max(0, available-1)
		if len(expanded) > maximum {
			expanded = append([]string(nil), expanded[:maximum]...)
			if maximum > 0 {
				expanded[maximum-1] = pickerSecondaryStyle().Render(clipLine("  └ …", m.width))
			}
		}
	}
	detailLines := len(expanded)
	visible := max(1, available-detailLines)
	start := min(max(0, m.selected-visible/2), max(0, len(m.page.Items)-visible))
	end := min(len(m.page.Items), start+visible)
	lines := make([]string, 0, end-start+detailLines)
	for index := start; index < end; index++ {
		item := m.page.Items[index]
		marker := "  "
		if index == m.selected {
			marker = pickerSelectedStyle().Render("› ")
		}
		age := formatPickerAge(m.now(), item.UpdatedAt)
		title := item.Title
		if item.MatchSnippet != "" {
			title += " — " + item.MatchSnippet
		}
		state := pickerItemBadge(item)
		prefixWidth := 2 + 12
		availableTitle := max(1, m.width-prefixWidth-pickerLineWidth(state))
		title = clipLine(title, availableTitle)
		if index == m.selected {
			title = pickerSelectedStyle().Render(title)
		}
		line := marker + pickerSecondaryStyle().Render(fmt.Sprintf("%-12s", age)) + title
		if state != "" {
			line += pickerSecondaryStyle().Render(state)
		}
		lines = append(lines, clipPickerStyledLine(line, m.width))
		if index == m.selected && m.expanded {
			lines = append(lines, expanded...)
		}
	}
	return lines
}

func pickerItemBadge(item codingpicker.Item) string {
	states := make([]string, 0, 5)
	switch item.Location {
	case codingpicker.LocationMissing:
		states = append(states, "missing")
	case codingpicker.LocationMoved:
		states = append(states, "moved")
	case codingpicker.LocationUnknown:
		states = append(states, "unknown")
	}
	if item.Dirty {
		states = append(states, "dirty")
	}
	if item.Stale {
		states = append(states, "stale")
	}
	if item.Locked {
		states = append(states, "locked")
	}
	if len(states) == 0 {
		return ""
	}
	return "  [" + strings.Join(states, ", ") + "]"
}

func pickerItemState(item codingpicker.Item) string {
	states := make([]string, 0, 6)
	switch item.Location {
	case codingpicker.LocationMissing:
		states = append(states, "[missing]")
	case codingpicker.LocationMoved:
		states = append(states, "[moved]")
	case codingpicker.LocationUnknown:
		states = append(states, "[state unknown]")
	}
	if item.RepositoryKnown {
		if item.Dirty {
			states = append(states, "[dirty]")
		} else {
			states = append(states, "[clean]")
		}
	}
	if item.Stale {
		states = append(states, "[stale]")
	}
	if item.Locked {
		states = append(states, "[locked]")
	}
	if item.StateIncomplete && item.Location != codingpicker.LocationUnknown {
		states = append(states, "[state incomplete]")
	}
	if len(states) == 0 {
		return "available"
	}
	return strings.Join(states, " · ")
}

func (m *pickerModel) expandedItemLines(item codingpicker.Item) []string {
	lines := []string{
		pickerSecondaryStyle().Render(clipLine("  │ Session:   "+item.ThreadID, m.width)),
		pickerSecondaryStyle().Render(clipLine("  │ Directory: "+item.InvocationCWD, m.width)),
		pickerSecondaryStyle().Render(clipLine("  │ Branch:    "+pickerItemBranch(item), m.width)),
		pickerSecondaryStyle().Render(clipLine("  │ State:     "+pickerItemState(item), m.width)),
	}
	if item.MatchKind != "" {
		match := item.MatchKind
		if item.MatchedMessage > 0 {
			match += fmt.Sprintf(" · message %d", item.MatchedMessage)
		}
		if !item.MatchedAt.IsZero() {
			match += " · " + item.MatchedAt.Local().Format(time.RFC3339)
		}
		lines = append(lines, pickerSecondaryStyle().Render(clipLine("  │ Match:     "+match, m.width)))
	}
	preview := strings.TrimSpace(item.Preview)
	if item.MatchSnippet != "" {
		preview = strings.TrimSpace(item.MatchSnippet)
	}
	if preview == "" {
		return lines
	}
	lines = append(lines, pickerSecondaryStyle().Render(clipLine("  │ Preview:", m.width)))
	remaining := preview
	for range 3 {
		if remaining == "" {
			break
		}
		line, rest := splitPickerDetail(remaining, max(1, m.width-6))
		prefix := "  │ "
		if rest == "" {
			prefix = "  └ "
		}
		lines = append(lines, pickerSecondaryStyle().Render(clipLine(prefix+line, m.width)))
		remaining = rest
	}
	return lines
}

func pickerItemBranch(item codingpicker.Item) string {
	if strings.TrimSpace(item.Branch) == "" {
		return "unknown"
	}
	return item.Branch
}

func pickerTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true)
}

func pickerSecondaryStyle() lipgloss.Style {
	return lipgloss.NewStyle().Faint(true)
}

func pickerSelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#337AB7"))
}

func pickerWarningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#8A5A00"))
}

func pickerErrorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#B42318"))
}

func pickerToolbarValue(value string, active bool) string {
	style := pickerSecondaryStyle()
	if active {
		style = lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	return style.Render(" " + value + " ")
}

func clipPickerStyledLine(value string, width int) string {
	if width <= 0 || ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func splitPickerDetail(value string, width int) (string, string) {
	if pickerLineWidth(value) <= width {
		return value, ""
	}
	runes := []rune(value)
	end := 0
	for end < len(runes) && pickerLineWidth(string(runes[:end+1])) <= width {
		end++
	}
	if end == 0 {
		end = 1
	}
	cut := end
	for cut > 0 && !unicode.IsSpace(runes[cut-1]) {
		cut--
	}
	if cut == 0 {
		cut = end
	}
	return strings.TrimSpace(string(runes[:cut])), strings.TrimSpace(string(runes[cut:]))
}

func formatPickerAge(now, updated time.Time) string {
	if updated.IsZero() {
		return "age unknown"
	}
	age := now.Sub(updated)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
	}
}

func pickerLineWidth(value string) int {
	return ansi.StringWidth(value)
}

var _ tea.Model = (*pickerModel)(nil)
