//go:build linux

package desktop

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

const (
	ownedX11StartupBudget = 4 * time.Second
	ownedX11StopGrace     = 350 * time.Millisecond
)

// OwnedX11Config is trusted policy for starting a new private Xvfb. The
// executable pin and dimensions are selected by the host.
type OwnedX11Config struct {
	Executable string
	SHA256     string
	Width      int
	Height     int
}

// OwnedX11Binding names the ready local socket and its original server.
// The authority path refers to a file in the display's private directory.
type OwnedX11Binding struct {
	Display   string
	Authority string
	ServerPID int32
	ServerUID uint32
}

// OwnedX11Display retains the child it started and the files for that display.
// Its lifetime is explicit. The startup context only bounds initialization.
type OwnedX11Display struct {
	binding  OwnedX11Binding
	dir      string
	done     chan struct{}
	mu       sync.Mutex
	pidfd    int
	stopping bool
	cleanup  error
}

// StartOwnedX11Display starts a digest-checked Xvfb and verifies its kernel
// identity without opening an X11 client. The keeper can establish its first
// recording epoch before an application connects.
func StartOwnedX11Display(ctx context.Context, config OwnedX11Config) (*OwnedX11Display, error) {
	if !filepath.IsAbs(config.Executable) || config.Width < 1 || config.Width > 4096 ||
		config.Height < 1 || config.Height > 4096 {
		return nil, computer.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, ownedX11StartupBudget)
	defer cancel()
	dir, err := os.MkdirTemp("", "mintclaw-owned-x11-")
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = os.RemoveAll(dir)
		}
	}()
	var cookie [16]byte
	defer clear(cookie[:])
	if _, randomErr := rand.Read(cookie[:]); randomErr != nil {
		return nil, computer.ErrUnavailable
	}
	authority := filepath.Join(dir, "authority")
	if authorityErr := writeOwnedX11Authority(authority, "", cookie[:]); authorityErr != nil {
		return nil, authorityErr
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	command, snapshot, err := newOwnedX11Command(config, dir, authority)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	command.ExtraFiles = append(command.ExtraFiles, writer)
	pidfd := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd, Pdeathsig: syscall.SIGKILL}
	if startupErr := ctx.Err(); startupErr != nil {
		return nil, startupErr
	}
	owner, err := isolation.StartParentBoundCommand(command)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	_ = writer.Close()
	if pidfd < 0 {
		// Our original child is still unreaped here.
		_ = command.Process.Kill()
		_ = owner.Wait()
		return nil, ErrUnsupported
	}
	display := &OwnedX11Display{
		dir: dir, done: make(chan struct{}), pidfd: pidfd,
		binding: OwnedX11Binding{
			Authority: authority, ServerPID: int32(command.Process.Pid), ServerUID: uint32(os.Geteuid()),
		},
	}
	handedOff = true
	go display.reap(owner)
	name, err := readOwnedX11Display(ctx, reader)
	if err == nil {
		err = writeOwnedX11Authority(authority, name[1:], cookie[:])
	}
	if err == nil {
		display.binding.Display = name
		_, err = display.Binding(ctx)
	}
	if err != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), ownedX11StartupBudget)
		defer stop()
		return nil, errors.Join(err, display.Close(stopCtx))
	}
	return display, nil
}

func newOwnedX11Command(config OwnedX11Config, dir, authority string) (*exec.Cmd, *os.File, error) {
	fd, err := unix.Open(config.Executable, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, computer.ErrDenied
	}
	file := os.NewFile(uintptr(fd), "configured Xvfb")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return nil, nil, computer.ErrDenied
	}
	screen := fmt.Sprintf("%dx%dx24", config.Width, config.Height)
	command, snapshot, err := isolation.NewSealedExecutableCommand(
		"mintclaw-owned-xvfb", file, config.SHA256, 64<<20,
		"-displayfd", "4", "-screen", "0", screen, "-nolisten", "tcp", "-noreset", "-auth", authority,
	)
	if errors.Is(err, isolation.ErrSealedExecutableIdentity) {
		return nil, nil, computer.ErrDenied
	}
	if err != nil {
		return nil, nil, computer.ErrUnavailable
	}
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + dir, "TMPDIR=" + dir}
	return command, snapshot, nil
}

func writeOwnedX11Authority(path, number string, cookie []byte) error {
	data := binary.BigEndian.AppendUint16(nil, 65535)
	for _, field := range [][]byte{nil, []byte(number), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		data = binary.BigEndian.AppendUint16(data, uint16(len(field)))
		data = append(data, field...)
	}
	defer clear(data)
	next := path + ".next"
	if err := os.WriteFile(next, data, 0o600); err != nil {
		return computer.ErrUnavailable
	}
	if err := os.Rename(next, path); err != nil {
		return computer.ErrUnavailable
	}
	return nil
}

func readOwnedX11Display(ctx context.Context, reader *os.File) (string, error) {
	deadline, _ := ctx.Deadline()
	if err := reader.SetReadDeadline(deadline); err != nil {
		return "", computer.ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = reader.SetReadDeadline(time.Now()) })
	defer stop()
	line, err := bufio.NewReaderSize(reader, 16).ReadSlice('\n')
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || len(line) > 6 {
		return "", computer.ErrUnavailable
	}
	number := string(line[:len(line)-1])
	display, err := strconv.ParseUint(number, 10, 16)
	if err != nil || strconv.FormatUint(display, 10) != number {
		return "", computer.ErrUnavailable
	}
	return ":" + number, nil
}

// Binding revalidates the original process and socket. It never adopts a
// replacement server that reuses the display locator.
func (display *OwnedX11Display) Binding(ctx context.Context) (OwnedX11Binding, error) {
	ctx, cancel := context.WithTimeout(ctx, ownedX11StartupBudget)
	defer cancel()
	if !display.running(ctx) {
		return OwnedX11Binding{}, computer.ErrStale
	}
	endpoint, err := x11Display(display.binding.Display)
	if err != nil {
		return OwnedX11Binding{}, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint.socket)
	if err != nil {
		return OwnedX11Binding{}, computer.ErrUnavailable
	}
	defer func() { _ = conn.Close() }()
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return OwnedX11Binding{}, computer.ErrUnavailable
	}
	peer, err := localX11Peer(socket)
	if err != nil || peer.Pid != display.binding.ServerPID || peer.Uid != display.binding.ServerUID ||
		!display.running(ctx) {
		return OwnedX11Binding{}, computer.ErrStale
	}
	return display.binding, nil
}

func (display *OwnedX11Display) running(ctx context.Context) bool {
	display.mu.Lock()
	defer display.mu.Unlock()
	return !display.stopping && display.pidfd >= 0 && pidfdAlive(ctx, display.pidfd, unix.Poll)
}

// Done closes when the original Xvfb has been reaped and cleanup attempted.
func (display *OwnedX11Display) Done() <-chan struct{} { return display.done }

// Close revokes access, stops only the retained process and waits for its
// retirement. A timed out wait can be retried against the same handle.
func (display *OwnedX11Display) Close(ctx context.Context) error {
	if err := display.signalStop(unix.SIGTERM); err != nil {
		return err
	}
	grace := time.NewTimer(ownedX11StopGrace)
	defer grace.Stop()
	select {
	case <-display.done:
		return display.cleanup
	case <-ctx.Done():
	case <-grace.C:
	}
	if err := display.signalStop(unix.SIGKILL); err != nil {
		return err
	}
	select {
	case <-display.done:
		return display.cleanup
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (display *OwnedX11Display) signalStop(signal unix.Signal) error {
	display.mu.Lock()
	defer display.mu.Unlock()
	display.stopping = true
	if display.pidfd < 0 {
		return nil
	}
	if err := unix.PidfdSendSignal(display.pidfd, signal, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return computer.ErrUnavailable
	}
	return nil
}

func (display *OwnedX11Display) reap(owner *isolation.ParentBoundCommand) {
	_ = owner.Wait()
	cleanup := os.RemoveAll(display.dir)
	display.mu.Lock()
	defer display.mu.Unlock()
	if err := unix.Close(display.pidfd); cleanup != nil || err != nil {
		display.cleanup = computer.ErrUnavailable
	}
	display.pidfd = -1
	close(display.done)
}
