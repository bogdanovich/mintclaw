//go:build darwin

package workerprocess

import "golang.org/x/sys/unix"

const darwinExitedProcessState = 5

func snapshotUnixProcessTable() (map[int]unixProcessInfo, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]unixProcessInfo, len(processes))
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		started := uint64(process.Proc.P_starttime.Sec)*1_000_000 +
			uint64(process.Proc.P_starttime.Usec)
		table[pid] = unixProcessInfo{
			identity: unixProcessIdentity{pid: pid, started: started},
			parent:   int(process.Eproc.Ppid),
			exited:   process.Proc.P_stat == darwinExitedProcessState,
		}
	}
	return table, nil
}
