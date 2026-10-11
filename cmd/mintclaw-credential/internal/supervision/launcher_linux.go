//go:build linux

package supervision

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/runtimeimage"
)

const (
	launcherLockPath = "/run/mintclaw-credential-supervisor/lock"
	bootstrapPath    = "bin/mintclaw-credential"
)

type runtimeMount interface {
	BorrowRoot(context.Context) (*os.File, error)
	Close() error
}

// Launcher owns the fixed Root launch resources. Only protected Root startup
// can use this API. It is not a grant, broker transport, or model interface.
type Launcher struct {
	mu                        sync.Mutex
	image                     runtimeMount
	supervisor                *Supervisor
	nsenter, bubblewrap, lock *os.File
	uid, gid                  uint32
	active                    *Worker
	closed, quarantined       bool
}

// Worker owns one allocation, including its original context and all Root
// descriptors. Exported endpoint copies belong to the protected broker; that
// broker must retire its proxy and sockets on the same lease/caller lifetime.
type Worker struct {
	mu                                        sync.Mutex
	launcher                                  *Launcher
	ctx                                       context.Context
	cancel                                    context.CancelFunc
	network                                   *Network
	namespace, image, listener, broker, child *os.File
	forest                                    *Forest
	exported, started, retired                bool
	once                                      sync.Once
	done                                      chan struct{}
	result                                    error
}

// OpenLauncher requires operator-installed dedicated system roles, an admitted
// runtime mount, protected helpers/lock and an empty cgroup parent. It never
// creates accounts, installs software, opens a token or enables an agent tool.
func OpenLauncher(ctx context.Context) (result *Launcher, resultErr error) {
	if !root() || ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	uid, gid, err := browserRole()
	if err != nil || unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil ||
		unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil {
		return nil, errUnavailable
	}
	launcher := &Launcher{uid: uid, gid: gid}
	defer func() {
		if resultErr != nil {
			_ = launcher.Close(context.Background())
		}
	}()
	launcher.lock, err = protectedLauncherFile(launcherLockPath, false)
	if err != nil || unix.Flock(int(launcher.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, errUnavailable
	}
	launcher.nsenter, err = protectedLauncherFile("/usr/bin/nsenter", true)
	if err != nil {
		return nil, errUnavailable
	}
	launcher.bubblewrap, err = protectedLauncherFile("/usr/bin/bwrap", true)
	if err != nil {
		return nil, errUnavailable
	}
	launcher.image, err = runtimeimage.Open(ctx)
	if err != nil {
		return nil, errUnavailable
	}
	launcher.supervisor, err = Open()
	if err != nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	return launcher, nil
}

func browserRole() (uint32, uint32, error) {
	broker, err := user.Lookup("mintclaw-credential")
	if err != nil {
		return 0, 0, errUnavailable
	}
	browser, err := user.Lookup("mintclaw-credential-browser")
	if err != nil {
		return 0, 0, errUnavailable
	}
	values := make([]uint32, 4)
	for index, value := range []string{broker.Uid, broker.Gid, browser.Uid, browser.Gid} {
		number, parseErr := strconv.ParseUint(value, 10, 32)
		if parseErr != nil || number == 0 || number == uint64(^uint32(0)) || strconv.FormatUint(number, 10) != value {
			return 0, 0, errUnavailable
		}
		values[index] = uint32(number)
	}
	if values[0] == values[2] || values[1] == values[3] {
		return 0, 0, errUnavailable
	}
	return values[2], values[3], nil
}

// Prepare creates no process. The broker must adopt these endpoints and attach
// the enforcing proxy to its actual original lease before Root calls Start.
// The 20-hour resource ceiling is an upper bound, never approval authority.
func (launcher *Launcher) Prepare(ctx context.Context) (*Worker, error) {
	if launcher == nil || !root() || ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if launcher.closed || launcher.quarantined || launcher.active != nil || launcher.image == nil ||
		launcher.supervisor == nil || launcher.nsenter == nil || launcher.bubblewrap == nil {
		return nil, errUnavailable
	}
	lifetime, cancel := context.WithTimeout(ctx, 20*time.Hour)
	worker := &Worker{launcher: launcher, ctx: lifetime, cancel: cancel, done: make(chan struct{})}
	var err error
	worker.network, err = OpenNetwork(lifetime)
	if err == nil {
		worker.namespace, worker.listener, err = worker.network.Borrow(lifetime)
	}
	if err == nil {
		worker.image, err = launcher.image.BorrowRoot(lifetime)
	}
	if err == nil {
		err = qualifyLaunchMount(worker.image)
	}
	if err == nil {
		var endpoints [2]int
		endpoints, err = unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			worker.broker = os.NewFile(uintptr(endpoints[0]), "private credential control")
			worker.child = os.NewFile(uintptr(endpoints[1]), "private credential worker control")
		}
	}
	if err != nil || lifetime.Err() != nil {
		cancel()
		if worker.closeFiles() != nil {
			launcher.quarantined = true
		}
		return nil, errUnavailable
	}
	launcher.active = worker
	go func() { <-lifetime.Done(); worker.retire() }()
	return worker, nil
}

// Endpoints exports exactly one pair: the private-namespace TCP listener and
// private control socket. It never exports a namespace, runtime, process or
// command handle. No secret-bearing operation is enabled by this resource API.
func (worker *Worker) Endpoints(ctx context.Context) (*os.File, *os.File, error) {
	if worker == nil || !root() || ctx == nil || ctx.Err() != nil {
		return nil, nil, errUnavailable
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.retired || worker.exported || worker.ctx.Err() != nil {
		return nil, nil, errUnavailable
	}
	listener, err := duplicateLaunchFile(worker.listener)
	if err != nil {
		return nil, nil, errUnavailable
	}
	control, err := duplicateLaunchFile(worker.broker)
	if err != nil || ctx.Err() != nil || worker.ctx.Err() != nil {
		_ = listener.Close()
		if control != nil {
			_ = control.Close()
		}
		return nil, nil, errUnavailable
	}
	worker.exported = true
	return listener, control, nil
}

func duplicateLaunchFile(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, errUnavailable
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private credential startup descriptor"), nil
}

// Start constructs only the fixed, qualified program. URLs, proxy pins, login
// data and flow configuration travel later over the inherited private channel.
func (worker *Worker) Start(ctx context.Context) error {
	if worker == nil || !root() || ctx == nil || ctx.Err() != nil {
		return errUnavailable
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.launcher.mu.Lock()
	closed := worker.launcher.closed
	worker.launcher.mu.Unlock()
	if closed || worker.retired || worker.started || !worker.exported || worker.ctx.Err() != nil {
		return errUnavailable
	}
	command := worker.command()
	forest, err := worker.launcher.supervisor.Start(worker.ctx, command)
	if err != nil {
		// Start can have begun asynchronous forest retirement without returning
		// its handle. Withdraw the whole launcher rather than admit another
		// allocation before that uncertain transition has been verified.
		worker.result = errUnavailable
		worker.launcher.mu.Lock()
		worker.launcher.quarantined = true
		worker.launcher.mu.Unlock()
		worker.cancel()
		return errUnavailable
	}
	worker.forest, worker.started = forest, true
	go func() { <-forest.done; worker.retire() }()
	if ctx.Err() != nil || worker.ctx.Err() != nil {
		worker.cancel()
		return errUnavailable
	}
	return nil
}

func (worker *Worker) command() *exec.Cmd {
	launcher := worker.launcher
	arguments := []string{
		"/usr/bin/nsenter", "--net=/proc/self/fd/4", "--", "/proc/self/fd/7",
		"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup",
		"--ro-bind", "/proc/self/fd/5", "/", "--proc", "/proc", "--dev", "/dev",
		"--perms", "1777", "--tmpfs", "/tmp", "--chdir", "/", "--clearenv",
		"--setenv", "PATH", "/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8", "--setenv", "TZ", "UTC",
		"--setenv", "HOME", "/tmp", "--setenv", "TMPDIR", "/tmp",
		"--cap-drop", "ALL", "--cap-add", "CAP_SETUID", "--cap-add", "CAP_SETGID", "--cap-add", "CAP_SETPCAP",
		"--", "/bin/setpriv", "--reuid", strconv.FormatUint(uint64(launcher.uid), 10),
		"--regid", strconv.FormatUint(uint64(launcher.gid), 10), "--clear-groups", "--inh-caps=-all",
		"--ambient-caps=-all", "--bounding-set=-all", "--no-new-privs", "--",
		"/" + bootstrapPath, "bootstrap-browser",
	}
	return &exec.Cmd{
		Path: "/proc/self/fd/6", Args: arguments,
		Env:        []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TZ=UTC", "HOME=/tmp", "TMPDIR=/tmp"},
		ExtraFiles: []*os.File{worker.child, worker.namespace, worker.image, launcher.nsenter, launcher.bubblewrap},
	}
}

func (worker *Worker) retire() {
	worker.once.Do(func() {
		worker.mu.Lock()
		worker.retired = true
		worker.cancel()
		worker.mu.Unlock()
		go func() {
			worker.mu.Lock()
			if worker.forest != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				worker.result = worker.forest.Retire(ctx)
				cancel()
				if worker.forest.exitFailed.Load() {
					worker.result = errUnavailable
				}
			} else if worker.result != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = worker.launcher.supervisor.Close(ctx)
				cancel()
			}
			if worker.closeFiles() != nil {
				worker.result = errUnavailable
			}
			worker.mu.Unlock()
			worker.launcher.mu.Lock()
			if worker.result != nil {
				worker.launcher.quarantined = true
			} else {
				worker.launcher.active = nil
			}
			worker.launcher.mu.Unlock()
			close(worker.done)
		}()
	})
}

func (worker *Worker) closeFiles() error {
	failed := false
	for _, file := range []*os.File{worker.child, worker.broker, worker.listener, worker.namespace, worker.image} {
		if file != nil && file.Close() != nil {
			failed = true
		}
	}
	worker.child, worker.broker, worker.listener, worker.namespace, worker.image = nil, nil, nil, nil, nil
	if worker.network.Close() != nil {
		failed = true
	}
	worker.network = nil
	if failed {
		return errUnavailable
	}
	return nil
}

func (worker *Worker) Retire(ctx context.Context) error {
	if worker == nil || !root() || ctx == nil {
		return errUnavailable
	}
	worker.retire()
	select {
	case <-worker.done:
		if ctx.Err() == nil {
			return worker.result
		}
	case <-ctx.Done():
	}
	return errUnavailable
}

func (worker *Worker) Done() <-chan struct{} {
	if worker == nil {
		return nil
	}
	return worker.done
}

func (launcher *Launcher) Close(ctx context.Context) error {
	if launcher == nil || !root() || ctx == nil {
		return errUnavailable
	}
	launcher.mu.Lock()
	launcher.closed = true
	active := launcher.active
	launcher.mu.Unlock()
	retirementFailed := false
	if active != nil && active.Retire(ctx) != nil {
		select {
		case <-active.done:
			retirementFailed = true
		default:
			// Keep the singleton/resource pins until bounded cleanup has
			// finished; a canceled caller may try Close again afterwards.
			return errUnavailable
		}
	}
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	failed := retirementFailed || launcher.quarantined || ctx.Err() != nil
	if launcher.supervisor != nil {
		failed = launcher.supervisor.Close(ctx) != nil || failed
		launcher.supervisor = nil
	}
	if launcher.image != nil {
		failed = launcher.image.Close() != nil || failed
		launcher.image = nil
	}
	for _, file := range []*os.File{launcher.nsenter, launcher.bubblewrap, launcher.lock} {
		if file != nil && file.Close() != nil {
			failed = true
		}
	}
	launcher.nsenter, launcher.bubblewrap, launcher.lock = nil, nil, nil
	if failed {
		return errUnavailable
	}
	return nil
}
