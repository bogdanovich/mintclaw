//go:build linux

package startup

import (
	"os"

	"golang.org/x/sys/unix"
)

type serviceIdentity struct{ uid, gid uint32 }

// Qualified startup must apply NoNewPrivileges and drop capabilities/groups
// before exec, so all runtime threads inherit them. This checks the executing
// thread and identity, then hardens process-wide dumps before opening a source.
// It does not prove caller role, agent confinement, or consumer readiness.
func protectServiceProcess() (serviceIdentity, error) {
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 || gid == 0 || os.Getuid() != uid || os.Getgid() != gid {
		return serviceIdentity{}, errUnavailable
	}
	groups, err := os.Getgroups()
	if err != nil {
		return serviceIdentity{}, errUnavailable
	}
	for _, group := range groups {
		if group != gid {
			return serviceIdentity{}, errUnavailable
		}
	}
	noPrivileges, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || noPrivileges != 1 {
		return serviceIdentity{}, errUnavailable
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if unix.Capget(&header, &capabilities[0]) != nil {
		return serviceIdentity{}, errUnavailable
	}
	for _, capability := range capabilities {
		if capability.Effective != 0 || capability.Permitted != 0 || capability.Inheritable != 0 {
			return serviceIdentity{}, errUnavailable
		}
	}
	if unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil ||
		unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil {
		return serviceIdentity{}, errUnavailable
	}
	return serviceIdentity{uint32(uid), uint32(gid)}, nil
}
