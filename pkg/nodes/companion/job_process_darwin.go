//go:build darwin

package companion

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

const darwinZombieProcessState = 5

func jobProcessSupported() error {
	_, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return errors.New("durable node jobs require process-table observation")
	}
	return nil
}

func jobProcessLeaderExited(pid int) (bool, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return process.Proc.P_stat == darwinZombieProcessState, nil
}

func liveJobProcessGroupMembers(processGroup, leader int) (int, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", processGroup)
	if err != nil {
		return 0, err
	}
	members := 0
	for _, process := range processes {
		if int(process.Proc.P_pid) == leader ||
			process.Proc.P_stat == darwinZombieProcessState {
			continue
		}
		if int(process.Eproc.Pgid) == processGroup {
			members++
		}
	}
	return members, nil
}
