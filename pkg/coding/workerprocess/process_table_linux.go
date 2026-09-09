//go:build linux

package workerprocess

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func snapshotUnixProcessTable() (map[int]unixProcessInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	table := make(map[int]unixProcessInfo, len(entries))
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		process, readErr := readLinuxProcessInfo(pid)
		if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, os.ErrPermission) ||
			errors.Is(readErr, syscall.ESRCH) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		table[pid] = process
	}
	return table, nil
}

func readLinuxProcessInfo(pid int) (unixProcessInfo, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return unixProcessInfo{}, err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 || closing+2 >= len(data) {
		return unixProcessInfo{}, errors.New("invalid procfs process state")
	}
	fields := strings.Fields(string(data[closing+2:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return unixProcessInfo{}, errors.New("invalid procfs process state")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return unixProcessInfo{}, errors.New("invalid procfs parent process")
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return unixProcessInfo{}, errors.New("invalid procfs process start time")
	}
	return unixProcessInfo{
		identity: unixProcessIdentity{pid: pid, started: started},
		parent:   parent,
		exited:   fields[0][0] == 'Z' || fields[0][0] == 'X',
	}, nil
}
