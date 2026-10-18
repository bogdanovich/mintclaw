package computerhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/cua"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

const maxSessions = 128

type windowObserver interface {
	Identity() desktop.WindowIdentity
	Alive(context.Context) bool
	Close() error
}

type captureWorker interface {
	computer.CaptureDriver
	io.Closer
}

type nativeCapture struct {
	guard  windowObserver
	worker captureWorker
	target computer.Target
}

func (driver *nativeCapture) Capture(ctx context.Context, target computer.Target) (computer.Capture, error) {
	if target != driver.target || driver.worker == nil || !driver.guard.Alive(ctx) {
		return computer.Capture{}, computer.ErrStale
	}
	capture, err := driver.worker.Capture(ctx, target)
	if err != nil || !driver.guard.Alive(ctx) {
		// The private worker aborts on transport failure/cancellation. Never
		// retry or replace it while retaining the old session/worker binding.
		return computer.Capture{}, computer.ErrStale
	}
	capture.AccessibilityText = "" // Cua's general native root identity is not qualified.
	return capture, nil
}

type sessionEntry struct {
	gate       chan struct{}
	epoch      uint64
	core       *computer.ReadOnlyHost
	initial    computer.Session
	driver     *nativeCapture
	openCancel context.CancelFunc
	timer      *time.Timer
	retired    atomic.Bool
}

// Host is explicitly constructed local authority. One core Arbiter covers all
// profiles for this GUI user, independently of profile/display labels. It is
// process-local arbitration, not an OS-wide exclusion or an input capability.
type Host struct {
	nodeID   nodes.ID
	profiles map[string]companion.ComputerProfilePolicy
	aliases  []string
	metadata []nodes.ComputerProfileDescriptor
	arbiter  *computer.Arbiter
	openGate chan struct{}
	watch    func(context.Context, desktop.WindowRequest) (windowObserver, error)
	start    func(context.Context, cua.Config) (captureWorker, error)
	mu       sync.Mutex
	sessions map[string]*sessionEntry
	epoch    atomic.Uint64
	closed   bool
	outputs  outputStore
}

func New(nodeID nodes.ID, profiles map[string]companion.ComputerProfilePolicy) (*Host, error) {
	return newHost(nodeID, profiles,
		func(ctx context.Context, request desktop.WindowRequest) (windowObserver, error) {
			return desktop.WatchWindow(ctx, request)
		}, func(ctx context.Context, config cua.Config) (captureWorker, error) { return cua.New(ctx, config) })
}

func newHost(
	nodeID nodes.ID, profiles map[string]companion.ComputerProfilePolicy,
	watch func(context.Context, desktop.WindowRequest) (windowObserver, error),
	start func(context.Context, cua.Config) (captureWorker, error),
) (*Host, error) {
	profiles, err := companion.NormalizeComputerProfiles(profiles, "")
	if err != nil || nodeID.Validate() != nil || !companion.HasEnabledComputerProfile(profiles) || watch == nil ||
		start == nil {
		return nil, computer.ErrInvalid
	}
	host := &Host{
		nodeID: nodeID, profiles: profiles, arbiter: computer.NewArbiter(), openGate: make(chan struct{}, 1),
		watch: watch, start: start, sessions: make(map[string]*sessionEntry),
	}
	for alias, profile := range profiles {
		if profile.Enabled {
			host.aliases = append(host.aliases, alias)
		}
	}
	slices.Sort(host.aliases)
	for _, alias := range host.aliases {
		profile := profiles[alias]
		host.metadata = append(host.metadata, nodes.ComputerProfileDescriptor{
			Alias: alias, Revision: profile.Revision, OS: nativeOS(),
			Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "cua",
		})
	}
	if _, err = nodes.ComputerCommandDescriptors(host.metadata); err != nil {
		return nil, computer.ErrInvalid
	}
	host.outputs.items = make(map[string]*imageOutput)
	return host, nil
}

func nativeOS() computer.OperatingSystem {
	if runtime.GOOS == "darwin" {
		return computer.OSMacOS
	}
	return computer.OSLinux
}

func (host *Host) ComputerProfiles() []nodes.ComputerProfileDescriptor {
	return slices.Clone(host.metadata)
}

func (host *Host) Targets(ctx context.Context, principal runtimecap.Principal) (nodes.ComputerTargetsResult, error) {
	if principal.Validate() != nil {
		return nodes.ComputerTargetsResult{}, computer.ErrDenied
	}
	epoch := host.epoch.Load()
	targets := []computer.Target{}
	for _, alias := range host.aliases {
		profile := host.profiles[alias]
		if !profile.Admits(principal) {
			continue
		}
		guard, err := host.watch(ctx, windowRequest(profile))
		if err != nil {
			return nodes.ComputerTargetsResult{}, safeFailure(err)
		}
		target := host.target(alias, profile, guard.Identity())
		alive := guard.Alive(ctx)
		closeErr := guard.Close()
		if !alive || closeErr != nil || epoch != host.epoch.Load() {
			return nodes.ComputerTargetsResult{}, computer.ErrStale
		}
		targets = append(targets, target)
	}
	host.mu.Lock()
	closed := host.closed
	host.mu.Unlock()
	if closed || ctx.Err() != nil {
		return nodes.ComputerTargetsResult{}, computer.ErrUnavailable
	}
	return nodes.ComputerTargetsResult{Targets: targets, ObservedAt: time.Now()}, nil
}

func windowRequest(profile companion.ComputerProfilePolicy) desktop.WindowRequest {
	return desktop.WindowRequest{
		PID: profile.PID, WindowID: profile.WindowID,
		Display: profile.Environment["DISPLAY"], XAuthority: profile.Environment["XAUTHORITY"],
	}
}

func (host *Host) target(
	alias string,
	profile companion.ComputerProfilePolicy,
	identity desktop.WindowIdentity,
) computer.Target {
	display := sha256.Sum256([]byte(profile.Environment["DISPLAY"] + "\x00" + profile.Environment["XAUTHORITY"]))
	return computer.Target{
		Alias: alias, Placement: computer.PlacementNode, Node: string(host.nodeID), OS: nativeOS(),
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "cua",
		Scope: computer.Scope{
			DesktopID: "gui-user-" + strconv.Itoa(os.Getuid()), DisplayID: hex.EncodeToString(display[:16]),
			WindowID: strconv.FormatUint(uint64(identity.WindowID), 10), WindowGeneration: identity.Generation,
		},
	}
}

func (host *Host) Open(
	ctx context.Context,
	request nodes.ComputerHostOpenRequest,
) (nodes.ComputerSessionResult, error) {
	profile, found := host.profiles[request.Profile]
	if !found || !profile.Enabled || request.ProfileRevision != profile.Revision || !profile.Admits(request.Principal) {
		return nodes.ComputerSessionResult{}, computer.ErrDenied
	}
	epoch := host.epoch.Load()
	if err := acquire(ctx, host.openGate); err != nil {
		return nodes.ComputerSessionResult{}, err
	}
	defer relinquish(host.openGate)
	guard, err := host.watch(ctx, windowRequest(profile))
	if err != nil {
		return nodes.ComputerSessionResult{}, safeFailure(err)
	}
	driver := &nativeCapture{guard: guard, target: host.target(request.Profile, profile, guard.Identity())}
	workerID, err := opaqueID()
	if err != nil {
		_ = guard.Close()
		return nodes.ComputerSessionResult{}, err
	}
	core, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
		WorkerID: workerID, Revision: profile.Revision, Targets: []computer.Target{driver.target},
		LeaseTTL: profile.LeaseTTL(), ObservationTTL: time.Duration(profile.ObservationSeconds) * time.Second,
		Admit: func(principal runtimecap.Principal, _ computer.Target) bool { return profile.Admits(principal) },
	}, driver, host.arbiter)
	if err != nil {
		_ = guard.Close()
		return nodes.ComputerSessionResult{}, err
	}
	session, err := core.Open(ctx, computer.OpenRequest{Principal: request.Principal, Target: driver.target})
	if err != nil {
		_ = guard.Close()
		return nodes.ComputerSessionResult{}, err
	}
	startup, cancel := context.WithCancel(ctx)
	entry := &sessionEntry{
		gate: make(chan struct{}, 1), epoch: epoch, core: core, initial: session, driver: driver, openCancel: cancel,
	}
	entry.gate <- struct{}{}
	defer relinquish(entry.gate)
	completed := false
	defer func() {
		cancel()
		if !completed {
			_, _ = core.Lose(context.Background(), session.Binding)
			_ = host.closeEntry(entry)
		}
	}()
	host.mu.Lock()
	for id, previous := range host.sessions {
		if len(host.sessions) >= maxSessions && previous.retired.Load() {
			delete(host.sessions, id)
		}
	}
	admitted := !host.closed && epoch == host.epoch.Load() && len(host.sessions) < maxSessions
	if admitted {
		host.sessions[session.Binding.SessionID] = entry
	}
	host.mu.Unlock()
	if !admitted {
		return nodes.ComputerSessionResult{}, computer.ErrStale
	}
	driver.worker, err = host.start(startup, cua.Config{
		Executable: profile.Executable, SHA256: profile.SHA256, PID: profile.PID, Env: profile.Environment,
		Target: driver.target,
		ScopeAlive: func(check context.Context, scope computer.Scope) bool {
			return scope == driver.target.Scope && epoch == host.epoch.Load() && guard.Alive(check)
		},
	})
	if err != nil || ctx.Err() != nil || epoch != host.epoch.Load() || !guard.Alive(ctx) {
		return nodes.ComputerSessionResult{}, computer.ErrStale
	}
	if session.AgentAuthority(time.Now()) != nil {
		return nodes.ComputerSessionResult{}, computer.ErrExpired
	}
	entry.timer = time.AfterFunc(time.Until(session.ExpiresAt), func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, _ = core.Lose(cleanup, session.Binding)
		if acquire(cleanup, entry.gate) == nil {
			defer relinquish(entry.gate)
			_ = host.closeEntry(entry)
		}
	})
	completed = true
	return nodes.ComputerSessionResult{Session: session, ObservedAt: time.Now()}, nil
}

func (host *Host) locked(ctx context.Context, request nodes.ComputerHostStatusRequest) (*sessionEntry, error) {
	if request.Principal.Validate() != nil {
		return nil, computer.ErrDenied
	}
	host.mu.Lock()
	entry := host.sessions[request.SessionID]
	host.mu.Unlock()
	if entry == nil || entry.initial.Binding.Principal != request.Principal {
		return nil, computer.ErrDenied
	}
	if err := acquire(ctx, entry.gate); err != nil {
		return nil, err
	}
	if entry.epoch != host.epoch.Load() {
		relinquish(entry.gate)
		return nil, computer.ErrStale
	}
	return entry, nil
}

func (host *Host) refresh(ctx context.Context, entry *sessionEntry) (computer.Session, error) {
	if err := ctx.Err(); err != nil {
		return computer.Session{}, err
	}
	session, err := entry.core.Status(ctx, entry.initial.Binding.Principal, entry.initial.Binding.SessionID)
	if err != nil {
		return computer.Session{}, err
	}
	if !session.Control.Terminal() && !entry.driver.guard.Alive(ctx) && ctx.Err() == nil {
		session, err = entry.core.Lose(ctx, entry.initial.Binding)
	}
	if session.Control.Terminal() {
		// Revoked control alone does not confirm worker/observer destruction.
		// Status may reconcile owned cleanup, but must retain an unresolved error.
		err = errors.Join(err, host.closeEntry(entry))
	}
	if err == nil && entry.epoch != host.epoch.Load() {
		err = computer.ErrStale
	}
	return session, err
}

func (host *Host) Status(
	ctx context.Context,
	request nodes.ComputerHostStatusRequest,
) (nodes.ComputerSessionResult, error) {
	entry, err := host.locked(ctx, request)
	if err != nil {
		return nodes.ComputerSessionResult{}, err
	}
	defer relinquish(entry.gate)
	session, err := host.refresh(ctx, entry)
	return nodes.ComputerSessionResult{Session: session, ObservedAt: time.Now()}, err
}

func (host *Host) Control(
	ctx context.Context,
	request nodes.ComputerHostControlRequest,
) (nodes.ComputerSessionResult, error) {
	entry, err := host.locked(ctx, request.ComputerHostStatusRequest)
	if err != nil {
		return nodes.ComputerSessionResult{}, err
	}
	defer relinquish(entry.gate)
	// Release does not inspect a user's application or perform a fresh capture.
	current, err := entry.core.Status(ctx, request.Principal, request.SessionID)
	if err != nil || current.Binding.Generation != request.Generation {
		return nodes.ComputerSessionResult{}, computer.ErrStale
	}
	if request.Event != computer.EventRelease {
		current, err = host.refresh(ctx, entry)
		if err != nil || current.Binding.Generation != request.Generation {
			return nodes.ComputerSessionResult{}, computer.ErrStale
		}
	}
	var next computer.Session
	switch request.Event {
	case computer.EventHandoff:
		next, err = entry.core.Handoff(ctx, current.Binding)
	case computer.EventRequestResume:
		next, err = entry.core.RequestResume(ctx, current.Binding)
	case computer.EventResume:
		next, err = entry.core.Resume(ctx, current.Binding)
	case computer.EventRelease:
		next, err = entry.core.Release(ctx, current.Binding)
	default:
		return nodes.ComputerSessionResult{}, computer.ErrDenied
	}
	if err != nil {
		return nodes.ComputerSessionResult{}, err
	}
	host.outputs.removeSession(request.SessionID)
	if next.Control.Terminal() {
		if err = host.closeEntry(entry); err != nil {
			return nodes.ComputerSessionResult{}, errors.Join(companion.ErrInvocationOutcomeUnknown, err)
		}
	}
	if entry.epoch != host.epoch.Load() {
		return nodes.ComputerSessionResult{}, errors.Join(companion.ErrInvocationOutcomeUnknown, computer.ErrStale)
	}
	return nodes.ComputerSessionResult{Session: next, ObservedAt: time.Now()}, nil
}

func (host *Host) Observe(
	ctx context.Context,
	request nodes.ComputerHostObserveRequest,
) (nodes.ComputerObservationResult, error) {
	entry, err := host.locked(ctx, request.ComputerHostStatusRequest)
	if err != nil {
		return nodes.ComputerObservationResult{}, err
	}
	defer relinquish(entry.gate)
	session, err := host.refresh(ctx, entry)
	if err != nil || session.Binding.Generation != request.Generation {
		return nodes.ComputerObservationResult{}, computer.ErrStale
	}
	host.outputs.removeSession(request.SessionID)
	observation, err := entry.core.Observe(ctx, session)
	if err != nil {
		_, _ = host.refresh(context.Background(), entry)
		return nodes.ComputerObservationResult{}, safeFailure(err)
	}
	if entry.epoch != host.epoch.Load() || ctx.Err() != nil {
		clear(observation.Screenshot)
		return nodes.ComputerObservationResult{}, computer.ErrStale
	}
	defer clear(observation.Screenshot)
	descriptor, err := host.outputs.register(observation, request)
	if err != nil {
		return nodes.ComputerObservationResult{}, err
	}
	observation.Screenshot = nil
	text := observation.AccessibilityText
	observation.AccessibilityText = ""
	result := nodes.ComputerObservationResult{
		Session:           session,
		Observation:       observation,
		ObservedAt:        time.Now(),
		AccessibilityText: text,
		Output:            &descriptor,
	}
	if result.Validate(host.metadata) != nil {
		host.outputs.removeSession(request.SessionID)
		return nodes.ComputerObservationResult{}, computer.ErrStale
	}
	return result, nil
}

func (host *Host) closeEntry(entry *sessionEntry) error {
	if entry.retired.Load() {
		return nil
	}
	host.outputs.removeSession(entry.initial.Binding.SessionID)
	entry.openCancel()
	if entry.timer != nil {
		entry.timer.Stop()
	}
	var workerErr error
	if entry.driver.worker != nil {
		workerErr = entry.driver.worker.Close()
	}
	guardErr := entry.driver.guard.Close()
	if workerErr != nil || guardErr != nil {
		return computer.ErrUnavailable
	}
	entry.retired.Store(true)
	return nil
}

func (host *Host) Disconnect(ctx context.Context) error { return host.disconnect(ctx, false) }

func (host *Host) Shutdown(ctx context.Context) error { return host.disconnect(ctx, true) }

func (host *Host) disconnect(ctx context.Context, shutdown bool) error {
	host.mu.Lock()
	if shutdown {
		host.closed = true
	}
	epoch := host.epoch.Add(1)
	entries := make([]*sessionEntry, 0, len(host.sessions))
	for _, entry := range host.sessions {
		if entry.epoch < epoch {
			entries = append(entries, entry)
			entry.openCancel()
		}
	}
	host.mu.Unlock()
	var cleanupErr error
	for _, entry := range entries {
		// Invalidate before waiting for an in-flight capture's serialization
		// gate, so disconnect promptly cancels and fences that capture.
		_, _ = entry.core.Lose(context.Background(), entry.initial.Binding)
		if err := acquire(ctx, entry.gate); err != nil {
			cleanupErr = computer.ErrUnavailable
			continue
		}
		if err := host.closeEntry(entry); err != nil {
			cleanupErr = err
		}
		relinquish(entry.gate)
	}
	return cleanupErr
}

func acquire(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func relinquish(gate chan struct{}) { <-gate }

func opaqueID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", computer.ErrUnavailable
	}
	return hex.EncodeToString(value[:]), nil
}

func safeFailure(err error) error {
	for _, known := range []error{
		computer.ErrDenied, computer.ErrInvalid, computer.ErrStale, computer.ErrBusy, computer.ErrExpired,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return computer.ErrUnavailable
}
