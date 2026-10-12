//go:build linux

package supervision

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/runtimeimage"
)

// This executable stands in for Node only in the disposable fixture. The same
// real nsenter/bwrap/setpriv/bootstrap path is exercised before this entry.
func TestMain(tests *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "resource-packet-handoff" {
		file := os.NewFile(3, "synthetic handed-off packet")
		connection, err := net.FileConn(file)
		if err != nil {
			os.Exit(2)
		}
		packet, ok := connection.(*net.UnixConn)
		if !ok {
			os.Exit(2)
		}
		if _, _, err := packet.WriteMsgUnix([]byte{1, 0}, nil, nil); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 4 && os.Args[1] == "resource-broker" {
		os.Exit(syntheticResourceBroker(os.Args[2], os.Args[3]))
	}
	if len(os.Args) == 2 && os.Args[1] == "bootstrap-browser" {
		_ = BootstrapBrowser(context.Background())
		_, _ = unix.Write(3, []byte{90}) // Fixed fixture-only pre-Node failure witness.
		os.Exit(90)
	}
	if len(os.Args) == 2 && os.Args[1] == "launcher-denied" {
		if launcher, err := OpenLauncher(context.Background()); launcher != nil || !errors.Is(err, errUnavailable) {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "/"+runtimeimage.WorkerPath {
		result := syntheticNode()
		if result != 0 {
			_, _ = unix.Write(3, []byte{80}) // Fixed fixture-only Node failure witness.
		}
		os.Exit(result)
	}
	os.Exit(tests.Run())
}

func syntheticNode() int {
	if os.Getuid() != 65532 || os.Geteuid() != 65532 || os.Getgid() != 65532 || os.Getegid() != 65532 {
		return 2
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		return 2
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !regexp.MustCompile(`(?m)^NoNewPrivs:\s+1$`).Match(status) {
		return 2
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if !regexp.MustCompile(`(?m)^` + key + `:\s+0+$`).Match(status) {
			return 2
		}
	}
	if _, err := os.Stat("/home"); !errors.Is(err, os.ErrNotExist) {
		return 2
	}
	if _, err := os.Stat("/etc"); !errors.Is(err, os.ErrNotExist) {
		return 2
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 2
	}
	for _, entry := range entries {
		if entry.Name() != "1" && entry.Name() != "2" && regexp.MustCompile(`^[0-9]+$`).MatchString(entry.Name()) {
			return 2 // No host/gateway/broker process appears in private proc.
		}
	}
	if os.WriteFile("/tmp/synthetic-private-profile", []byte("synthetic"), 0o600) != nil {
		return 2
	}
	if err := os.WriteFile("/runtime/synthetic-root-write", []byte("synthetic"), 0o600); err == nil {
		return 2
	}
	// The bootstrap has removed the namespace/runtime/helper setup pins. Go
	// may reuse their numbers for its own poller, so check objects, not integers.
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 2
	}
	for _, fd := range fds {
		if fd.Name() == "0" || fd.Name() == "1" || fd.Name() == "2" || fd.Name() == "3" {
			continue
		}
		link, readErr := os.Readlink("/proc/self/fd/" + fd.Name())
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return 2
		}
		if strings.HasPrefix(link, "net:[") || strings.HasSuffix(link, "/nsenter") ||
			strings.HasSuffix(link, "/bwrap") || strings.Contains(link, "mintclaw-launcher-qual-") {
			return 2
		}
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			if family == unix.AF_INET6 && errors.Is(err, unix.EAFNOSUPPORT) {
				continue
			}
			return 2
		}
		var address unix.Sockaddr = &unix.SockaddrInet4{Port: 443, Addr: [4]byte{198, 51, 100, 1}}
		if family == unix.AF_INET6 {
			address = &unix.SockaddrInet6{
				Port: 443,
				Addr: [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
			}
		}
		connectErr := unix.Connect(fd, address)
		_ = unix.Close(fd)
		if !errors.Is(connectErr, unix.ENETUNREACH) {
			return 2
		}
	}
	control := os.NewFile(3, "private synthetic control")
	if _, err := control.Write([]byte{1}); err != nil {
		return 2
	}
	var port [2]byte
	if _, err := io.ReadFull(control, port[:]); err != nil {
		return 2
	}
	socket, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 2
	}
	defer func() { _ = unix.Close(socket) }()
	if unix.Connect(
		socket,
		&unix.SockaddrInet4{Port: int(binary.BigEndian.Uint16(port[:])), Addr: [4]byte{127, 0, 0, 1}},
	) != nil {
		return 2
	}
	if _, err := unix.Write(socket, []byte{42}); err != nil {
		return 2
	}
	var exit [1]byte
	if _, err := io.ReadFull(control, exit[:]); err != nil || exit[0] != 1 {
		return 2
	}
	return 0
}

type fixtureRuntimeMount struct{ root *os.File }

func (image *fixtureRuntimeMount) BorrowRoot(context.Context) (*os.File, error) {
	return duplicateLaunchFile(image.root)
}
func (image *fixtureRuntimeMount) Close() error { return image.root.Close() }

func launcherFixtureImage(t *testing.T, invalidNode bool) *fixtureRuntimeMount {
	t.Helper()
	path, err := os.MkdirTemp("/run", "mintclaw-launcher-qual-")
	if err != nil || unix.Mount("none", path, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=128m") != nil {
		t.Fatal("private synthetic runtime allocation failed")
	}
	t.Cleanup(func() {
		if unix.Unmount(path, 0) != nil || os.Remove(path) != nil {
			t.Error("synthetic runtime cleanup failed")
		}
	})
	copyFile := func(source, target string) {
		t.Helper()
		data, readErr := os.ReadFile(source)
		if readErr != nil || os.MkdirAll(filepath.Dir(filepath.Join(path, target)), 0o755) != nil ||
			os.WriteFile(filepath.Join(path, target), data, 0o555) != nil {
			t.Fatal("public fixture artifact copy failed")
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal("fixture executable unavailable")
	}
	copyFile(self, bootstrapPath)
	copyFile(self, runtimeimage.NodePath)
	if invalidNode &&
		os.WriteFile(
			filepath.Join(path, runtimeimage.NodePath),
			[]byte("\x7fELFinvalid-synthetic-node"),
			0o555,
		) != nil {
		t.Fatal("synthetic post-exec failure artifact unavailable")
	}
	copyFile("/usr/bin/setpriv", "bin/setpriv")
	libraries, err := exec.Command("/usr/bin/ldd", "/usr/bin/setpriv").Output()
	if err != nil {
		t.Fatal("fixed system setpriv dependencies unavailable")
	}
	for _, library := range regexp.MustCompile(`/[^\s()]+`).FindAllString(string(libraries), -1) {
		copyFile(library, strings.TrimPrefix(library, "/"))
	}
	for _, name := range []string{"proc", "dev", "tmp", "run", "runtime"} {
		if os.MkdirAll(filepath.Join(path, name), 0o555) != nil {
			t.Fatal("fixture private mount slot unavailable")
		}
	}
	if filepath.WalkDir(path, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(name, 0o555)
		}
		return nil
	}) != nil || unix.Mount("none", path, "tmpfs", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID, "") != nil {
		t.Fatal("synthetic runtime could not be sealed read-only")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal("synthetic runtime pin unavailable")
	}
	return &fixtureRuntimeMount{os.NewFile(uintptr(fd), "private synthetic runtime")}
}

func TestLauncherFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE") != "1" {
		t.Skip("disposable Root Linux systemd fixture only")
	}
	if !root() || os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "" {
		t.Fatal("Root fixture without token material required")
	}
	if unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil {
		t.Fatal("fixture core limit could not be disabled")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal("fixture executable unavailable")
	}
	unprivileged := exec.Command(self, "launcher-denied")
	unprivileged.Env = []string{"PATH=/usr/bin:/bin"}
	unprivileged.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if unprivileged.Run() != nil {
		t.Fatal("non-Root launcher constructor was admitted")
	}
	exerciseLauncherArtifacts(t)
	parent := fixtureParent(t)
	supervisor, err := open(parent)
	if err != nil {
		t.Fatal("fixture Root cgroup admission failed")
	}
	launcher := &Launcher{image: launcherFixtureImage(t, false), supervisor: supervisor, uid: 65532, gid: 65532}
	launcher.nsenter, err = protectedLauncherFile("/usr/bin/nsenter", true)
	if err != nil {
		t.Fatal("fixed nsenter was not admitted")
	}
	launcher.bubblewrap, err = protectedLauncherFile("/usr/bin/bwrap", true)
	if err != nil {
		t.Fatal("fixed bubblewrap was not admitted")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		closeErr := launcher.Close(ctx)
		if !launcher.quarantined && closeErr != nil || launcher.quarantined && !errors.Is(closeErr, errUnavailable) {
			t.Error("fixed launcher cleanup was not verified")
		}
		if launcher.supervisor != nil || launcher.image != nil || launcher.nsenter != nil ||
			launcher.bubblewrap != nil {
			t.Error("closed launcher retained startup resources")
		}
	})
	for _, scenario := range []string{"before-start", "explicit", "cancellation", "leader-exit"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			worker, err := launcher.Prepare(ctx)
			if err != nil {
				t.Fatal("fixed worker allocation failed")
			}
			if worker.Start(ctx) == nil {
				t.Fatal("worker started before endpoint export")
			}
			listenerFile, controlFile, err := worker.Endpoints(ctx)
			if err != nil {
				t.Fatal("private fixture endpoints unavailable")
			}
			listener, listenErr := net.FileListener(listenerFile)
			connection, controlErr := net.FileConn(controlFile)
			_ = listenerFile.Close()
			_ = controlFile.Close()
			if listenErr != nil || controlErr != nil {
				t.Fatal("private fixture endpoint adoption failed")
			}
			defer func() { _ = listener.Close(); _ = connection.Close() }()
			if extraListener, extraControl, err := worker.Endpoints(ctx); extraListener != nil || extraControl != nil ||
				!errors.Is(err, errUnavailable) {
				t.Fatal("private endpoints exported twice")
			}
			if scenario != "before-start" {
				if worker.Start(ctx) != nil || worker.Start(ctx) == nil {
					t.Fatal("fixed worker start or single-start rule failed")
				}
				if connection.SetDeadline(time.Now().Add(5*time.Second)) != nil {
					t.Fatal("fixture deadline unavailable")
				}
				var ready [1]byte
				if _, err := io.ReadFull(connection, ready[:]); err != nil || ready[0] != 1 {
					t.Fatal("private identity/filesystem/PID/network/bootstrap witness failed")
				}
				var port [2]byte
				binary.BigEndian.PutUint16(port[:], uint16(listener.Addr().(*net.TCPAddr).Port))
				if _, err := connection.Write(port[:]); err != nil {
					t.Fatal("private fixture port delivery failed")
				}
				if listener.(*net.TCPListener).SetDeadline(time.Now().Add(3*time.Second)) != nil {
					t.Fatal("fixture accept deadline unavailable")
				}
				socket, err := listener.Accept()
				if err != nil {
					t.Fatal("outside fixture could not serve private loopback socket")
				}
				_ = socket.SetDeadline(time.Now().Add(time.Second))
				var data [1]byte
				_, readErr := io.ReadFull(socket, data[:])
				_ = socket.Close()
				if readErr != nil || data[0] != 42 {
					t.Fatal("private loopback data did not cross the owned listener")
				}
			}
			switch scenario {
			case "before-start", "cancellation":
				cancel()
			case "leader-exit":
				if _, err := connection.Write([]byte{1}); err != nil {
					t.Fatal("synthetic leader exit request failed")
				}
			default:
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				defer done()
				if worker.Retire(cleanup) != nil {
					t.Fatal("fixed worker retirement failed")
				}
			}
			select {
			case <-worker.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("owned worker resources did not retire")
			}
			if worker.result != nil || worker.Start(context.Background()) == nil {
				t.Fatal("retirement failed or retired worker restarted")
			}
		})
	}
	t.Run("start-failure-quarantines", func(t *testing.T) {
		worker, err := launcher.Prepare(t.Context())
		if err != nil {
			t.Fatal("failure fixture allocation unavailable")
		}
		listener, control, err := worker.Endpoints(t.Context())
		if err != nil {
			t.Fatal("failure fixture endpoints unavailable")
		}
		_ = listener.Close()
		_ = control.Close()
		if launcher.nsenter.Close() != nil {
			t.Fatal("synthetic launch failure could not be prepared")
		}
		if !errors.Is(worker.Start(t.Context()), errUnavailable) {
			t.Fatal("missing helper did not deny startup")
		}
		select {
		case <-worker.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("failed startup cleanup did not finish")
		}
		if !errors.Is(worker.result, errUnavailable) || !launcher.quarantined {
			t.Fatal("uncertain start did not quarantine launcher")
		}
		if next, err := launcher.Prepare(t.Context()); next != nil || !errors.Is(err, errUnavailable) {
			t.Fatal("quarantined launcher renewed work")
		}
		// The closed helper has already served its fault-injection purpose.
		// Avoid manufacturing a second descriptor-close failure in final cleanup.
		launcher.nsenter = nil
	})
}

func exerciseLauncherArtifacts(t *testing.T) {
	t.Helper()
	directory, err := os.MkdirTemp("/run", "mintclaw-launcher-artifacts-")
	if err != nil {
		t.Fatal("public artifact fixture allocation failed")
	}
	defer func() { _ = os.RemoveAll(directory) }()
	lockPath := filepath.Join(directory, "lock")
	if os.WriteFile(lockPath, nil, 0o600) != nil {
		t.Fatal("synthetic lock unavailable")
	}
	first, err := protectedLauncherFile(lockPath, false)
	if err != nil {
		t.Fatal("protected lock not admitted")
	}
	defer func() { _ = first.Close() }()
	second, err := protectedLauncherFile(lockPath, false)
	if err != nil {
		t.Fatal("second lock pin unavailable")
	}
	defer func() { _ = second.Close() }()
	if unix.Flock(int(first.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil ||
		!errors.Is(unix.Flock(int(second.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK) {
		t.Fatal("singleton lock did not exclude another owner")
	}
	if os.Chmod(lockPath, 0o666) != nil {
		t.Fatal("unsafe lock fixture unavailable")
	}
	if unsafe, err := protectedLauncherFile(lockPath, false); unsafe != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("writable lock admitted")
	}
	if os.Chmod(lockPath, 0o600) != nil || os.Chmod(directory, 0o777) != nil {
		t.Fatal("unsafe ancestor fixture unavailable")
	}
	if unsafe, err := protectedLauncherFile(lockPath, false); unsafe != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("writable ancestor admitted")
	}
	if os.Chmod(directory, 0o700) != nil || os.Symlink(lockPath, filepath.Join(directory, "link")) != nil {
		t.Fatal("symlink fixture unavailable")
	}
	if unsafe, err := protectedLauncherFile(filepath.Join(directory, "link"), false); unsafe != nil ||
		!errors.Is(err, errUnavailable) {
		t.Fatal("symlink lock admitted")
	}
}

func TestLauncherFailureFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE") != "1" {
		t.Skip("disposable Root Linux systemd fixture only")
	}
	if !root() || os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "" ||
		unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil {
		t.Fatal("Root fixture without token/core material required")
	}
	supervisor, err := open(fixtureParent(t))
	if err != nil {
		t.Fatal("failure fixture supervisor unavailable")
	}
	launcher := &Launcher{image: launcherFixtureImage(t, true), supervisor: supervisor, uid: 65532, gid: 65532}
	launcher.nsenter, err = protectedLauncherFile("/usr/bin/nsenter", true)
	if err != nil {
		t.Fatal("failure fixture nsenter unavailable")
	}
	launcher.bubblewrap, err = protectedLauncherFile("/usr/bin/bwrap", true)
	if err != nil {
		t.Fatal("failure fixture bubblewrap unavailable")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		closeErr := launcher.Close(ctx)
		if launcher.quarantined && !errors.Is(closeErr, errUnavailable) || !launcher.quarantined && closeErr != nil {
			t.Error("failed launcher close outcome incorrect")
		}
		if launcher.image != nil || launcher.supervisor != nil || launcher.nsenter != nil ||
			launcher.bubblewrap != nil {
			t.Error("failed launcher retained Root startup resources")
		}
	})
	worker, err := launcher.Prepare(t.Context())
	if err != nil {
		t.Fatal("post-exec failure fixture allocation unavailable")
	}
	listener, control, err := worker.Endpoints(t.Context())
	if err != nil {
		t.Fatal("failure fixture endpoint export unavailable")
	}
	_ = listener.Close()
	_ = control.Close()
	if worker.Start(t.Context()) != nil {
		t.Fatal("initial helper exec did not succeed")
	}
	select {
	case <-worker.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("post-exec bootstrap failure did not retire")
	}
	if worker.forest == nil || worker.forest.result != nil || !worker.forest.exitFailed.Load() ||
		!errors.Is(worker.result, errUnavailable) || !launcher.quarantined {
		t.Fatal("successful forest cleanup hid unsuccessful bootstrap")
	}
	if next, err := launcher.Prepare(t.Context()); next != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("post-exec failed launcher renewed work")
	}
}
