//go:build !windows

package tools

import (
	"errors"
	"fmt"
	"syscall"
)

func killProcessGroup(pid int) error {
	return killProcessGroupWith(pid, syscall.Kill)
}

func killProcessGroupWith(pid int, kill func(int, syscall.Signal) error) error {
	groupErr := kill(-pid, syscall.SIGKILL)
	if groupErr == nil {
		return nil
	}
	processErr := kill(pid, syscall.SIGKILL)
	if errors.Is(groupErr, syscall.ESRCH) && (processErr == nil || errors.Is(processErr, syscall.ESRCH)) {
		return nil
	}
	return errors.Join(
		fmt.Errorf("kill process group %d: %w", pid, groupErr),
		wrapProcessKillError(pid, processErr),
	)
}

func wrapProcessKillError(pid int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("kill process %d: %w", pid, err)
}
