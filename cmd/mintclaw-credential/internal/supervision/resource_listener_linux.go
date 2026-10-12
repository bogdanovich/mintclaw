//go:build linux

package supervision

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

const (
	resourceDirectoryPath = "/run/mintclaw-credential-supervision"
	resourceSocketName    = "worker.sock"
)

type resourceListener struct {
	listener  *net.UnixListener
	directory *os.File
	socket    unix.Stat_t
}

// Only protected startup supplies the fixed production directory. The final
// directory permits broker traversal, never broker writes. Every ancestor is
// pinned and protected against replacement by either non-Root role.
func openResourceDirectory(path string, brokerGID uint32) (*os.File, error) {
	if !root() || brokerGID == 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		len(path) > 256 || strings.Count(path, "/") > 16 {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
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
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Gid != brokerGID ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o750 ||
		!absentAttribute(fd, "system.posix_acl_access") || !absentAttribute(fd, "system.posix_acl_default") {
		return nil, errUnavailable
	}
	pin, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(pin), "private credential resource directory"), nil
}

func openResourceListener(path string, brokerGID uint32) (result *resourceListener, resultErr error) {
	directory, err := openResourceDirectory(path, brokerGID)
	if err != nil {
		return nil, errUnavailable
	}
	endpoint := &resourceListener{directory: directory}
	defer func() {
		if resultErr != nil {
			_ = endpoint.close()
		}
	}()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, errUnavailable
	}
	file := os.NewFile(uintptr(fd), "private credential resource listener")
	defer func() { _ = file.Close() }()
	// Bind through the retained directory. Never remove an existing socket or
	// reuse a stale endpoint from a previous supervisor.
	name := "/proc/self/fd/" + strconv.Itoa(int(directory.Fd())) + "/" + resourceSocketName
	if unix.Bind(fd, &unix.SockaddrUnix{Name: name}) != nil {
		return nil, errUnavailable
	}
	if unix.Fstatat(int(directory.Fd()), resourceSocketName, &endpoint.socket, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		endpoint.socket.Mode&unix.S_IFMT != unix.S_IFSOCK || endpoint.socket.Uid != 0 {
		return nil, errUnavailable
	}
	if unix.Fchownat(int(directory.Fd()), resourceSocketName, 0, int(brokerGID), unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fchmodat(int(directory.Fd()), resourceSocketName, 0o660, 0) != nil ||
		unix.Listen(fd, 1) != nil {
		return nil, errUnavailable
	}
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, errUnavailable
	}
	var ok bool
	endpoint.listener, ok = listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		return nil, errUnavailable
	}
	endpoint.listener.SetUnlinkOnClose(false)
	if credentialipc.PrepareListener(endpoint.listener) != nil {
		return nil, errUnavailable
	}
	return endpoint, nil
}

func (endpoint *resourceListener) close() error {
	failed := false
	if endpoint.listener != nil {
		if err := endpoint.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			failed = true
		}
		endpoint.listener = nil
	}
	if endpoint.directory != nil {
		if endpoint.socket.Ino != 0 {
			var current unix.Stat_t
			fd := int(endpoint.directory.Fd())
			if unix.Fstatat(fd, resourceSocketName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
				current.Dev != endpoint.socket.Dev || current.Ino != endpoint.socket.Ino ||
				unix.Unlinkat(fd, resourceSocketName, 0) != nil {
				failed = true
			}
		}
		failed = endpoint.directory.Close() != nil || failed
		endpoint.directory = nil
	}
	if failed {
		return errUnavailable
	}
	return nil
}
