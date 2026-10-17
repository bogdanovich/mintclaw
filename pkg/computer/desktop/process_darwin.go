//go:build darwin

package desktop

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

// darwinProcess retains a kernel watch of the original process, not a kill(0)
// check that could accept a reused PID. Executing another image is terminal.
type darwinProcess struct {
	fd   int
	lost bool
}

func watchDarwinProcess(pid uint32) (*darwinProcess, error) {
	before, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil || before.Proc.P_pid != int32(pid) || before.Eproc.Ucred.Uid != uint32(os.Getuid()) {
		return nil, computer.ErrDenied
	}
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, ErrUnsupported
	}
	unix.CloseOnExec(fd)
	process := &darwinProcess{fd: fd}
	failed := true
	defer func() {
		if failed {
			_ = process.close()
		}
	}()
	changes := []unix.Kevent_t{{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR | unix.EV_RECEIPT,
		Fflags: unix.NOTE_EXIT | unix.NOTE_EXEC,
	}}
	receipt := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(fd, changes, receipt, nil)
	if err != nil || n != 1 || receipt[0].Flags&unix.EV_ERROR == 0 || receipt[0].Data != 0 {
		return nil, computer.ErrDenied
	}
	after, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil || after.Proc.P_pid != int32(pid) ||
		after.Proc.P_starttime != before.Proc.P_starttime || after.Eproc.Ucred.Uid != uint32(os.Getuid()) ||
		!process.alive() {
		return nil, computer.ErrStale
	}
	failed = false
	return process, nil
}

func (process *darwinProcess) alive() bool {
	if process == nil || process.lost || process.fd < 0 {
		return false
	}
	events := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(process.fd, nil, events, &unix.Timespec{})
	if err != nil || n != 0 {
		process.lost = true
		return false
	}
	return true
}

func (process *darwinProcess) close() error {
	if process == nil || process.fd < 0 {
		return nil
	}
	process.lost = true
	fd := process.fd
	process.fd = -1
	return unix.Close(fd)
}
