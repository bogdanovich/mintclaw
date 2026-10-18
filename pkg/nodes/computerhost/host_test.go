package computerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/cua"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type fixtureApplication struct {
	alive       atomic.Bool
	incarnation atomic.Uint64
	watches     atomic.Uint64
	starts      atomic.Int32
	captures    atomic.Int32
	mu          sync.Mutex
	workers     []*fixtureWorker
	capture     func(context.Context, computer.Target) (computer.Capture, error)
}

type fixtureWindow struct {
	app         *fixtureApplication
	incarnation uint64
	identity    desktop.WindowIdentity
	closed      atomic.Bool
}

func (window *fixtureWindow) Identity() desktop.WindowIdentity { return window.identity }
func (window *fixtureWindow) Alive(ctx context.Context) bool {
	return ctx.Err() == nil && !window.closed.Load() && window.app.alive.Load() &&
		window.app.incarnation.Load() == window.incarnation
}
func (window *fixtureWindow) Close() error { window.closed.Store(true); return nil }

type fixtureWorker struct {
	app       *fixtureApplication
	closed    atomic.Bool
	done      chan struct{}
	failClose atomic.Int32
}

func (worker *fixtureWorker) Capture(ctx context.Context, target computer.Target) (computer.Capture, error) {
	if worker.closed.Load() {
		return computer.Capture{}, computer.ErrUnavailable
	}
	worker.app.captures.Add(1)
	return worker.app.capture(ctx, target)
}

func (worker *fixtureWorker) Close() error {
	if worker.failClose.CompareAndSwap(1, 0) {
		return errors.New("PRIVATE_NATIVE_CLOSE_CANARY")
	}
	if worker.closed.CompareAndSwap(false, true) {
		close(worker.done)
	}
	return nil
}

func nativeHostFixture(t *testing.T, mutate func(*companion.ComputerProfilePolicy)) (*Host, *fixtureApplication) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native host policy supports Linux and macOS")
	}
	profile := companion.ComputerProfilePolicy{
		Enabled: true, Executable: "/explicit/pinned-driver", SHA256: strings.Repeat("a", 64), PID: 123, WindowID: 456,
		AllowedActors: []string{"actor"}, AllowedAgents: []string{"agent"},
		AllowedRuntimes: []runtimecap.Kind{runtimecap.KindGateway}, LeaseSeconds: 300, ObservationSeconds: 15,
	}
	if runtime.GOOS == "linux" {
		profile.Environment = map[string]string{"DISPLAY": ":1", "XAUTHORITY": "/explicit/authority"}
	}
	if mutate != nil {
		mutate(&profile)
	}
	app := &fixtureApplication{}
	app.alive.Store(true)
	app.incarnation.Store(1)
	pixels := fixturePNG(t)
	app.capture = func(_ context.Context, target computer.Target) (computer.Capture, error) {
		return computer.Capture{
			Scope: target.Scope, Geometry: computer.Geometry{Width: 120, Height: 80, Scale: 1},
			ContentType: "image/png", Screenshot: bytes.Clone(pixels), AccessibilityText: "UNSOLICITED_AX_CANARY",
		}, nil
	}
	host, err := newHost("fixture-node", map[string]companion.ComputerProfilePolicy{"fixture": profile},
		func(ctx context.Context, request desktop.WindowRequest) (windowObserver, error) {
			if ctx.Err() != nil || !app.alive.Load() {
				return nil, computer.ErrUnavailable
			}
			return &fixtureWindow{app: app, incarnation: app.incarnation.Load(), identity: desktop.WindowIdentity{
				PID: request.PID, WindowID: request.WindowID, Generation: app.watches.Add(1),
			}}, nil
		}, func(_ context.Context, config cua.Config) (captureWorker, error) {
			if config.Target.Scope.WindowID != "456" || !config.ScopeAlive(t.Context(), config.Target.Scope) {
				return nil, computer.ErrStale
			}
			app.starts.Add(1)
			worker := &fixtureWorker{app: app, done: make(chan struct{})}
			app.mu.Lock()
			app.workers = append(app.workers, worker)
			app.mu.Unlock()
			return worker, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if app.watches.Load() != 0 || app.starts.Load() != 0 {
		t.Fatal("constructing host inspected the native desktop")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			t.Error("fixture host cleanup failed", err)
		}
		if !app.alive.Load() {
			t.Error("host cleanup changed fixture application lifetime")
		}
	})
	return host, app
}

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	img.Set(0, 0, color.RGBA{R: 123, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func fixturePrincipal() runtimecap.Principal {
	return runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "actor", AgentID: "agent", SessionID: "thread", ExecutionID: "turn",
	}
}

func openFixture(t *testing.T, host *Host) computer.Session {
	t.Helper()
	opened, err := host.Open(t.Context(), nodes.ComputerHostOpenRequest{
		Principal: fixturePrincipal(), Profile: "fixture", ProfileRevision: host.ComputerProfiles()[0].Revision,
		InvocationID: "open-invocation",
	})
	if err != nil {
		t.Fatal(err)
	}
	return opened.Session
}

func observeFixture(
	t *testing.T,
	host *Host,
	session computer.Session,
	invocation string,
) nodes.ComputerObservationResult {
	t.Helper()
	observed, err := host.Observe(t.Context(), nodes.ComputerHostObserveRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: session.Binding.Principal, SessionID: session.Binding.SessionID,
		},
		Generation: session.Binding.Generation, InvocationID: invocation, WorkspaceID: "workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Validate(host.ComputerProfiles()) != nil || observed.Output == nil ||
		observed.AccessibilityText != "" || len(observed.Observation.Screenshot) != 0 {
		t.Fatal("native observation escaped its protected pixel-only boundary")
	}
	return observed
}

func controlFixture(t *testing.T, host *Host, session computer.Session, event computer.ControlEvent) computer.Session {
	t.Helper()
	result, err := host.Control(t.Context(), nodes.ComputerHostControlRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: session.Binding.Principal, SessionID: session.Binding.SessionID,
		},
		Generation: session.Binding.Generation, Event: event,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Session
}

func TestNativeHostDiscoveryAndSameResourceLifecycle(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	targets, err := host.Targets(t.Context(), fixturePrincipal())
	if err != nil || len(targets.Targets) != 1 || app.starts.Load() != 0 || app.captures.Load() != 0 {
		t.Fatal("discovery launched a worker or captured content", err)
	}
	session := openFixture(t, host)
	first := observeFixture(t, host, session, "observe-1")
	human := controlFixture(t, host, session, computer.EventHandoff)
	if _, err = host.Observe(t.Context(), nodes.ComputerHostObserveRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: fixturePrincipal(), SessionID: human.Binding.SessionID,
		},
		Generation: human.Binding.Generation, InvocationID: "while-human", WorkspaceID: "workspace",
	}); !errors.Is(err, computer.ErrDenied) {
		t.Fatal("human-owned session was observed", err)
	}
	pending := controlFixture(t, host, human, computer.EventRequestResume)
	resumed := controlFixture(t, host, pending, computer.EventResume)
	second := observeFixture(t, host, resumed, "observe-2")
	if first.Observation.ID == second.Observation.ID || first.Output.TransferID == second.Output.TransferID ||
		first.Session.Binding.SessionID != resumed.Binding.SessionID ||
		first.Session.Binding.WorkerID != resumed.Binding.WorkerID || first.Session.Binding.Target != resumed.Binding.Target ||
		app.watches.Load() != 2 || app.starts.Load() != 1 || app.captures.Load() != 2 {
		t.Fatal("resume replaced native authority or reused pre-handoff evidence")
	}
	released := controlFixture(t, host, resumed, computer.EventRelease)
	if released.Control != computer.ControlReleased || !app.alive.Load() || !app.workers[0].closed.Load() {
		t.Fatal("release did not close only automation resources")
	}
	again := controlFixture(t, host, released, computer.EventRelease)
	if again != released {
		t.Fatal("release was not idempotent")
	}
	replacement := openFixture(t, host)
	if replacement.Binding.SessionID == session.Binding.SessionID ||
		replacement.Binding.WorkerID == session.Binding.WorkerID ||
		replacement.Binding.Target.Scope.WindowGeneration == session.Binding.Target.Scope.WindowGeneration {
		t.Fatal("explicit new admission recycled immutable authority")
	}
}

func TestNativeHostOwnershipAndTypedControl(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	for name, mutate := range map[string]func(*runtimecap.Principal){
		"actor":     func(p *runtimecap.Principal) { p.ActorID = "other" },
		"agent":     func(p *runtimecap.Principal) { p.AgentID = "other" },
		"thread":    func(p *runtimecap.Principal) { p.SessionID = "other" },
		"execution": func(p *runtimecap.Principal) { p.ExecutionID = "other" },
		"runtime":   func(p *runtimecap.Principal) { p.Runtime = runtimecap.KindCoding },
	} {
		t.Run(name, func(t *testing.T) {
			owner := fixturePrincipal()
			mutate(&owner)
			if _, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
				Principal: owner, SessionID: session.Binding.SessionID,
			}); !errors.Is(err, computer.ErrDenied) {
				t.Fatal("cross-principal status accepted", err)
			}
		})
	}
	if _, err := host.Control(t.Context(), nodes.ComputerHostControlRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
		},
		Generation: session.Binding.Generation, Event: computer.EventLose,
	}); !errors.Is(err, computer.ErrDenied) {
		t.Fatal("caller dispatched trusted lifetime invalidation", err)
	}
	if _, err := host.Open(t.Context(), nodes.ComputerHostOpenRequest{
		Principal: fixturePrincipal(), Profile: "fixture", ProfileRevision: "forged", InvocationID: "other",
	}); !errors.Is(err, computer.ErrDenied) || app.starts.Load() != 1 {
		t.Fatal("wrong policy started a worker", err)
	}
}

func TestNativeHostWindowLossDuringHumanControl(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	original := openFixture(t, host)
	human := controlFixture(t, host, original, computer.EventHandoff)
	app.incarnation.Add(1) // Same numeric PID/window, different native incarnation.
	status, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
		Principal: fixturePrincipal(), SessionID: human.Binding.SessionID,
	})
	if err != nil || status.Session.Control != computer.ControlLost || !app.workers[0].closed.Load() {
		t.Fatal("human-owned destroyed window was silently reacquired", err)
	}
	replacement := openFixture(t, host)
	if replacement.Binding.Target.Scope.WindowGeneration == original.Binding.Target.Scope.WindowGeneration {
		t.Fatal("replacement native admission reused scope")
	}
}

func TestNativeHostDisconnectFencesInflightCapture(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	started := make(chan struct{})
	app.capture = func(ctx context.Context, _ computer.Target) (computer.Capture, error) {
		close(started)
		<-ctx.Done()
		return computer.Capture{}, errors.New("PRIVATE_DRIVER_ERROR")
	}
	session := openFixture(t, host)
	done := make(chan error, 1)
	go func() {
		_, err := host.Observe(t.Context(), nodes.ComputerHostObserveRequest{
			ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
				Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
			},
			Generation: session.Binding.Generation, InvocationID: "capture", WorkspaceID: "workspace",
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := host.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, computer.ErrStale) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("disconnect returned private capture content/error", err)
	}
	if _, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
		Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
	}); !errors.Is(err, computer.ErrStale) {
		t.Fatal("reconnected caller reacquired old session", err)
	}
	replacement := openFixture(t, host)
	if replacement.Binding.WorkerID == session.Binding.WorkerID || !app.workers[0].closed.Load() {
		t.Fatal("disconnect did not retire original worker")
	}
}

func TestNativeHostLeaseExpiryPreservesApplication(t *testing.T) {
	host, app := nativeHostFixture(t, func(profile *companion.ComputerProfilePolicy) {
		profile.LeaseSeconds, profile.ObservationSeconds = 1, 1
	})
	session := openFixture(t, host)
	select {
	case <-app.workers[0].done:
	case <-time.After(3 * time.Second):
		t.Fatal("expired lease retained its worker")
	}
	status, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
		Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
	})
	if err != nil || status.Session.Control != computer.ControlLost || !app.alive.Load() {
		t.Fatal("expiry did not revoke only automation", err)
	}
}

func TestNativeHostDisconnectRevokesEvenWhenCleanupContextIsCanceled(t *testing.T) {
	host, _ := nativeHostFixture(t, nil)
	original := openFixture(t, host)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := host.Disconnect(ctx); err == nil {
		t.Fatal("canceled cleanup was reported complete")
	}
	replacement := openFixture(t, host)
	if replacement.Binding.SessionID == original.Binding.SessionID {
		t.Fatal("old canceled-cleanup lease blocked or replaced new authority")
	}
}

func TestNativeHostUncertainReleaseIsNotReexecutedOrLogged(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	app.workers[0].failClose.Store(1)
	ledgerPath := companion.InvocationLedgerPath(t.TempDir())
	ledger, err := companion.NewFileInvocationLedger(
		ledgerPath, companion.DefaultInvocationLedgerLimit,
		companion.DefaultInvocationLedgerBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	commandRuntime, err := companion.NewRuntime("fixture-node", "fixture", nodes.LocalCommandPolicy{
		Revision: "test-policy", AllowedCommands: []string{nodes.ComputerCommandRelease},
		MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 10, MaxOutputBytes: 64 * 1024,
	}, ledger, companion.WithComputerHost(host))
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := nodes.ComputerCommandDescriptors(host.ComputerProfiles())
	if err != nil {
		t.Fatal(err)
	}
	var release nodes.CommandDescriptor
	for _, descriptor := range descriptors {
		if descriptor.Name == nodes.ComputerCommandRelease {
			release = descriptor
		}
	}
	input, err := json.Marshal(nodes.ComputerControlInput{
		ComputerTurn: nodes.ComputerTurn{
			Runtime:     fixturePrincipal().Runtime,
			ExecutionID: fixturePrincipal().ExecutionID,
		},
		SessionID:  session.Binding.SessionID,
		Generation: session.Binding.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := commandRuntime.Catalog()
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	profile := commandRuntime.ExecutionProfile()
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "uncertain-release", IdempotencyKey: "uncertain-release", NodeID: "fixture-node",
		CatalogHash: catalogHash, Command: release.Name, Input: input,
		ActorID: "actor", AgentID: "agent", SessionID: "thread", TimeoutSeconds: 10, OutputLimitBytes: 64 * 1024,
	}, release, profile.Executor, profile.PolicyRevision, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = commandRuntime.Invoke(t.Context(), plan); !errors.Is(err, companion.ErrInvocationOutcomeUnknown) ||
			strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("uncertain release was replayed or leaked details", err)
		}
	}
	if app.workers[0].closed.Load() {
		t.Fatal("unknown invocation was reexecuted")
	}
	durable, err := os.ReadFile(ledgerPath)
	if err != nil || bytes.Contains(durable, []byte("PRIVATE_NATIVE_CLOSE_CANARY")) {
		t.Fatal("uncertain cleanup persisted private provider error", err)
	}
	status, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
		Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
	})
	if err != nil || status.Session.Control != computer.ControlReleased || !app.workers[0].closed.Load() {
		t.Fatal("fresh status did not reconcile known authority and retry only owned cleanup", err)
	}
}
