package approval

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Enrollment is a distinct handler capability. Trusted startup gives the
// ordinary phone handler only Controller; it retains OwnerSetup here for a
// separately authenticated private console path. Issue is unexported and is
// reachable in production only through the root-authenticated console server.
type Enrollment struct {
	mu       sync.Mutex
	setup    *OwnerSetup
	secret   string
	issued   time.Time
	ceremony string
	boundary phoneBoundary
	serving  atomic.Bool
	now      func() time.Time
}

func NewEnrollment(setup *OwnerSetup) (*Enrollment, error) {
	if setup == nil || setup.controller == nil {
		return nil, ErrUnavailable
	}
	controller := setup.controller
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.usable(context.Background()) || len(controller.owner.Credentials) != 0 {
		return nil, ErrUnavailable
	}
	return &Enrollment{setup: setup, now: time.Now, boundary: newPhoneBoundary(controller.origin)}, nil
}

func (enrollment *Enrollment) issue(ctx context.Context) ([]byte, error) {
	if !enrollment.valid() {
		return nil, ErrUnavailable
	}
	enrollment.mu.Lock()
	defer enrollment.mu.Unlock()
	controller := enrollment.setup.controller
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.usable(ctx) || len(controller.owner.Credentials) != 0 {
		return nil, ErrDenied
	}
	// Reissuing deliberately invalidates the previous bootstrap and ceremony.
	if pending, exists := controller.ceremonies[enrollment.ceremony]; exists && pending.kind == enroll {
		delete(controller.ceremonies, enrollment.ceremony)
	}
	enrollment.clear()
	var nonce [32]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, ErrUnavailable
	}
	enrollment.secret = hex.EncodeToString(nonce[:])
	enrollment.issued = enrollment.now()
	return []byte(controller.origin + "/#enroll=" + enrollment.secret), nil
}

func (enrollment *Enrollment) accepts(secret string) bool {
	return len(secret) == 64 && len(enrollment.secret) == 64 &&
		subtle.ConstantTimeCompare([]byte(secret), []byte(enrollment.secret)) == 1 &&
		withinLifetime(enrollment.now(), enrollment.issued)
}

func (enrollment *Enrollment) begin(ctx context.Context, secret string) (Ceremony, error) {
	if !enrollment.valid() {
		return Ceremony{}, ErrUnavailable
	}
	enrollment.mu.Lock()
	defer enrollment.mu.Unlock()
	if !enrollment.accepts(secret) {
		return Ceremony{}, ErrDenied
	}
	if enrollment.ceremony != "" {
		controller := enrollment.setup.controller
		controller.mu.Lock()
		defer controller.mu.Unlock()
		pending, exists := controller.ceremonies[enrollment.ceremony]
		if !controller.usable(ctx) || !exists || pending.kind != enroll || !controller.ceremonyLive(pending) {
			return Ceremony{}, ErrDenied
		}
		return publicCeremony(enrollment.ceremony, pending), nil
	}
	ceremony, err := enrollment.setup.Begin(ctx)
	if err != nil {
		enrollment.clear()
		return Ceremony{}, err
	}
	enrollment.ceremony = ceremony.ID
	return ceremony, nil
}

func (enrollment *Enrollment) finish(ctx context.Context, secret, id string, response []byte) error {
	if !enrollment.valid() {
		return ErrUnavailable
	}
	enrollment.mu.Lock()
	defer enrollment.mu.Unlock()
	if !enrollment.accepts(secret) || id == "" || id != enrollment.ceremony {
		return ErrDenied
	}
	issued := enrollment.issued
	enrollment.clear()
	// Bound the registration to the console invite's original deadline as well
	// as the verifier ceremony. It cannot gain five extra minutes by starting late.
	if ctx == nil || ctx.Err() != nil || !withinLifetime(enrollment.now(), issued) {
		// Consume the already-selected ceremony even when finish was canceled,
		// so a fresh protected setup action cannot be blocked by an orphan.
		_ = enrollment.setup.Finish(ctx, id, nil)
		return ErrDenied
	}
	finishCtx, cancel := context.WithTimeout(ctx, time.Until(issued.Add(PendingLifetime)))
	defer cancel()
	if err := enrollment.setup.Finish(finishCtx, id, response); err != nil {
		return err
	}
	if !withinLifetime(enrollment.now(), issued) {
		return ErrDenied
	}
	return nil
}

func (enrollment *Enrollment) clear() {
	enrollment.secret, enrollment.ceremony = "", ""
	enrollment.issued = time.Time{}
}

func (enrollment *Enrollment) valid() bool {
	return enrollment != nil && enrollment.setup != nil && enrollment.setup.controller != nil && enrollment.now != nil
}

func (*Enrollment) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private owner enrollment]")
}
func (*Enrollment) MarshalJSON() ([]byte, error) { return []byte(`"[private owner enrollment]"`), nil }
