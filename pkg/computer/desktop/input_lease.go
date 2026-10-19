package desktop

import (
	"context"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type inputLeaseBackend interface {
	check(context.Context) error
	done() <-chan struct{}
	close() error
}

type inputLeaseProvider interface {
	acquireInputLease(context.Context) (inputLeaseBackend, error)
}

// InputLease is cooperative OS-local input exclusion on the original native
// guard's desktop, not a model permission, input executor or held-state receipt.
// The future host must still enforce policy, principal/generation/fresh evidence,
// exact native dispatch and durable no-replay outcomes. Non-cooperating apps and
// human input are not blocked by this lease.
type InputLease struct {
	guard     *WindowGuard
	backend   inputLeaseBackend
	closeOnce sync.Once
	closeErr  error
}

// AcquireInputLease never chooses a desktop from an operator alias or reconnects
// a lost guard. Currently only the qualified Linux X11 mechanism is implemented;
// other native backends refuse explicitly. The context bounds acquisition only:
// canceling it later must not unlock before the executor cleans up owned input.
func (guard *WindowGuard) AcquireInputLease(ctx context.Context) (*InputLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if guard == nil || guard.backend == nil {
		return nil, computer.ErrInvalid
	}
	if guard.lost.Load() {
		return nil, computer.ErrStale
	}
	provider, ok := guard.backend.(inputLeaseProvider)
	if !ok {
		return nil, ErrUnsupported
	}
	backend, err := provider.acquireInputLease(ctx)
	if err != nil {
		return nil, err
	}
	lease := &InputLease{guard: guard, backend: backend}
	if err = lease.Check(ctx); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return lease, nil
}

// Check revalidates this original lease and native window. It never reacquires
// ownership. A canceled caller does not by itself revoke a previously held
// lease; contention may return ErrBusy rather than queue an unbounded waiter.
func (lease *InputLease) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease == nil || lease.guard == nil || lease.backend == nil {
		return computer.ErrInvalid
	}
	if lease.guard.lost.Load() {
		return computer.ErrStale
	}
	return lease.backend.check(ctx)
}

var unavailableInputLeaseDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// Done closes on release or native ownership/target/transport loss. It means
// authority was revoked, NOT that held keys/buttons or external effects were
// cleaned up. The executor must stop and report any uncertain effects itself.
func (lease *InputLease) Done() <-chan struct{} {
	if lease == nil || lease.backend == nil {
		return unavailableInputLeaseDone
	}
	return lease.backend.done()
}

// Close idempotently revokes this lease and relinquishes only its own native
// ownership resource. It never closes the selected app/window, touches the
// clipboard, cleans up a different owner's lease or asserts held-state cleanup.
func (lease *InputLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.closeOnce.Do(func() {
		if lease.backend != nil {
			lease.closeErr = lease.backend.close()
		}
	})
	return lease.closeErr
}

func (lease *InputLease) String() string   { return "native computer input lease" }
func (lease *InputLease) GoString() string { return lease.String() }
