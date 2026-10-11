//go:build linux

package supervision

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func absentAttribute(fd int, name string) bool {
	_, err := unix.Fgetxattr(fd, name, nil)
	return errors.Is(err, unix.ENODATA) || errors.Is(err, unix.EOPNOTSUPP)
}

func launcherDirectory(fd int) bool {
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && stat.Uid == 0 && stat.Gid == 0 &&
		stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o7022 == 0 &&
		absentAttribute(fd, "system.posix_acl_access") && absentAttribute(fd, "system.posix_acl_default")
}

// The only production callers supply fixed helper/lock paths. Walk and pin
// every ancestor; do not execute through a later mutable pathname lookup.
func protectedLauncherFile(path string, executable bool) (*os.File, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Count(path, "/") > 16 {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		if !launcherDirectory(fd) {
			return nil, errUnavailable
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, errUnavailable
		}
		_ = unix.Close(fd)
		fd = next
	}
	if !launcherDirectory(fd) {
		return nil, errUnavailable
	}
	pin, err := unix.Openat(fd, parts[len(parts)-1], unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = unix.Close(pin) }()
	var original unix.Stat_t
	if unix.Fstat(pin, &original) != nil || !launcherRegular(original, executable) {
		return nil, errUnavailable
	}
	dataFD, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errUnavailable
	}
	file := os.NewFile(uintptr(dataFD), "private credential launch artifact")
	var actual unix.Stat_t
	var prefix [4]byte
	valid := unix.Fstat(dataFD, &actual) == nil && original.Dev == actual.Dev && original.Ino == actual.Ino &&
		launcherRegular(actual, executable) && absentAttribute(dataFD, "system.posix_acl_access") &&
		absentAttribute(dataFD, "security.capability")
	if executable {
		count, readErr := file.ReadAt(prefix[:], 0)
		valid = valid && readErr == nil && count == 4 && string(prefix[:]) == "\x7fELF"
	}
	if !valid {
		_ = file.Close()
		return nil, errUnavailable
	}
	return file, nil
}

func launcherRegular(stat unix.Stat_t, executable bool) bool {
	if stat.Uid != 0 || stat.Gid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		stat.Mode&0o7022 != 0 {
		return false
	}
	if executable {
		return stat.Mode&0o111 == 0o111 && stat.Size >= 4 && stat.Size <= 64<<20
	}
	return stat.Mode&0o7777 == 0o600 && stat.Size == 0
}

func qualifyLaunchMount(file *os.File) error {
	if file == nil {
		return errUnavailable
	}
	fd := int(file.Fd())
	var fs unix.Statfs_t
	if !launcherDirectory(fd) || unix.Fstatfs(fd, &fs) != nil || fs.Flags&unix.ST_RDONLY == 0 ||
		fs.Flags&unix.ST_NOEXEC != 0 || (fs.Type != unix.TMPFS_MAGIC && fs.Type != unix.SQUASHFS_MAGIC) {
		return errUnavailable
	}
	for _, name := range []string{"proc", "dev", "tmp", "run"} {
		child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return errUnavailable
		}
		directory := os.NewFile(uintptr(child), "private credential mount slot")
		names, readErr := directory.Readdirnames(1)
		valid := launcherDirectory(child) && len(names) == 0 && errors.Is(readErr, io.EOF)
		closeErr := directory.Close()
		if !valid || closeErr != nil {
			return errUnavailable
		}
	}
	for _, name := range []string{"bin/setpriv", bootstrapPath} {
		bin, err := unix.Openat(fd, "bin", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return errUnavailable
		}
		pin, openErr := unix.Openat(
			bin,
			strings.TrimPrefix(name, "bin/"),
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(bin)
		if openErr != nil {
			return errUnavailable
		}
		var stat unix.Stat_t
		var prefix [4]byte
		count, readErr := unix.Pread(pin, prefix[:], 0)
		valid := unix.Fstat(pin, &stat) == nil && launcherRegular(stat, true) && stat.Mode&0o7777 == 0o555 &&
			count == 4 && readErr == nil && string(prefix[:]) == "\x7fELF"
		closeErr := unix.Close(pin)
		if !valid || closeErr != nil {
			return errUnavailable
		}
	}
	return nil
}
