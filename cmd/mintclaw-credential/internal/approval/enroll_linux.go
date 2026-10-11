//go:build linux

package approval

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const consoleSocket = "setup.sock"

// Enroll prints a five-minute setup link only to the protected operator's
// controlling terminal. No link/token is accepted as an argument, returned,
// written to stdout, or included in errors. Service startup remains separate.
func Enroll(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getgid() != 0 ||
		os.Getegid() != 0 {
		return ErrUnavailable
	}
	if unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil || unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil {
		return ErrUnavailable
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = terminal.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(int(terminal.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFCHR ||
		!term.IsTerminal(int(terminal.Fd())) {
		return ErrUnavailable
	}
	account, err := user.Lookup("mintclaw-credential")
	if err != nil {
		return ErrUnavailable
	}
	uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return ErrUnavailable
	}
	directory, err := openConsoleDirectory(uint32(uid), uint32(gid))
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = unix.Close(directory) }()
	invite, err := requestConsoleInvite(ctx, directory, uint32(uid), uint32(gid))
	if err != nil {
		return ErrUnavailable
	}
	defer clear(invite)
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	return writeConsoleTerminal(ctx, terminal, invite)
}

func writeConsoleTerminal(parent context.Context, terminal *os.File, invite []byte) (resultErr error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	fd := int(terminal.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || unix.SetNonblock(fd, true) != nil {
		return ErrUnavailable
	}
	defer func() {
		if _, restoreErr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags); restoreErr != nil {
			resultErr = ErrUnavailable
		}
	}()
	message := append(
		[]byte(
			"Open this private link on your Android phone within five minutes.\nKeep it out of agent chat and logs.\n",
		),
		invite...)
	message = append(message, '\n')
	defer clear(message)
	for offset := 0; offset < len(message); {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		_, pollErr := unix.Poll(fds, 100)
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil || fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return ErrUnavailable
		}
		if fds[0].Revents&unix.POLLOUT == 0 {
			continue
		}
		count, writeErr := unix.Write(fd, message[offset:])
		if errors.Is(writeErr, unix.EINTR) || errors.Is(writeErr, unix.EAGAIN) {
			continue
		}
		if writeErr != nil || count <= 0 {
			return ErrUnavailable
		}
		offset += count
	}
	return nil
}

func openConsoleDirectory(uid, gid uint32) (int, error) {
	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnavailable
	}
	defer func() { _ = unix.Close(root) }()
	var rootStat unix.Stat_t
	if unix.Fstat(root, &rootStat) != nil || rootStat.Uid != 0 || rootStat.Mode&0o022 != 0 {
		return -1, ErrUnavailable
	}
	run, err := unix.Openat(root, "run", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnavailable
	}
	defer func() { _ = unix.Close(run) }()
	if unix.Fstat(run, &rootStat) != nil || rootStat.Uid != 0 || rootStat.Mode&0o022 != 0 {
		return -1, ErrUnavailable
	}
	directory, err := unix.Openat(
		run,
		"mintclaw-credential-owner",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return -1, ErrUnavailable
	}
	var stat unix.Stat_t
	_, aclErr := unix.Fgetxattr(directory, "system.posix_acl_access", nil)
	if unix.Fstat(directory, &stat) != nil || stat.Uid != uid || stat.Gid != gid || stat.Mode&0o7777 != 0o700 ||
		aclErr != unix.ENODATA {
		_ = unix.Close(directory)
		return -1, ErrUnavailable
	}
	return directory, nil
}

func requestConsoleInvite(parent context.Context, directory int, uid, gid uint32) ([]byte, error) {
	if parent == nil || parent.Err() != nil || directory < 0 || uid == 0 || gid == 0 {
		return nil, ErrUnavailable
	}
	var socket unix.Stat_t
	if unix.Fstatat(directory, consoleSocket, &socket, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Mode&0o7777 != 0o600 || socket.Uid != uid || socket.Gid != gid {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	path := fmt.Sprintf("/proc/self/fd/%d/%s", directory, consoleSocket)
	connection, err := (&net.Dialer{}).DialContext(ctx, "unixpacket", path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = connection.Close() }()
	packet, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, ErrUnavailable
	}
	peer, err := credentialipc.ConnectedPeer(packet)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer peer.Close()
	var current unix.Stat_t
	if peer.UID != uid || peer.GID != gid ||
		unix.Fstatat(directory, consoleSocket, &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		current.Dev != socket.Dev ||
		current.Ino != socket.Ino ||
		current.Mode != socket.Mode ||
		current.Uid != socket.Uid ||
		current.Gid != socket.Gid {
		return nil, ErrUnavailable
	}
	raw, err := packet.SyscallConn()
	if err != nil {
		return nil, ErrUnavailable
	}
	var passErr error
	if raw.Control(
		func(fd uintptr) { passErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) },
	) != nil ||
		passErr != nil {
		return nil, ErrUnavailable
	}
	deadline, _ := ctx.Deadline()
	if packet.SetDeadline(deadline) != nil {
		return nil, ErrUnavailable
	}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); <-ctx.Done(); _ = packet.SetDeadline(time.Now()) }()
	defer func() { cancel(); <-watchDone }()
	if _, _, err = packet.WriteMsgUnix([]byte(consoleRequest), nil, nil); err != nil {
		return nil, ErrUnavailable
	}
	buffer := make([]byte, 1025)
	defer clear(buffer)
	control := make([]byte, unix.CmsgSpace(12)+unix.CmsgSpace(16*4))
	defer clear(control)
	count, controlCount, flags, readErr := credentialipc.ReadPacket(packet, buffer, control)
	if !credentialipc.ValidSender(control[:controlCount], peer) || readErr != nil || count == 0 || count > 1024 ||
		flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || ctx.Err() != nil || !peer.Alive() || !validInvite(buffer[:count]) {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), buffer[:count]...), nil
}

func validInvite(invite []byte) bool {
	parsed, err := url.Parse(string(invite))
	if err != nil || parsed.Path != "/" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		!strings.HasPrefix(
			parsed.Fragment,
			"enroll=",
		) || !intent.ValidReference(strings.TrimPrefix(parsed.Fragment, "enroll=")) ||
		parsed.String() != string(invite) {
		return false
	}
	_, err = newVerifier(parsed.Scheme + "://" + parsed.Host)
	return err == nil && parsed.User == nil
}
