//go:build linux

package startup

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	aclVersion = 2
	aclUserObj = 1
	aclUser    = 2
	aclGroup   = 4
	aclMask    = 16
	aclOther   = 32
	aclNoID    = ^uint32(0)
)

func descriptorACL(descriptor int) ([]byte, error) {
	buffer := make([]byte, 128)
	count, err := unix.Fgetxattr(descriptor, "system.posix_acl_access", buffer)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil || count <= 0 || count > len(buffer) {
		return nil, errUnavailable
	}
	return buffer[:count], nil
}

// Accept exactly the systemd service-only access shape, not arbitrary ACLs.
// Root-owned files require one named service UID; the group mode is the ACL
// mask. The service-owned no-ACL fallback also requires read-only mounting.
func validCredentialAccess(stat unix.Stat_t, acl []byte, identity serviceIdentity, directory bool) bool {
	permission := uint16(4)
	if directory {
		permission = 5
	}
	mode := stat.Mode & 0o7777
	if stat.Uid == identity.uid && (stat.Gid == 0 || stat.Gid == identity.gid) {
		return len(acl) == 0 && mode == uint32(permission)<<6
	}
	if stat.Uid != 0 || stat.Gid != 0 || mode != uint32(permission)<<6|uint32(permission)<<3 ||
		len(acl) != 44 || binary.LittleEndian.Uint32(acl[:4]) != aclVersion {
		return false
	}
	expected := []struct {
		tag, permission uint16
		id              uint32
	}{
		{aclUserObj, permission, aclNoID},
		{aclUser, permission, identity.uid},
		{aclGroup, 0, aclNoID},
		{aclMask, permission, aclNoID},
		{aclOther, 0, aclNoID},
	}
	for index, entry := range expected {
		offset := 4 + index*8
		if binary.LittleEndian.Uint16(acl[offset:]) != entry.tag ||
			binary.LittleEndian.Uint16(acl[offset+2:]) != entry.permission ||
			binary.LittleEndian.Uint32(acl[offset+4:]) != entry.id {
			return false
		}
	}
	return true
}
