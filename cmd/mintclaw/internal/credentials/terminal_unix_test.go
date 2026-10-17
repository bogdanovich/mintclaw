//go:build linux || darwin

package credentials

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const privateTTYFixtureToken = "synthetic-private-tty-token-C62"

func TestPrivateTTYTokenHelper(t *testing.T) {
	mode := os.Getenv("MINTCLAW_PRIVATE_TTY_TEST")
	if mode == "" {
		return
	}
	token, err := readPrivateToken()
	valid := err == nil && bytes.Equal(token, []byte(privateTTYFixtureToken))
	if mode == "cancel" {
		valid = errors.Is(err, errSetupUnavailable) && token == nil
	}
	clear(token)
	if !valid {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPrivateTokenLineBoundsAndClearsCanceledInput(t *testing.T) {
	for _, input := range []string{"\n", privateTTYFixtureToken + string(byte(3)), strings.Repeat("x", 32769) + "\n"} {
		value, err := readPrivateTokenLine(strings.NewReader(input))
		if !errors.Is(err, errSetupUnavailable) || value != nil {
			t.Fatal("invalid or canceled private input escaped its bounded projection")
		}
	}
	value, err := readPrivateTokenLine(strings.NewReader("temporaryX\b\n"))
	if err != nil || !bytes.Equal(value, []byte("temporary")) {
		t.Fatal("private input correction was unavailable")
	}
	defer clear(value)
}

func TestPrivateTTYHidesTokenBeforePromptAndRestoresAfterInputOrCancellation(t *testing.T) {
	for _, mode := range []string{"success", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivateTTYTokenHelper$")
			command.Env = []string{"MINTCLAW_PRIVATE_TTY_TEST=" + mode, "GOTRACEBACK=none"}
			terminal, slave, err := pty.Open()
			if err != nil {
				t.Fatal("private terminal fixture unavailable")
			}
			defer func() { _ = terminal.Close() }()
			defer func() { _ = slave.Close() }()
			initial, err := term.GetState(int(terminal.Fd()))
			if err != nil {
				t.Fatal("initial terminal state unavailable")
			}
			command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if command.Start() != nil {
				t.Fatal("private terminal process unavailable")
			}
			_ = slave.Close()
			output := make(chan []byte, 8)
			go func() {
				defer close(output)
				var buffer [4096]byte
				for {
					n, readErr := terminal.Read(buffer[:])
					if n > 0 {
						output <- append([]byte(nil), buffer[:n]...)
					}
					if readErr != nil {
						return
					}
				}
			}()
			var captured bytes.Buffer
			prompt := false
			for chunk := range output {
				captured.Write(chunk)
				if strings.Contains(captured.String(), "(input hidden): ") {
					prompt = true
					break
				}
			}
			if !prompt {
				t.Fatal("private terminal prompt unavailable")
			}
			input := privateTTYFixtureToken + "\n"
			if mode == "cancel" {
				input = privateTTYFixtureToken + string(byte(3))
			}
			if _, err = io.WriteString(terminal, input); err != nil || command.Wait() != nil {
				t.Fatal("private token fixture input failed")
			}
			for chunk := range output {
				captured.Write(chunk)
			}
			if bytes.Contains(captured.Bytes(), []byte(privateTTYFixtureToken)) {
				t.Fatal("private terminal echoed a token")
			}
			state, stateErr := term.GetState(int(terminal.Fd()))
			if stateErr != nil || !reflect.DeepEqual(initial, state) {
				t.Fatal("private token entry did not restore terminal state")
			}
		})
	}
}
