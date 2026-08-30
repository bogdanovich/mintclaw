package tui

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

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
	controller := newController(t)
	controller.AssistantAccumulated("turn-1", strings.Repeat("x", finalAnswerBytes+100), true)
	output := &bytes.Buffer{}

	err := Run(t.Context(), controller, Options{
		Output: output,
		InitialInput: frontend.TurnInput{
			Text: "fix it",
			Attachments: []frontend.TurnAttachment{{
				Path: "/tmp/screenshot.png", ContentType: "image/png",
			}},
		},
		AlternateScreen: true,
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			rendered, ok := model.(*Model)
			if !ok || !rendered.initialTurnPending {
				t.Fatalf("initial turn was not marked pending before input: %T", model)
			}
			return fakeProgram{model: model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if controller.submits.Load() != 1 || controller.closes.Load() != 1 {
		t.Fatalf("submits=%d closes=%d", controller.submits.Load(), controller.closes.Load())
	}
	inputs := controller.submittedInputs()
	if len(inputs) != 1 || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].Path != "/tmp/screenshot.png" {
		t.Fatalf("initial structured inputs = %+v", inputs)
	}
	if !strings.Contains(output.String(), "thread-1") || len(output.String()) > finalAnswerBytes+200 {
		t.Fatalf("final summary is missing or unbounded: bytes=%d", output.Len())
	}
}

func TestRunClosesControllerAfterProgramFailure(t *testing.T) {
	controller := newController(t)
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

func TestRunNoColorDisablesComposerANSI(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	controller := newController(t)
	rendered := ""
	err := Run(t.Context(), controller, Options{
		NoColor: true,
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			codingModel, ok := model.(*Model)
			if !ok {
				t.Fatalf("model = %T", model)
			}
			rendered = codingModel.composer.FocusedStyle.Text.Render("visible")
			return fakeProgram{model: model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "visible" {
		t.Fatalf("--no-color rendered ANSI styling: %q", rendered)
	}
}

func TestDetectTerminalCapabilitiesRejectsRedirectedStreams(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := input.Close(); err != nil {
			t.Errorf("close input: %v", err)
		}
	})
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Errorf("close output: %v", err)
		}
	})

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
