//go:build linux

package startup

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// readApprovalFile pins a bounded Root-owned regular inode under protected
// Root ancestors. Only the dedicated service group can read its private data;
// the service cannot write it. No caller/model path reaches this helper.
func readApprovalFile(ctx context.Context, path string, identity serviceIdentity) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || !validDirectoryPath(path) || identity.uid == 0 || identity.gid == 0 {
		return nil, errUnavailable
	}
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		var parent unix.Stat_t
		acl, aclErr := descriptorACL(descriptor)
		if unix.Fstat(descriptor, &parent) != nil || parent.Uid != 0 || parent.Mode&0o022 != 0 ||
			aclErr != nil || len(acl) != 0 || ctx.Err() != nil {
			_ = unix.Close(descriptor)
			return nil, errUnavailable
		}
		flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index == len(parts)-1 {
			flags = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
		}
		next, openErr := unix.Openat(descriptor, part, flags, 0)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return nil, errUnavailable
		}
		descriptor = next
	}
	defer func() { _ = unix.Close(descriptor) }()
	var original unix.Stat_t
	if unix.Fstat(descriptor, &original) != nil || !validApprovalFile(original, identity) || ctx.Err() != nil {
		return nil, errUnavailable
	}
	// Follow only our own still-open pin. O_PATH inspection rejects devices,
	// FIFOs, symlinks and oversized files before any data-open or allocation.
	reopened, err := unix.Open(
		"/proc/self/fd/"+strconv.Itoa(descriptor),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, errUnavailable
	}
	file := os.NewFile(uintptr(reopened), "private approval startup file")
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	acl, aclErr := descriptorACL(reopened)
	if unix.Fstat(reopened, &stat) != nil || stat.Dev != original.Dev || stat.Ino != original.Ino ||
		!validApprovalFile(stat, identity) || aclErr != nil || len(acl) != 0 || ctx.Err() != nil {
		return nil, errUnavailable
	}
	buffer := make([]byte, maxApprovalConfigBytes+1)
	count, readErr := io.ReadFull(file, buffer)
	var after unix.Stat_t
	if readErr != io.ErrUnexpectedEOF || count != int(stat.Size) || ctx.Err() != nil ||
		unix.Fstat(reopened, &after) != nil || !validApprovalFile(after, identity) ||
		after.Size != stat.Size || after.Mtim != stat.Mtim || after.Ctim != stat.Ctim {
		clear(buffer)
		return nil, errUnavailable
	}
	return buffer[:count:count], nil
}

func validApprovalFile(stat unix.Stat_t, identity serviceIdentity) bool {
	mode := stat.Mode & 0o7777
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1 && stat.Uid == 0 &&
		stat.Gid == identity.gid && (mode == 0o440 || mode == 0o640) &&
		stat.Size > 0 && stat.Size <= maxApprovalConfigBytes
}

func loadApprovalConfig(ctx context.Context, path string, identity serviceIdentity) (approvalConfig, error) {
	data, err := readApprovalFile(ctx, path, identity)
	if err != nil {
		return approvalConfig{}, errUnavailable
	}
	defer clear(data)
	config, err := decodeApprovalConfig(data)
	if err != nil || config.ServiceUID != identity.uid || config.ServiceGID != identity.gid || ctx.Err() != nil {
		return approvalConfig{}, errUnavailable
	}
	return config, nil
}
