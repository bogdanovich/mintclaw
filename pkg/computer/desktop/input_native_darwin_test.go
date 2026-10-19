//go:build darwin

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type macOwnershipFixtureEvent struct {
	Kind      string   `json:"kind"`
	InspectID string   `json:"inspect_id"`
	PID       uint32   `json:"pid"`
	Windows   []uint32 `json:"windows"`
}

// Invoked only as a child of the unchanged approved companion by the private
// WSS controller. The child has no invented bundle ID: it verifies its real TCC
// responsibility context, then qualifies the same retained AX/native mechanism.
// Public WatchWindow's main-app readiness gate is unchanged and tested apart.
// This suite sends no input, captures no pixels and creates only owned windows.
func TestNativeMacInputOwnership(t *testing.T) {
	fixturePath := os.Getenv("MINTCLAW_COMPUTER_NATIVE_FIXTURE")
	if fixturePath == "" {
		t.Skip("actual approved companion and disposable Mac fixture required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	report, err := Inspect(ctx)
	if err != nil || !report.ActiveGUI || !report.Accessibility || !report.ScreenRecording {
		t.Fatal("actual-app child native permissions/session unavailable", err)
	}
	t.Log("native ownership TCC preflight: Accessibility=true, Screen Recording=true")
	for index := range 20 {
		if !t.Run(strconv.Itoa(index), func(t *testing.T) {
			qualifyMacInputOwnership(t, ctx, fixturePath)
		}) {
			t.FailNow()
		}
	}
}

func qualifyMacInputOwnership(t *testing.T, ctx context.Context, fixturePath string) {
	t.Helper()
	command := exec.CommandContext(ctx, fixturePath)
	command.Env = []string{"PATH=/usr/bin:/bin", "MINTCLAW_FIXTURE_KEEP_GUARD=1"}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal("owned Mac fixture launch", err)
	}
	processDone := make(chan struct{})
	go func() { _ = command.Wait(); close(processDone) }()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			t.Error("owned fixture did not exit")
		}
	})
	events := make(chan macOwnershipFixtureEvent, 32)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var event macOwnershipFixtureEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	next := func(kind, id string) macOwnershipFixtureEvent {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatal("fixture exited before lifetime verification")
				}
				if event.Kind == kind && event.InspectID == id {
					return event
				}
			case <-ctx.Done():
				t.Fatal("native ownership qualification budget expired")
			case <-timer.C:
				t.Fatal("independent fixture lifetime receipt missing")
			}
		}
	}
	ready := next("ready", "")
	if ready.PID != uint32(command.Process.Pid) || len(ready.Windows) != 2 || ready.Windows[0] == ready.Windows[1] {
		t.Fatal("owned fixture identity invalid")
	}
	guards := make([]*WindowGuard, 2)
	for index, window := range ready.Windows {
		backend, openErr := openDarwinWindow(ctx, WindowRequest{PID: ready.PID, WindowID: window})
		if openErr != nil {
			t.Fatal("original native AX guard", openErr)
		}
		guards[index] = &WindowGuard{backend: backend}
		t.Cleanup(func() { _ = backend.close() })
	}
	acquireCtx, acquireCancel := context.WithCancel(ctx)
	defer acquireCancel()
	first, err := guards[0].AcquireInputLease(acquireCtx)
	if err != nil {
		t.Fatal("native ownership admission", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	acquireCancel()
	select {
	case <-first.Done():
		t.Fatal("acquisition context unlocked before owned-input cleanup")
	default:
	}
	if other, busyErr := guards[1].AcquireInputLease(ctx); other != nil || !errors.Is(busyErr, computer.ErrBusy) {
		t.Fatal("another native window/guard bypassed per-user exclusion", busyErr)
	}
	child, _ := startDarwinLockChild(t, "", "busy")
	if err = child.Wait(); err != nil {
		t.Fatal("other process bypassed original native lock", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(stdin, "{\"inspect_id\":\"released\"}\n"); err != nil {
		t.Fatal(err)
	}
	lifetime := next("lifetime", "released")
	if lifetime.PID != ready.PID || len(lifetime.Windows) != 2 ||
		lifetime.Windows[0] != ready.Windows[0] || lifetime.Windows[1] != ready.Windows[1] ||
		!guards[0].Alive(ctx) || !guards[1].Alive(ctx) {
		t.Fatal("ownership release closed/replaced an independently owned window")
	}
	second, err := guards[1].AcquireInputLease(ctx)
	if err != nil {
		t.Fatal("explicit fresh sibling admission after release", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.Close() != nil || second.Check(ctx) != nil {
		t.Fatal("stale cleanup cleared a newer native owner")
	}
	_ = second.Close()
	original, err := guards[0].AcquireInputLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Close() })
	if _, err = io.WriteString(stdin, "{\"inspect_id\":\"closed\",\"close_target\":true}\n"); err != nil {
		t.Fatal(err)
	}
	lifetime = next("lifetime", "closed")
	if lifetime.PID != ready.PID || len(lifetime.Windows) != 1 || lifetime.Windows[0] != ready.Windows[1] {
		t.Fatal("target destruction was not independently attested")
	}
	waitDarwinInputLeaseLoss(t, original.backend.(*darwinInputLease))
	if guards[0].Alive(ctx) || !guards[1].Alive(ctx) {
		t.Fatal("original destroyed window was rebound or sibling was lost")
	}
	if replaced, staleErr := guards[0].AcquireInputLease(
		ctx,
	); replaced != nil ||
		!errors.Is(staleErr, computer.ErrStale) {
		t.Fatal("destroyed native guard reacquired input ownership", staleErr)
	}
	last, err := guards[1].AcquireInputLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = last.Close() })
	_ = command.Process.Kill()
	select {
	case <-processDone:
	case <-ctx.Done():
		t.Fatal("owned process exit not verified")
	}
	waitDarwinInputLeaseLoss(t, last.backend.(*darwinInputLease))
}
