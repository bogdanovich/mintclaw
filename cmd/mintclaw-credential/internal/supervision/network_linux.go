//go:build linux

package supervision

import (
	"context"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

// Network owns a fresh network namespace with only loopback and one TCP
// listener. Qualified Root startup passes the listener to the private broker
// and enters the namespace before dropping the browser's privileges. No
// bridge, veth, DNS service, external route or host firewall rule is created.
type Network struct {
	mu        sync.Mutex
	namespace *os.File
	listener  *os.File
	identity  unix.Stat_t
}

type networkResult struct {
	network *Network
	err     error
}

// OpenNetwork performs thread-local namespace setup on a dedicated locked OS
// thread. A failed restore discards that thread instead of returning it to Go's
// thread pool. This is a Root resource primitive, not browser/lease admission.
func OpenNetwork(ctx context.Context) (*Network, error) {
	if !root() || ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	result := make(chan networkResult, 1)
	go createNetwork(ctx, result)
	select {
	case value := <-result:
		if ctx.Err() != nil {
			_ = value.network.Close()
			return nil, errUnavailable
		}
		return value.network, value.err
	case <-ctx.Done():
		// Setup owns any descriptors until it reports its restored state.
		go func() { value := <-result; _ = value.network.Close() }()
		return nil, errUnavailable
	}
}

func createNetwork(ctx context.Context, result chan<- networkResult) {
	runtime.LockOSThread()
	restored := true
	value := networkResult{err: errUnavailable}
	defer func() {
		if restored {
			runtime.UnlockOSThread()
		}
		result <- value
	}()
	original, err := openNetworkNamespace()
	if err != nil {
		return
	}
	defer func() { _ = original.Close() }()
	if ctx.Err() != nil || unix.Unshare(unix.CLONE_NEWNET) != nil {
		return
	}
	restored = false
	defer func() {
		if unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) != nil {
			_ = value.network.Close()
			value = networkResult{err: errUnavailable}
			return
		}
		restored = true
	}()
	namespace, err := openNetworkNamespace()
	if err != nil {
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = namespace.Close()
		}
	}()
	var identity, previous unix.Stat_t
	if unix.Fstat(int(namespace.Fd()), &identity) != nil || unix.Fstat(int(original.Fd()), &previous) != nil ||
		(identity.Dev == previous.Dev && identity.Ino == previous.Ino) || enableLoopback() != nil || ctx.Err() != nil {
		return
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return
	}
	listener := os.NewFile(uintptr(fd), "private credential proxy listener")
	defer func() {
		if !keep {
			_ = listener.Close()
		}
	}()
	if unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}) != nil || unix.Listen(fd, 128) != nil ||
		listenerNamespace(fd, identity) != nil || ctx.Err() != nil {
		return
	}
	keep = true
	value = networkResult{network: &Network{namespace: namespace, listener: listener, identity: identity}}
}

func openNetworkNamespace() (*os.File, error) {
	// This intentional proc magic-link open pins the calling thread's kernel
	// namespace, not a configurable filesystem path or model-provided inode.
	fd, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	var fs unix.Statfs_t
	kind, kindErr := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.NSFS_MAGIC || kindErr != nil || kind != unix.CLONE_NEWNET {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private credential network namespace"), nil
}

func enableLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = unix.Close(fd) }()
	request, err := unix.NewIfreq("lo")
	if err != nil || unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request) != nil {
		return errUnavailable
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	if unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request) != nil {
		return errUnavailable
	}
	return nil
}

func listenerNamespace(fd int, expected unix.Stat_t) error {
	address, err := unix.Getsockname(fd)
	endpoint, ipv4 := address.(*unix.SockaddrInet4)
	typeValue, typeErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	listening, listenErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	reuse, reuseErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT)
	if err != nil || !ipv4 || endpoint.Addr != [4]byte{127, 0, 0, 1} || endpoint.Port < 1024 ||
		typeErr != nil || typeValue != unix.SOCK_STREAM || listenErr != nil || listening != 1 ||
		reuseErr != nil || reuse != 0 {
		return errUnavailable
	}
	namespace, err := unix.IoctlRetInt(fd, unix.SIOCGSKNS)
	if err != nil {
		return errUnavailable
	}
	unix.CloseOnExec(namespace)
	defer func() { _ = unix.Close(namespace) }()
	var stat unix.Stat_t
	if unix.Fstat(namespace, &stat) != nil || stat.Dev != expected.Dev || stat.Ino != expected.Ino {
		return errUnavailable
	}
	return nil
}

// Borrow duplicates both owned descriptors only for qualified Root startup.
// The private broker receives only the listener through authenticated IPC.
// Borrowed descriptors have independent ownership: teardown must close the
// proxy and its accepted sockets, retire the forest, then close all borrowed
// pins. Closing Network alone cannot revoke somebody else's descriptor.
func (network *Network) Borrow(ctx context.Context) (*os.File, *os.File, error) {
	if network == nil || !root() || ctx == nil || ctx.Err() != nil {
		return nil, nil, errUnavailable
	}
	network.mu.Lock()
	defer network.mu.Unlock()
	if ctx.Err() != nil || network.namespace == nil || network.listener == nil ||
		listenerNamespace(int(network.listener.Fd()), network.identity) != nil {
		return nil, nil, errUnavailable
	}
	first, err := unix.FcntlInt(network.namespace.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errUnavailable
	}
	second, err := unix.FcntlInt(network.listener.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(first)
		return nil, nil, errUnavailable
	}
	if ctx.Err() != nil {
		_ = unix.Close(first)
		_ = unix.Close(second)
		return nil, nil, errUnavailable
	}
	return os.NewFile(uintptr(first), "private credential network namespace"),
		os.NewFile(uintptr(second), "private credential proxy listener"), nil
}

func (network *Network) Close() error {
	if network == nil {
		return nil
	}
	network.mu.Lock()
	defer network.mu.Unlock()
	var failed bool
	for _, file := range []*os.File{network.listener, network.namespace} {
		if file != nil && file.Close() != nil {
			failed = true
		}
	}
	network.listener, network.namespace = nil, nil
	if failed {
		return errUnavailable
	}
	return nil
}
