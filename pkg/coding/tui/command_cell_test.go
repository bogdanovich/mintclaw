package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestCompactCommandEvidenceUsesFiveLineHeadTailPreview(t *testing.T) {
	lines := compactCommandEvidenceLines(strings.Join([]string{
		"line-1", "line-2", "line-3", "line-4", "line-5", "line-6", "line-7", "line-8",
	}, "\n"), 80)
	if len(lines) != 5 {
		t.Fatalf("compact evidence lines = %d, want 5: %+v", len(lines), lines)
	}
	text := make([]string, len(lines))
	for index := range lines {
		text[index] = lines[index].plainText()
	}
	joined := strings.Join(text, "\n")
	for _, want := range []string{"line-1", "line-2", "… 4 lines omitted …", "line-7", "line-8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("head-tail preview omits %q: %q", want, joined)
		}
	}
}

func TestTruncatedCommandRendersOneExplicitCompactMarker(t *testing.T) {
	item := semanticToolItem("command", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	item.Tool.Command = &frontend.CommandState{
		Command: "printf output", Status: frontend.CommandSucceeded, Output: "bounded", Truncated: true,
	}
	document := newPresentationCell(item).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	text := strings.ToLower(document.plainText())
	if !document.Truncated || strings.Count(text, "truncated") != 1 || strings.Contains(text, "[…truncated]") {
		t.Fatalf("truncated command marker = %q", document.plainText())
	}
}

func TestCommandCellsRenderDistinctLifecycleAndUserShellStates(t *testing.T) {
	exitZero := 0
	exitFailure := 9
	tests := []struct {
		name    string
		command frontend.CommandState
		want    string
	}{
		{
			name:    "running",
			command: frontend.CommandState{Command: "go test", Status: frontend.CommandRunning},
			want:    "Running go test",
		},
		{
			name: "success", command: frontend.CommandState{
				Command: "go test", Status: frontend.CommandSucceeded, ExitCode: &exitZero,
			}, want: "Ran go test",
		},
		{
			name: "user shell", command: frontend.CommandState{
				Command: "git status", Status: frontend.CommandSucceeded,
				Source: frontend.CommandSourceUserShell, ExitCode: &exitZero,
			}, want: "You ran git status",
		},
		{
			name: "failed", command: frontend.CommandState{
				Command: "go test", Status: frontend.CommandFailed, ExitCode: &exitFailure,
			}, want: "Command failed go test",
		},
		{
			name: "interrupted", command: frontend.CommandState{
				Command: "go test", Status: frontend.CommandCanceled,
			}, want: "Command interrupted go test",
		},
		{
			name: "unknown orphan", command: frontend.CommandState{
				Command: "go test", Status: frontend.CommandUnknown, Orphan: true,
			}, want: "Command outcome unknown go test",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := semanticToolItem("command", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
			item.Tool.Command = &test.command
			text := newPresentationCell(item).Render(
				cellRenderContext{Width: 80},
				cellRenderFull,
			).plainText()
			if !strings.Contains(text, test.want) {
				t.Fatalf("command cell omits %q: %q", test.want, text)
			}
			if test.command.Orphan && !strings.Contains(text, "without a matching start event") {
				t.Fatalf("orphan cell is not explicit: %q", text)
			}
		})
	}
}

func TestCommandCellSanitizesTerminalControlsAtNarrowAndWideWidths(t *testing.T) {
	item := semanticToolItem("command", 1, 1, frontend.PresentationFailed, frontend.ToolFailed)
	item.Tool.Command = &frontend.CommandState{
		Command: "printf '\x1b[2J'\x1b]0;forged\a", CWD: "/repo\rforged", Status: frontend.CommandFailed,
		Transcript: []frontend.CommandTranscriptEntry{{
			Sequence: 1, Stream: "stdout", Text: "safe\x1b[31m red\x1b[0m\rforged\a\n",
		}},
	}
	for _, width := range []int{16, 120} {
		for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
			rendered := renderCellDocument(
				newPresentationCell(item).Render(cellRenderContext{Width: width}, mode),
				cellRenderContext{Width: width},
				mode,
			)
			plain := ansi.Strip(rendered)
			if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\a") || strings.Contains(plain, "\r") {
				t.Fatalf("width %d mode %d retained terminal controls: %q", width, mode, plain)
			}
			for _, line := range strings.Split(rendered, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("width %d mode %d overflowed: %q", width, mode, line)
				}
			}
		}
	}
}

func TestFullTranscriptPanelIsPlainCompleteBoundedAndToggleable(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	projector.ToolStarted("turn-1", "call-1", "exec", "fields: command")
	exitCode := 0
	projector.ToolCommandOutput("turn-1", "call-1", frontend.CommandState{
		Action: "run", Command: "printf output", CWD: "/repo", Source: frontend.CommandSourceAgent,
		Status: frontend.CommandSucceeded, OwnsProcess: true, ExitCode: &exitCode, Duration: time.Second,
		Transcript: []frontend.CommandTranscriptEntry{
			{Sequence: 1, Stream: "stdout", Text: "first\n"},
			{Sequence: 2, Stream: "stderr", Text: "second\x1b[2J\n"},
		},
	})
	projector.ToolCompleted("turn-1", "call-1", "exec", "", time.Second, false, nil)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(64, 16)
	model.transcript.hasOlder = true

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	if !model.transcriptOverlay.active {
		t.Fatal("Ctrl+T did not open the transcript overlay")
	}
	content := model.transcriptOverlayView()
	for _, want := range []string{
		"Full transcript", "earlier transcript omitted", "› inspect", "$ printf output", "stdout> first",
		"stderr> second", "succeeded · exit 0 · 1s",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("full transcript omits %q: %q", want, content)
		}
	}
	if strings.Contains(content, "\x1b") {
		t.Fatalf("full transcript contains terminal styling/control: %q", content)
	}
	for _, line := range strings.Split(content, "\n") {
		if ansi.StringWidth(line) > 64 {
			t.Fatalf("full transcript line is too wide: %q", line)
		}
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	if model.transcriptOverlay.active {
		t.Fatal("second Ctrl+T left transcript overlay open")
	}
}
