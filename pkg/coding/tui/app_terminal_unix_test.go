//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
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

type panicWatchController struct {
	*fakeController
}

func (*panicWatchController) Watch(
	context.Context,
	frontend.Revision,
) (<-chan frontend.Delta, error) {
	panic("induced TUI watch panic")
}

func TestTUIHelperProcess(t *testing.T) {
	mode := os.Getenv(terminalHelperMode)
	if mode == "" {
		t.Skip("helper process")
	}
	controller, _ := newController(t)
	var active frontend.Controller = controller
	if mode == "panic" {
		active = &panicWatchController{fakeController: controller}
	}
	err := Run(context.Background(), active, Options{
		Input:           os.Stdin,
		Output:          os.Stdout,
		AlternateScreen: true,
		ReportFocus:     true,
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
	for _, mode := range []string{"ctrl-c", "sigterm", "panic"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestTUIHelperProcess$")
			command.Env = append(os.Environ(), terminalHelperMode+"="+mode, "TERM=xterm-256color")
			terminal, err := pty.Start(command)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			output := &lockedBuffer{}
			readDone := make(chan struct{})
			go func() {
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
						break
					}
				}
				close(readDone)
			}()

			waitForTerminalSequence(t, output, "\x1b[?1049h")
			switch mode {
			case "ctrl-c":
				if _, err := terminal.Write([]byte{3}); err != nil {
					t.Fatal(err)
				}
			case "sigterm":
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}

			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			select {
			case err := <-wait:
				if err != nil {
					t.Fatalf("helper exit: %v\n%s", err, output.String())
				}
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				t.Fatalf("helper did not exit\n%s", output.String())
			}
			select {
			case <-readDone:
			case <-time.After(time.Second):
			}
			rendered := output.String()
			for _, sequence := range []string{"\x1b[?1049l", "\x1b[?2004l", "\x1b[?25h"} {
				if !strings.Contains(rendered, sequence) {
					t.Fatalf("%s output omitted restoration sequence %q\n%q", mode, sequence, rendered)
				}
			}
		})
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
