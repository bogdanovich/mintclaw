//go:build linux

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/isolation"
	"github.com/bogdanovich/mintclaw/pkg/mcp"
)

func TestOwnedX11DisplayNumberRejectsIncompleteOrNonlocalResult(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"0\n", ":0"},
		{"65535\n", ":65535"},
		{"65536\n", ""},
		{"0001\n", ""},
		{"remote:1\n", ""},
		{"1", ""},
		{"12345678901234567890\n", ""},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			_, err = io.WriteString(writer, tc.reply)
			_ = writer.Close()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := readOwnedX11Display(ctx, reader)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("display = %q, err = %v", got, err)
			}
		})
	}
}

func TestOwnedX11DisplayNative(t *testing.T) {
	cfg := OwnedX11Config{
		Executable: os.Getenv("MINTCLAW_TEST_XVFB_PATH"),
		SHA256:     os.Getenv("MINTCLAW_TEST_XVFB_SHA"),
		Width:      1920, Height: 1200,
	}
	keeperPath, keeperSHA := os.Getenv("MINTCLAW_TEST_KEEPER_PATH"), os.Getenv("MINTCLAW_TEST_KEEPER_SHA")
	if cfg.Executable == "" && cfg.SHA256 == "" && keeperPath == "" && keeperSHA == "" {
		t.Skip("requires pinned private Linux Xvfb and keeper binaries")
	}
	if cfg.Executable == "" || cfg.SHA256 == "" || keeperPath == "" || keeperSHA == "" {
		t.Fatal("incomplete native test pins")
	}
	bad := cfg
	bad.SHA256 = strings.Repeat("0", 64)
	if got, err := StartOwnedX11Display(t.Context(), bad); got != nil || !errors.Is(err, computer.ErrDenied) {
		t.Fatalf("wrong Xvfb pin admitted: %v", err)
	}
	start := func() (*OwnedX11Display, OwnedX11Binding) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		display, err := StartOwnedX11Display(ctx, cfg)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := display.Close(ctx); err != nil {
				t.Error("owned display cleanup", err)
			}
		})
		binding, err := display.Binding(t.Context())
		if err != nil {
			t.Fatal("startup cancellation revoked the ready display", err)
		}
		return display, binding
	}
	first, original := start()
	keeper := startOwnedX11TestKeeper(t, original, keeperPath, keeperSHA)
	defer keeper.Close()
	checkOwnedX11TestClient(t, original, cfg)
	sibling, other := start()
	if original.Display == other.Display || original.ServerPID == other.ServerPID ||
		original.Authority == other.Authority {
		t.Fatal("separate launches shared display authority")
	}
	checkOwnedX11TestClient(t, other, cfg)
	stop, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := first.Close(stop); err != nil {
		t.Fatal("retire original display", err)
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("display close returned before the original process was reaped")
	}
	if _, err := first.Binding(stop); !errors.Is(err, computer.ErrStale) {
		t.Fatal("closed display still returned authority", err)
	}
	if _, err := os.Stat(original.Authority); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired display retained its authority file", err)
	}
	if err := keeper.Close(); err != nil {
		t.Fatal("retire keeper after display loss", err)
	}
	current, err := sibling.Binding(t.Context())
	if err != nil || current != other {
		t.Fatal("retiring the original display affected its sibling", err)
	}
	checkOwnedX11TestClient(t, current, cfg)
	stalled, unresponsive := start()
	stalled.mu.Lock()
	err = unix.PidfdSendSignal(stalled.pidfd, unix.SIGSTOP, nil, 0)
	stalled.mu.Unlock()
	if err != nil {
		t.Fatal("stop the test display's original process", err)
	}
	status := "/proc/" + strconv.FormatInt(int64(unresponsive.ServerPID), 10) + "/status"
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(status)
		if err != nil || time.Now().After(deadline) {
			t.Fatal("original test display did not reach stopped state", err)
		}
		if strings.Contains(string(data), "\nState:\tT") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	force, cancelForce := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelForce()
	started := time.Now()
	if err := stalled.Close(force); err != nil {
		t.Fatal("force-stop unresponsive owned display", err)
	}
	if time.Since(started) < ownedX11StopGrace {
		t.Fatal("unresponsive display did not exercise stop escalation")
	}
	if _, err := os.Stat(unresponsive.Authority); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("force-stopped display retained its authority file", err)
	}
	current, err = sibling.Binding(t.Context())
	if err != nil || current != other {
		t.Fatal("forced stop affected the sibling display", err)
	}
	checkOwnedX11TestClient(t, current, cfg)
}

func startOwnedX11TestKeeper(
	t *testing.T, binding OwnedX11Binding, path, digest string,
) mcp.IsolatedCommandConnection {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd, snapshot, err := isolation.NewSealedExecutableCommand(
		"x11-keeper-test", file, digest, 64<<20, "--owned-device-bootstrap",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "LC_ALL=C", "DISPLAY=" + binding.Display, "XAUTHORITY=" + binding.Authority,
		"MINTCLAW_PRIVATE_XVFB_PID=" + strconv.FormatInt(int64(binding.ServerPID), 10),
	}
	keeper, err := mcp.StartPrivateIsolatedCommand(t.Context(), "retained-x11-test", cmd, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keeper.Close() })
	type readiness struct {
		Ready           bool `json:"ready"`
		ProtocolVersion uint `json:"protocol_version"`
		KeeperPID       int  `json:"keeper_pid"`
	}
	reply := make(chan readiness, 1)
	go func() {
		var state readiness
		_ = json.NewDecoder(io.LimitReader(keeper, 512)).Decode(&state)
		reply <- state
	}()
	select {
	case state := <-reply:
		if !state.Ready || state.ProtocolVersion != 1 || state.KeeperPID != cmd.Process.Pid {
			t.Fatal("original keeper did not become ready before the first authenticated client")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("private keeper readiness timed out")
	}
	return keeper
}

func checkOwnedX11TestClient(t *testing.T, binding OwnedX11Binding, cfg OwnedX11Config) {
	t.Helper()
	endpoint, err := x11Display(binding.Display)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := x11Cookie(binding.Authority, endpoint.number)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.DialTimeout("unix", endpoint.socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := xgb.NewConnNetWithCookieHex(raw, cookie)
	if err != nil {
		t.Fatal("owned test client could not authenticate", err)
	}
	defer client.Close()
	screen := xproto.Setup(client).DefaultScreen(client)
	if int(screen.WidthInPixels) != cfg.Width || int(screen.HeightInPixels) != cfg.Height {
		t.Fatal("unexpected owned display screen")
	}
}
