//go:build linux && mintclaw_private_native_keyboard

package desktop

// Private qualification only: no ordinary input host, model permission or
// second replay ledger. A desktop channel and an action have separate lifetimes.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

const privateKeyboardFrameLimit = 64 * 1024

type privateKeyboardConfig struct {
	Window   WindowRequest
	Identity WindowIdentity
	Chords   [][]byte
	BudgetMS uint32
}

func (privateKeyboardConfig) String() string {
	return "private native keyboard (protected input omitted)"
}

func (privateKeyboardConfig) GoString() string {
	return "private native keyboard (protected input omitted)"
}

type privateKeyboardBootstrap struct {
	Socket string                `json:"socket"`
	Screen int                   `json:"screen"`
	Auth   [16]byte              `json:"auth"`
	Server privateKeyboardServer `json:"server"`
}

type privateKeyboardBinding struct {
	Window      uint32 `json:"window"`
	PID         uint32 `json:"pid"`
	LeaseWindow uint32 `json:"lease_window"`
	LeasePID    uint32 `json:"lease_pid"`
}

type privateKeyboardStart struct {
	Operation privateKeyboardOperation `json:"operation"`
	Binding   privateKeyboardBinding   `json:"binding"`
	// Go byte slices encode as base64; this native wire requires byte arrays.
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
	InputQuiescent        bool                     `json:"input_quiescent"`
	Resources             privateKeyboardResources `json:"resource_disposition"`
}

type privateKeyboardActionState uint8

const (
	privateKeyboardActionActive privateKeyboardActionState = iota
	privateKeyboardActionTerminal
	privateKeyboardActionClosed
)

// Neither an ended context nor a closed stream attests input quiescence.
// The trusted caller owns the lease, including after any UNKNOWN outcome.
type privateKeyboard struct {
	channel     *privateKeyboardChannel
	actionCtx   context.Context
	lease       *InputLease
	identity    WindowIdentity
	operation   privateKeyboardOperation
	state       privateKeyboardActionState // protected by channel.mu
	stopped     bool                       // protected by channel.mu, never reset
	watchDone   chan struct{}
	watchExit   chan struct{}
	watchOnce   sync.Once
	closeOnce   sync.Once
	closeErr    error
	pending     string // protocol fields below protected by channel.readMu
	expected    string
	ordinal     uint32
	edges       uint32
	keyboard    uint8
	receipt     *privateKeyboardReceipt
	terminalErr error
}

// No pair/focus/input authority is granted here. The original Go epoch must
// still pass allow AFTER the newly scoped native subscriptions are prepared.
func preparePrivateKeyboard(
	ctx context.Context, channel *privateKeyboardChannel, lease *InputLease, config privateKeyboardConfig,
) (*privateKeyboard, error) {
	if ctx.Err() != nil || channel == nil || config.BudgetMS == 0 || config.BudgetMS > 10000 ||
		len(config.Chords) == 0 || len(config.Chords) > 256 {
		return nil, computer.ErrInvalid
	}
	start := privateKeyboardStart{BudgetMS: config.BudgetMS}
	defer func() {
		for _, chord := range start.Chords {
			clear(chord)
		}
	}()
	var edges uint32
	for _, chord := range config.Chords {
		if len(chord) == 0 || len(chord) > 8 {
			return nil, computer.ErrInvalid
		}
		owned := make([]uint16, len(chord))
		start.Chords = append(start.Chords, owned)
		edges += uint32(2 * len(chord))
		for index, code := range chord {
			if code < 8 || bytes.IndexByte(chord[:index], code) >= 0 {
				return nil, computer.ErrInvalid
			}
			owned[index] = uint16(code)
		}
	}
	bootstrap, binding, err := privateKeyboardAuthority(lease, config.Window, config.Identity)
	if err != nil {
		return nil, err
	}
	defer clear(bootstrap.Auth[:])
	if privateKeyboardDesktopOf(bootstrap) != channel.desktop {
		return nil, computer.ErrStale
	}
	start.Binding = binding
	if _, err = rand.Read(start.Operation.Nonce[:]); err != nil ||
		start.Operation.Nonce == (privateKeyboardToken{}) {
		return nil, computer.ErrUnavailable
	}
	owner := &privateKeyboard{
		channel: channel, actionCtx: ctx, lease: lease, identity: config.Identity,
		watchDone: make(chan struct{}), watchExit: make(chan struct{}), expected: "prepared", edges: edges,
	}
	channel.mu.Lock()
	if channel.state == privateKeyboardChannelBusy {
		channel.mu.Unlock()
		return nil, computer.ErrBusy
	}
	if channel.state != privateKeyboardChannelReady || channel.ctx.Err() != nil ||
		channel.generation == 0 || channel.generation == ^uint64(0) {
		channel.mu.Unlock()
		return nil, computer.ErrUnavailable
	}
	start.Operation.Channel, start.Operation.Generation = channel.id, channel.generation
	channel.generation++ // before an uncertain write: never resend this generation
	owner.operation = start.Operation
	channel.current, channel.state = owner, privateKeyboardChannelBusy
	channel.mu.Unlock()
	if err = owner.send("start", start); err != nil {
		_ = owner.close()
		return nil, err
	}
	if _, err = owner.receive(); err != nil {
		_ = owner.close()
		return nil, err
	}
	// No input is granted while preparation is pending. Begin asynchronous
	// revocation after the subscription barrier, before any caller can Grant.
	owner.startWatcher()
	// A pre-grant cancellation may already have delivered a terminal, verified
	// no-input receipt. Return it to the caller instead of discarding evidence.
	return owner, nil
}

func privateKeyboardAuthority(
	lease *InputLease, request WindowRequest, identity WindowIdentity,
) (privateKeyboardBootstrap, privateKeyboardBinding, error) {
	var bootstrap privateKeyboardBootstrap
	var binding privateKeyboardBinding
	if lease == nil || lease.guard == nil || lease.backend == nil || identity.Generation == 0 ||
		lease.guard.Identity() != identity || request.PID != identity.PID ||
		request.WindowID != identity.WindowID {
		return bootstrap, binding, computer.ErrStale
	}
	native, ok := lease.backend.(*x11InputLease)
	guard, nativeGuard := lease.guard.backend.(*x11Window)
	endpoint, err := x11Display(request.Display)
	if !ok || !nativeGuard || err != nil || native.guard != guard || native.window <= 1 ||
		uint32(guard.window) != identity.WindowID {
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
	cookie, err := x11Cookie(request.XAuthority, endpoint.number)
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
		Window: identity.WindowID, PID: identity.PID,
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

func privateKeyboardDecode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return computer.ErrUnavailable
	}
	return nil
}

func (owner *privateKeyboard) startWatcher() {
	owner.watchOnce.Do(func() {
		go func() {
			defer close(owner.watchExit)
			select {
			case <-owner.actionCtx.Done():
				_ = owner.stop()
			case <-owner.lease.Done():
				_ = owner.stop()
			case <-owner.channel.done:
			case <-owner.watchDone:
			}
		}()
	})
}

func (owner *privateKeyboard) send(kind string, data any) error {
	channel := owner.channel
	channel.mu.Lock()
	if channel.current != owner || owner.state != privateKeyboardActionActive {
		channel.mu.Unlock()
		return computer.ErrStale
	}
	if channel.state != privateKeyboardChannelBusy {
		channel.mu.Unlock()
		return computer.ErrUnavailable
	}
	if owner.stopped && kind != "stop" {
		channel.mu.Unlock()
		return computer.ErrStale
	}
	err := channel.sendLocked(kind, data)
	channel.mu.Unlock()
	if err != nil {
		channel.fail()
	}
	return err
}

func (owner *privateKeyboard) current() bool {
	channel := owner.channel
	channel.mu.Lock()
	defer channel.mu.Unlock()
	return channel.current == owner && owner.state == privateKeyboardActionActive
}

// One action owns the reader until close. An old action cannot read a later
// generation's packet, even if its caller keeps a reference to this object.
func (owner *privateKeyboard) receive() (privateKeyboardStep, error) {
	var step privateKeyboardStep
	if !owner.current() {
		return step, computer.ErrStale
	}
	channel := owner.channel
	channel.readMu.Lock()
	defer channel.readMu.Unlock()
	if !owner.current() {
		return step, computer.ErrStale
	}
	packet, err := channel.readPacket()
	if err != nil {
		channel.fail()
		return step, err
	}
	defer clear(packet.Data)
	if packet.Kind == "quarantined" {
		var refusal struct {
			Operation privateKeyboardOperation `json:"operation"`
		}
		channel.mu.Lock()
		valid := owner.expected == "admitted" && owner.operation.Generation == 1 &&
			channel.resources == privateKeyboardNotCreated
		channel.mu.Unlock()
		if !valid || privateKeyboardDecode(packet.Data, &refusal) != nil || refusal.Operation != owner.operation {
			channel.fail()
			return step, computer.ErrUnavailable
		}
		channel.mu.Lock()
		owner.state = privateKeyboardActionTerminal
		channel.state = privateKeyboardChannelEnded
		channel.mu.Unlock()
		owner.terminalErr = computer.ErrBusy
		return step, computer.ErrBusy
	}
	if packet.Kind == "complete" {
		var wire struct {
			Operation             privateKeyboardOperation `json:"operation"`
			Acknowledged          *bool                    `json:"acknowledged"`
			Canceled              *bool                    `json:"canceled"`
			CancellationDelivered *bool                    `json:"cancellation_delivered"`
			InputQuiescent        *bool                    `json:"input_quiescent"`
			Resources             privateKeyboardResources `json:"resource_disposition"`
		}
		if privateKeyboardDecode(packet.Data, &wire) != nil || wire.Operation != owner.operation ||
			wire.Acknowledged == nil || wire.Canceled == nil || wire.CancellationDelivered == nil ||
			wire.InputQuiescent == nil {
			channel.fail()
			return step, computer.ErrUnavailable
		}
		receipt := privateKeyboardReceipt{
			Operation: wire.Operation, Acknowledged: *wire.Acknowledged, Canceled: *wire.Canceled,
			CancellationDelivered: *wire.CancellationDelivered, InputQuiescent: *wire.InputQuiescent,
			Resources: wire.Resources,
		}
		channel.mu.Lock()
		previous := channel.resources
		channel.mu.Unlock()
		valid := (!receipt.Acknowledged || owner.expected == "complete" && !receipt.Canceled) &&
			(!receipt.CancellationDelivered || receipt.Canceled)
		switch receipt.Resources {
		case privateKeyboardNotCreated:
			valid = valid && receipt.InputQuiescent && receipt.Canceled && !receipt.Acknowledged &&
				owner.keyboard == 0 && owner.ordinal == 0 && owner.operation.Generation == 1 &&
				previous == privateKeyboardNotCreated
		case privateKeyboardRetained:
			valid = valid && receipt.InputQuiescent &&
				(owner.expected != "prepared" || previous == privateKeyboardRetained)
		case privateKeyboardUnverified:
			valid = valid && !receipt.InputQuiescent
		default:
			valid = false
		}
		if !valid {
			channel.fail()
			return step, computer.ErrUnavailable
		}
		owner.receipt, owner.pending = &receipt, "complete"
		channel.mu.Lock()
		owner.state = privateKeyboardActionTerminal
		if channel.state == privateKeyboardChannelBusy {
			channel.resources = receipt.Resources
			if !receipt.InputQuiescent {
				channel.state = privateKeyboardChannelUnknown
			}
			if receipt.Resources == privateKeyboardNotCreated {
				channel.state = privateKeyboardChannelEnded
			}
		}
		channel.mu.Unlock()
		return step, nil
	}
	if owner.pending != "" || packet.Kind != owner.expected ||
		privateKeyboardDecode(packet.Data, &step) != nil || step.Operation != owner.operation {
		channel.fail()
		return step, computer.ErrUnavailable
	}
	valid := false
	switch packet.Kind {
	case "prepared", "dispatched":
		valid = step.Keyboard == nil && step.Ordinal == nil && step.Held == nil
	case "admitted":
		valid = step.Keyboard != nil && *step.Keyboard >= 2 && *step.Keyboard < 128 &&
			step.Ordinal == nil && step.Held == nil
		if valid {
			owner.keyboard = *step.Keyboard
			channel.mu.Lock()
			if channel.state == privateKeyboardChannelBusy {
				channel.resources = privateKeyboardRetained
			}
			channel.mu.Unlock()
		}
	case "edge":
		valid = step.Keyboard == nil && step.Ordinal != nil && *step.Ordinal == owner.ordinal &&
			owner.ordinal < owner.edges && step.Held != nil && *step.Held <= 8
	}
	if !valid {
		channel.fail()
		return privateKeyboardStep{}, computer.ErrUnavailable
	}
	owner.pending = packet.Kind
	return step, nil
}

// Only the already-retained original guard/generation/lease may authorize this
// prepared scope. Readiness and app effects remain independent caller gates.
func (owner *privateKeyboard) allow(ctx context.Context) error {
	if !owner.current() {
		return computer.ErrStale
	}
	channel := owner.channel
	channel.readMu.Lock()
	defer channel.readMu.Unlock()
	if !owner.current() {
		return computer.ErrStale
	}
	if owner.actionCtx.Err() != nil || owner.lease == nil || owner.lease.guard == nil ||
		owner.identity != owner.lease.guard.Identity() || owner.lease.Check(ctx) != nil {
		_ = owner.stop()
		return computer.ErrStale
	}
	var kind, next string
	switch owner.pending {
	case "prepared":
		kind, next = "grant", "admitted"
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
		owner.ordinal++ // never retry an uncertain edge
	}
	owner.pending, owner.expected = "", next
	return owner.send(kind, data)
}

func (owner *privateKeyboard) stop() error {
	channel := owner.channel
	channel.mu.Lock()
	if channel.current != owner || owner.state != privateKeyboardActionActive {
		channel.mu.Unlock()
		return computer.ErrStale
	}
	if channel.state != privateKeyboardChannelBusy {
		channel.mu.Unlock()
		return computer.ErrUnavailable
	}
	if owner.stopped {
		channel.mu.Unlock()
		return nil
	}
	owner.stopped = true
	err := channel.sendLocked("stop", struct {
		Operation privateKeyboardOperation `json:"operation"`
	}{owner.operation})
	channel.mu.Unlock()
	if err != nil {
		channel.fail()
	}
	return err
}

func (owner *privateKeyboard) close() error {
	owner.closeOnce.Do(func() {
		_ = owner.stop() // terminal/old actions never write
		close(owner.watchDone)
		owner.watchOnce.Do(func() { close(owner.watchExit) })
		<-owner.watchExit
		channel := owner.channel
		channel.readMu.Lock()
		verified := owner.receipt != nil && owner.receipt.InputQuiescent
		if !verified {
			owner.closeErr = computer.ErrUnavailable
			if owner.terminalErr != nil {
				owner.closeErr = owner.terminalErr
			}
		}
		channel.mu.Lock()
		owner.state = privateKeyboardActionClosed
		if channel.current == owner {
			channel.current = nil
			if verified && channel.state == privateKeyboardChannelBusy {
				channel.state = privateKeyboardChannelReady
			}
		}
		channel.mu.Unlock()
		channel.readMu.Unlock()
		if !verified && owner.terminalErr == nil {
			channel.fail()
		}
	})
	return owner.closeErr
}
