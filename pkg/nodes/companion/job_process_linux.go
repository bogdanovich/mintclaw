//go:build linux

package companion

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func jobProcessSupported() error {
	_, err := linuxJobProcessStat(os.Getpid())
	if err != nil {
		return errors.New("durable node jobs require readable procfs process state")
	}
	return nil
}

func jobProcessLeaderExited(pid int) (bool, error) {
	state, err := linuxJobProcessStat(pid)
	if errors.Is(err, os.ErrNotExist) {
		return false, ErrJobConflict
	}
	if err != nil {
		return false, err
	}
	return state == 'Z' || state == 'X', nil
}

func liveJobProcessGroupMembers(processGroup, leader int) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	members := 0
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid == leader {
			continue
		}
		state, group, statErr := linuxJobProcessStateAndGroup(pid)
		if errors.Is(statErr, os.ErrNotExist) || errors.Is(statErr, syscall.ESRCH) {
			continue
		}
		if statErr != nil {
			return 0, statErr
		}
		if group == processGroup && state != 'Z' && state != 'X' {
			members++
		}
	}
	return members, nil
}

func linuxJobProcessStat(pid int) (byte, error) {
	state, _, err := linuxJobProcessStateAndGroup(pid)
	return state, err
}

func linuxJobProcessStateAndGroup(pid int) (byte, int, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 || closing+2 >= len(data) {
		return 0, 0, errors.New("invalid procfs process state")
	}
	fields := strings.Fields(string(data[closing+2:]))
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, errors.New("invalid procfs process state")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, errors.New("invalid procfs process group")
	}
	return fields[0][0], group, nil
}
