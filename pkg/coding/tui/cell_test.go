package tui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

type codexLikeUXScenario struct {
	Name       string             `json:"name"`
	Width      int                `json:"width"`
	Theme      string             `json:"theme"`
	ColorLevel string             `json:"color_level"`
	Golden     string             `json:"compact_golden"`
	Events     []codexLikeUXEvent `json:"events"`
}

type codexLikeUXEvent struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Lifecycle string `json:"lifecycle"`
}

type fixtureTranscriptCell struct {
	event codexLikeUXEvent
}

func (cell fixtureTranscriptCell) Identity() cellIdentity {
	return cellIdentity{ID: cell.event.ID, Kind: cell.event.Kind, Revision: 1}
}

func (cell fixtureTranscriptCell) Render(_ cellRenderContext, mode cellRenderMode) cellDocument {
	header := cell.event.Kind + " " + cell.event.Lifecycle
	switch mode {
	case cellRenderFull:
		return cellDocument{Lines: []cellLine{
			plainCellLine(header),
			plainCellLine("event " + cell.event.ID),
		}}
	case cellRenderPlain:
		return cellDocument{Lines: []cellLine{plainCellLine(header)}}
	default:
		return cellDocument{Lines: []cellLine{{Spans: []cellSpan{
			{Text: "• ", Role: cellStyleMuted},
			{Text: header, Role: cellStyleAccent},
		}}}}
	}
}

func TestCodexLikeUXGoldenScenariosExerciseCellModesAndLayout(t *testing.T) {
	scenarios := loadCodexLikeUXScenarios(t)
	wantNames := []string{
		"command_activity",
		"status_card",
		"working_and_failure",
		"elapsed_working",
		"commentary_compaction_and_diff",
		"plan_pending",
		"plan_progress",
	}
	if len(scenarios) != len(wantNames) {
		t.Fatalf("scenario count = %d, want %d", len(scenarios), len(wantNames))
	}
	for index, scenario := range scenarios {
		if scenario.Name != wantNames[index] {
			t.Fatalf("scenario[%d] = %q, want %q", index, scenario.Name, wantNames[index])
		}
		context := scenarioCellContext(t, scenario)
		cells := make([]transcriptCell, 0, len(scenario.Events))
		seen := make(map[string]struct{}, len(scenario.Events))
		for _, event := range scenario.Events {
			if event.ID == "" || event.Kind == "" || event.Lifecycle == "" {
				t.Fatalf("scenario %q has incomplete event: %+v", scenario.Name, event)
			}
			if _, duplicate := seen[event.ID]; duplicate {
				t.Fatalf("scenario %q repeats event ID %q", scenario.Name, event.ID)
			}
			seen[event.ID] = struct{}{}
			cells = append(cells, fixtureTranscriptCell{event: event})
		}
		for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
			rendered, layout := renderTranscriptCells(cells, context, mode, false, false, false)
			if mode == cellRenderCompact && rendered != scenario.Golden {
				t.Fatalf(
					"scenario %q compact golden mismatch:\nwant: %q\n got: %q",
					scenario.Name,
					scenario.Golden,
					rendered,
				)
			}
			if len(layout.blocks) != len(cells) {
				t.Fatalf("scenario %q mode %d layout = %+v", scenario.Name, mode, layout)
			}
			for eventIndex, event := range scenario.Events {
				if layout.blocks[eventIndex].id != event.ID || !strings.Contains(rendered, event.Kind) {
					t.Fatalf(
						"scenario %q mode %d lost event %+v: %q layout=%+v",
						scenario.Name,
						mode,
						event,
						rendered,
						layout,
					)
				}
			}
			for _, line := range strings.Split(rendered, "\n") {
				if width := ansi.StringWidth(line); width > scenario.Width {
					t.Fatalf(
						"scenario %q mode %d line width %d > %d: %q",
						scenario.Name,
						mode,
						width,
						scenario.Width,
						line,
					)
				}
			}
			if mode == cellRenderFull && !strings.Contains(rendered, "event "+scenario.Events[0].ID) {
				t.Fatalf("scenario %q full transcript omitted event detail: %q", scenario.Name, rendered)
			}
		}
	}
}

func TestLegacyTranscriptCellIdentityRevisionAndModes(t *testing.T) {
	entry := transcriptViewEntry{id: "assistant-1", label: "MintClaw", text: "hello", truncated: true}
	cell := newLegacyTranscriptCell(entry)
	identity := cell.Identity()
	if identity.ID != entry.id || identity.Kind != "legacy" || identity.Revision == 0 {
		t.Fatalf("identity = %+v", identity)
	}
	updated := newLegacyTranscriptCell(transcriptViewEntry{id: entry.id, label: entry.label, text: "hello again"})
	if updated.Identity().Revision == identity.Revision {
		t.Fatal("changed legacy content did not advance its structural revision")
	}
	for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
		document := cell.Render(cellRenderContext{Width: 20}, mode)
		if !document.Truncated || document.plainText() != "MintClaw\n  hello\n  […truncated]" {
			t.Fatalf("mode %d document = %+v text=%q", mode, document, document.plainText())
		}
	}
}

func loadCodexLikeUXScenarios(t *testing.T) []codexLikeUXScenario {
	t.Helper()
	content, err := os.ReadFile("testdata/codex_like_ux_scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var scenarios []codexLikeUXScenario
	if err := decoder.Decode(&scenarios); err != nil {
		t.Fatal(err)
	}
	return scenarios
}

func scenarioCellContext(t *testing.T, scenario codexLikeUXScenario) cellRenderContext {
	t.Helper()
	if !slices.Contains([]int{40, 80, 120}, scenario.Width) {
		t.Fatalf("scenario %q width = %d", scenario.Name, scenario.Width)
	}
	context := cellRenderContext{Width: scenario.Width}
	switch scenario.Theme {
	case "dark":
		context.Theme = cellThemeDark
	case "light":
		context.Theme = cellThemeLight
	default:
		t.Fatalf("scenario %q theme = %q", scenario.Name, scenario.Theme)
	}
	switch scenario.ColorLevel {
	case "none":
		context.ColorLevel = cellColorNone
	case "ansi16":
		context.ColorLevel = cellColorANSI16
	case "ansi256":
		context.ColorLevel = cellColorANSI256
	case "truecolor":
		context.ColorLevel = cellColorTrueColor
	default:
		t.Fatalf("scenario %q color level = %q", scenario.Name, scenario.ColorLevel)
	}
	return context
}
