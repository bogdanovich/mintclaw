//go:build linux || darwin

package workerprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type processDomain struct {
	command *exec.Cmd
}

func prepareProcessDomain(command *exec.Cmd) (processDomain, error) {
	if command == nil {
		return processDomain{}, errors.New("coding worker command is required")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return processDomain{command: command}, nil
}

func (domain processDomain) started() error { return nil }

func (domain processDomain) stop(_ time.Duration) error {
	if domain.command == nil || domain.command.Process == nil || domain.command.Process.Pid <= 0 {
		return nil
	}
	processGroup := domain.command.Process.Pid
	groupErr := syscall.Kill(-processGroup, syscall.SIGKILL)
	if errors.Is(groupErr, syscall.ESRCH) {
		groupErr = nil
	}
	leaderErr := domain.command.Process.Kill()
	if errors.Is(leaderErr, os.ErrProcessDone) {
		leaderErr = nil
	}
	if err := errors.Join(groupErr, leaderErr); err != nil {
		return fmt.Errorf("terminate coding worker process domain: %w", err)
	}
	return nil
}

func (domain processDomain) close() error {
	// command.Wait has reaped the leader before close is called. If any
	// descendant survived, however, it still belongs to the leader's process
	// group and keeps that group ID allocated. Drain the group before waiting
	// for inherited stderr descriptors to reach EOF.
	return domain.stop(DefaultStopTimeout)
}
