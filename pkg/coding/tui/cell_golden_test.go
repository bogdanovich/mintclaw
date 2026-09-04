package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type semanticCellScenario struct {
	Name       string                      `json:"name"`
	Width      int                         `json:"width"`
	Theme      string                      `json:"theme"`
	ColorLevel string                      `json:"color_level"`
	Items      []frontend.PresentationItem `json:"items"`
	Expected   []semanticCellExpectation   `json:"expected"`
}

type semanticCellExpectation struct {
	ID        string                         `json:"id"`
	Kind      frontend.PresentationKind      `json:"kind"`
	Lifecycle frontend.PresentationLifecycle `json:"lifecycle"`
	Revision  uint64                         `json:"revision"`
}

func TestSemanticCellGoldenScenarios(t *testing.T) {
	scenarios := loadSemanticCellScenarios(t)
	wantNames := []string{
		"command_activity",
		"status_card",
		"working_and_failure",
		"elapsed_working",
		"commentary_compaction_and_diff",
		"plan_pending",
		"plan_progress",
	}
	if names := scenarioNames(scenarios); !slices.Equal(names, wantNames) {
		t.Fatalf("scenario names = %q, want %q", names, wantNames)
	}
	seenWidths := make(map[int]bool)
	seenThemes := make(map[cellTheme]bool)
	seenColors := make(map[cellColorLevel]bool)
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			context := scenarioRenderContext(t, scenario)
			seenWidths[context.Width] = true
			seenThemes[context.Theme] = true
			seenColors[context.ColorLevel] = true
			assertDeterministicSemanticFixture(t, scenario)

			store, err := newSemanticCellStore(scenario.Items)
			if err != nil {
				t.Fatal(err)
			}
			if storedItems := semanticStoreItems(store); !reflect.DeepEqual(storedItems, scenario.Items) {
				t.Fatalf("semantic store changed fixture payloads: store=%+v fixture=%+v", storedItems, scenario.Items)
			}
			assertSemanticCellExpectations(t, store, scenario.Expected)
			for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
				name := cellRenderModeName(mode)
				rendered, layout := renderSemanticCells(store.cells(), context, mode)
				goldenPath := filepath.Join("testdata", "semantic_cells", scenario.Name+"."+name+".golden")
				golden, readErr := os.ReadFile(goldenPath)
				if readErr != nil {
					t.Errorf("read %s: %v\nrendered:\n%s", goldenPath, readErr, rendered)
				} else if want := strings.TrimRight(string(golden), "\r\n"); rendered != want {
					difference := firstStringDifference(want, rendered)
					t.Errorf(
						"%s mismatch at byte %d (want len %d, got len %d):\n"+
							"want: %q\n got: %q",
						name,
						difference,
						len(want),
						len(rendered),
						want,
						rendered,
					)
				}
				if len(layout.Blocks) != len(scenario.Expected) {
					t.Fatalf("%s layout = %+v", name, layout)
				}
				for index, expected := range scenario.Expected {
					if layout.Blocks[index].ID != expected.ID {
						t.Fatalf("%s layout[%d] ID = %q, want %q", name, index, layout.Blocks[index].ID, expected.ID)
					}
				}
				for _, line := range strings.Split(rendered, "\n") {
					if width := ansi.StringWidth(line); width > context.Width {
						t.Fatalf("%s line width %d > %d: %q", name, width, context.Width, line)
					}
				}
			}
		})
	}
	if !reflect.DeepEqual(seenWidths, map[int]bool{40: true, 80: true, 120: true}) {
		t.Fatalf("scenario widths = %v", seenWidths)
	}
	if !reflect.DeepEqual(seenThemes, map[cellTheme]bool{cellThemeDark: true, cellThemeLight: true}) {
		t.Fatalf("scenario themes = %v", seenThemes)
	}
	if !reflect.DeepEqual(seenColors, map[cellColorLevel]bool{
		cellColorNone: true, cellColorANSI16: true, cellColorANSI256: true, cellColorTrueColor: true,
	}) {
		t.Fatalf("scenario color levels = %v", seenColors)
	}
}

func loadSemanticCellScenarios(t *testing.T) []semanticCellScenario {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "semantic_cell_scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var scenarios []semanticCellScenario
	if err := decoder.Decode(&scenarios); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("semantic cell fixture trailing JSON: %v", err)
	}
	return scenarios
}

func scenarioNames(scenarios []semanticCellScenario) []string {
	names := make([]string, len(scenarios))
	for index, scenario := range scenarios {
		names[index] = scenario.Name
	}
	return names
}

func scenarioRenderContext(t *testing.T, scenario semanticCellScenario) cellRenderContext {
	t.Helper()
	context := cellRenderContext{Width: scenario.Width}
	switch scenario.Theme {
	case "dark":
		context.Theme = cellThemeDark
	case "light":
		context.Theme = cellThemeLight
	default:
		t.Fatalf("unknown theme %q", scenario.Theme)
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
		t.Fatalf("unknown color level %q", scenario.ColorLevel)
	}
	if err := validateCellRenderContext(context); err != nil {
		t.Fatal(err)
	}
	return context
}

func assertDeterministicSemanticFixture(t *testing.T, scenario semanticCellScenario) {
	t.Helper()
	if strings.TrimSpace(scenario.Name) == "" || len(scenario.Items) == 0 || len(scenario.Expected) == 0 {
		t.Fatalf("incomplete semantic fixture: %+v", scenario)
	}
	for _, item := range scenario.Items {
		if !item.CreatedAt.IsZero() || !item.StartedAt.IsZero() || item.CompletedAt != nil {
			t.Fatalf("scenario %q contains host-dependent timestamp in %+v", scenario.Name, item)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(encoded))
		for _, forbidden := range []string{"/users/", "/home/", `c:\\`, "secret", "animation_phase"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("scenario %q contains forbidden host/secret state %q in %s", scenario.Name, forbidden, encoded)
			}
		}
	}
}

func assertSemanticCellExpectations(
	t *testing.T,
	store semanticCellStore,
	expected []semanticCellExpectation,
) {
	t.Helper()
	if len(store.ordered) != len(expected) {
		t.Fatalf("semantic cells = %d, want %d", len(store.ordered), len(expected))
	}
	for index, want := range expected {
		identity := store.ordered[index].Identity()
		if identity.ID != want.ID || identity.Kind != want.Kind || identity.Lifecycle != want.Lifecycle ||
			identity.Revision != want.Revision {
			t.Fatalf("semantic identity[%d] = %+v, want %+v", index, identity, want)
		}
		if index != 0 && identity.Sequence <= store.ordered[index-1].Identity().Sequence {
			t.Fatalf("semantic sequence did not advance at %d: %+v", index, identity)
		}
	}
}

func cellRenderModeName(mode cellRenderMode) string {
	switch mode {
	case cellRenderCompact:
		return "compact"
	case cellRenderFull:
		return "full"
	case cellRenderPlain:
		return "plain"
	default:
		return fmt.Sprintf("unknown-%d", mode)
	}
}

func semanticStoreItems(store semanticCellStore) []frontend.PresentationItem {
	items := make([]frontend.PresentationItem, len(store.ordered))
	for index, cell := range store.ordered {
		items[index] = cloneCellPresentationItem(cell.item)
	}
	return items
}

func firstStringDifference(left, right string) int {
	limit := min(len(left), len(right))
	for index := range limit {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}
