package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestVisualFollowupIntegratedSurfaceGoldens(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		theme      cellTheme
		colorLevel cellColorLevel
	}{
		{name: "narrow-no-color", width: 40, theme: cellThemeDark, colorLevel: cellColorNone},
		{name: "standard-light", width: 80, theme: cellThemeLight, colorLevel: cellColorANSI16},
		{name: "wide-dark", width: 120, theme: cellThemeDark, colorLevel: cellColorTrueColor},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model := visualFollowupModel(t, testCase.theme)
			model.colorLevel = testCase.colorLevel
			model.resize(testCase.width, 32)
			raw := model.View()
			plain := trimVisualLinePadding(ansi.Strip(raw))
			goldenPath := filepath.Join("testdata", "visual_followup", testCase.name+".golden")
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Errorf("read %s: %v\nrendered:\n%s", goldenPath, err, plain)
			} else if want := strings.TrimRight(string(golden), "\r\n"); plain != want {
				t.Errorf("%s mismatch:\nwant:\n%s\n\ngot:\n%s", testCase.name, want, plain)
			}

			for _, forbidden := range []string{"## Repository summary", "**runtime**", "| Area |", "```"} {
				if strings.Contains(plain, forbidden) {
					t.Fatalf("surface retained Markdown delimiter %q:\n%s", forbidden, plain)
				}
			}
			lines := strings.Split(plain, "\n")
			if len(lines) >= 32 || len(lines) < 3 || lines[len(lines)-2] != "" {
				t.Fatalf("surface geometry uses %d rows or omits composer/footer gap:\n%s", len(lines), plain)
			}
			boundaryFound := false
			for _, line := range lines {
				if strings.Trim(line, "─") == "" && line != "" {
					boundaryFound = true
					if ansi.StringWidth(line) != testCase.width {
						t.Fatalf("boundary width = %d, want %d", ansi.StringWidth(line), testCase.width)
					}
				}
				if ansi.StringWidth(line) > testCase.width {
					t.Fatalf("line width %d > %d: %q", ansi.StringWidth(line), testCase.width, line)
				}
			}
			if !boundaryFound {
				t.Fatalf("surface omitted full-width turn boundary:\n%s", plain)
			}
			if testCase.colorLevel == cellColorNone && strings.Contains(raw, "\x1b[") {
				t.Fatalf("no-color surface emitted ANSI styling: %q", raw)
			} else if testCase.colorLevel != cellColorNone && !strings.Contains(raw, "\x1b[") {
				t.Fatalf("color-capable surface emitted no ANSI styling: %q", raw)
			}
		})
	}
}

func trimVisualLinePadding(value string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.Join(lines, "\n")
}

func visualFollowupModel(t *testing.T, theme cellTheme) *Model {
	t.Helper()
	projector, err := frontend.NewProjector("thread-visual", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.ThreadMetadataUpdated(frontend.ThreadMetadata{
		Title: "Repository summary", ProjectRoot: "/workspace/project", CWD: "/workspace/project",
		Model: "gpt-5.6-sol", Provider: "openai",
	})
	projector.RuntimeStatusUpdated(frontend.RuntimeStatus{
		ReasoningEffort: "off", Permission: frontend.PermissionFullAccess, Autonomy: frontend.AutonomyYolo,
	})
	projector.TurnStarted("turn-visual", "Summarize this repository")
	projector.ToolStarted("turn-visual", "tool-visual", "list_dir", "{}")
	projector.ToolCompleted("turn-visual", "tool-visual", "list_dir", "", 125*time.Millisecond, false, nil)
	projector.AssistantAccumulated(
		"turn-visual",
		"## Repository summary\n\nThe **runtime** keeps coding sessions durable.\n\n"+
			"| Area | Purpose |\n| --- | --- |\n| CLI | Start and resume threads |\n| TUI | Show work clearly |\n\n"+
			"- Sessions survive restarts.\n- Project files remain authoritative.",
		true,
	)
	projector.TurnCompleted("turn-visual", "completed")
	model, err := newModel(
		t.Context(),
		&fakeController{Projector: projector},
		modelOptions{adaptiveHeight: true, theme: theme, motionMode: MotionDisabled},
	)
	if err != nil {
		t.Fatal(err)
	}
	return model
}
