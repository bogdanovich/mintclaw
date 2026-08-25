package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	transcriptPageSize           = 64
	maxHydratedTranscriptEntries = 256
)

type transcriptPageMode uint8

const (
	transcriptPageInitial transcriptPageMode = iota
	transcriptPageOlder
	transcriptPageLatest
)

type transcriptWindow struct {
	historical  []frontend.TranscriptEntry
	start       int
	end         int
	total       int
	hasOlder    bool
	hasNewer    bool
	loading     bool
	initialized bool
	disabled    bool
}

func (w *transcriptWindow) apply(page frontend.TranscriptPage, mode transcriptPageMode) {
	w.loading = false
	w.initialized = true
	switch mode {
	case transcriptPageOlder:
		w.historical = mergeTranscriptEntries(page.Entries, w.historical)
		w.start = page.Start
		w.hasOlder = page.HasOlder
		w.total = page.Total
		if len(w.historical) > maxHydratedTranscriptEntries {
			w.historical = slices.Clone(w.historical[:maxHydratedTranscriptEntries])
			w.hasNewer = true
		} else {
			w.hasNewer = w.hasNewer || page.HasNewer
		}
	default:
		w.historical = boundedTranscriptEntries(page.Entries)
		w.start = page.Start
		w.end = page.End
		w.total = page.Total
		w.hasOlder = page.HasOlder
		w.hasNewer = page.HasNewer
	}
}

func (w *transcriptWindow) entries(live []frontend.TranscriptEntry) []frontend.TranscriptEntry {
	return mergeTranscriptEntries(w.historical, live)
}

func boundedTranscriptEntries(entries []frontend.TranscriptEntry) []frontend.TranscriptEntry {
	if len(entries) > maxHydratedTranscriptEntries {
		entries = entries[len(entries)-maxHydratedTranscriptEntries:]
	}
	return slices.Clone(entries)
}

func mergeTranscriptEntries(groups ...[]frontend.TranscriptEntry) []frontend.TranscriptEntry {
	seen := make(map[string]int)
	merged := make([]frontend.TranscriptEntry, 0)
	for _, entries := range groups {
		for _, entry := range entries {
			if index, ok := seen[entry.ID]; ok {
				merged[index] = entry
				continue
			}
			seen[entry.ID] = len(merged)
			merged = append(merged, entry)
		}
	}
	return merged
}

type transcriptViewEntry struct {
	id        string
	label     string
	text      string
	truncated bool
}

func buildTranscriptView(
	entries []frontend.TranscriptEntry,
	tools []frontend.ToolState,
	changedFiles []frontend.ChangedFile,
	workspace *codingworkspace.Snapshot,
	selectedToolID string,
	expandedToolID string,
) []transcriptViewEntry {
	display := make([]transcriptViewEntry, 0, len(entries)+len(tools)+2)
	toolsByTurn := make(map[string][]frontend.ToolState)
	for _, tool := range tools {
		toolsByTurn[tool.TurnID] = append(toolsByTurn[tool.TurnID], tool)
	}
	appendTools := func(turnID string) {
		for _, tool := range toolsByTurn[turnID] {
			id := toolViewID(tool)
			display = append(display, transcriptViewEntry{
				id:    id,
				label: toolCardLabel(tool, id == selectedToolID),
				text:  toolCardText(tool, id == expandedToolID),
			})
		}
		delete(toolsByTurn, turnID)
	}
	appendRepositoryState := func() {
		if entry, ok := verifiedWritesEntry(changedFiles); ok {
			display = append(display, entry)
		}
		if workspace != nil {
			display = append(display, workspaceChangesEntry(*workspace))
		}
	}
	lastAssistant := -1
	for index := range entries {
		if entries[index].Kind == frontend.EntryAssistant {
			lastAssistant = index
		}
	}
	for index, entry := range entries {
		if entry.Kind == frontend.EntryAssistant {
			appendTools(entry.TurnID)
			if index == lastAssistant {
				appendRepositoryState()
			}
		}
		display = append(display, transcriptViewEntry{
			id: entry.ID, label: transcriptEntryLabel(entry.Kind), text: entry.Text, truncated: entry.Truncated,
		})
	}
	for _, tool := range tools {
		appendTools(tool.TurnID)
	}
	if lastAssistant < 0 {
		appendRepositoryState()
	}
	return display
}

func toolViewID(tool frontend.ToolState) string {
	return "view:tool:" + tool.TurnID + ":" + tool.CallID
}

func toolCardLabel(tool frontend.ToolState, selected bool) string {
	marker := " "
	if selected {
		marker = "▶"
	}
	name := boundedSingleLine(tool.Name, 256)
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("%s Tool %s %s", marker, toolStatusMarker(tool.Status), name)
}

func toolStatusMarker(status frontend.ToolStatus) string {
	switch status {
	case frontend.ToolRunning:
		return "[running]"
	case frontend.ToolSuspended:
		return "[suspended]"
	case frontend.ToolSucceeded:
		return "[ok]"
	case frontend.ToolFailed:
		return "[failed]"
	case frontend.ToolInterrupted:
		return "[interrupted]"
	default:
		return "[unknown]"
	}
}

func toolCardText(tool frontend.ToolState, expanded bool) string {
	metadata := make([]string, 0, 8)
	metadata = append(metadata, "status "+toolStatusText(tool.Status))
	if tool.Duration > 0 {
		metadata = append(metadata, "duration "+formatToolDuration(tool.Duration))
	}
	if command := tool.Command; command != nil {
		commandStatus := strings.TrimSpace(string(command.Status))
		if commandStatus == "" {
			commandStatus = "unknown"
		}
		metadata = append(metadata, "command "+commandStatus)
		if command.ExitCode != nil {
			metadata = append(metadata, "exit "+strconv.Itoa(*command.ExitCode))
		}
		if command.Background {
			metadata = append(metadata, "background")
		}
		if command.Canceled {
			metadata = append(metadata, "canceled")
		}
		if command.TimedOut {
			metadata = append(metadata, "timed out")
		}
	}
	lines := []string{strings.Join(metadata, " · ")}
	truncated := tool.OutputTruncated || tool.Command != nil && tool.Command.Truncated
	if truncated {
		lines = append(lines, "[output truncated]")
	}
	writeLines := make([]string, 0, len(tool.WriteAudit))
	for _, audit := range tool.WriteAudit {
		if !audit.Success || audit.Kind != "file" {
			continue
		}
		writeLines = append(
			writeLines,
			"  "+strings.TrimSpace(boundedSingleLine(audit.Action, 128)+" "+boundedSingleLine(audit.Target, 512)),
		)
	}
	if len(writeLines) > 0 {
		lines = append(lines, "verified writes:")
		lines = append(lines, writeLines...)
	}
	if !expanded {
		if toolHasDisplayOutput(tool) {
			lines = append(lines, "output available · Alt+J/K select · Ctrl+O expand")
		}
		return strings.Join(lines, "\n")
	}
	lines = append(lines, expandedToolOutput(tool)...)
	lines = append(lines, "Ctrl+O collapse")
	return strings.Join(lines, "\n")
}

func toolStatusText(status frontend.ToolStatus) string {
	value := strings.TrimSpace(string(status))
	if value == "" {
		return "unknown"
	}
	return value
}

func toolHasDisplayOutput(tool frontend.ToolState) bool {
	return tool.Command != nil &&
		(tool.Command.Stdout != "" || tool.Command.Stderr != "" || tool.Command.Output != "")
}

func expandedToolOutput(tool frontend.ToolState) []string {
	if tool.Command == nil {
		return nil
	}
	command := tool.Command
	lines := make([]string, 0, 6)
	if command.Stdout != "" {
		lines = append(lines, "stdout:", command.Stdout)
	}
	if command.Stderr != "" {
		lines = append(lines, "stderr:", command.Stderr)
	}
	if command.Stdout == "" && command.Stderr == "" && command.Output != "" {
		lines = append(lines, "output:", command.Output)
	}
	if len(lines) == 0 {
		lines = append(lines, "output: (empty)")
	}
	return lines
}

func formatToolDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return duration.Round(time.Millisecond).String()
	case duration < time.Minute:
		return duration.Round(100 * time.Millisecond).String()
	default:
		return duration.Round(time.Second).String()
	}
}

func verifiedWritesEntry(files []frontend.ChangedFile) (transcriptViewEntry, bool) {
	if len(files) == 0 {
		return transcriptViewEntry{}, false
	}
	lines := []string{"Successful file write audits from this session:"}
	for _, file := range files {
		lines = append(lines, boundedSingleLine(file.Action, 128)+" "+boundedSingleLine(file.Path, 512))
	}
	return transcriptViewEntry{
		id:    "view:verified-writes",
		label: "Verified writes",
		text:  strings.Join(lines, "\n"),
	}, true
}

func workspaceChangesEntry(snapshot codingworkspace.Snapshot) transcriptViewEntry {
	lines := make([]string, 0, len(snapshot.ChangedPaths)+5)
	switch {
	case !snapshot.Git.Available:
		lines = append(lines, "repository status unavailable")
	case !snapshot.Git.StatusAvailable:
		lines = append(lines, "repository status unknown")
	case snapshot.Git.Dirty:
		lines = append(lines, "repository is dirty")
	default:
		lines = append(lines, "repository is clean")
	}
	if snapshot.DiffStatAvailable {
		stat := snapshot.DiffStat
		lines = append(lines, fmt.Sprintf(
			"diff stat: %d files · +%d -%d · %d binary",
			stat.Files,
			stat.Additions,
			stat.Deletions,
			stat.BinaryFiles,
		))
	}
	for _, path := range snapshot.ChangedPaths {
		line := boundedSingleLine(path.Status, 32) + " " + boundedSingleLine(path.Path, 512)
		if path.OriginalPath != "" {
			line += " ← " + boundedSingleLine(path.OriginalPath, 512)
		}
		lines = append(lines, line)
	}
	if snapshot.Truncated {
		lines = append(lines, "[workspace observation truncated]")
	}
	if snapshot.Warning != "" {
		lines = append(lines, "warning: "+boundedSingleLine(snapshot.Warning, 512))
	}
	lines = append(lines, "Ctrl+R refresh repository status")
	return transcriptViewEntry{id: "view:workspace", label: "Repository changes", text: strings.Join(lines, "\n")}
}

func boundedSingleLine(value string, maximumBytes int) string {
	value = sanitizeTerminalText(value)
	value = strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(value)
	if maximumBytes <= 0 || len(value) <= maximumBytes {
		return value
	}
	value = value[:maximumBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

type transcriptBlock struct {
	id    string
	start int
	end   int
}

type transcriptLayout struct {
	blocks []transcriptBlock
}

type transcriptAnchor struct {
	id     string
	offset int
	valid  bool
}

func (l transcriptLayout) anchorAt(line int) transcriptAnchor {
	for _, block := range l.blocks {
		if line >= block.start && line < block.end {
			return transcriptAnchor{id: block.id, offset: line - block.start, valid: true}
		}
		if line < block.start {
			return transcriptAnchor{id: block.id, valid: true}
		}
	}
	if len(l.blocks) > 0 {
		block := l.blocks[len(l.blocks)-1]
		return transcriptAnchor{id: block.id, offset: max(0, block.end-block.start-1), valid: true}
	}
	return transcriptAnchor{}
}

func (l transcriptLayout) lineFor(anchor transcriptAnchor) (int, bool) {
	if !anchor.valid {
		return 0, false
	}
	for _, block := range l.blocks {
		if block.id == anchor.id {
			return block.start + min(anchor.offset, max(0, block.end-block.start-1)), true
		}
	}
	return 0, false
}

func renderTranscript(
	entries []transcriptViewEntry,
	width int,
	hasOlder bool,
	hasNewer bool,
	loading bool,
) (string, transcriptLayout) {
	width = max(1, width)
	var content strings.Builder
	layout := transcriptLayout{blocks: make([]transcriptBlock, 0, len(entries))}
	line := 0
	appendText := func(value string) {
		if content.Len() > 0 {
			content.WriteByte('\n')
			line++
		}
		content.WriteString(value)
		line += strings.Count(value, "\n")
	}
	if loading {
		appendText("Loading earlier transcript…")
	} else if hasOlder {
		appendText("↑ More transcript available (Page Up)")
	}
	for _, entry := range entries {
		if content.Len() > 0 {
			content.WriteString("\n\n")
			line += 2
		}
		start := line
		label := sanitizeTerminalText(entry.label)
		body := sanitizeTerminalText(entry.text)
		if entry.truncated {
			body += "\n[…truncated]"
		}
		wrapped := ansi.Wrap(body, max(1, width-2), "")
		block := ansi.Wrap(label, width, "")
		if wrapped != "" {
			block += "\n" + indentTranscript(wrapped, "  ")
		}
		content.WriteString(block)
		line += strings.Count(block, "\n")
		layout.blocks = append(layout.blocks, transcriptBlock{id: entry.id, start: start, end: line + 1})
	}
	if hasNewer {
		appendText("↓ Newer hydrated transcript omitted; press Alt+End to reload latest")
	}
	return content.String(), layout
}

func transcriptEntryLabel(kind frontend.EntryKind) string {
	switch kind {
	case frontend.EntryUser:
		return "You"
	case frontend.EntryAssistant:
		return "MintClaw"
	case frontend.EntryReasoning:
		return "Reasoning"
	case frontend.EntryTool:
		return "Tool"
	case frontend.EntryWarning:
		return "Warning"
	case frontend.EntryError:
		return "Error"
	default:
		return "Transcript"
	}
}

func sanitizeTerminalText(value string) string {
	value = ansi.Strip(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
}

func indentTranscript(value string, prefix string) string {
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}
