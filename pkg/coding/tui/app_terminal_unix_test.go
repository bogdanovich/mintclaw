//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const terminalHelperMode = "MINTCLAW_TUI_HELPER_MODE"

type panicSubscribeController struct {
	*fakeController
}

type interruptingTerminalController struct {
	*fakeController
}

func (controller *interruptingTerminalController) Interrupt(context.Context) error {
	controller.interrupts.Add(1)
	controller.TurnInterrupted("turn-active", "interrupted by operator")
	return nil
}

func (*panicSubscribeController) Subscribe(
	context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	panic("induced TUI subscription panic")
}

func TestTUIHelperProcess(t *testing.T) {
	mode := os.Getenv(terminalHelperMode)
	if mode == "" {
		t.Skip("helper process")
	}
	controller := newController(t)
	var active frontend.Controller = controller
	switch mode {
	case "panic":
		active = &panicSubscribeController{fakeController: controller}
	case "interrupt":
		controller.TurnStarted("turn-active", "stop this work")
		controller.AssistantAccumulated("turn-active", "Working before interruption.", false)
		controller.ToolStarted("turn-active", "command-active", "exec", `{"command":"long-running-check"}`)
		controller.ToolCommandOutput("turn-active", "command-active", frontend.CommandState{
			Action: "run", Command: "long-running-check", Status: frontend.CommandRunning,
			Source: frontend.CommandSourceAgent, OwnsProcess: true,
		})
		active = &interruptingTerminalController{fakeController: controller}
	case "compaction":
		controller.TurnStarted("turn-compact", "continue after compaction")
		controller.CompactionUpdate(frontend.CompactionState{
			TurnID: "turn-compact", AttemptID: "compact-1", Status: frontend.CompactionRunning,
			Reason: "context_pressure",
		})
		controller.CompactionUpdate(frontend.CompactionState{
			TurnID: "turn-compact", AttemptID: "compact-1", Status: frontend.CompactionCompleted,
			Reason: "context_pressure", TokensBefore: 12_000, TokensAfter: 4_000,
			TokensSaved: 8_000, TokenCountsObserved: true, SummariesCreated: 2,
			LeafSummaries: 1, CondensedSummaries: 1, Duration: 1500 * time.Millisecond,
		})
		controller.AssistantAccumulated("turn-compact", "Continued after compacting context.", true)
		controller.TurnCompleted("turn-compact", "completed")
	case "fallback":
		controller.TurnStarted("turn-fallback", "recover from provider failure")
		controller.Warning(
			"turn-fallback",
			"fallback-1",
			"model fallback 1: openai/gpt-recovery succeeded (rate_limit)",
		)
		controller.AssistantAccumulated("turn-fallback", "Recovered once through the fallback provider.", true)
		controller.TurnCompleted("turn-fallback", "completed")
	case "resume":
		controller.Open(true)
		controller.ThreadMetadataUpdated(frontend.ThreadMetadata{
			Title: "Recovered coding thread", ProjectRoot: "/workspace/project", CWD: "/workspace/project",
			Model: "gpt-coding", Provider: "openai",
		})
		controller.RuntimeStatusUpdated(frontend.RuntimeStatus{
			Version: "test", Resumed: true, Permission: frontend.PermissionFullAccess,
			Autonomy: frontend.AutonomyYolo,
		})
		controller.TurnStarted("turn-before-crash", "inspect before restart")
		controller.AssistantAccumulated("turn-before-crash", "Work retained before restart.", false)
		controller.TurnInterrupted("turn-before-crash", "interrupted by process restart")
		controller.TurnStarted("turn-after-crash", "resume after restart")
		controller.AssistantAccumulated("turn-after-crash", "Resumed without duplicating prior work.", true)
		controller.TurnCompleted("turn-after-crash", "completed")
	}
	err := Run(context.Background(), active, Options{
		Input:           os.Stdin,
		Output:          os.Stdout,
		AlternateScreen: true,
		ReportFocus:     true,
		MotionMode:      MotionDisabled,
		Environment:     os.Environ(),
	})
	if mode == "panic" && err == nil {
		t.Fatal("induced panic returned no error")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestTerminalLifecycleEmitsRestorationForExitSignalAndPanic(t *testing.T) {
	for _, mode := range []string{"exit", "ctrl-c", "sigterm", "panic"} {
		t.Run(mode, func(t *testing.T) {
			session := startTerminalHelper(t, mode, nil, 80, 24)

			waitForTerminalSequence(t, session.output, "\x1b[?1049h")
			switch mode {
			case "exit":
				session.write(t, "/exit\r")
			case "ctrl-c":
				session.write(t, string([]byte{3}))
			case "sigterm":
				if err := session.command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			rendered := session.finish(t)
			assertTerminalRestored(t, mode, rendered)
		})
	}
}

func TestTerminalPTYMatrixCoversRemoteNarrowAndRecoveryPresentation(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		environment []string
		width       uint16
		height      uint16
		visible     []string
		openStatus  bool
	}{
		{
			name: "ssh provider fallback", mode: "fallback", width: 80, height: 24,
			environment: []string{"SSH_CONNECTION=192.0.2.1 2200 192.0.2.2 22", "SSH_TTY=/dev/pts/test"},
			visible:     []string{"model fallback 1", "Recovered once through the fallback provider."},
		},
		{
			name: "tmux compaction", mode: "compaction", width: 120, height: 30,
			environment: []string{"TMUX=/tmp/mintclaw-test,1,0", "TMUX_PANE=%1", "TERM=screen-256color"},
			visible:     []string{"Context compacted", "Continued after compacting context."},
		},
		{
			name: "narrow crash resume", mode: "resume", width: 40, height: 16,
			environment: []string{"NO_COLOR=1"}, openStatus: true,
			visible: []string{
				"Work retained before restart.",
				"Resumed without duplicating prior work.",
				"resumed · active",
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			session := startTerminalHelper(
				t,
				testCase.mode,
				testCase.environment,
				testCase.width,
				testCase.height,
			)
			waitForTerminalSequence(t, session.output, "\x1b[?1049h")
			for index, visible := range testCase.visible {
				if testCase.openStatus && index == len(testCase.visible)-1 {
					session.write(t, "/status\r")
				}
				waitForTerminalSequence(t, session.output, visible)
			}
			if testCase.openStatus {
				session.write(t, "\x1b")
			}
			session.write(t, "/exit\r")
			rendered := session.finish(t)
			assertTerminalRestored(t, testCase.name, rendered)
		})
	}
}

func TestTerminalPTYInterruptsActiveWorkThenReturnsToUsableShell(t *testing.T) {
	session := startTerminalHelper(t, "interrupt", nil, 80, 24)
	waitForTerminalSequence(t, session.output, "Working before interruption.")
	session.write(t, string([]byte{3}))
	waitForTerminalSequence(t, session.output, "Work interrupted")
	session.write(t, string([]byte{3}))
	rendered := session.finish(t)
	assertTerminalRestored(t, "active interruption", rendered)
	if !strings.Contains(rendered, "interrupted by operator") {
		t.Fatalf("active interruption omitted final status\n%q", rendered)
	}
}

func TestTerminalLifecycleRunsInsideTmuxWhenAvailable(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is unavailable")
	}
	socket := fmt.Sprintf("mintclaw-tui-%d", os.Getpid())
	t.Cleanup(func() {
		cleanup := exec.Command(tmux, "-L", socket, "kill-server")
		cleanup.Env = environmentWithout(os.Environ(), "TMUX", "TMUX_PANE", "TMUX_TMPDIR")
		_ = cleanup.Run()
	})
	command := exec.Command(
		tmux,
		"-L",
		socket,
		"-f",
		"/dev/null",
		"new-session",
		"-x",
		"80",
		"-y",
		"24",
		os.Args[0],
		"-test.run=^TestTUIHelperProcess$",
	)
	command.Env = append(
		environmentWithout(os.Environ(), "TMUX", "TMUX_PANE", "TMUX_TMPDIR", terminalHelperMode, "TERM"),
		terminalHelperMode+"=fallback",
		"TERM=xterm-256color",
	)
	session := startTerminalCommand(t, command, 80, 24)
	waitForTerminalSequence(t, session.output, "Recovered once through the fallback provider.")
	session.write(t, "/exit\r")
	rendered := session.finish(t)
	assertTerminalRestored(t, "real tmux", rendered)
}

type terminalHelperSession struct {
	terminal *os.File
	command  *exec.Cmd
	output   *lockedBuffer
	readDone <-chan struct{}
}

func startTerminalHelper(
	t *testing.T,
	mode string,
	environment []string,
	width, height uint16,
) *terminalHelperSession {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestTUIHelperProcess$")
	command.Env = append(
		append(os.Environ(), terminalHelperMode+"="+mode, "TERM=xterm-256color"),
		environment...,
	)
	return startTerminalCommand(t, command, width, height)
}

func startTerminalCommand(t *testing.T, command *exec.Cmd, width, height uint16) *terminalHelperSession {
	t.Helper()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: width, Rows: height})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := terminal.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close pseudo-terminal: %v", err)
		}
		if command.Process != nil && command.ProcessState == nil {
			_ = command.Process.Kill()
		}
	})
	output := &lockedBuffer{}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4_096)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				chunk := append([]byte(nil), buffer[:count]...)
				_, _ = output.Write(chunk)
				if bytes.Contains(chunk, []byte("\x1b]11;?\x1b\\")) {
					_, _ = terminal.Write([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
				}
				if bytes.Contains(chunk, []byte("\x1b[6n")) {
					_, _ = terminal.Write([]byte("\x1b[1;1R"))
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	return &terminalHelperSession{terminal: terminal, command: command, output: output, readDone: readDone}
}

func environmentWithout(environment []string, names ...string) []string {
	removed := make(map[string]struct{}, len(names))
	for _, name := range names {
		removed[name] = struct{}{}
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, remove := removed[name]; !remove {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (session *terminalHelperSession) write(t *testing.T, value string) {
	t.Helper()
	if _, err := session.terminal.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
}

func (session *terminalHelperSession) finish(t *testing.T) string {
	t.Helper()
	wait := make(chan error, 1)
	go func() { wait <- session.command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("helper exit: %v\n%s", err, session.output.String())
		}
	case <-time.After(5 * time.Second):
		_ = session.command.Process.Kill()
		t.Fatalf("helper did not exit\n%s", session.output.String())
	}
	select {
	case <-session.readDone:
	case <-time.After(time.Second):
	}
	return session.output.String()
}

func assertTerminalRestored(t *testing.T, scenario, rendered string) {
	t.Helper()
	for _, sequence := range []string{"\x1b[?1049l", "\x1b[?2004l", "\x1b[?25h"} {
		if !strings.Contains(rendered, sequence) {
			t.Fatalf("%s output omitted restoration sequence %q\n%q", scenario, sequence, rendered)
		}
	}
	if strings.Contains(rendered, fmt.Sprintf("%s=", terminalHelperMode)) {
		t.Fatalf("%s output leaked helper environment\n%q", scenario, rendered)
	}
}

func waitForTerminalSequence(t *testing.T, output *lockedBuffer, sequence string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(output.String(), sequence) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal did not emit %q\n%q", sequence, output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
