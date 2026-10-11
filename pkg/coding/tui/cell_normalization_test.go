package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestExpandCellLineTabsPreservesUnicodeColumnsAndRoles(t *testing.T) {
	line := cellLine{Spans: []cellSpan{
		{Text: "a界\tb", Role: cellStyleDefault},
		{Text: "\te\u0301", Role: cellStyleMuted},
	}}

	expanded := expandCellLineTabs(line, 4)
	if got, want := expanded.plainText(), "a界 b   e\u0301"; got != want {
		t.Fatalf("expanded tabs = %q, want %q", got, want)
	}
	if got, want := len(expanded.Spans), 2; got != want {
		t.Fatalf("expanded spans = %d, want %d: %+v", got, want, expanded.Spans)
	}
	if expanded.Spans[0].Role != cellStyleDefault || expanded.Spans[1].Role != cellStyleMuted {
		t.Fatalf("expanded roles = %+v", expanded.Spans)
	}
}

func TestReplaceOverwideCellSpanGraphemesPreservesTextBetweenReplacements(t *testing.T) {
	spans := []cellSpan{{Text: "a界b👨‍👩‍👧‍👦c", Role: cellStyleDefault}}

	bounded := replaceOverwideCellSpanGraphemes(spans, 1)
	if got, want := (cellLine{Spans: bounded}).plainText(), "a�b�c"; got != want {
		t.Fatalf("bounded graphemes = %q, want %q", got, want)
	}
	unmodified := replaceOverwideCellSpanGraphemes(spans, 2)
	if got := (cellLine{Spans: unmodified}).plainText(); got != spans[0].Text {
		t.Fatalf("width-two graphemes = %q, want original text", got)
	}
}

func TestCellSpanNormalizationMatchesGraphemeReference(t *testing.T) {
	lines := []cellLine{
		{},
		{Spans: []cellSpan{{Text: "plain", Role: cellStyleDefault}}},
		{Spans: []cellSpan{{Text: "\tleading", Role: cellStyleMuted}}},
		{Spans: []cellSpan{
			{Text: "界\tmiddle\t", Role: cellStyleDefault},
			{Text: "e\u0301👨‍👩‍👧‍👦\ttail", Role: cellStyleAccent},
		}},
	}
	for _, line := range lines {
		for _, tabWidth := range []int{1, 2, 4, 8} {
			got := expandCellLineTabs(line, tabWidth)
			want := referenceExpandCellLineTabs(line, tabWidth)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("tab width %d expanded line = %+v, want %+v", tabWidth, got, want)
			}
		}
	}

	spans := []cellSpan{
		{Text: "plain 界 text", Role: cellStyleDefault},
		{Text: "e\u0301👨‍👩‍👧‍👦界", Role: cellStyleMuted},
	}
	for _, width := range []int{1, 2, 4} {
		got := replaceOverwideCellSpanGraphemes(spans, width)
		want := referenceReplaceOverwideCellSpanGraphemes(spans, width)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("width %d bounded spans = %+v, want %+v", width, got, want)
		}
	}
}

func TestCellSpanNormalizationAllocationsRemainBounded(t *testing.T) {
	line := cellLine{Spans: []cellSpan{{
		Text: strings.Repeat("bounded hydration text ", 512),
		Role: cellStyleDefault,
	}}}

	expandAllocations := testing.AllocsPerRun(25, func() {
		_ = expandCellLineTabs(line, 4)
	})
	if expandAllocations > 8 {
		t.Fatalf("tab-free expansion allocations = %.0f, want at most 8", expandAllocations)
	}
	tabbedLine := cellLine{Spans: []cellSpan{{
		Text: strings.Repeat("a\t", 512),
		Role: cellStyleDefault,
	}}}
	tabbedAllocations := testing.AllocsPerRun(25, func() {
		_ = expandCellLineTabs(tabbedLine, 4)
	})
	if tabbedAllocations > 16 {
		t.Fatalf("tab-heavy expansion allocations = %.0f, want at most 16", tabbedAllocations)
	}
	replaceAllocations := testing.AllocsPerRun(25, func() {
		_ = replaceOverwideCellSpanGraphemes(line.Spans, 80)
	})
	if replaceAllocations > 8 {
		t.Fatalf("unchanged grapheme allocations = %.0f, want at most 8", replaceAllocations)
	}
}

func referenceExpandCellLineTabs(line cellLine, tabWidth int) cellLine {
	tabWidth = max(1, tabWidth)
	expanded := cellLine{RowStyle: line.RowStyle, structuralBlank: line.structuralBlank}
	column := 0
	for _, span := range line.Spans {
		value := span.Text
		for value != "" {
			cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			if cluster == "\t" {
				spaces := tabWidth - column%tabWidth
				appendCellSpan(&expanded.Spans, strings.Repeat(" ", spaces), span.Role)
				column += spaces
				continue
			}
			appendCellSpan(&expanded.Spans, cluster, span.Role)
			column += max(0, width)
		}
	}
	return expanded
}

func referenceReplaceOverwideCellSpanGraphemes(spans []cellSpan, widthLimit int) []cellSpan {
	widthLimit = max(1, widthLimit)
	bounded := make([]cellSpan, 0, len(spans))
	for _, span := range spans {
		value := span.Text
		for value != "" {
			cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			if width > widthLimit {
				cluster = "�"
			}
			appendCellSpan(&bounded, cluster, span.Role)
		}
	}
	return bounded
}
