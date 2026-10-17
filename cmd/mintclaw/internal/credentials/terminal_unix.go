//go:build linux || darwin

package credentials

import (
	"io"
	"os"

	"golang.org/x/term"
)

func openPrivateTerminal() (io.WriteCloser, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil, errSetupUnavailable
	}
	if !term.IsTerminal(int(terminal.Fd())) {
		_ = terminal.Close()
		return nil, errSetupUnavailable
	}
	return terminal, nil
}

// Read directly from the controlling terminal with echo disabled. Stdin,
// command arguments, environment variables and Cobra output are never token
// sources. The operator must use an unrecorded private terminal.
func readPrivateToken() ([]byte, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errSetupUnavailable
	}
	defer func() { _ = terminal.Close() }()
	fd := int(terminal.Fd())
	if !term.IsTerminal(fd) {
		return nil, errSetupUnavailable
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, errSetupUnavailable
	}
	defer func() { _ = term.Restore(fd, state) }()
	if _, err = io.WriteString(terminal, "1Password service-account token (input hidden): "); err != nil {
		return nil, errSetupUnavailable
	}
	token, readErr := readPrivateTokenLine(terminal)
	_, _ = io.WriteString(terminal, "\r\n")
	return token, readErr
}

// Echo is already disabled before the prompt appears. Raw input lets Ctrl-C
// cancel through normal return/deferred restoration instead of terminating the
// process while the terminal is still in a private-input mode.
func readPrivateTokenLine(reader io.Reader) ([]byte, error) {
	const limit = 32 * 1024
	buffer := make([]byte, limit)
	length, overflow := 0, false
	var input [1]byte
	for {
		if _, err := io.ReadFull(reader, input[:]); err != nil {
			clear(buffer)
			return nil, errSetupUnavailable
		}
		switch input[0] {
		case '\r', '\n':
			if overflow || length == 0 {
				clear(buffer)
				return nil, errSetupUnavailable
			}
			return buffer[:length], nil
		case 3, 4: // Ctrl-C / Ctrl-D.
			clear(buffer)
			return nil, errSetupUnavailable
		case 8, 127:
			if length > 0 && !overflow {
				length--
				buffer[length] = 0
			}
		default:
			if length == limit {
				overflow = true
			} else if !overflow {
				buffer[length] = input[0]
				length++
			}
		}
		input[0] = 0
	}
}
