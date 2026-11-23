//go:build linux

package companion

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
)

func TestManagedX11WheelValidatesNativeReceipts(t *testing.T) {
	const good = `{"request_seq":1,"attempted":true,"dispatch_verified":true,"recovery_verified":true}`
	const unknown = `{"request_seq":1,"attempted":true,"dispatch_verified":false,"recovery_verified":false}`
	const refused = `{"request_seq":1,"attempted":false,"dispatch_verified":false,"recovery_verified":false}`
	const recovered = `{"request_seq":1,"attempted":true,"dispatch_verified":false,"recovery_verified":true}`
	for _, tc := range []struct {
		frame string
		cut   bool
		want  ManagedX11WheelReceipt
	}{
		{frame: good, want: ManagedX11WheelReceipt{AttemptKnown: true, Attempted: true, DispatchVerified: true, Neutral: true}},
		{frame: unknown, want: ManagedX11WheelReceipt{AttemptKnown: true, Attempted: true}},
		{frame: refused, want: ManagedX11WheelReceipt{AttemptKnown: true, Neutral: true}},
		{frame: recovered, cut: true, want: ManagedX11WheelReceipt{AttemptKnown: true, Attempted: true, Neutral: true}},
		{frame: good, cut: true},
		{frame: refused, cut: true},
		{frame: strings.Replace(good, `"request_seq":1`, `"request_seq":2`, 1)},
		{frame: strings.Replace(good, `,"recovery_verified":true`, "", 1)},
		{frame: strings.Replace(good, `"recovery_verified":true`, `"recovery_verified":false`, 1)},
		{frame: strings.Replace(good, `"attempted":true`, `"attempted":false`, 1)},
	} {
		epoch := ManagedX11Pointer{reader: bufio.NewReader(strings.NewReader(tc.frame + "\n"))}
		var response x11WheelFrame
		if err := epoch.readFrame(&response); err != nil {
			t.Fatal(err)
		}
		receipt, err := response.receipt(1, tc.cut)
		if receipt != tc.want || (err == nil) != tc.want.AttemptKnown {
			t.Errorf("receipt = %+v, %v for %s", receipt, err, tc.frame)
		}
	}
	for _, frame := range []string{good, good + " " + good + "\n", strings.Repeat(" ", managedPointerFrame) + good + "\n"} {
		epoch := ManagedX11Pointer{reader: bufio.NewReaderSize(strings.NewReader(frame), managedPointerFrame)}
		if err := epoch.readFrame(new(x11WheelFrame)); !errors.Is(err, computer.ErrUnavailable) {
			t.Fatal("invalid framing admitted", err)
		}
	}
}

func TestManagedX11WheelRejectsInexactNativeCoordinates(t *testing.T) {
	geometry := computer.Geometry{OriginX: -20, OriginY: 160, Width: 100, Height: 200, Scale: 1}
	point := computer.InputPoint{X: 99, Y: 199}
	want := x11WheelPoint{left: -20, top: 160, x: 99, y: 199, width: 100, height: 200}
	if native, err := newX11WheelPoint(geometry, point); err != nil || native != want {
		t.Fatalf("native geometry = %+v, %v", native, err)
	}
	for _, invalid := range []computer.Geometry{
		{Width: 100, Height: 200, Scale: 2},
		{OriginX: 32768, Width: 100, Height: 200, Scale: 1},
		{OriginX: 0.5, Width: 100, Height: 200, Scale: 1},
		{Width: 90, Height: 200, Scale: 1},
	} {
		if _, err := newX11WheelPoint(invalid, point); !errors.Is(err, computer.ErrInvalid) {
			t.Errorf("invalid geometry %+v: %v", invalid, err)
		}
	}
}

func TestManagedX11PointerLifetimeNative(t *testing.T) {
	config := ManagedX11PointerConfig{
		Display: desktop.OwnedX11Config{
			Executable: os.Getenv("MINTCLAW_TEST_XVFB_PATH"), SHA256: os.Getenv("MINTCLAW_TEST_XVFB_SHA"),
			Width: 1600, Height: 1000,
		},
		WorkerExecutable: os.Getenv("MINTCLAW_TEST_KEEPER_PATH"), WorkerSHA256: os.Getenv("MINTCLAW_TEST_KEEPER_SHA"),
	}
	if config.Display.Executable == "" && config.Display.SHA256 == "" &&
		config.WorkerExecutable == "" && config.WorkerSHA256 == "" {
		t.Skip("requires pinned disposable Xvfb and native worker")
	}
	if config.Display.Executable == "" || config.Display.SHA256 == "" ||
		config.WorkerExecutable == "" || config.WorkerSHA256 == "" {
		t.Fatal("incomplete disposable X11 test pins")
	}
	bad := config
	bad.WorkerSHA256 = strings.Repeat("0", 64)
	if worker, err := StartManagedX11Pointer(t.Context(), bad); worker != nil || !errors.Is(err, computer.ErrDenied) {
		t.Fatal("wrong worker pin admitted", err)
	}
	sibling, err := desktop.StartOwnedX11Display(t.Context(), config.Display)
	if err != nil {
		t.Fatal("start unrelated test display", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := sibling.Close(ctx); stopErr != nil {
			t.Error("retire sibling fixture", stopErr)
		}
	})
	other, err := sibling.Binding(t.Context())
	if err != nil {
		t.Fatal("attest unrelated test display", err)
	}
	for _, loss := range []string{"operator-stop", "keeper-dies", "display-dies"} {
		t.Run(loss, func(t *testing.T) {
			startup, cancel := context.WithCancel(t.Context())
			epoch, err := StartManagedX11Pointer(startup, config)
			cancel()
			if err != nil {
				t.Fatal("provision test epoch", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if stopErr := epoch.Close(ctx); stopErr != nil {
					t.Error("retire original test epoch", stopErr)
				}
			})
			ready, err := epoch.Binding(t.Context())
			keeper := epoch.command.Process.Pid
			if err != nil || ready.ServerPID == other.ServerPID || ready.Display == other.Display || keeper <= 1 {
				t.Fatal("startup cancellation invalidated epoch or reused another display", err)
			}
			// Only signal a pidfd for the original owned test processes.
			pid := keeper
			if loss == "display-dies" {
				pid = int(ready.ServerPID)
			}
			fd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(fd) }()
			stop := unix.SIGKILL
			if loss == "operator-stop" {
				stop = unix.SIGSTOP
			}
			if err := unix.PidfdSendSignal(fd, stop, nil, 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if loss == "operator-stop" {
				if err := epoch.Close(ctx); err != nil {
					t.Fatal("emergency close could not retire stalled keeper", err)
				}
			}
			select {
			case <-epoch.Done():
			case <-ctx.Done():
				t.Fatal("original epoch did not retire after loss")
			}
			if err := epoch.Close(ctx); err != nil || epoch.command.ProcessState == nil {
				t.Fatal("retirement did not reap the original keeper", err)
			}
			if _, err := epoch.Binding(ctx); !errors.Is(err, computer.ErrStale) {
				t.Fatal("retired epoch returned authority", err)
			}
			if _, err := os.Stat(ready.Authority); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("retired epoch still has an authority file", err)
			}
			if binding, err := sibling.Binding(ctx); err != nil || binding != other {
				t.Fatal("losing the test epoch affected its sibling display", err)
			}
		})
	}
}
