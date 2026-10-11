//go:build linux

package supervision

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNetworkFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE") != "1" {
		t.Skip("disposable Root network fixture only")
	}
	if os.Getenv("MINTCLAW_NETWORK_FIXTURE_CHILD") == "1" {
		if os.Getuid() != 65532 || os.Getgid() != 65532 {
			t.Fatal("synthetic child identity unavailable")
		}
		if _, err := OpenNetwork(context.Background()); !errors.Is(err, errUnavailable) {
			t.Fatal("unprivileged network setup admitted")
		}
		return
	}
	if os.Getenv("MINTCLAW_NETWORK_FIXTURE_CHILD") == "allocation-failure" {
		exerciseNetworkAllocationFailure(t)
		return
	}
	if !root() {
		t.Fatal("Root network fixture required")
	}
	before, err := openNetworkNamespace()
	if err != nil {
		t.Fatal("original namespace unavailable")
	}
	defer func() { _ = before.Close() }()
	var original unix.Stat_t
	if unix.Fstat(int(before.Fd()), &original) != nil {
		t.Fatal("original namespace identity unavailable")
	}
	for range 3 {
		network, err := OpenNetwork(context.Background())
		if err != nil {
			t.Fatal("private namespace allocation failed")
		}
		func() {
			defer func() {
				if network.Close() != nil {
					t.Error("private namespace close failed")
				}
			}()
			namespace, socket, err := network.Borrow(context.Background())
			if err != nil {
				t.Fatal("private listener borrow failed")
			}
			defer func() { _ = namespace.Close(); _ = socket.Close() }()
			var isolated unix.Stat_t
			if unix.Fstat(int(namespace.Fd()), &isolated) != nil || isolated.Ino == original.Ino {
				t.Fatal("namespace was not isolated")
			}
			listener, err := net.FileListener(socket)
			if err != nil {
				t.Fatal("broker listener adoption failed")
			}
			defer func() { _ = listener.Close() }()
			address, ok := listener.Addr().(*net.TCPAddr)
			if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) || address.Port < 1024 {
				t.Fatal("private listener endpoint changed")
			}
			if listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)) != nil {
				t.Fatal("private listener deadline failed")
			}
			accepted := make(chan bool, 1)
			go func() {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					accepted <- false
					return
				}
				defer func() { _ = connection.Close() }()
				if connection.SetDeadline(time.Now().Add(time.Second)) != nil {
					accepted <- false
					return
				}
				var data [1]byte
				_, readErr := io.ReadFull(connection, data[:])
				accepted <- readErr == nil && data[0] == 42
			}()
			if !exerciseIsolatedNetwork(namespace, address.Port) {
				t.Fatal("private network communication or direct-egress denial failed")
			}
			select {
			case ok := <-accepted:
				if !ok {
					t.Fatal("outside broker did not serve inherited listener")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("outside broker acceptance timed out")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if first, second, denied := network.Borrow(
				ctx,
			); !errors.Is(denied, errUnavailable) || first != nil ||
				second != nil {
				t.Fatal("canceled descriptor transfer admitted")
			}
			if network.Close() != nil {
				t.Fatal("private network close failed")
			}
			if first, second, denied := network.Borrow(
				context.Background(),
			); !errors.Is(denied, errUnavailable) || first != nil ||
				second != nil {
				t.Fatal("closed namespace renewed")
			}
		}()
		current, err := openNetworkNamespace()
		if err != nil {
			t.Fatal("restored namespace unavailable")
		}
		var restored unix.Stat_t
		statErr := unix.Fstat(int(current.Fd()), &restored)
		_ = current.Close()
		if statErr != nil || restored.Dev != original.Dev || restored.Ino != original.Ino {
			t.Fatal("Go namespace state was contaminated")
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNetworkFixture$", "-test.count=1")
	child.Env = []string{
		"PATH=/usr/bin:/bin",
		"MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE=1",
		"MINTCLAW_NETWORK_FIXTURE_CHILD=1",
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if child.Run() != nil {
		t.Fatal("unprivileged network denial failed")
	}
	failure := exec.Command(os.Args[0], "-test.run=^TestNetworkFixture$", "-test.count=1")
	failure.Env = []string{
		"PATH=/usr/bin:/bin", "MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE=1",
		"MINTCLAW_NETWORK_FIXTURE_CHILD=allocation-failure",
	}
	if failure.Run() != nil {
		t.Fatal("partial network allocation cleanup failed")
	}
}

// Exhaust only this disposable child's descriptor limit, leaving exactly one
// slot for the original namespace pin. The new namespace pin then fails after
// unshare. The parent process and its limits remain untouched.
func exerciseNetworkAllocationFailure(t *testing.T) {
	t.Helper()
	if !root() {
		t.Fatal("Root allocation-failure child required")
	}
	original, err := openNetworkNamespace()
	if err != nil {
		t.Fatal("original failure-fixture namespace unavailable")
	}
	defer func() { _ = original.Close() }()
	var before unix.Stat_t
	if unix.Fstat(int(original.Fd()), &before) != nil {
		t.Fatal("original failure-fixture identity unavailable")
	}
	var limit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &limit) != nil || limit.Max < 32 {
		t.Fatal("failure-fixture descriptor limit unavailable")
	}
	if unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 32, Max: limit.Max}) != nil {
		t.Fatal("failure-fixture descriptor limit setup failed")
	}
	defer func() {
		if unix.Setrlimit(unix.RLIMIT_NOFILE, &limit) != nil {
			t.Error("failure-fixture limit restore failed")
		}
	}()
	var filled []int
	defer func() {
		for _, fd := range filled {
			_ = unix.Close(fd)
		}
	}()
	for {
		fd, openErr := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.EMFILE) {
			break
		}
		if openErr != nil {
			t.Fatal("failure-fixture descriptor exhaustion failed")
		}
		filled = append(filled, fd)
	}
	if len(filled) < 2 {
		t.Fatal("failure-fixture descriptor slots unavailable")
	}
	if unix.Close(filled[len(filled)-1]) != nil {
		t.Fatal("failure-fixture original pin slot unavailable")
	}
	filled = filled[:len(filled)-1]
	network, denied := OpenNetwork(context.Background())
	if network != nil {
		_ = network.Close()
	}
	if !errors.Is(denied, errUnavailable) {
		t.Fatal("exhausted network allocation admitted")
	}
	// Close fillers before obtaining another namespace pin for the witness.
	for _, fd := range filled {
		_ = unix.Close(fd)
	}
	filled = nil
	current, err := openNetworkNamespace()
	if err != nil {
		t.Fatal("namespace after partial allocation unavailable")
	}
	defer func() { _ = current.Close() }()
	var after unix.Stat_t
	if unix.Fstat(int(current.Fd()), &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino {
		t.Fatal("partial allocation contaminated Go namespace state")
	}
}

func exerciseIsolatedNetwork(namespace *os.File, port int) bool {
	result := make(chan bool, 1)
	go func() {
		runtime.LockOSThread()
		restored, good := true, false
		defer func() {
			if restored {
				runtime.UnlockOSThread()
			}
			result <- good
		}()
		original, err := openNetworkNamespace()
		if err != nil {
			return
		}
		defer func() { _ = original.Close() }()
		if unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNET) != nil {
			return
		}
		restored = false
		defer func() {
			if unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) != nil {
				good = false
				return
			}
			restored = true
		}()
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return
		}
		defer func() { _ = unix.Close(fd) }()
		if unix.Connect(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: port}) != nil {
			return
		}
		if n, err := unix.Write(fd, []byte{42}); err != nil || n != 1 {
			return
		}
		outside, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			return
		}
		defer func() { _ = unix.Close(outside) }()
		if !errors.Is(
			unix.Connect(outside, &unix.SockaddrInet4{Addr: [4]byte{198, 51, 100, 1}, Port: 443}),
			unix.ENETUNREACH,
		) {
			return
		}
		ipv6, err := unix.Socket(unix.AF_INET6, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if errors.Is(err, unix.EAFNOSUPPORT) {
			good = true
			return
		}
		if err != nil {
			return
		}
		defer func() { _ = unix.Close(ipv6) }()
		good = errors.Is(
			unix.Connect(
				ipv6,
				&unix.SockaddrInet6{Addr: [16]byte{0x20, 1, 0xd, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Port: 443},
			),
			unix.ENETUNREACH,
		)
	}()
	select {
	case good := <-result:
		return good
	case <-time.After(3 * time.Second):
		return false
	}
}
