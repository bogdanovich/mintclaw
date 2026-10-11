package credentialbroker

import (
	"context"
	"fmt"
	"io"
	"time"
)

type grantLifetime struct {
	grant Grant
	done  chan struct{}
	timer *time.Timer
}

var deniedLeaseDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// GrantLease binds one authenticated principal/account/revision to a live grant
// generation. It is private consumer authority, never a model tool or result.
// Done signals expiry, revocation, policy replacement or authority shutdown.
// Consumers must also Check current authenticated scope and trusted live origin:
// a delayed timer, cached cookies or a retained reference never authorize use.
// Cancellation cannot undo an already-dispatched private effect; consumers must
// acknowledge termination and quarantine an unknown session outcome.
type GrantLease struct {
	authority *Authority
	grantID   string
	request   Request
	lifetime  *grantLifetime
}

// Lease does not renew the grant or allocate another timer/channel. Principal
// must come from authenticated runtime identity and Origin from trusted state.
func (authority *Authority) Lease(ctx context.Context, grantID string, request Request) (GrantLease, error) {
	if authority == nil || ctx == nil || ctx.Err() != nil {
		return GrantLease{}, ErrDenied
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if ctx.Err() != nil {
		return GrantLease{}, ErrDenied
	}
	if err := authority.authorize(grantID, request); err != nil {
		return GrantLease{}, err
	}
	if ctx.Err() != nil {
		return GrantLease{}, ErrDenied
	}
	return GrantLease{authority, grantID, request, authority.grants[grantID]}, nil
}

// Check compares the current authenticated principal/account/revision to the
// immutable lease and checks trusted live browser or connector origin. Caller
// identity and execution state must not come from model-supplied fields.
func (lease GrantLease) Check(ctx context.Context, request Request) error {
	if ctx == nil || ctx.Err() != nil || lease.authority == nil || lease.lifetime == nil {
		return ErrDenied
	}
	if request.Principal != lease.request.Principal || request.Account != lease.request.Account ||
		request.PolicyRevision != lease.request.PolicyRevision {
		return ErrDenied
	}
	authority := lease.authority
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if ctx.Err() != nil {
		return ErrDenied
	}
	if err := authority.authorize(lease.grantID, request); err != nil {
		return err
	}
	if authority.grants[lease.grantID] != lease.lifetime {
		return ErrApprovalRequired
	}
	if ctx.Err() != nil {
		return ErrDenied
	}
	return nil
}

// Done is a shared read-only signal. The zero lease is already canceled.
func (lease GrantLease) Done() <-chan struct{} {
	if lease.lifetime == nil || lease.lifetime.done == nil {
		return deniedLeaseDone
	}
	return lease.lifetime.done
}

// Close permanently disables this authority and every borrowed lease. Policy
// replacement cannot reopen it. A fresh authority starts with no grants.
func (authority *Authority) Close() {
	if authority == nil {
		return
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.closed = true
	for id := range authority.grants {
		authority.invalidate(id)
	}
}

func (authority *Authority) expire(id string, lifetime *grantLifetime) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.grants[id] == lifetime {
		authority.invalidate(id)
	}
}

// invalidate requires the authority lock and retires exactly one generation.
func (authority *Authority) invalidate(id string) {
	lifetime, exists := authority.grants[id]
	if !exists {
		return
	}
	delete(authority.grants, id)
	if lifetime.timer != nil {
		lifetime.timer.Stop()
	}
	close(lifetime.done)
}

func (GrantLease) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private scoped grant lease]")
}
func (GrantLease) MarshalJSON() ([]byte, error) { return []byte(`"[private scoped grant lease]"`), nil }
func (GrantLease) MarshalText() ([]byte, error) { return []byte("[private scoped grant lease]"), nil }
