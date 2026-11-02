//go:build darwin

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

// A deterministic native-thread probe stand-in, not AX/window qualification.
// Kernel file/process behavior is real; the opt-in actual-app fixture exercises
// the original retained AX observer independently.
func testDarwinInputGuard(t *testing.T) (*darwinWindow, *atomic.Int32) {
	t.Helper()
	guard := &darwinWindow{
		pid: uint32(os.Getpid()), loss: make(chan struct{}), probes: make(chan macProbe),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	checks := &atomic.Int32{}
	go func() {
		defer close(guard.done)
		for {
			select {
			case <-guard.stop:
				return
			case probe := <-guard.probes:
				checks.Add(1)
				probe.result <- !guard.lost.Load() && probe.ctx.Err() == nil
			}
		}
	}()
	t.Cleanup(func() { _ = guard.close() })
	return guard, checks
}

func testDarwinInputLease(t *testing.T, pid uint32) (*darwinInputLease, string) {
	t.Helper()
	guard, _ := testDarwinInputGuard(t)
	root := privateDarwinInputRoot(t)
	file := privateDarwinFileLease(t, root)
	process, err := watchDarwinProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	lease := &darwinInputLease{guard: guard, file: file, process: process, ended: make(chan struct{})}
	t.Cleanup(func() { _ = lease.close() })
	go lease.monitor()
	return lease, root
}

func waitDarwinInputLeaseLoss(t *testing.T, lease *darwinInputLease) {
	t.Helper()
	select {
	case <-lease.done():
	case <-time.After(2 * time.Second):
		t.Fatal("native ownership loss was not observed")
	}
	if !errors.Is(lease.check(t.Context()), computer.ErrStale) {
		t.Fatal("native ownership was reacquired after loss")
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinInputLeaseLosesOnOriginalGuardAndFile(t *testing.T) {
	for _, loss := range []string{"guard", "file", "process"} {
		t.Run(loss, func(t *testing.T) {
			pid := uint32(os.Getpid())
			var kill func()
			if loss == "process" {
				child, _ := startDarwinLockChild(t, privateDarwinInputRoot(t), "hold")
				pid = uint32(child.Process.Pid)
				kill = func() { _ = child.Process.Kill(); _ = child.Wait() }
			}
			lease, root := testDarwinInputLease(t, pid)
			switch loss {
			case "guard":
				lease.guard.invalidate()
			case "file":
				path := filepath.Join(root, lease.file.name)
				if err := os.Rename(path, path+".moved"); err != nil {
					t.Fatal(err)
				}
			case "process":
				kill()
			}
			waitDarwinInputLeaseLoss(t, lease)
			if unix.Kill(os.Getpid(), 0) != nil {
				t.Fatal("lease cleanup terminated its caller")
			}
		})
	}
}

func TestDarwinInputLeaseCancellationAndConcurrentClose(t *testing.T) {
	lease, root := testDarwinInputLease(t, uint32(os.Getpid()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(lease.check(ctx), context.Canceled) {
		t.Fatal("canceled caller admitted input ownership")
	}
	select {
	case <-lease.done():
		t.Fatal("canceled check released ownership before held-state cleanup")
	default:
	}
	if next, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700); next != nil ||
		!errors.Is(err, computer.ErrBusy) {
		t.Fatal("canceled caller unlocked the desktop", err)
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if err := lease.close(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	waitDarwinInputLeaseLoss(t, lease)
	newer := privateDarwinFileLease(t, root)
	if lease.close() != nil || newer.check() != nil {
		t.Fatal("stale close cleared newer input owner")
	}
}

func TestDarwinInputLeaseClosedCheckDoesNotProbeAX(t *testing.T) {
	guard, checks := testDarwinInputGuard(t)
	lease := &darwinInputLease{guard: guard, ended: make(chan struct{})}
	if lease.close() != nil || !errors.Is(lease.check(t.Context()), computer.ErrStale) || checks.Load() != 0 {
		t.Fatal("released native lease queried the application again")
	}
}
