//go:build linux

package supervision

import (
	"context"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/runtimeimage"
)

// BootstrapBrowser is the fixed in-image pre-Node program. It accepts no
// arguments, secret or command. Root's setpriv must already have dropped the
// host browser identity and every capability. All setup/runtime descriptors are
// closed on this locked thread before the fixed Node exec; only FD3 survives.
func BootstrapBrowser(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || os.Getuid() == 0 || os.Getgid() == 0 ||
		os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return errUnavailable
	}
	runtime.LockOSThread()
	discardThread := false
	defer func() {
		if !discardThread {
			runtime.UnlockOSThread()
		}
	}()
	// On failure the locked thread exits with this goroutine; do not recycle a
	// thread whose descriptor table may already have been unshared and closed.
	groups, err := os.Getgroups()
	if err != nil {
		return errUnavailable
	}
	for _, group := range groups {
		if group != os.Getgid() {
			return errUnavailable
		}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || len(status) > 16384 {
		return errUnavailable
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(string(status), "\n") {
		key, value, found := strings.Cut(line, ":")
		if found {
			fields[key] = strings.TrimSpace(value)
		}
	}
	if fields["NoNewPrivs"] != "1" {
		return errUnavailable
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if fields[key] != "0000000000000000" {
			return errUnavailable
		}
	}
	var core unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_CORE, &core) != nil || core.Cur != 0 || core.Max != 0 ||
		unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil || !bootstrapDescriptors() || ctx.Err() != nil {
		return errUnavailable
	}
	environment := []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TZ=UTC", "HOME=/tmp", "TMPDIR=/tmp"}
	// Bubblewrap sets PWD after applying --clearenv and --chdir. Admit only
	// its fixed root value, then omit it from the Node environment below.
	if len(os.Environ()) != len(environment)+1 || os.Getenv("PWD") != "/" {
		return errUnavailable
	}
	for _, value := range environment {
		name, expected, _ := strings.Cut(value, "=")
		if os.Getenv(name) != expected {
			return errUnavailable
		}
	}
	executable := "/" + runtimeimage.NodePath
	arguments := []string{executable, "/" + runtimeimage.WorkerPath}
	discardThread = true
	if unix.CloseRange(4, uint(^uint32(0)), unix.CLOSE_RANGE_UNSHARE) != nil {
		return errUnavailable
	}
	// No further Go/file operations occur between closing startup descriptors
	// and exec. Unsupported close_range never falls back to guessed FD numbers.
	if unix.Exec(executable, arguments, environment) != nil {
		return errUnavailable
	}
	return errUnavailable
}

func bootstrapDescriptors() bool {
	kind, err := unix.GetsockoptInt(3, unix.SOL_SOCKET, unix.SO_TYPE)
	peer, peerErr := unix.GetsockoptUcred(3, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || kind != unix.SOCK_STREAM || !unnamedUnixEndpoint(3, unix.SYS_GETSOCKNAME) ||
		!unnamedUnixEndpoint(3, unix.SYS_GETPEERNAME) || peerErr != nil || peer.Uid != 0 || peer.Gid != 0 {
		return false
	}
	var namespace, current, image, rootStat unix.Stat_t
	var namespaceFS, imageFS unix.Statfs_t
	if unix.Fstat(4, &namespace) != nil || unix.Fstatfs(4, &namespaceFS) != nil ||
		namespaceFS.Type != unix.NSFS_MAGIC || unix.Stat("/proc/thread-self/ns/net", &current) != nil ||
		namespace.Dev != current.Dev || namespace.Ino != current.Ino ||
		unix.Fstat(5, &image) != nil || unix.Stat("/", &rootStat) != nil ||
		image.Dev != rootStat.Dev || image.Ino != rootStat.Ino || image.Uid != 0 || image.Gid != 0 ||
		image.Mode&0o7777 != 0o555 || image.Mode&unix.S_IFMT != unix.S_IFDIR ||
		unix.Fstatfs(5, &imageFS) != nil || imageFS.Flags&unix.ST_RDONLY == 0 {
		return false
	}
	for _, fd := range []int{6, 7} {
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || !launcherRegular(stat, true) {
			return false
		}
	}
	return true
}

// x/sys's high-level Unix address conversion renders both an unnamed socket
// and an empty abstract name as "@". Check the kernel sockaddr length instead:
// a socketpair has only sa_family_t, with no path/name bytes at all.
func unnamedUnixEndpoint(fd int, operation uintptr) bool {
	var address unix.RawSockaddrUnix
	length := uint32(unix.SizeofSockaddrUnix)
	_, _, errno := unix.Syscall(
		operation,
		uintptr(fd),
		uintptr(unsafe.Pointer(&address)),
		uintptr(unsafe.Pointer(&length)),
	)
	return errno == 0 && length == 2 && address.Family == unix.AF_UNIX
}
