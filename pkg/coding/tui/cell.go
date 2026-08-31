package tui

import (
	"hash/fnv"
	"strings"
)

// cellRenderMode keeps the compact viewport, full transcript, and plain-text
// representations separate. A cell may share content between modes, but the
// caller never has to infer a full transcript from a compact preview.
type cellRenderMode uint8

const (
	cellRenderCompact cellRenderMode = iota
	cellRenderFull
	cellRenderPlain
)

type cellTheme uint8

const (
	cellThemeUnknown cellTheme = iota
	cellThemeDark
	cellThemeLight
)

type cellColorLevel uint8

const (
	cellColorNone cellColorLevel = iota
	cellColorANSI16
	cellColorANSI256
	cellColorTrueColor
)

// cellRenderContext contains only deterministic presentation capabilities. It
// intentionally has no Bubble Tea, viewport, terminal, or runtime dependency.
type cellRenderContext struct {
	Width      int
	Theme      cellTheme
	ColorLevel cellColorLevel
}

type cellIdentity struct {
	ID       string
	Kind     string
	Revision uint64
}

// cellStyleRole expresses semantic intent without choosing a terminal color.
// Later renderers can map these roles to a detected palette while plain mode
// discards them.
type cellStyleRole uint8

const (
	cellStyleDefault cellStyleRole = iota
	cellStyleMuted
	cellStyleAccent
	cellStyleSuccess
	cellStyleFailure
	cellStyleInsertion
	cellStyleDeletion
)

type cellSpan struct {
	Text string
	Role cellStyleRole
}

type cellLine struct {
	Spans []cellSpan
}

func plainCellLine(value string) cellLine {
	return cellLine{Spans: []cellSpan{{Text: value}}}
}

func (line cellLine) plainText() string {
	var text strings.Builder
	for _, span := range line.Spans {
		text.WriteString(span.Text)
	}
	return text.String()
}

type cellDocument struct {
	Lines     []cellLine
	Truncated bool
}

func (document cellDocument) plainText() string {
	lines := make([]string, 0, len(document.Lines))
	for _, line := range document.Lines {
		lines = append(lines, line.plainText())
	}
	return strings.Join(lines, "\n")
}

// transcriptCell is the renderer seam for one semantic transcript unit.
// Implementations own compact/full/plain representations and expose a stable
// revision so a future active-cell store can invalidate only changed layout.
type transcriptCell interface {
	Identity() cellIdentity
	Render(cellRenderContext, cellRenderMode) cellDocument
}

type legacyTranscriptCell struct {
	entry transcriptViewEntry
}

func newLegacyTranscriptCell(entry transcriptViewEntry) transcriptCell {
	return legacyTranscriptCell{entry: entry}
}

func (cell legacyTranscriptCell) Identity() cellIdentity {
	return cellIdentity{
		ID:       cell.entry.id,
		Kind:     "legacy",
		Revision: legacyTranscriptRevision(cell.entry),
	}
}

func (cell legacyTranscriptCell) Render(context cellRenderContext, _ cellRenderMode) cellDocument {
	width := max(1, context.Width)
	label := sanitizeTerminalText(cell.entry.label)
	body := sanitizeTerminalText(cell.entry.text)
	if cell.entry.truncated {
		body += "\n[…truncated]"
	}
	wrapped := wrapCellText(body, max(1, width-2))
	block := wrapCellText(label, width)
	if wrapped != "" {
		block += "\n" + indentTranscript(wrapped, "  ")
	}
	lines := strings.Split(block, "\n")
	document := cellDocument{Lines: make([]cellLine, 0, len(lines)), Truncated: cell.entry.truncated}
	for _, line := range lines {
		document.Lines = append(document.Lines, plainCellLine(line))
	}
	return document
}

func legacyTranscriptRevision(entry transcriptViewEntry) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(entry.id))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(entry.label))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(entry.text))
	truncationState := byte(0)
	if entry.truncated {
		truncationState = 1
	}
	_, _ = hash.Write([]byte{truncationState})
	return hash.Sum64()
}
