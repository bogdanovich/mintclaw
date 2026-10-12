//go:build linux

package approval

import (
	"errors"

	"golang.org/x/sys/unix"
)

func checkOwnerACL(descriptor int) error {
	var acl [128]byte
	_, err := unix.Fgetxattr(descriptor, "system.posix_acl_access", acl[:])
	if !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
		return ErrUnavailable
	}
	return nil
}
