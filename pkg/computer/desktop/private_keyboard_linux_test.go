//go:build linux && mintclaw_private_native_keyboard

package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func privateKeyboardWireFixture(t *testing.T) (*privateKeyboard, *net.UnixConn) {
	t.Helper()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, descriptor := range descriptors {
		file := os.NewFile(uintptr(descriptor), "owned private wire fixture")
		connection, fileErr := net.FileConn(file)
		_ = file.Close()
		if fileErr != nil {
			t.Fatal(fileErr)
		}
		connections = append(connections, connection.(*net.UnixConn))
		t.Cleanup(func() { _ = connection.Close() })
	}
	return &privateKeyboard{
		operation: [32]byte{37},
		wire:      connections[0],
		expected:  "prepared",
		edges:     2,
	}, connections[1]
}

func privateKeyboardWriteFixture(t *testing.T, peer *net.UnixConn, kind string, data any) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"kind": kind, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	for _, part := range [][]byte{header[:], encoded} {
		if _, err = peer.Write(part); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrivateKeyboardRefusesForeignOutOfOrderAndIncompleteReceipts(t *testing.T) {
	for _, variant := range []string{"foreign", "early_ack", "missing_cleanup", "missing_canceled", "next_phase"} {
		t.Run(variant, func(t *testing.T) {
			owner, peer := privateKeyboardWireFixture(t)
			data := map[string]any{
				"operation": owner.operation, "acknowledged": false, "canceled": true,
				"cancellation_delivered": true, "cleanup_confirmed": true,
			}
			kind := "complete"
			switch variant {
			case "foreign":
				data["operation"] = [32]byte{38}
			case "early_ack":
				data["acknowledged"], data["canceled"], data["cancellation_delivered"] = true, false, false
			case "missing_cleanup":
				delete(data, "cleanup_confirmed")
			case "missing_canceled":
				delete(data, "canceled")
			case "next_phase":
				kind, data = "dispatched", map[string]any{"operation": owner.operation}
			}
			privateKeyboardWriteFixture(t, peer, kind, data)
			if _, err := owner.receive(); err == nil || owner.receipt != nil {
				t.Fatal("invalid native metadata manufactured a current receipt")
			}
		})
	}
}

func TestPrivateKeyboardStopReceiptIsSeparateFromActionAcknowledgement(t *testing.T) {
	owner, peer := privateKeyboardWireFixture(t)
	privateKeyboardWriteFixture(t, peer, "complete", privateKeyboardReceipt{
		Operation: owner.operation, Canceled: true, CancellationDelivered: true, CleanupConfirmed: true,
	})
	if _, err := owner.receive(); err != nil || owner.receipt == nil || owner.receipt.Acknowledged ||
		!owner.receipt.CleanupConfirmed {
		t.Fatal("independent stop cleanup was confused with action completion")
	}
	if _, err := owner.receive(); err == nil {
		t.Fatal("terminal native outcome was reused")
	}
}

func TestPrivateKeyboardFrameLengthIsBoundedBeforeReadingPayload(t *testing.T) {
	for _, length := range []uint32{0, privateKeyboardFrameLimit + 1, ^uint32(0)} {
		owner, peer := privateKeyboardWireFixture(t)
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], length)
		if _, err := peer.Write(header[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.receive(); err == nil {
			t.Fatal("invalid frame length admitted")
		}
	}
}

func TestPrivateKeyboardExecutableRequiresPinnedOwnedNonwritableInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-private-artifact")
	data := []byte("private executable attestation fixture; never executed")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	encoded := hex.EncodeToString(digest[:])
	file, err := openPrivateKeyboardExecutable(path, encoded)
	if err != nil {
		t.Fatal("exact private inode denied", err)
	}
	_ = file.Close()
	wrong := digest
	wrong[0] ^= 1
	if other, otherErr := openPrivateKeyboardExecutable(
		path,
		hex.EncodeToString(wrong[:]),
	); other != nil ||
		otherErr == nil {
		t.Fatal("different digest was admitted")
	}
	link := filepath.Join(filepath.Dir(path), "symlink")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if other, otherErr := openPrivateKeyboardExecutable(link, encoded); other != nil || otherErr == nil {
		t.Fatal("artifact pathname symlink was followed")
	}
	if err = os.Chmod(path, 0o720); err != nil {
		t.Fatal(err)
	}
	if other, otherErr := openPrivateKeyboardExecutable(path, encoded); other != nil || otherErr == nil {
		t.Fatal("group-writable executable was admitted")
	}
}

func TestPrivateKeyboardOperationHasExactWireWidthAndByteRange(t *testing.T) {
	for _, value := range []any{[]uint16{37}, make([]uint16, 33), "not an operation", nil} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var operation privateKeyboardOperation
		if json.Unmarshal(encoded, &operation) == nil {
			t.Fatal("non-exact native operation identity admitted")
		}
	}
	values := make([]uint16, 32)
	values[0] = 256
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	var operation privateKeyboardOperation
	if json.Unmarshal(encoded, &operation) == nil {
		t.Fatal("out-of-byte-range operation identity admitted")
	}
}

type privateKeyboardFixture struct {
	connection *xgb.Conn
	root       xproto.Window
	window     xproto.Window
	guard      *WindowGuard
	other      *WindowGuard
	lease      *InputLease
	config     privateKeyboardConfig
}

func newPrivateKeyboardFixture(t *testing.T) privateKeyboardFixture {
	t.Helper()
	if os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_TEST") != "1" {
		t.Skip("explicit owned private native-host qualification required")
	}
	connection, window, guard, other := nativeInputLeaseFixture(t)
	lease, err := guard.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if err = xproto.ChangeWindowAttributesChecked(connection, window, xproto.CwEventMask,
		[]uint32{xproto.EventMaskKeyPress | xproto.EventMaskKeyRelease}).Check(); err != nil {
		t.Fatal(err)
	}
	setup := xproto.Setup(connection)
	mapping, err := xproto.GetKeyboardMapping(connection, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).
		Reply()
	if err != nil || mapping == nil || mapping.KeysymsPerKeycode == 0 {
		t.Fatal("fixture keymap unavailable")
	}
	var key byte
	for index := 0; index < len(mapping.Keysyms); index += int(mapping.KeysymsPerKeycode) {
		if mapping.Keysyms[index] == 'a' {
			key = byte(setup.MinKeycode) + byte(index/int(mapping.KeysymsPerKeycode))
			break
		}
	}
	if key < 8 {
		t.Fatal("fixture map lacks its explicit ASCII key")
	}
	if err = xproto.SetInputFocusChecked(connection, xproto.InputFocusNone,
		xproto.Window(other.Identity().WindowID), xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	return privateKeyboardFixture{
		connection: connection, root: setup.Roots[0].Root, window: window, guard: guard, other: other, lease: lease,
		config: privateKeyboardConfig{
			Executable: os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER"),
			SHA256:     os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256"),
			Window: nativeWindowRequest(
				window,
			),
			Identity: guard.Identity(),
			Chords:   [][]byte{{key}},
			BudgetMS: 10000,
		},
	}
}

func (fixture privateKeyboardFixture) prepare(t *testing.T, ctx context.Context) *privateKeyboard {
	t.Helper()
	owner, err := preparePrivateKeyboard(ctx, fixture.lease, fixture.config)
	if err != nil {
		t.Fatal("private worker preparation failed", err)
	}
	t.Cleanup(func() { _ = owner.close() })
	if owner.pending != "prepared" {
		t.Fatal("native subscriptions were not prepared")
	}
	return owner
}

func privateKeyboardReceive(t *testing.T, owner *privateKeyboard, kind string) privateKeyboardStep {
	t.Helper()
	step, err := owner.receive()
	if err != nil || owner.pending != kind {
		t.Fatal("unexpected native host phase", kind, owner.pending, err)
	}
	return step
}

func privateKeyboardAllow(t *testing.T, owner *privateKeyboard) {
	t.Helper()
	if err := owner.allow(t.Context()); err != nil {
		t.Fatal("original host scope was refused", err)
	}
}

func (fixture privateKeyboardFixture) events(t *testing.T) int {
	t.Helper()
	focus, err := xproto.GetInputFocus(fixture.connection).Reply()
	if err != nil || focus == nil || uint32(focus.Focus) != fixture.other.Identity().WindowID {
		t.Fatal("private input changed the core focus")
	}
	count := 0
	for index := 0; index < 1024; index++ {
		event, eventErr := fixture.connection.PollForEvent()
		if eventErr != nil {
			t.Fatal("fixture event stream lost")
		}
		if event == nil {
			return count
		}
		switch value := event.(type) {
		case xproto.KeyPressEvent:
			if value.Event != fixture.window || byte(value.Detail) != fixture.config.Chords[0][0] {
				t.Fatal("input reached a different target/key")
			}
			count++
		case xproto.KeyReleaseEvent:
			if value.Event != fixture.window || byte(value.Detail) != fixture.config.Chords[0][0] {
				t.Fatal("release reached a different target/key")
			}
			count++
		}
	}
	t.Fatal("fixture event stream did not settle")
	return 0
}

func (fixture privateKeyboardFixture) heldOutsideWorker(t *testing.T) {
	t.Helper()
	if fixture.lease.Check(t.Context()) != nil || !fixture.guard.Alive(t.Context()) {
		t.Fatal("worker/caller teardown released original host authority")
	}
	contender, err := fixture.other.AcquireInputLease(t.Context())
	if contender != nil || !errors.Is(err, computer.ErrBusy) {
		if contender != nil {
			_ = contender.Close()
		}
		t.Fatal("host lease did not outlive the input worker", err)
	}
}

func privateKeyboardAdmit(t *testing.T, owner *privateKeyboard) {
	t.Helper()
	privateKeyboardAllow(t, owner)
	privateKeyboardReceive(t, owner, "admitted")
	// These raw fixtures deliberately implement core key-event handling. GTK
	// application readiness/effects remain independently qualified elsewhere.
	privateKeyboardAllow(t, owner)
	privateKeyboardReceive(t, owner, "edge")
}

func TestPrivateNativeKeyboardOriginalEpochDeniesFreshNumericReplacement(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	original := fixture.guard.Identity()
	if err := xproto.DestroyWindowChecked(fixture.connection, fixture.window).Check(); err != nil {
		t.Fatal(err)
	}
	createX11FixtureWindow(t, fixture.connection, fixture.root, fixture.window)
	if err := xproto.ChangeWindowAttributesChecked(fixture.connection, fixture.window, xproto.CwEventMask,
		[]uint32{xproto.EventMaskKeyPress | xproto.EventMaskKeyRelease}).Check(); err != nil {
		t.Fatal(err)
	}
	// The native process begins AFTER same-PID/same-XID replacement. Its
	// Prepared message proves fresh native admission alone can accept it.
	owner := fixture.prepare(t, t.Context())
	if fixture.guard.Identity() != original || fixture.guard.Alive(t.Context()) {
		t.Fatal("original Go observer resurrected a replacement")
	}
	if err := owner.allow(t.Context()); !errors.Is(err, computer.ErrStale) {
		t.Fatal("old original generation authorized new native focus", err)
	}
	privateKeyboardReceive(t, owner, "complete")
	if owner.receipt.Acknowledged || !owner.receipt.Canceled || fixture.events(t) != 0 {
		t.Fatal("replacement received input or fabricated completion")
	}
	if _, err := xproto.GetGeometry(fixture.connection, xproto.Drawable(fixture.window)).Reply(); err != nil {
		t.Fatal("denial closed the replacement application window")
	}
}

func TestPrivateNativeKeyboardDispatchRetainsHostOwnership(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	owner := fixture.prepare(t, t.Context())
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	step := privateKeyboardReceive(t, owner, "edge")
	if *step.Ordinal != 1 || *step.Held != 1 {
		t.Fatal("actual held-edge phase missing")
	}
	privateKeyboardAllow(t, owner)
	privateKeyboardReceive(t, owner, "dispatched")
	if fixture.events(t) != 2 {
		t.Fatal("independent original app did not observe exactly one balanced key")
	}
	privateKeyboardAllow(t, owner)
	privateKeyboardReceive(t, owner, "complete")
	if !owner.receipt.Acknowledged || !owner.receipt.CleanupConfirmed || owner.close() != nil {
		t.Fatal("original input did not finish with independent native cleanup")
	}
	fixture.heldOutsideWorker(t)
}

func TestPrivateNativeKeyboardGenerationIsRecheckedBeforeEachEdge(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	owner := fixture.prepare(t, t.Context())
	privateKeyboardAdmit(t, owner)
	owner.identity.Generation++
	if err := owner.allow(t.Context()); !errors.Is(err, computer.ErrStale) {
		t.Fatal("changed expected generation passed the per-edge join", err)
	}
	privateKeyboardReceive(t, owner, "complete")
	if owner.receipt.Acknowledged || !owner.receipt.CleanupConfirmed || fixture.events(t) != 0 {
		t.Fatal("stale generation dispatched or lost its original cleanup")
	}
	fixture.heldOutsideWorker(t)
}

func TestPrivateNativeKeyboardCancellationDoesNotReleaseHostLease(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	owner := fixture.prepare(t, ctx)
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	step := privateKeyboardReceive(t, owner, "edge")
	if *step.Held != 1 {
		t.Fatal("cancellation did not exercise held state")
	}
	start := time.Now()
	cancel()
	privateKeyboardReceive(t, owner, "complete")
	if !owner.receipt.Canceled || !owner.receipt.CancellationDelivered || !owner.receipt.CleanupConfirmed ||
		owner.receipt.Acknowledged || time.Since(start) >= time.Second || owner.close() != nil {
		t.Fatal("cancellation did not confirm independently bounded cleanup")
	}
	if fixture.events(t) < 1 {
		t.Fatal("fixture never received the held key")
	}
	fixture.heldOutsideWorker(t)
}

func TestPrivateNativeKeyboardLeaseLossStopsHeldInput(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	owner := fixture.prepare(t, t.Context())
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	step := privateKeyboardReceive(t, owner, "edge")
	if *step.Held != 1 {
		t.Fatal("ownership loss did not exercise held state")
	}
	native := fixture.lease.backend.(*x11InputLease)
	for _, window := range []xproto.Window{0, native.window} {
		if err := xproto.SetSelectionOwnerChecked(fixture.connection, window, native.selection, xproto.TimeCurrentTime).
			Check(); err != nil {
			t.Fatal(err)
		}
	}
	nativeLeaseEnded(t, fixture.lease)
	privateKeyboardReceive(t, owner, "complete")
	if owner.receipt.Acknowledged || !owner.receipt.Canceled || !owner.receipt.CleanupConfirmed ||
		fixture.lease.Check(t.Context()) == nil {
		t.Fatal("lost/restored lease allowed input or fabricated a current generation")
	}
	if !fixture.guard.Alive(t.Context()) || fixture.events(t) < 1 {
		t.Fatal("ownership stop destroyed the app or did not exercise held state")
	}
}

func TestPrivateNativeKeyboardWireLossRetainsUnknownHostOwnership(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	owner := fixture.prepare(t, t.Context())
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	privateKeyboardReceive(t, owner, "edge")
	_ = owner.wire.Close()
	if owner.close() == nil || owner.receipt != nil {
		t.Fatal("lost wire fabricated a cleanup receipt")
	}
	fixture.heldOutsideWorker(t)
}

func TestPrivateNativeKeyboardKeeperLossRetainsUnknownHostOwnership(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	owner := fixture.prepare(t, t.Context())
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	step := privateKeyboardReceive(t, owner, "edge")
	if *step.Held != 1 || fixture.events(t) < 1 {
		t.Fatal("keeper loss did not exercise actual held input")
	}
	pidfd, _ := privateKeyboardOwnedInputChild(t, owner)
	if err := owner.process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.receive(); err == nil || owner.receipt != nil || owner.close() == nil {
		t.Fatal("keeper loss manufactured a completion/cleanup receipt")
	}
	until := time.Now().Add(time.Second)
	for {
		process := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		if _, err := unix.Poll(process, 5); err != nil {
			t.Fatal(err)
		}
		if process[0].Revents&unix.POLLIN != 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("original backup input process did not finish after keeper loss")
		}
	}
	fixture.heldOutsideWorker(t)
}

func privateKeyboardOwnedInputChild(t *testing.T, owner *privateKeyboard) (int, string) {
	t.Helper()
	parent := strconv.Itoa(owner.process.Process.Pid)
	children, err := os.ReadFile("/proc/" + parent + "/task/" + parent + "/children")
	pids := strings.Fields(string(children))
	if err != nil || len(pids) != 1 {
		t.Fatal("expected one owned private input child")
	}
	pid, err := strconv.Atoi(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); _ = unix.Close(pidfd) })
	childExecutable, childErr := os.Stat("/proc/" + pids[0] + "/exe")
	parentExecutable, parentErr := os.Stat("/proc/" + parent + "/exe")
	statusPath := "/proc/" + pids[0] + "/status"
	status, statusErr := os.ReadFile(statusPath)
	process := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	_, pollErr := unix.Poll(process, 0)
	if childErr != nil || parentErr != nil || statusErr != nil || pollErr != nil || process[0].Revents != 0 ||
		!os.SameFile(childExecutable, parentExecutable) || !strings.Contains(string(status), "PPid:\t"+parent+"\n") {
		t.Fatal("private input child's kernel identity changed")
	}
	return pidfd, statusPath
}

func TestPrivateNativeKeyboardCancellationInterruptsBlockedChildAcknowledgement(t *testing.T) {
	fixture := newPrivateKeyboardFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	owner := fixture.prepare(t, ctx)
	privateKeyboardAdmit(t, owner)
	privateKeyboardAllow(t, owner)
	step := privateKeyboardReceive(t, owner, "edge")
	if *step.Held != 1 {
		t.Fatal("blocked-child case did not exercise held state")
	}
	pidfd, statusPath := privateKeyboardOwnedInputChild(t, owner)
	if err := unix.PidfdSendSignal(pidfd, unix.SIGSTOP, nil, 0); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(250 * time.Millisecond)
	for {
		status, err := os.ReadFile(statusPath)
		if err == nil && strings.Contains(string(status), "State:\tT ") {
			break
		}
		if time.Now().After(until) {
			t.Fatal("owned input child did not enter the kernel stopped state")
		}
		time.Sleep(time.Millisecond)
	}
	privateKeyboardAllow(t, owner) // keeper is now awaiting this stopped child's reply
	start := time.Now()
	cancel()
	privateKeyboardReceive(t, owner, "complete")
	if !owner.receipt.Canceled || !owner.receipt.CancellationDelivered || !owner.receipt.CleanupConfirmed ||
		owner.receipt.Acknowledged || time.Since(start) >= time.Second || owner.close() != nil {
		t.Fatal("blocked child concealed the original host's scoped stop")
	}
	fixture.heldOutsideWorker(t)
}
