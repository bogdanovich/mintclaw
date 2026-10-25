//go:build linux && mintclaw_private_native_keyboard

package desktop

// This private qualification adapter is absent from ordinary builds. It joins
// the native backend to an already-retained Go guard/lease; it is not an input
// host, a model grant, an application-effect oracle or a second replay ledger.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

const privateKeyboardFrameLimit = 64 * 1024

type privateKeyboardOperation [32]byte

func (operation *privateKeyboardOperation) UnmarshalJSON(raw []byte) error {
	var values []uint16
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &values) != nil || len(values) != len(operation) {
		return computer.ErrUnavailable
	}
	for index, value := range values {
		if value > 255 {
			return computer.ErrUnavailable
		}
		operation[index] = byte(value)
	}
	return nil
}

type privateKeyboardConfig struct {
	Executable string
	SHA256     string
	Window     WindowRequest
	Identity   WindowIdentity
	Chords     [][]byte
	BudgetMS   uint32
}

func (privateKeyboardConfig) String() string {
	return "private native keyboard (protected input omitted)"
}

func (privateKeyboardConfig) GoString() string {
	return "private native keyboard (protected input omitted)"
}

type privateKeyboardBootstrap struct {
	Socket string   `json:"socket"`
	Screen int      `json:"screen"`
	Auth   [16]byte `json:"auth"`
	Server struct {
		PID  int32  `json:"pid"`
		UID  uint32 `json:"uid"`
		Root uint32 `json:"root"`
	} `json:"server"`
}

type privateKeyboardBinding struct {
	Window      uint32 `json:"window"`
	PID         uint32 `json:"pid"`
	LeaseWindow uint32 `json:"lease_window"`
	LeasePID    uint32 `json:"lease_pid"`
}

type privateKeyboardStart struct {
	Operation privateKeyboardOperation `json:"operation"`
	Bootstrap privateKeyboardBootstrap `json:"bootstrap"`
	Binding   privateKeyboardBinding   `json:"binding"`
	// []byte is base64 in Go JSON, whereas Rust's bounded wire requires arrays.
	Chords   [][]uint16 `json:"chords"`
	BudgetMS uint32     `json:"budget_ms"`
}

type privateKeyboardReply struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type privateKeyboardStep struct {
	Operation privateKeyboardOperation `json:"operation"`
	Keyboard  *uint8                   `json:"keyboard,omitempty"`
	Ordinal   *uint32                  `json:"ordinal,omitempty"`
	Held      *uint8                   `json:"held,omitempty"`
}

type privateKeyboardReceipt struct {
	Operation             privateKeyboardOperation `json:"operation"`
	Acknowledged          bool                     `json:"acknowledged"`
	Canceled              bool                     `json:"canceled"`
	CancellationDelivered bool                     `json:"cancellation_delivered"`
	CleanupConfirmed      bool                     `json:"cleanup_confirmed"`
}

// The lease is deliberately NOT closed by this object's cleanup. The trusted
// owner must retain it on an unknown receipt, including keeper loss. A signal,
// canceled context or closed stream never supplies CleanupConfirmed.
type privateKeyboard struct {
	actionCtx context.Context
	lease     *InputLease
	identity  WindowIdentity
	operation privateKeyboardOperation
	wire      *net.UnixConn
	process   *exec.Cmd
	exited    chan error
	watchDone chan struct{}
	watchExit chan struct{}
	watchOnce sync.Once
	writeMu   sync.Mutex
	stopped   bool // protected by writeMu; never reset/rebound
	pending   string
	expected  string
	ordinal   uint32
	edges     uint32
	receipt   *privateKeyboardReceipt
	closeOnce sync.Once
	closeErr  error
}

// Preparation has no pair/focus/input authority. In particular, fresh native
// admission of matching numeric IDs cannot substitute for the original Go
// epoch: allow() checks that retained epoch AFTER native subscriptions exist.
func preparePrivateKeyboard(
	ctx context.Context, lease *InputLease, config privateKeyboardConfig,
) (*privateKeyboard, error) {
	if ctx.Err() != nil || config.BudgetMS == 0 || config.BudgetMS > 10000 ||
		len(config.Chords) == 0 || len(config.Chords) > 256 {
		return nil, computer.ErrInvalid
	}
	start := privateKeyboardStart{BudgetMS: config.BudgetMS}
	var edges uint32
	defer func() {
		clear(start.Bootstrap.Auth[:])
		for _, chord := range start.Chords {
			clear(chord)
		}
	}()
	for _, chord := range config.Chords {
		if len(chord) == 0 || len(chord) > 8 {
			return nil, computer.ErrInvalid
		}
		owned := make([]uint16, len(chord))
		edges += uint32(2 * len(chord))
		start.Chords = append(start.Chords, owned)
		for index, code := range chord {
			if code < 8 || bytes.IndexByte(chord[:index], code) >= 0 {
				return nil, computer.ErrInvalid
			}
			owned[index] = uint16(code)
		}
	}
	var err error
	start.Bootstrap, start.Binding, err = privateKeyboardAuthority(lease, config)
	if err != nil {
		return nil, err
	}
	if _, err = rand.Read(start.Operation[:]); err != nil || start.Operation == [32]byte{} {
		return nil, computer.ErrUnavailable
	}
	executable, err := openPrivateKeyboardExecutable(config.Executable, config.SHA256)
	if err != nil {
		return nil, err
	}
	defer func() { _ = executable.Close() }()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	parent, child := os.NewFile(uintptr(descriptors[0]), "private keyboard host"),
		os.NewFile(uintptr(descriptors[1]), "private keyboard child")
	defer func() { _ = parent.Close(); _ = child.Close() }()
	connection, err := net.FileConn(parent)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	// Execute the already-open attested inode, not a pathname resolved again.
	command := exec.Command("/proc/self/fd/3", "--keeper")
	command.ExtraFiles, command.Stdin = []*os.File{executable}, child
	command.Env = []string{"CUA_TELEMETRY=0", "DO_NOT_TRACK=1"}
	owner := &privateKeyboard{
		actionCtx: ctx, lease: lease, identity: config.Identity, operation: start.Operation,
		wire:    connection.(*net.UnixConn),
		process: command, exited: make(chan error, 1), watchDone: make(chan struct{}), watchExit: make(chan struct{}),
		expected: "prepared", edges: edges,
	}
	if err = command.Start(); err != nil {
		_ = connection.Close()
		return nil, computer.ErrUnavailable
	}
	go func() { owner.exited <- command.Wait() }()
	if err = owner.send("start", start); err != nil {
		_ = owner.close()
		return nil, computer.ErrUnavailable
	}
	if _, err = owner.receive(); err != nil || owner.pending != "prepared" {
		_ = owner.close()
		return nil, computer.ErrUnavailable
	}
	return owner, nil
}

func privateKeyboardAuthority(
	lease *InputLease, config privateKeyboardConfig,
) (privateKeyboardBootstrap, privateKeyboardBinding, error) {
	var bootstrap privateKeyboardBootstrap
	var binding privateKeyboardBinding
	if lease == nil || lease.guard == nil || lease.backend == nil || config.Identity.Generation == 0 ||
		lease.guard.Identity() != config.Identity || config.Window.PID != config.Identity.PID ||
		config.Window.WindowID != config.Identity.WindowID {
		return bootstrap, binding, computer.ErrStale
	}
	native, ok := lease.backend.(*x11InputLease)
	guard, nativeGuard := lease.guard.backend.(*x11Window)
	endpoint, err := x11Display(config.Window.Display)
	if !ok || !nativeGuard || err != nil || native.guard != guard || native.window <= 1 ||
		uint32(guard.window) != config.Identity.WindowID {
		return bootstrap, binding, computer.ErrDenied
	}
	transport, ok := guard.socket.(*x11ObservedTransport)
	if !ok {
		return bootstrap, binding, computer.ErrDenied
	}
	socket, ok := transport.Conn.(*net.UnixConn)
	if !ok || socket.RemoteAddr().String() != endpoint.socket {
		return bootstrap, binding, computer.ErrDenied
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return bootstrap, binding, computer.ErrUnavailable
	}
	var peer *unix.Ucred
	var peerErr error
	if err = raw.Control(
		func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) },
	); err != nil || peerErr != nil || peer == nil || peer.Pid <= 0 ||
		peer.Uid != uint32(os.Getuid()) {
		return bootstrap, binding, computer.ErrDenied
	}
	roots := xproto.Setup(guard.connection).Roots
	if endpoint.screen >= len(roots) || roots[endpoint.screen].Root != guard.root {
		return bootstrap, binding, computer.ErrDenied
	}
	cookie, err := x11Cookie(config.Window.XAuthority, endpoint.number)
	if err != nil {
		return bootstrap, binding, computer.ErrDenied
	}
	auth, err := hex.DecodeString(cookie)
	defer clear(auth)
	if err != nil || len(auth) != len(bootstrap.Auth) {
		return bootstrap, binding, computer.ErrDenied
	}
	bootstrap.Socket, bootstrap.Screen = endpoint.socket, endpoint.screen
	copy(bootstrap.Auth[:], auth)
	bootstrap.Server.PID, bootstrap.Server.UID, bootstrap.Server.Root = peer.Pid, peer.Uid, uint32(guard.root)
	binding = privateKeyboardBinding{
		Window: config.Identity.WindowID, PID: config.Identity.PID,
		LeaseWindow: uint32(native.window), LeasePID: uint32(os.Getpid()),
	}
	return bootstrap, binding, nil
}

func openPrivateKeyboardExecutable(path, digest string) (*os.File, error) {
	expected, err := hex.DecodeString(digest)
	if !filepath.IsAbs(path) || err != nil || len(expected) != sha256.Size {
		return nil, computer.ErrInvalid
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, computer.ErrDenied
	}
	file := os.NewFile(uintptr(fd), "private keyboard executable")
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0o022 != 0 ||
		info.Mode().Perm()&0o111 == 0 || info.Size() == 0 || info.Size() > 64*1024*1024 {
		return nil, computer.ErrDenied
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return nil, computer.ErrDenied
	}
	hash := sha256.New()
	if _, err = io.Copy(
		hash,
		io.LimitReader(file, 64*1024*1024+1),
	); err != nil ||
		!bytes.Equal(hash.Sum(nil), expected) {
		return nil, computer.ErrDenied
	}
	valid = true
	return file, nil
}

func (owner *privateKeyboard) send(kind string, data any) error {
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	if owner.stopped && kind != "stop" {
		return computer.ErrStale
	}
	encoded, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Data any    `json:"data"`
	}{kind, data})
	defer clear(encoded)
	if err != nil || len(encoded) == 0 || len(encoded) > privateKeyboardFrameLimit {
		return computer.ErrInvalid
	}
	if err = owner.wire.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return computer.ErrUnavailable
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	for _, part := range [][]byte{header[:], encoded} {
		for len(part) > 0 {
			n, writeErr := owner.wire.Write(part)
			if writeErr != nil || n == 0 {
				return computer.ErrUnavailable
			}
			part = part[n:]
		}
	}
	return nil
}

func privateKeyboardDecode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return computer.ErrUnavailable
	}
	return nil
}

// One reader owns this conversation. Stop uses the independently serialized
// writer and immutable operation, so an old stop cannot address a newer action.
func (owner *privateKeyboard) receive() (privateKeyboardStep, error) {
	var step privateKeyboardStep
	if owner.receipt != nil || owner.wire.SetReadDeadline(time.Now().Add(3*time.Second)) != nil {
		return step, computer.ErrUnavailable
	}
	var header [4]byte
	if _, err := io.ReadFull(owner.wire, header[:]); err != nil {
		return step, computer.ErrUnavailable
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > privateKeyboardFrameLimit {
		return step, computer.ErrUnavailable
	}
	encoded := make([]byte, length)
	defer clear(encoded)
	if _, err := io.ReadFull(owner.wire, encoded); err != nil {
		return step, computer.ErrUnavailable
	}
	var packet privateKeyboardReply
	if privateKeyboardDecode(encoded, &packet) != nil {
		return step, computer.ErrUnavailable
	}
	defer clear(packet.Data)
	if packet.Kind == "complete" {
		var wire struct {
			Operation             privateKeyboardOperation `json:"operation"`
			Acknowledged          *bool                    `json:"acknowledged"`
			Canceled              *bool                    `json:"canceled"`
			CancellationDelivered *bool                    `json:"cancellation_delivered"`
			CleanupConfirmed      *bool                    `json:"cleanup_confirmed"`
		}
		if privateKeyboardDecode(packet.Data, &wire) != nil || wire.Operation != owner.operation ||
			wire.Acknowledged == nil || wire.Canceled == nil || wire.CancellationDelivered == nil ||
			wire.CleanupConfirmed == nil {
			return step, computer.ErrUnavailable
		}
		receipt := privateKeyboardReceipt{
			Operation: wire.Operation, Acknowledged: *wire.Acknowledged, Canceled: *wire.Canceled,
			CancellationDelivered: *wire.CancellationDelivered, CleanupConfirmed: *wire.CleanupConfirmed,
		}
		if receipt.Acknowledged && (owner.expected != "complete" || receipt.Canceled) ||
			receipt.CancellationDelivered && !receipt.Canceled {
			return step, computer.ErrUnavailable
		}
		owner.receipt, owner.pending = &receipt, "complete"
		return step, nil
	}
	if owner.pending != "" || packet.Kind != owner.expected ||
		privateKeyboardDecode(packet.Data, &step) != nil || step.Operation != owner.operation {
		return step, computer.ErrUnavailable
	}
	valid := false
	switch packet.Kind {
	case "prepared", "dispatched":
		valid = step.Keyboard == nil && step.Ordinal == nil && step.Held == nil
	case "admitted":
		valid = step.Keyboard != nil && *step.Keyboard >= 2 && *step.Keyboard < 128 &&
			step.Ordinal == nil && step.Held == nil
	case "edge":
		valid = step.Keyboard == nil && step.Ordinal != nil && *step.Ordinal == owner.ordinal &&
			owner.ordinal < owner.edges && step.Held != nil && *step.Held <= 8
	}
	if !valid {
		return privateKeyboardStep{}, computer.ErrUnavailable
	}
	owner.pending = packet.Kind
	return step, nil
}

// The native subscription is already live at Prepared. Only THIS retained
// guard/generation/lease may now authorize focus, readiness or an input edge.
// Application readiness/effect must be established separately before allowing
// Admitted/Dispatched. Calling allow does not itself attest either one.
func (owner *privateKeyboard) allow(ctx context.Context) error {
	if owner.actionCtx.Err() != nil || owner.lease == nil || owner.lease.guard == nil ||
		owner.identity != owner.lease.guard.Identity() ||
		owner.lease.Check(ctx) != nil {
		_ = owner.stop()
		return computer.ErrStale
	}
	var kind, next string
	switch owner.pending {
	case "prepared":
		kind, next = "grant", "admitted"
		owner.watchOnce.Do(func() {
			go func() {
				defer close(owner.watchExit)
				select {
				case <-owner.actionCtx.Done():
					_ = owner.stop()
				case <-owner.lease.Done():
					_ = owner.stop()
				case <-owner.watchDone:
				}
			}()
		})
	case "admitted":
		kind, next = "ready", "edge"
	case "edge":
		kind, next = "continue", "edge"
		if owner.ordinal+1 == owner.edges {
			next = "dispatched"
		}
	case "dispatched":
		kind, next = "finish", "complete"
	default:
		return computer.ErrStale
	}
	data := struct {
		Operation privateKeyboardOperation `json:"operation"`
		Ordinal   *uint32                  `json:"ordinal,omitempty"`
	}{Operation: owner.operation}
	if kind == "continue" {
		ordinal := owner.ordinal
		data.Ordinal = &ordinal
		owner.ordinal++ // before an uncertain write: this edge is not retryable
	}
	owner.pending = ""
	owner.expected = next
	return owner.send(kind, data)
}

func (owner *privateKeyboard) stop() error {
	owner.writeMu.Lock()
	owner.stopped = true
	owner.writeMu.Unlock()
	return owner.send("stop", struct {
		Operation privateKeyboardOperation `json:"operation"`
	}{owner.operation})
}

func (owner *privateKeyboard) close() error {
	owner.closeOnce.Do(func() {
		// A caller that abandons the protocol cannot fabricate cleanup evidence.
		// Closing the original stream tells the keeper to stop independently.
		if owner.receipt == nil {
			_ = owner.stop()
			owner.closeErr = computer.ErrUnavailable
		}
		close(owner.watchDone)
		owner.watchOnce.Do(func() { close(owner.watchExit) })
		<-owner.watchExit
		_ = owner.wire.Close()
		select {
		case err := <-owner.exited:
			if err != nil {
				owner.closeErr = computer.ErrUnavailable
			}
		case <-time.After(time.Second):
			// Only this retained os.Process, never a supplied/reused PID. Losing
			// the keeper still does NOT prove cleanup or relinquish the lease.
			_ = owner.process.Process.Kill()
			owner.closeErr = computer.ErrUnavailable
		}
		if owner.receipt == nil || !owner.receipt.CleanupConfirmed {
			owner.closeErr = computer.ErrUnavailable
		}
	})
	return owner.closeErr
}
