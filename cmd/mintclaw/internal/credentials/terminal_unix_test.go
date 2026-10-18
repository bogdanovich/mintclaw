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

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

const privateTTYFixtureToken = "synthetic-private-tty-token-C62"

func TestPrivateTTYSelectionHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_PRIVATE_SELECTION_TEST") != "1" {
		return
	}
	cfg := discoveryConfig(t)
	command := newCredentialsCommand(commandDeps{
		loadConfig:   func() (*config.Config, error) { return cfg, nil },
		openTerminal: openPrivateTerminal,
		readGateway:  func() *pid.PidFileData { return nil },
		findLogins: func(context.Context, string, string, vault.DiscoveryQuery) ([]vault.Candidate, error) {
			return []vault.Candidate{{Binding: vault.Binding{VaultID: "vault", ItemID: "chosen"}, Name: "Example"}}, nil
		},
		updateConfig: func(mutate func(*config.Config) error) error { return mutate(cfg) },
	})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"find", "--name", "example", "--bind", "example"})
	if command.Execute() != nil || cfg.Credentials.Accounts[0].ItemID != "chosen" {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPrivateTTYBindingReadsTheOperatorChoice(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivateTTYSelectionHelper$")
	command.Env = []string{"MINTCLAW_PRIVATE_SELECTION_TEST=1", "GOTRACEBACK=none"}
	terminal, slave, err := pty.Open()
	if err != nil {
		t.Fatal("private terminal fixture unavailable")
	}
	defer func() { _ = terminal.Close() }()
	defer func() { _ = slave.Close() }()
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if command.Start() != nil {
		t.Fatal("private terminal process unavailable")
	}
	_ = slave.Close()
	var captured bytes.Buffer
	var buffer [4096]byte
	for !strings.Contains(captured.String(), "Choose an item number") {
		n, readErr := terminal.Read(buffer[:])
		captured.Write(buffer[:n])
		if readErr != nil {
			t.Fatal("private selection prompt unavailable")
		}
	}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, terminal)
		close(drained)
	}()
	_, writeErr := io.WriteString(terminal, "1\n")
	waitErr := command.Wait()
	_ = terminal.Close()
	<-drained
	if writeErr != nil || waitErr != nil {
		t.Fatal("real private terminal could not select and save the reference")
	}
}

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
