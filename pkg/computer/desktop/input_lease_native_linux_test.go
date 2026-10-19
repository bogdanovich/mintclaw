//go:build linux

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func nativeInputLeaseFixture(t *testing.T) (*xgb.Conn, xproto.Window, *WindowGuard, *WindowGuard) {
	t.Helper()
	if os.Getenv("MINTCLAW_NATIVE_X11_LEASE_TEST") != "1" {
		t.Skip("explicit native X11 input-lease test required")
	}
	connection, _, create := nativeX11Fixture(t)
	first, second := create(), create()
	watch := func(window xproto.Window) *WindowGuard {
		t.Helper()
		guard, err := WatchWindow(t.Context(), nativeWindowRequest(window))
		if err != nil {
			t.Fatal("fixture guard admission failed", err)
		}
		t.Cleanup(func() { _ = guard.Close() })
		return guard
	}
	return connection, first, watch(first), watch(second)
}

func nativeSelectionOwner(t *testing.T, connection *xgb.Conn, selection xproto.Atom) xproto.Window {
	t.Helper()
	reply, err := xproto.GetSelectionOwner(connection, selection).Reply()
	if err != nil || reply == nil {
		t.Fatal("private input-control selection unreadable", err)
	}
	return reply.Owner
}

func nativeLeaseEnded(t *testing.T, lease *InputLease) {
	t.Helper()
	select {
	case <-lease.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("native input ownership loss was not notified")
	}
}

func TestNativeX11InputLeaseLifecycle(t *testing.T) {
	connection, window, first, second := nativeInputLeaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := first.AcquireInputLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if lease.Check(t.Context()) != nil {
		t.Fatal("new original input lease is not live")
	}
	if other, err := second.AcquireInputLease(t.Context()); other != nil || !errors.Is(err, computer.ErrBusy) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("another native window guard on the same server bypassed exclusion", err)
	}
	cancel()
	if !errors.Is(lease.Check(ctx), context.Canceled) || lease.Check(t.Context()) != nil {
		t.Fatal("caller cancellation granted input or discarded held ownership")
	}
	select {
	case <-lease.Done():
		t.Fatal("caller cancellation relinquished control before executor cleanup")
	default:
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	nativeLeaseEnded(t, lease)
	if lease.Check(t.Context()) == nil {
		t.Fatal("released lease remained valid")
	}
	next, err := second.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal("second guard could not acquire after explicit release", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if lease.Close() != nil || next.Check(t.Context()) != nil {
		t.Fatal("stale cleanup released a newer owner")
	}
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
	if !first.Alive(t.Context()) || !second.Alive(t.Context()) {
		t.Fatal("input release invalidated original window observers")
	}
	if _, err = xproto.GetGeometry(connection, xproto.Drawable(window)).Reply(); err != nil {
		t.Fatal("input lease cleanup closed the app fixture", err)
	}
}

func TestNativeX11InputLeaseLossCannotResurrectOrClearNewOwner(t *testing.T) {
	connection, _, first, _ := nativeInputLeaseFixture(t)
	lease, err := first.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	native := lease.backend.(*x11InputLease)
	root := xproto.Setup(connection).Roots[0].Root
	foreign, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(connection, 0, foreign, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	// Simulate interference only with the private lease held by this test.
	// Neither CLIPBOARD nor PRIMARY is read, claimed or restored.
	for _, owner := range []xproto.Window{foreign, native.window} {
		if err = xproto.SetSelectionOwnerChecked(connection, owner, native.selection, xproto.TimeCurrentTime).
			Check(); err != nil {
			t.Fatal(err)
		}
	}
	nativeLeaseEnded(t, lease)
	if !errors.Is(lease.Check(t.Context()), computer.ErrStale) || !first.Alive(t.Context()) {
		t.Fatal("restoring the numeric owner resurrected a lost lease or lost the target")
	}
	if err = xproto.SetSelectionOwnerChecked(connection, foreign, native.selection, xproto.TimeCurrentTime).
		Check(); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil || nativeSelectionOwner(t, connection, native.selection) != foreign {
		t.Fatal("old owner's cleanup cleared a different current owner", err)
	}
	// Destroy only this fixture's token; never clear an arbitrary selection.
	if err = xproto.DestroyWindowChecked(connection, foreign).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeX11InputLeaseDestroyedTokenAndTarget(t *testing.T) {
	connection, window, first, second := nativeInputLeaseFixture(t)
	lease, err := first.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	native := lease.backend.(*x11InputLease)
	if err = xproto.DestroyWindowChecked(connection, native.window).Check(); err != nil {
		t.Fatal(err)
	}
	nativeLeaseEnded(t, lease)
	if lease.Check(t.Context()) == nil || lease.Close() != nil || !first.Alive(t.Context()) {
		t.Fatal("destroyed control token was reused or its absence corrupted the target guard")
	}
	next, err := first.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal("explicit acquisition after released token failed", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if err = xproto.DestroyWindowChecked(connection, window).Check(); err != nil {
		t.Fatal(err)
	}
	nativeLeaseEnded(t, next)
	if next.Check(t.Context()) == nil || first.Alive(t.Context()) {
		t.Fatal("original target destruction retained input authority")
	}
	if err = next.Close(); !errors.Is(err, computer.ErrStale) {
		t.Fatal("target loss cleanup pretended the original observer was still verified", err)
	}
	replacement, err := second.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal("closing lost original connection did not relinquish input ownership", err)
	}
	_ = replacement.Close()
}

func TestNativeX11InputLeaseOtherScreen(t *testing.T) {
	connection, _, first, _ := nativeInputLeaseFixture(t)
	roots := xproto.Setup(connection).Roots
	if len(roots) < 2 {
		t.Skip("selected actual X server has one screen; two-screen Xvfb is a separate qualification")
	}
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeWindowRequest(window)
	endpoint, _ := x11Display(request.Display)
	otherScreen := (endpoint.screen + 1) % len(roots)
	createX11FixtureWindow(t, connection, roots[otherScreen].Root, window)
	request.Display = ":" + endpoint.number + "." + strconv.Itoa(otherScreen)
	other, err := WatchWindow(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	lease, err := first.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if second, err := other.AcquireInputLease(t.Context()); second != nil || !errors.Is(err, computer.ErrBusy) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("different screen on the same X server bypassed input exclusion", err)
	}
}

func TestNativeX11InputLeaseOriginalTransportLoss(t *testing.T) {
	for _, failure := range []string{"explicit close", "server close"} {
		t.Run(failure, func(t *testing.T) {
			connection, window, first, second := nativeInputLeaseFixture(t)
			lease, err := first.AcquireInputLease(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lease.Close() })
			native := lease.backend.(*x11InputLease)
			if failure == "explicit close" {
				_ = first.backend.(*x11Window).socket.Close()
			} else {
				// Close only the observer client identified by this test's own
				// unmapped token, never the independently owned app connection.
				if err = xproto.KillClientChecked(connection, uint32(native.window)).Check(); err != nil {
					t.Fatal("fixture observer close unavailable", err)
				}
			}
			nativeLeaseEnded(t, lease)
			if lease.Check(t.Context()) == nil || !errors.Is(lease.Close(), computer.ErrStale) {
				t.Fatal("lost original transport remained authorized or was reconnected")
			}
			waitNativeInputDisowned(t, connection, native.selection)
			next, err := second.AcquireInputLease(t.Context())
			if err != nil {
				t.Fatal("separate original guard could not acquire after transport close-down", err)
			}
			t.Cleanup(func() { _ = next.Close() })
			if _, err = xproto.GetGeometry(connection, xproto.Drawable(window)).Reply(); err != nil {
				t.Fatal("control transport loss destroyed the independent app window", err)
			}
		})
	}
}

func waitNativeInputDisowned(t *testing.T, connection *xgb.Conn, selection xproto.Atom) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for nativeSelectionOwner(t, connection, selection) != xproto.WindowNone {
		if time.Now().After(deadline) {
			t.Fatal("dead control client's own resources were not released")
		}
		time.Sleep(time.Millisecond)
	}
}

type nativeInputOwnerEvent struct {
	Kind   string `json:"kind"`
	PID    int    `json:"pid"`
	Window uint32 `json:"window"`
}

// Child observes a parent-owned fixture window. Its crash must release only
// its own control token/connection, preserving the independently owned app.
func TestNativeX11InputLeaseOwnerHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_NATIVE_X11_LEASE_HELPER") != "1" {
		t.Skip("explicit native input ownership child only")
	}
	pid, pidErr := strconv.ParseUint(os.Getenv("MINTCLAW_NATIVE_X11_LEASE_PID"), 10, 32)
	window, windowErr := strconv.ParseUint(os.Getenv("MINTCLAW_NATIVE_X11_LEASE_WINDOW"), 10, 32)
	if pidErr != nil || windowErr != nil {
		t.Fatal("invalid parent-owned native fixture")
	}
	guard, err := WatchWindow(t.Context(), WindowRequest{
		PID: uint32(pid), WindowID: uint32(window), Display: os.Getenv("DISPLAY"), XAuthority: os.Getenv("XAUTHORITY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(nativeInputOwnerEvent{Kind: "ready", PID: os.Getpid(), Window: uint32(window)})
	input := bufio.NewReader(os.Stdin)
	if line, err := input.ReadString('\n'); err != nil || line != "acquire\n" {
		t.Fatal("fixture acquire barrier unavailable")
	}
	lease, err := guard.AcquireInputLease(t.Context())
	if errors.Is(err, computer.ErrBusy) {
		_ = encoder.Encode(nativeInputOwnerEvent{Kind: "busy", PID: os.Getpid(), Window: uint32(window)})
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	_ = encoder.Encode(nativeInputOwnerEvent{Kind: "acquired", PID: os.Getpid(), Window: uint32(window)})
	_, _ = io.Copy(io.Discard, input)
}

type nativeInputOwnerChild struct {
	process *exec.Cmd
	input   io.WriteCloser
	events  <-chan nativeInputOwnerEvent
	done    <-chan struct{}
}

func startNativeInputOwner(t *testing.T, window xproto.Window) nativeInputOwnerChild {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable,
		"-test.run=^TestNativeX11InputLeaseOwnerHelper$", "-test.count=1", "-test.timeout=30s")
	command.Env = []string{
		"PATH=/usr/bin:/bin", "DISPLAY=" + os.Getenv("DISPLAY"), "XAUTHORITY=" + os.Getenv("XAUTHORITY"),
		"MINTCLAW_NATIVE_X11_LEASE_HELPER=1",
		"MINTCLAW_NATIVE_X11_LEASE_PID=" + strconv.Itoa(os.Getpid()),
		"MINTCLAW_NATIVE_X11_LEASE_WINDOW=" + strconv.FormatUint(uint64(window), 10),
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	readDone := make(chan struct{})
	go func() { <-readDone; _ = command.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = input.Close()
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("owned input-lease helper did not terminate")
		}
	})
	events := make(chan nativeInputOwnerEvent, 4)
	go func() {
		defer close(events)
		defer close(readDone)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var event nativeInputOwnerEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	child := nativeInputOwnerChild{process: command, input: input, events: events, done: done}
	if event := child.next(t); event.Kind != "ready" || event.PID != command.Process.Pid ||
		event.Window != uint32(window) {
		t.Fatal("native helper did not attest the exact parent-owned fixture", event)
	}
	return child
}

func (child nativeInputOwnerChild) next(t *testing.T) nativeInputOwnerEvent {
	t.Helper()
	select {
	case event, ok := <-child.events:
		if !ok {
			t.Fatal("native input owner helper exited without a result")
		}
		return event
	case <-time.After(4 * time.Second):
		t.Fatal("native input owner barrier timed out")
	}
	return nativeInputOwnerEvent{}
}

func TestNativeX11InputLeaseProcessRaceAndCrash(t *testing.T) {
	connection, window, first, second := nativeInputLeaseFixture(t)
	one := startNativeInputOwner(t, window)
	two := startNativeInputOwner(t, xproto.Window(second.Identity().WindowID))
	for _, child := range []nativeInputOwnerChild{one, two} {
		if _, err := fmt.Fprintln(child.input, "acquire"); err != nil {
			t.Fatal(err)
		}
	}
	states := []string{one.next(t).Kind, two.next(t).Kind}
	if strings.Join(states, ",") != "acquired,busy" && strings.Join(states, ",") != "busy,acquired" {
		t.Fatal("separate processes both acquired or lost same-server exclusion", states)
	}
	if lease, err := first.AcquireInputLease(t.Context()); lease != nil || !errors.Is(err, computer.ErrBusy) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatal("parent guard bypassed child process's control lease", err)
	}
	for _, child := range []nativeInputOwnerChild{one, two} {
		_ = child.process.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(3 * time.Second):
			t.Fatal("owned helper process did not exit")
		}
	}
	// Process exit and X11 close-down are not one atomic observation. Wait
	// for the original server's authoritative disowning before reacquiring;
	// grabbing the server too early would postpone its pending close-down.
	atom, err := xproto.InternAtom(connection, true, uint16(len(x11InputSelection)), x11InputSelection).Reply()
	if err != nil || atom == nil {
		t.Fatal("private selection identity unavailable", err)
	}
	waitNativeInputDisowned(t, connection, atom.Atom)
	lease, err := first.AcquireInputLease(t.Context())
	if err != nil {
		t.Fatal("native process crash did not relinquish its own lease", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if !first.Alive(t.Context()) || !second.Alive(t.Context()) {
		t.Fatal("control helper crash invalidated independent app window guards")
	}
	if _, err = xproto.GetGeometry(connection, xproto.Drawable(window)).Reply(); err != nil {
		t.Fatal("control helper crash destroyed the parent-owned app", err)
	}
}
