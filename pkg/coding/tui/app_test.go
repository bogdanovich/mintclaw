package tui

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type fakeProgram struct {
	model tea.Model
	err   error
}

func (p fakeProgram) Run() (tea.Model, error) {
	return p.model, p.err
}

func TestRunClosesControllerAndLeavesBoundedAlternateScreenSummary(t *testing.T) {
	controller, snapshot := newController(t)
	controller.AssistantAccumulated("turn-1", strings.Repeat("x", finalAnswerBytes+100), true)
	output := &bytes.Buffer{}

	err := Run(t.Context(), controller, Options{
		Output:          output,
		InitialPrompt:   "fix it",
		AlternateScreen: true,
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			return fakeProgram{model: model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if controller.submits.Load() != 1 || controller.closes.Load() != 1 {
		t.Fatalf("submits=%d closes=%d", controller.submits.Load(), controller.closes.Load())
	}
	if !strings.Contains(output.String(), snapshot.ThreadID) || len(output.String()) > finalAnswerBytes+200 {
		t.Fatalf("final summary is missing or unbounded: bytes=%d", output.Len())
	}
}

func TestRunClosesControllerAfterProgramFailure(t *testing.T) {
	controller, _ := newController(t)
	injected := errors.New("induced terminal program failure")

	err := Run(t.Context(), controller, Options{
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			return fakeProgram{model: model, err: injected}
		},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Run() error = %v", err)
	}
	if controller.closes.Load() != 1 {
		t.Fatalf("closes = %d", controller.closes.Load())
	}
}

func TestDetectTerminalCapabilitiesRejectsRedirectedStreams(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	capabilities := DetectTerminalCapabilities(input, output, false)
	if capabilities.Interactive || capabilities.Color || capabilities.Reason == "" {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

func TestFinalSummaryPreservesValidUTF8AtBoundary(t *testing.T) {
	summary := FinalSummary(frontend.ThreadSnapshot{
		ThreadID: "thread-1",
		Activity: frontend.ActivityIdle,
		Entries: []frontend.TranscriptEntry{{
			Kind: frontend.EntryAssistant,
			Text: strings.Repeat("界", finalAnswerBytes),
		}},
	})
	if !strings.Contains(summary, "…") || !utf8.ValidString(summary) {
		t.Fatalf("summary boundary is invalid: %q", summary[len(summary)-20:])
	}
}
