package tui

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const (
	herdrEnvironmentVariable = "HERDR_ENV"
	herdrPaneVariable        = "HERDR_PANE_ID"
	herdrBinaryVariable      = "HERDR_BIN_PATH"
	herdrSocketVariable      = "HERDR_SOCKET_PATH"
	herdrReporterSource      = "custom:mintclaw-code"
	herdrReporterAgent       = "mintclaw"
	herdrReportTimeout       = 750 * time.Millisecond
)

type herdrRunFunc func(context.Context, string, []string, []string) error

type herdrLifecycle struct {
	state   string
	message string
}

// herdrLifecycleReporter is an optional, best-effort adapter for Herdr's
// documented custom-agent CLI. It stays inactive unless Herdr injected the
// complete pane environment and never turns status reporting into a TUI error.
type herdrLifecycleReporter struct {
	binary      string
	paneID      string
	environment []string
	run         herdrRunFunc

	mu       sync.Mutex
	sequence uint64
	last     herdrLifecycle
}

func newHerdrLifecycleReporter(
	environment []string,
	runner herdrRunFunc,
) *herdrLifecycleReporter {
	if environmentValue(environment, herdrEnvironmentVariable) != "1" {
		return nil
	}
	paneID := environmentValue(environment, herdrPaneVariable)
	binary := environmentValue(environment, herdrBinaryVariable)
	socket := environmentValue(environment, herdrSocketVariable)
	if paneID == "" || binary == "" || socket == "" {
		return nil
	}
	if runner == nil {
		runner = runHerdrCommand
	}
	return &herdrLifecycleReporter{
		binary:      binary,
		paneID:      paneID,
		environment: append([]string(nil), environment...),
		run:         runner,
		sequence:    uint64(time.Now().UnixNano()),
	}
}

func runHerdrCommand(
	ctx context.Context,
	binary string,
	args []string,
	environment []string,
) error {
	command := exec.CommandContext(ctx, binary, args...)
	if len(environment) != 0 {
		command.Env = environment
	}
	return command.Run()
}

func (reporter *herdrLifecycleReporter) reportCmd(
	snapshot frontend.ThreadSnapshot,
	initialTurnPending bool,
) tea.Cmd {
	if reporter == nil {
		return nil
	}
	lifecycle := herdrLifecycleForSnapshot(snapshot, initialTurnPending)

	reporter.mu.Lock()
	if reporter.last == lifecycle {
		reporter.mu.Unlock()
		return nil
	}
	reporter.last = lifecycle
	reporter.sequence++
	sequence := reporter.sequence
	reporter.mu.Unlock()

	args := []string{
		"pane", "report-agent", reporter.paneID,
		"--source", herdrReporterSource,
		"--agent", herdrReporterAgent,
		"--state", lifecycle.state,
		"--seq", strconv.FormatUint(sequence, 10),
	}
	if lifecycle.message != "" {
		args = append(args, "--message", lifecycle.message)
	}
	return reporter.command(args)
}

func (reporter *herdrLifecycleReporter) release() {
	if reporter == nil {
		return
	}
	reporter.mu.Lock()
	reporter.sequence++
	sequence := reporter.sequence
	reporter.mu.Unlock()
	args := []string{
		"pane", "release-agent", reporter.paneID,
		"--source", herdrReporterSource,
		"--agent", herdrReporterAgent,
		"--seq", strconv.FormatUint(sequence, 10),
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrReportTimeout)
	defer cancel()
	_ = reporter.run(ctx, reporter.binary, args, reporter.environment)
}

func (reporter *herdrLifecycleReporter) command(args []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), herdrReportTimeout)
		defer cancel()
		_ = reporter.run(ctx, reporter.binary, args, reporter.environment)
		return nil
	}
}

func herdrLifecycleForSnapshot(
	snapshot frontend.ThreadSnapshot,
	initialTurnPending bool,
) herdrLifecycle {
	if initialTurnPending || activeWork(snapshot.Activity) {
		return herdrLifecycle{state: "working"}
	}
	if snapshot.Activity == frontend.ActivityWaitingInput {
		message := boundedSingleLine(strings.TrimSpace(snapshot.Status), 160)
		if message == "" {
			message = "waiting for input"
		}
		return herdrLifecycle{state: "blocked", message: message}
	}
	return herdrLifecycle{state: "idle"}
}
