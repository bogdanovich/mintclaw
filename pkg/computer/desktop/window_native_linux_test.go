//go:build linux

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Opt-in native fixtures create only their own windows. No existing window
// content is read and no accessibility/clipboard/screen-reader setting changes.
func nativeX11Fixture(t *testing.T) (*xgb.Conn, xproto.Window, func() xproto.Window) {
	t.Helper()
	if os.Getenv("MINTCLAW_NATIVE_X11_GUARD_TEST") != "1" {
		t.Skip("explicit selected X11 native test required")
	}
	endpoint, err := x11Display(os.Getenv("DISPLAY"))
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := x11Cookie(os.Getenv("XAUTHORITY"), endpoint.number)
	if err != nil {
		t.Fatal("fixture authority unavailable", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint.socket)
	if err != nil {
		t.Fatal("fixture connection failed")
	}
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	connection, err := xgb.NewConnNetWithCookieHex(raw, cookie)
	if err != nil {
		_ = raw.Close()
		t.Fatal("fixture authentication failed")
	}
	t.Cleanup(func() { _ = raw.Close(); connection.Close() })
	_ = raw.SetDeadline(time.Time{})
	roots := xproto.Setup(connection).Roots
	if endpoint.screen >= len(roots) {
		t.Fatal("fixture screen unavailable")
	}
	root := roots[endpoint.screen].Root
	create := func() xproto.Window {
		t.Helper()
		window, allocationErr := xproto.NewWindowId(connection)
		if allocationErr != nil {
			t.Fatal(allocationErr)
		}
		createX11FixtureWindow(t, connection, root, window)
		return window
	}
	return connection, root, create
}

func createX11FixtureWindow(t *testing.T, connection *xgb.Conn, root, window xproto.Window) {
	t.Helper()
	// These raw protocol fixtures do not implement window-manager protocols.
	// Keep the WM from managing/destroying them independently of the observer;
	// real managed GTK capture is qualified separately by the Cua fixture.
	if err := xproto.CreateWindowChecked(connection, 0, window, root, 80, 80, 240, 140, 0, xproto.WindowClassInputOutput, 0,
		xproto.CwBackPixel|xproto.CwOverrideRedirect, []uint32{0x445566, 1}).
		Check(); err != nil {
		t.Fatal("could not create disposable native window", err)
	}
	if err := xproto.MapWindowChecked(connection, window).Check(); err != nil {
		t.Fatal(err)
	}
}

func nativeWindowRequest(window xproto.Window) WindowRequest {
	return WindowRequest{
		PID:        uint32(os.Getpid()),
		WindowID:   uint32(window),
		Display:    os.Getenv("DISPLAY"),
		XAuthority: os.Getenv("XAUTHORITY"),
	}
}

func TestNativeX11WindowIncarnation(t *testing.T) {
	connection, root, create := nativeX11Fixture(t)
	window := create()
	guard, err := WatchWindow(t.Context(), nativeWindowRequest(window))
	if err != nil {
		t.Fatal("native admission failed", err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	if !guard.Alive(t.Context()) || guard.Identity().Generation == 0 {
		t.Fatal("new native guard is not live")
	}
	first := guard.Identity()
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = xproto.GetGeometry(connection, xproto.Drawable(window)).Reply(); err != nil {
		t.Fatal("guard close destroyed application window", err)
	}
	guard, err = WatchWindow(t.Context(), nativeWindowRequest(window))
	if err != nil {
		t.Fatal(err)
	}
	if guard.Identity().Generation == first.Generation {
		t.Fatal("fresh native guard reused generation")
	}
	first = guard.Identity()
	if err = xproto.DestroyWindowChecked(connection, window).Check(); err != nil {
		t.Fatal(err)
	}
	// Reuse the exact numeric XID from the same process. Polling ID/PID alone
	// would accept this new window; the subscribed old guard must stay lost.
	createX11FixtureWindow(t, connection, root, window)
	if guard.Alive(t.Context()) || guard.Identity() != first {
		t.Fatal("old native identity reacquired replacement")
	}
	replacement, err := WatchWindow(t.Context(), nativeWindowRequest(window))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if replacement.Identity().Generation == first.Generation || !replacement.Alive(t.Context()) {
		t.Fatal("explicit replacement admission failed")
	}
	if guard.Alive(t.Context()) {
		t.Fatal("lost guard was resurrected")
	}
	// Drop only our guard transport. The application connection/window survives.
	_ = replacement.backend.(*x11Window).socket.Close()
	if replacement.Alive(t.Context()) {
		t.Fatal("lost observer transport accepted")
	}
	if _, err = xproto.GetGeometry(connection, xproto.Drawable(window)).Reply(); err != nil {
		t.Fatal("transport loss destroyed window", err)
	}
	t.Log("native close, same-PID exact-XID replacement and observer transport loss verified")
}

func TestNativeX11RejectsWrongProcessOwner(t *testing.T) {
	_, _, create := nativeX11Fixture(t)
	window := create()
	other := exec.CommandContext(t.Context(), "/bin/sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	request := nativeWindowRequest(window)
	request.PID = uint32(other.Process.Pid)
	if guard, err := WatchWindow(t.Context(), request); err == nil {
		_ = guard.Close()
		t.Fatal("X server owner mismatch accepted")
	}
}

func TestNativeX11ObserverEventPressure(t *testing.T) {
	connection, _, create := nativeX11Fixture(t)
	parent := create()
	child, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	// An unmapped child has no window-manager coalescing or visible UI motion.
	if err = xproto.CreateWindowChecked(connection, 0, child, parent, 0, 0, 12, 12, 0,
		xproto.WindowClassInputOutput, 0, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	guard, err := WatchWindow(t.Context(), nativeWindowRequest(child))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	// Exceed the pinned XGB event buffer (5000). A paused observer must drain
	// continuously or a later round-trip reply can deadlock behind that queue.
	for i := range 12000 {
		xproto.ConfigureWindow(connection, child, xproto.ConfigWindowWidth, []uint32{uint32(12 + i%2)})
	}
	if _, err = xproto.GetInputFocus(connection).Reply(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_ = guard.Alive(t.Context())
	if time.Since(started) > 3*time.Second {
		t.Fatal("event pressure stalled observer identity check")
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = xproto.GetGeometry(connection, xproto.Drawable(child)).Reply(); err != nil {
		t.Fatal("observer pressure cleanup altered application window", err)
	}
	// Pressure may explicitly invalidate an overwhelmed observer, never silently
	// lose destruction. A separately admitted observer must still work normally.
	fresh, err := WatchWindow(t.Context(), nativeWindowRequest(child))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if !fresh.Alive(t.Context()) {
		t.Fatal("fresh observer did not recover after event pressure")
	}
	if err = xproto.DestroyWindowChecked(connection, child).Check(); err != nil {
		t.Fatal(err)
	}
	if fresh.Alive(t.Context()) {
		t.Fatal("observer lost destruction after event pressure")
	}
	t.Log("native observer event pressure is bounded and teardown preserves the fixture")
}

// The owner helper is a second disposable process so exit is checked against a
// kernel-held process identity, rather than kill(pid,0) on a recyclable number.
func TestNativeX11OwnerFixtureHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_NATIVE_X11_OWNER_HELPER") != "1" {
		t.Skip("native child helper only")
	}
	_, _, create := nativeX11Fixture(t)
	window := create()
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		PID    int
		Window uint32
	}{os.Getpid(), uint32(window)})
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestNativeX11OwnerExit(t *testing.T) {
	if os.Getenv("MINTCLAW_NATIVE_X11_GUARD_TEST") != "1" {
		t.Skip("explicit selected X11 native test required")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(
		t.Context(),
		executable,
		"-test.run=^TestNativeX11OwnerFixtureHelper$",
		"-test.timeout=30s",
	)
	child.Env = []string{
		"PATH=/usr/bin:/bin", "MINTCLAW_NATIVE_X11_GUARD_TEST=1", "MINTCLAW_NATIVE_X11_OWNER_HELPER=1",
		"DISPLAY=" + os.Getenv("DISPLAY"), "XAUTHORITY=" + os.Getenv("XAUTHORITY"),
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() })
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 512), 4096)
	if !scanner.Scan() {
		t.Fatal("native owner helper did not become ready")
	}
	var ready struct {
		PID    int
		Window uint32
	}
	if json.Unmarshal(scanner.Bytes(), &ready) != nil || ready.PID != child.Process.Pid {
		t.Fatal("native owner identity invalid")
	}
	request := nativeWindowRequest(xproto.Window(ready.Window))
	request.PID = uint32(ready.PID)
	guard, err := WatchWindow(t.Context(), request)
	if err != nil {
		t.Fatal("child owner admission failed", err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	if !guard.Alive(t.Context()) {
		t.Fatal("child owner not initially live")
	}
	if err = stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal(fmt.Errorf("owned fixture exit: %w", err))
	}
	if guard.Alive(t.Context()) {
		t.Fatal("exited native process identity accepted")
	}
	t.Log("native owner exit revoked original kernel process reference")
}
