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

const credentialName = "op-service-account"

// loadServiceToken is private and unused by the current command. Future startup
// must qualify role, inherited restrictions, complete agent/browser isolation,
// actual systemd delivery, and consumer readiness before calling it. Directory
// comes from that trusted startup, never a model argument or token environment.
// File/ACL checks alone are not a certificate of those deployment boundaries.
func loadServiceToken(ctx context.Context, directory string) (serviceToken, error) {
	if ctx == nil || ctx.Err() != nil || !validDirectoryPath(directory) {
		return serviceToken{}, errUnavailable
	}
	identity, err := protectServiceProcess()
	if err != nil {
		return serviceToken{}, errUnavailable
	}
	dir, err := openCredentialDirectory(directory, identity)
	if err != nil {
		return serviceToken{}, errUnavailable
	}
	defer func() { _ = dir.Close() }()
	if ctx.Err() != nil {
		return serviceToken{}, errUnavailable
	}
	file, err := openCredentialFile(dir, identity)
	if err != nil {
		return serviceToken{}, errUnavailable
	}
	defer func() { _ = file.Close() }()
	if ctx.Err() != nil {
		return serviceToken{}, errUnavailable
	}
	// One allocation avoids io.ReadAll growth copies of plaintext. A regular
	// credential shorter than the bound ends with ErrUnexpectedEOF.
	buffer := make([]byte, maxTokenBytes+1)
	count, readErr := io.ReadFull(file, buffer)
	value := buffer[:count:count]
	if readErr != io.ErrUnexpectedEOF || ctx.Err() != nil || !validToken(value) ||
		checkCredentialDescriptor(int(dir.Fd()), identity, true) != nil ||
		checkCredentialDescriptor(int(file.Fd()), identity, false) != nil {
		clear(buffer)
		return serviceToken{}, errUnavailable
	}
	return serviceToken{&tokenState{value: value}}, nil
}

func openCredentialDirectory(path string, identity serviceIdentity) (*os.File, error) {
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		var parent unix.Stat_t
		if unix.Fstat(descriptor, &parent) != nil || parent.Uid != 0 || parent.Mode&0o022 != 0 {
			_ = unix.Close(descriptor)
			return nil, errUnavailable
		}
		next, openErr := unix.Openat(descriptor, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return nil, errUnavailable
		}
		descriptor = next
		if index == len(parts)-1 {
			if checkCredentialDescriptor(descriptor, identity, true) != nil {
				_ = unix.Close(descriptor)
				return nil, errUnavailable
			}
		}
	}
	return os.NewFile(uintptr(descriptor), "private credential directory"), nil
}

func openCredentialFile(dir *os.File, identity serviceIdentity) (*os.File, error) {
	// Pin the inode without opening a device or blocking on a FIFO. O_PATH plus
	// NOFOLLOW opens a symlink itself; the regular-file check rejects that inode.
	pin, err := unix.Openat(int(dir.Fd()), credentialName, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = unix.Close(pin) }()
	var original unix.Stat_t
	if unix.Fstat(pin, &original) != nil || original.Mode&unix.S_IFMT != unix.S_IFREG ||
		original.Nlink != 1 || original.Size <= 0 || original.Size > maxTokenBytes {
		return nil, errUnavailable
	}
	// The only followed magic link is our own still-open inode pin. No caller
	// supplies a descriptor or proc path; the pin stays owned until reopening.
	descriptor, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errUnavailable
	}
	file := os.NewFile(uintptr(descriptor), "private service credential")
	var reopened unix.Stat_t
	if unix.Fstat(descriptor, &reopened) != nil || reopened.Dev != original.Dev || reopened.Ino != original.Ino ||
		checkCredentialDescriptor(descriptor, identity, false) != nil {
		_ = file.Close()
		return nil, errUnavailable
	}
	return file, nil
}

func checkCredentialDescriptor(descriptor int, identity serviceIdentity, directory bool) error {
	var stat unix.Stat_t
	var mount unix.Statfs_t
	if unix.Fstat(descriptor, &stat) != nil || unix.Fstatfs(descriptor, &mount) != nil ||
		mount.Flags&unix.ST_RDONLY == 0 {
		return errUnavailable
	}
	if directory {
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errUnavailable
		}
	} else if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > maxTokenBytes {
		return errUnavailable
	}
	acl, err := descriptorACL(descriptor)
	if err != nil || !validCredentialAccess(stat, acl, identity, directory) {
		return errUnavailable
	}
	return nil
}
