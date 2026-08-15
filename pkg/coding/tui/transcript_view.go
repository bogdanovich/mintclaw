package tui

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
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

func transcriptDisplayEntries(
	entries []frontend.TranscriptEntry,
	tools []frontend.ToolState,
) []frontend.TranscriptEntry {
	display := slices.Clone(entries)
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			name = "tool"
		}
		display = append(display, frontend.TranscriptEntry{
			ID:       "tool:" + tool.CallID,
			TurnID:   tool.TurnID,
			Kind:     frontend.EntryTool,
			Text:     name + " · " + string(tool.Status),
			Complete: tool.Status != frontend.ToolRunning && tool.Status != frontend.ToolSuspended,
		})
	}
	return display
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
	entries []frontend.TranscriptEntry,
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
		label := transcriptEntryLabel(entry.Kind)
		body := sanitizeTerminalText(entry.Text)
		if entry.Truncated {
			body += "\n[…truncated]"
		}
		wrapped := ansi.Wordwrap(body, max(1, width-2), "")
		block := label
		if wrapped != "" {
			block += "\n" + indentTranscript(wrapped, "  ")
		}
		content.WriteString(block)
		line += strings.Count(block, "\n")
		layout.blocks = append(layout.blocks, transcriptBlock{id: entry.ID, start: start, end: line + 1})
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
