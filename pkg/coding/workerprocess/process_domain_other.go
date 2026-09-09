//go:build !linux && !darwin && !windows

package workerprocess

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

type processDomain struct {
	command *exec.Cmd
}

func prepareProcessDomain(command *exec.Cmd) (processDomain, error) {
	if command == nil {
		return processDomain{}, errors.New("coding worker command is required")
	}
	return processDomain{command: command}, nil
}

func (domain processDomain) started() error { return nil }

func (domain processDomain) stop(_ time.Duration) error {
	if domain.command == nil || domain.command.Process == nil {
		return nil
	}
	err := domain.command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (domain processDomain) close() error { return nil }
