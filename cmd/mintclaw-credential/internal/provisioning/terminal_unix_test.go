//go:build linux || darwin

package provisioning

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestPrivateTerminalInputDoesNotEchoAndRestoresState(t *testing.T) {
	for _, mode := range []string{"success", "backspace", "interrupt", "cancel", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close() }()
			defer func() { _ = slave.Close() }()
			masterFD, slaveFD := int(master.Fd()), int(slave.Fd())
			state, err := term.GetState(slaveFD)
			if err != nil || unix.SetNonblock(masterFD, true) != nil {
				t.Fatal("terminal fixture was unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			type result struct {
				token []byte
				err   error
			}
			done := make(chan result, 1)
			go func() {
				token, err := readTerminalToken(ctx, slave)
				done <- result{token, err}
			}()
			output := readPTYUntil(t, masterFD, []byte("Token: "))
			const canary = "SYNTHETIC_TTY_PRIVATE_CANARY"
			input := []byte(canary)
			switch mode {
			case "success":
				input = append(input, '\r')
			case "backspace":
				input = append(input, 'x', 0x7f, '\r')
			case "interrupt":
				input = append(input, 0x03)
			case "invalid":
				input = append(input, 0x00)
			}
			if _, err := unix.Write(masterFD, input); err != nil {
				t.Fatal("private fixture input could not be written")
			}
			if mode == "cancel" {
				cancel()
			}
			var received result
			select {
			case received = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal input did not terminate")
			}
			if mode == "success" || mode == "backspace" {
				if received.err != nil || string(received.token) != canary {
					t.Fatal("private terminal input was lost")
				}
				clear(received.token)
			} else if !errors.Is(received.err, ErrInput) || received.token != nil {
				t.Fatal("failed terminal input retained private data")
			}
			output = append(output, readPTYUntil(t, masterFD, []byte("\n"))...)
			after, err := term.GetState(slaveFD)
			if err != nil || !reflect.DeepEqual(state, after) || bytes.Contains(output, []byte(canary)) {
				t.Fatal("terminal input echoed private data or failed to restore state")
			}
		})
	}
}

func readPTYUntil(t *testing.T, fd int, marker []byte) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var result []byte
	var chunk [256]byte
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 50); err != nil {
			t.Fatal("terminal fixture poll failed")
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		count, err := unix.Read(fd, chunk[:])
		if err != nil {
			t.Fatal("terminal fixture read failed")
		}
		result = append(result, chunk[:count]...)
		if bytes.Contains(result, marker) {
			return result
		}
	}
	t.Fatal("terminal fixture output timed out")
	return nil
}
