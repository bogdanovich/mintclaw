package computer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

var (
	ErrBusy        = errors.New("computer desktop already leased")
	ErrUnavailable = errors.New("computer capture unavailable")
)

// Capture is protected, live-only driver output. Drivers must verify the
// selected scope's identity before and after capture, never fall back to an
// ambient desktop, and honor cancellation. This interface cannot dispatch input.
type Capture struct {
	Scope             Scope
	Geometry          Geometry
	ContentType       string
	Screenshot        []byte `json:"-"`
	AccessibilityText string `json:"-"`
}

func (Capture) String() string     { return "computer capture (protected payload omitted)" }
func (c Capture) GoString() string { return c.String() }

type CaptureDriver interface {
	// ErrStale reports permanent loss of the admitted scope. Other errors are
	// sanitized without asserting that the native resource was destroyed.
	Capture(context.Context, Target) (Capture, error)
}

// Arbiter is shared by every host on a physical desktop, including hosts for
// different profiles/displays. It coordinates this process only; it is not an
// OS-wide input lock or a substitute for a companion's local admission policy.
// Future input hosts must not claim cross-process exclusion using this arbiter.
type Arbiter struct {
	mu     sync.Mutex
	leases map[desktopKey]*lease
}

type desktopKey struct {
	placement Placement
	node      string
	desktop   string
}

type lease struct {
	session Session
	cancel  context.CancelFunc
	active  bool
}

func NewArbiter() *Arbiter { return &Arbiter{leases: make(map[desktopKey]*lease)} }

// ReadOnlyPolicy is trusted operator configuration, not model-authored input.
// Admit must be a fast, side-effect-free local policy check and must not call
// back into the host. It is rechecked on every operation, including status.
type ReadOnlyPolicy struct {
	WorkerID       string
	Revision       string
	Targets        []Target
	LeaseTTL       time.Duration
	ObservationTTL time.Duration
	Admit          func(runtimecap.Principal, Target) bool
}

// ReadOnlyHost enforces the stored owner, exact target/worker/policy binding,
// expiry and control generation. It is opt-in through explicit construction;
// there is no tool registration, ambient display selection or application close.
type ReadOnlyHost struct {
	policy   ReadOnlyPolicy
	driver   CaptureDriver
	arbiter  *Arbiter
	sessions map[string]*lease // guarded by arbiter.mu
	now      func() time.Time
}

var _ Host = (*ReadOnlyHost)(nil)

func NewReadOnlyHost(policy ReadOnlyPolicy, driver CaptureDriver, arbiter *Arbiter) (*ReadOnlyHost, error) {
	if driver == nil || arbiter == nil || arbiter.leases == nil || policy.Admit == nil ||
		!validIdentifier(policy.WorkerID) || !validIdentifier(policy.Revision) ||
		len(policy.Targets) == 0 || len(policy.Targets) > 32 || policy.LeaseTTL <= 0 ||
		policy.LeaseTTL > time.Hour || policy.ObservationTTL <= 0 ||
		policy.ObservationTTL > time.Minute || policy.ObservationTTL > policy.LeaseTTL {
		return nil, ErrInvalid
	}
	aliases := make(map[string]bool, len(policy.Targets))
	for _, target := range policy.Targets {
		if target.Validate() != nil || aliases[target.Alias] {
			return nil, ErrInvalid
		}
		aliases[target.Alias] = true
	}
	policy.Targets = append([]Target(nil), policy.Targets...)
	return &ReadOnlyHost{
		policy: policy, driver: driver, arbiter: arbiter,
		sessions: make(map[string]*lease), now: time.Now,
	}, nil
}

func (host *ReadOnlyHost) Targets(ctx context.Context, principal runtimecap.Principal) ([]Target, error) {
	if principal.Validate() != nil {
		return nil, ErrInvalid
	}
	var targets []Target
	for _, target := range host.policy.Targets {
		if host.policy.Admit(principal, target) {
			targets = append(targets, target)
		}
	}
	return targets, ctx.Err()
}

func (host *ReadOnlyHost) Open(ctx context.Context, request OpenRequest) (Session, error) {
	if request.Validate() != nil {
		return Session{}, ErrInvalid
	}
	host.arbiter.mu.Lock()
	defer host.arbiter.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if !host.admitted(request.Principal, request.Target) {
		return Session{}, ErrDenied
	}
	now := host.now()
	for id, entry := range host.sessions {
		if !now.Before(entry.session.ExpiresAt) {
			host.revoke(entry)
			delete(host.sessions, id)
		}
	}
	key := keyFor(request.Target)
	if previous := host.arbiter.leases[key]; previous != nil {
		if now.Before(previous.session.ExpiresAt) {
			return Session{}, ErrBusy
		}
		host.revoke(previous)
	}
	if len(host.sessions) >= 128 {
		// Keep bounded tombstones for status/idempotence, but never let completed
		// work prevent admission. Evict the oldest terminal entry, not a live lease.
		var oldest string
		for id, entry := range host.sessions {
			if entry.session.Control.Terminal() &&
				(oldest == "" || entry.session.ExpiresAt.Before(host.sessions[oldest].session.ExpiresAt)) {
				oldest = id
			}
		}
		if oldest == "" {
			return Session{}, ErrBusy
		}
		delete(host.sessions, oldest)
	}
	id, err := newID()
	if err != nil {
		return Session{}, ErrUnavailable
	}
	entry := &lease{session: Session{
		Binding: Binding{
			Principal: request.Principal, SessionID: id, Target: request.Target,
			WorkerID: host.policy.WorkerID, PolicyRevision: host.policy.Revision, Generation: 1,
		},
		Control: ControlAgent, ExpiresAt: now.Add(host.policy.LeaseTTL),
	}}
	host.sessions[id], host.arbiter.leases[key] = entry, entry
	return entry.session, nil
}

func (host *ReadOnlyHost) Status(
	ctx context.Context, principal runtimecap.Principal, sessionID string,
) (Session, error) {
	host.arbiter.mu.Lock()
	defer host.arbiter.mu.Unlock()
	entry, err := host.owned(ctx, principal, sessionID)
	if err != nil {
		return Session{}, err
	}
	return entry.session, nil
}

func (host *ReadOnlyHost) Observe(ctx context.Context, session Session) (Observation, error) {
	host.arbiter.mu.Lock()
	entry, err := host.bound(ctx, session.Binding)
	if err == nil {
		err = entry.session.AgentAuthority(host.now())
	}
	if err == nil && entry.active {
		err = ErrBusy
	}
	if err != nil {
		host.arbiter.mu.Unlock()
		return Observation{}, err
	}
	// Caller-authored control/expiry cannot extend authority or fabricate state.
	stored := entry.session
	capturedAt := host.now()
	expires := capturedAt.Add(host.policy.ObservationTTL)
	if expires.After(stored.ExpiresAt) {
		expires = stored.ExpiresAt
	}
	callCtx, cancel := context.WithDeadline(ctx, expires)
	entry.cancel, entry.active = cancel, true
	host.arbiter.mu.Unlock()

	capture, captureErr := host.driver.Capture(callCtx, stored.Binding.Target)
	cancel()
	host.arbiter.mu.Lock()
	defer host.arbiter.mu.Unlock()
	entry.cancel, entry.active = nil, false
	if errors.Is(captureErr, ErrStale) || captureErr == nil && capture.Scope != stored.Binding.Target.Scope {
		// A trusted identity failure invalidates this original lease even when
		// the caller canceled or handed off while capture was in flight. Never
		// let old capture completion revoke a newer lease on the same desktop.
		host.revoke(entry)
	}
	if _, err = host.bound(ctx, stored.Binding); err != nil {
		return Observation{}, err
	}
	if err = entry.session.AgentAuthority(host.now()); err != nil {
		return Observation{}, err
	}
	if captureErr != nil {
		if ctx.Err() != nil {
			return Observation{}, ctx.Err()
		}
		return Observation{}, ErrUnavailable // driver errors can contain protected text
	}
	if len(capture.Screenshot) > MaxCaptureBytes || len(capture.AccessibilityText) > MaxAccessibilityBytes {
		return Observation{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return Observation{}, ErrUnavailable
	}
	now := host.now()
	observation := Observation{
		ID: id, Binding: stored.Binding, Geometry: capture.Geometry, CapturedAt: capturedAt, ExpiresAt: expires,
		ContentType: capture.ContentType, Screenshot: append([]byte(nil), capture.Screenshot...),
		AccessibilityText: capture.AccessibilityText,
	}
	if err = observation.Validate(stored, now); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (host *ReadOnlyHost) Handoff(ctx context.Context, binding Binding) (Session, error) {
	return host.transition(ctx, binding, EventHandoff)
}

func (host *ReadOnlyHost) RequestResume(ctx context.Context, binding Binding) (Session, error) {
	return host.transition(ctx, binding, EventRequestResume)
}

func (host *ReadOnlyHost) Resume(ctx context.Context, binding Binding) (Session, error) {
	return host.transition(ctx, binding, EventResume)
}

func (host *ReadOnlyHost) Release(ctx context.Context, binding Binding) (Session, error) {
	return host.transition(ctx, binding, EventRelease)
}

// Lose is a trusted lifetime invalidation, not part of the model-facing Host
// interface. Loss spans handoff/control generations of this immutable resource
// binding, but cannot revoke another owner, session, worker, policy or scope.
func (host *ReadOnlyHost) Lose(ctx context.Context, binding Binding) (Session, error) {
	if binding.Validate() != nil {
		return Session{}, ErrInvalid
	}
	host.arbiter.mu.Lock()
	defer host.arbiter.mu.Unlock()
	entry, err := host.owned(ctx, binding.Principal, binding.SessionID)
	if err != nil {
		return Session{}, err
	}
	immutable := entry.session.Binding
	immutable.Generation = binding.Generation
	if immutable != binding {
		return Session{}, ErrStale
	}
	host.revoke(entry)
	return entry.session, nil
}

func (host *ReadOnlyHost) transition(ctx context.Context, binding Binding, event ControlEvent) (Session, error) {
	host.arbiter.mu.Lock()
	defer host.arbiter.mu.Unlock()
	entry, err := host.bound(ctx, binding)
	if err != nil {
		return Session{}, err
	}
	if !host.now().Before(entry.session.ExpiresAt) && event != EventRelease {
		host.revoke(entry)
		return Session{}, ErrExpired
	}
	next, err := entry.session.Transition(event, binding.Generation)
	if err != nil {
		return Session{}, err
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	entry.session = next
	if next.Control.Terminal() && host.arbiter.leases[keyFor(binding.Target)] == entry {
		delete(host.arbiter.leases, keyFor(binding.Target))
	}
	return next, nil
}

func (host *ReadOnlyHost) admitted(principal runtimecap.Principal, target Target) bool {
	for _, allowed := range host.policy.Targets {
		if allowed == target {
			return host.policy.Admit(principal, target)
		}
	}
	return false
}

// owned allows status/release of expired leases, but revokes capture authority.
func (host *ReadOnlyHost) owned(
	ctx context.Context, principal runtimecap.Principal, id string,
) (*lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if principal.Validate() != nil || !validIdentifier(id) {
		return nil, ErrInvalid
	}
	entry := host.sessions[id]
	if entry == nil || entry.session.Binding.Principal != principal {
		return nil, ErrDenied
	}
	if !host.admitted(principal, entry.session.Binding.Target) {
		host.revoke(entry)
		return nil, ErrDenied
	}
	if !host.now().Before(entry.session.ExpiresAt) {
		host.revoke(entry)
	}
	return entry, nil
}

func (host *ReadOnlyHost) bound(ctx context.Context, binding Binding) (*lease, error) {
	if binding.Validate() != nil {
		return nil, ErrInvalid
	}
	entry, err := host.owned(ctx, binding.Principal, binding.SessionID)
	if err != nil {
		return nil, err
	}
	if entry.session.Binding != binding {
		return nil, ErrStale
	}
	return entry, nil
}

func (host *ReadOnlyHost) revoke(entry *lease) {
	if !entry.session.Control.Terminal() {
		next, err := entry.session.Transition(EventLose, entry.session.Binding.Generation)
		if err == nil {
			entry.session = next
		} else {
			entry.session.Control = ControlLost
		}
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	key := keyFor(entry.session.Binding.Target)
	if host.arbiter.leases[key] == entry {
		delete(host.arbiter.leases, key)
	}
}

func keyFor(target Target) desktopKey {
	return desktopKey{placement: target.Placement, node: target.Node, desktop: target.Scope.DesktopID}
}

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
