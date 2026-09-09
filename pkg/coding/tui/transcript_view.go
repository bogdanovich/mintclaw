package tui

import (
	"fmt"
	"slices"
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
	merged := mergeTranscriptEntries(w.historical, live)
	return slices.DeleteFunc(merged, func(entry frontend.TranscriptEntry) bool {
		return entry.EvidenceOnly
	})
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

type staticCellContent struct {
	label     string
	text      string
	truncated bool
}

func workspaceChangesEntry(snapshot codingworkspace.Snapshot) staticCellContent {
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
	return staticCellContent{label: "Repository changes", text: strings.Join(lines, "\n")}
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

type transcriptAnchor struct {
	id     string
	offset int
	before bool
	valid  bool
}

func sanitizeTerminalText(value string) string {
	value = ansi.Strip(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unsafeBidiControl(r) {
			return -1
		}
		return r
	}, value)
}

func unsafeBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u206f')
}

func indentTranscript(value string, prefix string) string {
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}
