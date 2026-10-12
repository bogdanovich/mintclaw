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
