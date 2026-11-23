//go:build linux

package companion

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

const (
	managedPointerStartup = 6 * time.Second
	managedPointerStop    = 4 * time.Second
	managedPointerFrame   = 512
)

// ManagedX11PointerConfig pins the display server and its private input worker.
// It is local operator configuration and does not represent a model grant.
type ManagedX11PointerConfig struct {
	Display          desktop.OwnedX11Config
	WorkerExecutable string
	WorkerSHA256     string
}

// ManagedX11Pointer owns the original X11 display and its original native
// recorder. An unverified native disconnect retires this display. A failed
// action is never replayed, and a replacement display is never opened here.
type ManagedX11Pointer struct {
	display *desktop.OwnedX11Display
	server  desktop.OwnedX11Binding
	command *exec.Cmd
	stdin   *os.File
	stdout  *os.File
	reader  *bufio.Reader
	gate    chan struct{}
	stopped chan struct{}
	done    chan struct{}
	stop    sync.Once
	err     error  // published by closing done
	seq     uint64 // held under gate
}

type x11PointerStartup struct {
	Ready           bool   `json:"ready"`
	ProtocolVersion uint32 `json:"protocol_version"`
	Context         uint32 `json:"context"`
	Master          uint16 `json:"master"`
	Source          uint8  `json:"source"`
	Name            string `json:"name"`
	KeeperPID       int    `json:"keeper_pid"`
	WorkerPID       int    `json:"worker_pid"`
}

func (ready x11PointerStartup) valid(pid int) bool {
	return ready.Ready && ready.ProtocolVersion == 1 && ready.Context > 1 && ready.Master > 1 && ready.Source > 1 &&
		ready.Master != uint16(ready.Source) && ready.Name != "" && ready.KeeperPID == pid &&
		ready.WorkerPID > 1 && ready.WorkerPID != pid
}

// StartManagedX11Pointer provisions a new display and starts the native
// recorder before returning authority to open application connections.
// The context bounds startup. Close terminates the owned display; a handoff
// can retain it by leaving this owner alive.
func StartManagedX11Pointer(ctx context.Context, config ManagedX11PointerConfig) (*ManagedX11Pointer, error) {
	ctx, cancel := context.WithTimeout(ctx, managedPointerStartup)
	defer cancel()
	command, sealed, err := newX11PointerCommand(config)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sealed.Close() }()
	display, err := desktop.StartOwnedX11Display(ctx, config.Display)
	if err != nil {
		return nil, err
	}
	epoch := &ManagedX11Pointer{
		display: display, command: command, gate: make(chan struct{}, 1),
		stopped: make(chan struct{}), done: make(chan struct{}),
	}
	if err = epoch.start(ctx); err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 2*managedPointerStop)
		defer stop()
		return nil, errors.Join(err, epoch.Close(cleanup))
	}
	return epoch, nil
}

func newX11PointerCommand(config ManagedX11PointerConfig) (*exec.Cmd, *os.File, error) {
	if !filepath.IsAbs(config.WorkerExecutable) {
		return nil, nil, computer.ErrInvalid
	}
	fd, err := unix.Open(config.WorkerExecutable, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, computer.ErrDenied
	}
	file := os.NewFile(uintptr(fd), "configured X11 input worker")
	defer func() { _ = file.Close() }()
	var info unix.Stat_t
	if statErr := unix.Fstat(fd, &info); statErr != nil || info.Mode&unix.S_IFMT != unix.S_IFREG ||
		info.Mode&0o022 != 0 || info.Mode&0o111 == 0 || info.Uid != 0 && info.Uid != uint32(os.Geteuid()) {
		return nil, nil, computer.ErrDenied
	}
	command, snapshot, err := isolation.NewSealedExecutableCommand(
		"mintclaw-x11-pointer", file, config.WorkerSHA256, 64<<20, "--owned-device-bootstrap",
	)
	if errors.Is(err, isolation.ErrSealedExecutableIdentity) {
		return nil, nil, computer.ErrDenied
	}
	if err != nil {
		return nil, nil, computer.ErrUnavailable
	}
	return command, snapshot, nil
}

func (epoch *ManagedX11Pointer) start(ctx context.Context) error {
	command := epoch.command
	pidfd := -1
	// Start exactly one supervisor on all paths. It also closes the display
	// when starting the private worker fails before a process exists.
	defer func() { go epoch.supervise(pidfd) }()
	server, err := epoch.display.Binding(ctx)
	if err != nil {
		return err
	}
	epoch.server = server
	parentOut, childOut, err := os.Pipe()
	if err != nil {
		return computer.ErrUnavailable
	}
	defer func() { _ = childOut.Close() }()
	epoch.stdout = parentOut
	childIn, parentIn, err := os.Pipe()
	if err != nil {
		return computer.ErrUnavailable
	}
	defer func() { _ = childIn.Close() }()
	epoch.stdin = parentIn
	epoch.reader = bufio.NewReaderSize(parentOut, managedPointerFrame)
	command.Stdin, command.Stdout = childIn, childOut
	command.Dir = filepath.Dir(server.Authority)
	command.Env = []string{
		"DISPLAY=" + server.Display, "XAUTHORITY=" + server.Authority,
		"MINTCLAW_PRIVATE_XVFB_PID=" + strconv.FormatInt(int64(server.ServerPID), 10),
		"HOME=" + command.Dir, "TMPDIR=" + command.Dir, "LC_ALL=C", "PATH=/usr/bin:/bin",
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, PidFD: &pidfd, Pdeathsig: syscall.SIGKILL}
	if startErr := ctx.Err(); startErr != nil {
		return startErr
	}
	if startErr := command.Start(); startErr != nil {
		return computer.ErrUnavailable
	}
	_ = childOut.Close()
	_ = childIn.Close()
	if pidfd < 0 {
		return desktop.ErrUnsupported
	}
	if err = epoch.waitReady(ctx, command.Process.Pid); err != nil {
		return err
	}
	if _, err = epoch.display.Binding(ctx); err != nil || !x11PointerLive(pidfd) {
		return computer.ErrStale
	}
	return nil
}

func (epoch *ManagedX11Pointer) waitReady(ctx context.Context, pid int) error {
	deadline, _ := ctx.Deadline()
	if err := epoch.stdout.SetReadDeadline(deadline); err != nil {
		return computer.ErrUnavailable
	}
	interrupt := make(chan struct{})
	detach := context.AfterFunc(ctx, func() {
		_ = epoch.stdout.SetReadDeadline(time.Now())
		close(interrupt)
	})
	defer func() {
		if !detach() {
			<-interrupt
		}
	}()
	var response x11PointerStartup
	err := epoch.readFrame(&response)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !response.valid(pid) {
		return computer.ErrUnavailable
	}
	return nil
}

func x11PointerLive(pidfd int) bool {
	fd := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(fd, 0)
		if !errors.Is(err, unix.EINTR) {
			return err == nil && count == 0 && fd[0].Revents == 0
		}
	}
}

func (epoch *ManagedX11Pointer) supervise(pidfd int) {
	if pidfd >= 0 {
		epoch.waitForLoss(pidfd)
	}
	epoch.revoke()
	// The original display remains retired even if the worker cannot return
	// a verified neutral state. Retrying never attaches to another desktop.
	cleanup, cancel := context.WithTimeout(context.Background(), managedPointerStop)
	displayErr := epoch.display.Close(cleanup)
	cancel()
	if pidfd >= 0 {
		_ = unix.Close(pidfd)
	}
	if epoch.command.Process != nil {
		// Keep the unreaped leader as the group ID ownership token until all
		// possible signals are finished. Never target its numeric ID after Wait.
		if drained := drainJobProcessGroup(epoch.command, true); drained.observationUnknown {
			epoch.err = computer.ErrUnavailable
		}
		var exit *exec.ExitError
		if err := epoch.command.Wait(); err != nil && !errors.As(err, &exit) {
			epoch.err = computer.ErrUnavailable
		}
	}
	epoch.gate <- struct{}{}
	if epoch.stdin != nil {
		_ = epoch.stdin.Close()
	}
	if epoch.stdout != nil {
		_ = epoch.stdout.Close()
	}
	<-epoch.gate
	if displayErr != nil {
		epoch.err = computer.ErrUnavailable
	}
	close(epoch.done)
}

func (epoch *ManagedX11Pointer) waitForLoss(pidfd int) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for x11PointerLive(pidfd) {
		select {
		case <-epoch.stopped:
			return
		case <-epoch.display.Done():
			return
		case <-ticker.C:
		}
	}
}

func (epoch *ManagedX11Pointer) revoke() { epoch.stop.Do(func() { close(epoch.stopped) }) }

// Binding revalidates the originally owned server and rejects retired epochs.
func (epoch *ManagedX11Pointer) Binding(ctx context.Context) (desktop.OwnedX11Binding, error) {
	binding, err := epoch.display.Binding(ctx)
	if err != nil {
		return desktop.OwnedX11Binding{}, err
	}
	select {
	case <-epoch.stopped:
		return desktop.OwnedX11Binding{}, computer.ErrStale
	default:
		return binding, nil
	}
}

// Done closes after cleanup of the original process and display is attempted.
// Close yields the resulting cleanup status.
func (epoch *ManagedX11Pointer) Done() <-chan struct{} { return epoch.done }

// Close revokes new native work immediately and waits for cleanup within ctx.
// Cleanup continues even if this caller stops waiting.
func (epoch *ManagedX11Pointer) Close(ctx context.Context) error {
	epoch.revoke()
	select {
	case <-epoch.done:
		return epoch.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// String omits the display authority and other private protocol details.
func (epoch *ManagedX11Pointer) String() string { return "managed X11 input lifetime" }
