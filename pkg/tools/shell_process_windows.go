//go:build windows

package tools

import (
	"errors"
	"fmt"
	"os/exec"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	treeErr := killProcessGroup(pid)
	if treeErr == nil {
		return nil
	}
	processErr := cmd.Process.Kill()
	return errors.Join(treeErr, wrapCommandKillError(pid, processErr))
}

func wrapCommandKillError(pid int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("kill process %d: %w", pid, err)
}
