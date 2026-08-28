package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const (
	defaultCloseTimeout = 10 * time.Second
	finalAnswerBytes    = 2_000
)

// TerminalCapabilities records whether the attached streams can safely host
// the interactive application. Plain command rendering remains the fallback.
type TerminalCapabilities struct {
	Interactive bool
	Color       bool
	Reason      string
}

// DetectTerminalCapabilities centralizes the TTY, TERM, and no-color rules
// used before entering raw or alternate-screen mode.
func DetectTerminalCapabilities(input, output *os.File, noColor bool) TerminalCapabilities {
	capabilities := TerminalCapabilities{Color: !noColor && os.Getenv("NO_COLOR") == ""}
	switch {
	case input == nil || !term.IsTerminal(int(input.Fd())):
		capabilities.Reason = "standard input is not a terminal"
	case output == nil || !term.IsTerminal(int(output.Fd())):
		capabilities.Reason = "standard output is not a terminal"
	case strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb"):
		capabilities.Reason = "TERM=dumb does not support an interactive screen"
	default:
		capabilities.Interactive = true
	}
	if !capabilities.Interactive {
		capabilities.Color = false
	}
	return capabilities
}

// Options configures one foreground terminal application.
type Options struct {
	Input           io.Reader
	Output          io.Writer
	InitialPrompt   string
	AlternateScreen bool
	ReportFocus     bool
	NoColor         bool
	Environment     []string
	newProgram      func(tea.Model, ...tea.ProgramOption) program
}

type program interface {
	Run() (tea.Model, error)
}

func defaultProgram(model tea.Model, options ...tea.ProgramOption) program {
	return tea.NewProgram(model, options...)
}

// Run owns one interactive frontend session. Bubble Tea restores the terminal
// before returning; controller shutdown and lease release happen afterward.
func Run(ctx context.Context, controller frontend.Controller, options Options) (resultErr error) {
	if controller == nil {
		return fmt.Errorf("coding TUI controller is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), defaultCloseTimeout)
		defer cancelClose()
		resultErr = errors.Join(resultErr, controller.Close(closeCtx))
	}()
	frontendCtx, cancelFrontend := context.WithCancel(ctx)
	defer cancelFrontend()

	model, err := NewModel(frontendCtx, controller)
	if err != nil {
		return fmt.Errorf("coding TUI model: %w", err)
	}
	if strings.TrimSpace(options.InitialPrompt) != "" {
		if err := controller.Submit(ctx, options.InitialPrompt); err != nil {
			return fmt.Errorf("coding TUI submit initial prompt: %w", err)
		}
		model.admitInitialTurn()
	}

	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if options.Input != nil {
		programOptions = append(programOptions, tea.WithInput(options.Input))
	}
	if options.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(options.Output))
	}
	if options.AlternateScreen {
		programOptions = append(programOptions, tea.WithAltScreen())
	}
	if options.ReportFocus {
		programOptions = append(programOptions, tea.WithReportFocus())
	}
	environment := append([]string(nil), options.Environment...)
	if options.NoColor {
		environment = append(environment, "NO_COLOR=1")
	}
	if len(environment) > 0 {
		programOptions = append(programOptions, tea.WithEnvironment(environment))
	}
	programFactory := options.newProgram
	if programFactory == nil {
		programFactory = defaultProgram
	}
	finalModel, runErr := programFactory(model, programOptions...).Run()
	if runErr != nil {
		return fmt.Errorf("coding TUI: %w", runErr)
	}
	if options.AlternateScreen && options.Output != nil {
		if rendered, ok := finalModel.(*Model); ok {
			if _, err := io.WriteString(options.Output, FinalSummary(rendered.Snapshot())); err != nil {
				return fmt.Errorf("coding TUI final summary: %w", err)
			}
		}
	}
	return nil
}

// FinalSummary is deliberately bounded: alternate-screen exit leaves useful
// native scrollback without replaying the canonical transcript.
func FinalSummary(snapshot frontend.ThreadSnapshot) string {
	status := strings.TrimSpace(snapshot.Status)
	if status == "" {
		status = string(snapshot.Activity)
	}
	if status == "" {
		status = "idle"
	}
	answer := ""
	for index := len(snapshot.Entries) - 1; index >= 0; index-- {
		if snapshot.Entries[index].Kind == frontend.EntryAssistant {
			answer = boundUTF8(snapshot.Entries[index].Text, finalAnswerBytes)
			break
		}
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "\nMintClaw coding thread %s · %s\n", snapshot.ThreadID, status)
	if answer != "" {
		fmt.Fprintf(&summary, "%s\n", answer)
	}
	return summary.String()
}

func boundUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}
