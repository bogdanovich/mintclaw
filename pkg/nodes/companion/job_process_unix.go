//go:build linux || darwin

package companion

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareJobProcess(command *exec.Cmd) {
	if command == nil {
		return
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateJobProcess(command *exec.Cmd) (bool, error) {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func jobProcessDomainEmpty(command *exec.Cmd, guarantee JobCancelGuarantee) bool {
	if command == nil || command.ProcessState == nil || !command.ProcessState.Exited() {
		return false
	}
	if guarantee == JobCancelDirectProcess {
		return true
	}
	if command.Process == nil || command.Process.Pid <= 0 {
		return false
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func jobProcessSignal(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
