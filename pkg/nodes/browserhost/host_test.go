package browserhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	browserworker "github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

type fakeBrowserHostFactory struct {
	worker   browserworker.Worker
	err      error
	requests []browserworker.WorkerOpenRequest
}

type blockingBrowserHostFactory struct {
	worker  browserworker.Worker
	started chan struct{}
	release chan struct{}
}

func (factory *blockingBrowserHostFactory) Open(
	ctx context.Context,
	_ browserworker.WorkerOpenRequest,
) (browserworker.WorkerOpenResult, error) {
	close(factory.started)
	select {
	case <-ctx.Done():
		return browserworker.WorkerOpenResult{Owner: factory.worker}, ctx.Err()
	case <-factory.release:
		return browserworker.WorkerOpenResult{Owner: factory.worker}, nil
	}
}

func (factory *fakeBrowserHostFactory) Open(
	_ context.Context,
	request browserworker.WorkerOpenRequest,
) (browserworker.WorkerOpenResult, error) {
	factory.requests = append(factory.requests, request)
	return browserworker.WorkerOpenResult{Owner: factory.worker}, factory.err
}

type fakeBrowserHostWorker struct {
	status       browserworker.WorkerStatus
	statusErr    error
	observations []browserworker.DriverObservation
	observeCalls int
	actions      []browserworker.DriverAction
	executeErr   error
	executeFunc  func(context.Context, browserworker.DriverAction) error
	closeErr     error
	closeCalls   int
}

func (worker *fakeBrowserHostWorker) Status(context.Context) (browserworker.WorkerStatus, error) {
	return worker.status, worker.statusErr
}

func (worker *fakeBrowserHostWorker) Close(context.Context) error {
	worker.closeCalls++
	return worker.closeErr
}

func (worker *fakeBrowserHostWorker) Observe(context.Context) (browserworker.DriverObservation, error) {
	if worker.observeCalls >= len(worker.observations) {
		return browserworker.DriverObservation{}, errors.New("unexpected observe")
	}
	observation := worker.observations[worker.observeCalls]
	worker.observeCalls++
	return observation, nil
}

func (*fakeBrowserHostWorker) Resolve(
	context.Context,
	string,
) (browserworker.DriverElement, string, error) {
	return browserworker.DriverElement{}, "", browserworker.ErrStale
}

func (worker *fakeBrowserHostWorker) Execute(
	ctx context.Context,
	action browserworker.DriverAction,
) error {
	worker.actions = append(worker.actions, action)
	if worker.executeFunc != nil {
		return worker.executeFunc(ctx, action)
	}
	return worker.executeErr
}

func (*fakeBrowserHostWorker) CatalogRevision() string { return "driver-v1" }

func TestBrowserHostReusesWorkerForTypedLifecycle(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
			{
				URL: "https://example.com/", Origin: "https://example.com",
				Title: "Example", Snapshot: "- link \"More\" [ref=e1]",
				Elements: []browserworker.DriverElement{{Target: "e1", Role: "link", Name: "More"}},
			},
		},
	}
	factory := &fakeBrowserHostFactory{worker: worker}
	host := newTestBrowserHost(t, factory)

	opened, err := host.Open(t.Context(), browserHostOpenFixture())
	if err != nil || opened.SessionID != "browser_session_1" || opened.State != "ready" ||
		opened.TabID != "tab_primary" || !opened.Features.Navigate || opened.Features.Download {
		t.Fatalf("Open() = %#v, %v", opened, err)
	}
	rawOpened, err := json.Marshal(opened)
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := nodes.BrowserCommandDescriptors(host.BrowserProfiles())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nodes.ValidateInvocationOutput(
		descriptors[0], rawOpened, nodes.BrowserLimits{}.Effective().ToolResultBytes,
	); err != nil {
		t.Fatalf("Open() output violates its typed command schema: %v", err)
	}
	if len(factory.requests) != 1 || factory.requests[0].Target != companionBrowserTarget ||
		factory.requests[0].Profile != nodes.BrowserProfileManaged {
		t.Fatalf("worker open requests = %#v", factory.requests)
	}

	initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || initial.SnapshotGeneration != 1 || initial.URL != "about:blank" {
		t.Fatalf("initial Observe() = %#v, %v", initial, err)
	}

	navigated, err := host.Navigate(t.Context(), BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_1",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || navigated.SnapshotGeneration != 2 || navigated.Title != "Example" ||
		len(navigated.Elements) != 1 || navigated.Elements[0].Ref == "e1" ||
		strings.Contains(navigated.Snapshot, "[ref=e1]") {
		t.Fatalf("Navigate() = %#v, %v", navigated, err)
	}
	if len(worker.actions) != 1 || worker.actions[0].Kind != browserworker.DriverNavigate ||
		worker.actions[0].URL != "https://example.com/" {
		t.Fatalf("driver actions = %#v", worker.actions)
	}
	replayed := browserHostNavigateFixture()
	replayed.SnapshotGeneration = 2
	if _, err = host.Navigate(t.Context(), replayed); !errors.Is(err, ErrBrowserHostStale) ||
		len(worker.actions) != 1 {
		t.Fatalf("replayed successful Navigate() error = %v, actions = %d", err, len(worker.actions))
	}
	replayed.PreparedActionHash = strings.Repeat("c", 64)
	if _, err = host.Navigate(t.Context(), replayed); !errors.Is(err, ErrBrowserHostDenied) ||
		len(worker.actions) != 1 {
		t.Fatalf("rebound Navigate() error = %v, actions = %d", err, len(worker.actions))
	}

	_, err = host.Navigate(t.Context(), BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_2",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/stale"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("c", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if !errors.Is(err, ErrBrowserHostStale) || len(worker.actions) != 1 {
		t.Fatalf("stale Navigate() error = %v, actions = %d", err, len(worker.actions))
	}

	closed, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 1 {
		t.Fatalf("Close() = %#v, %v, calls = %d", closed, err, worker.closeCalls)
	}
	closed, err = host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 1 {
		t.Fatalf("repeated Close() = %#v, %v, calls = %d", closed, err, worker.closeCalls)
	}
}

func TestBrowserHostExecutesBoundedScrollWithReadEffect(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank", Snapshot: "after scroll"},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := host.Scroll(t.Context(), BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_scroll_1",
		Action:             nodes.BrowserAction{Kind: "scroll", Direction: "down", Amount: 2},
		Effect:             "read", CurrentOrigin: "about:blank",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", RoutedSessionID: "routed_session_1",
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || result.SnapshotGeneration != 2 || result.Snapshot != "after scroll" {
		t.Fatalf("Scroll() = %#v, %v", result, err)
	}
	if len(worker.actions) != 1 || worker.actions[0] != (browserworker.DriverAction{
		Kind: browserworker.DriverScroll, Direction: "down", Amount: 2,
	}) {
		t.Fatalf("driver actions = %#v", worker.actions)
	}
}

func TestBrowserHostExecutesOnlyAttestedSemanticallyFreshClick(t *testing.T) {
	newFixture := func(t *testing.T, dryRun bool) (*BrowserHost, *fakeBrowserHostWorker, BrowserHostNavigateRequest) {
		t.Helper()
		element := browserworker.DriverElement{Target: "driver_ref_1", Role: "button", Name: "Save"}
		observation := browserworker.DriverObservation{
			URL: "https://example.com/", Origin: "https://example.com", Title: "Fixture",
			Snapshot: "- button Save", Elements: []browserworker.DriverElement{element},
		}
		worker := &fakeBrowserHostWorker{
			status:       browserworker.WorkerReady,
			observations: []browserworker.DriverObservation{observation, observation, observation},
		}
		profile := browserHostProfileFixture()
		profile.AllowedActions = []string{"click", "download", "navigate", "scroll"}
		profile.DryRun = dryRun
		profile.AllowApprovedActions = !dryRun
		host, err := newBrowserHost(
			map[string]companion.BrowserProfilePolicy{"managed": profile},
			map[string]browserHostFactory{"managed": &fakeBrowserHostFactory{worker: worker}},
		)
		if err != nil {
			t.Fatal(err)
		}
		host.now = func() time.Time { return time.Unix(100, 0).UTC() }
		host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
		open := browserHostOpenFixture()
		open.DryRun = dryRun
		if _, err = host.Open(t.Context(), open); err != nil {
			t.Fatal(err)
		}
		initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		})
		if err != nil || len(initial.Elements) != 1 || initial.Elements[0].Ref == "driver_ref_1" ||
			strings.Contains(initial.Snapshot, "driver_ref_1") {
			t.Fatalf("initial safe observation = %#v, %v", initial, err)
		}
		request := BrowserHostNavigateRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_click_1",
			Action:             nodes.BrowserAction{Kind: "click", Ref: initial.Elements[0].Ref},
			Effect:             "external_commit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", ExpectedRole: "button", ExpectedName: "Save",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		}
		request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
		if err != nil {
			t.Fatal(err)
		}
		return host, worker, request
	}

	t.Run("success", func(t *testing.T) {
		host, worker, request := newFixture(t, false)
		result, err := host.Click(t.Context(), request)
		if err != nil || result.SnapshotGeneration != 2 {
			t.Fatalf("Click() = %#v, %v", result, err)
		}
		if len(worker.actions) != 1 || worker.actions[0] != (browserworker.DriverAction{
			Kind: browserworker.DriverClick, Target: "driver_ref_1", Element: "Save",
		}) {
			t.Fatalf("driver actions = %#v", worker.actions)
		}
	})

	t.Run("semantic drift", func(t *testing.T) {
		host, worker, request := newFixture(t, false)
		request.ExpectedName = "Delete"
		var err error
		request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.Click(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
			t.Fatalf("Click() semantic drift error = %v, want stale", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("semantic drift dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("duplicate target", func(t *testing.T) {
		host, worker, request := newFixture(t, false)
		worker.observations[1].Elements = append(
			worker.observations[1].Elements,
			browserworker.DriverElement{Target: "driver_ref_1", Role: "button", Name: "Save"},
		)
		if _, err := host.Click(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
			t.Fatalf("Click() duplicate target error = %v, want stale", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("duplicate target dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		host, worker, request := newFixture(t, false)
		request.ApprovalDigest = strings.Repeat("f", 64)
		if _, err := host.Click(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Click() digest mismatch error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("digest mismatch dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		host, worker, request := newFixture(t, true)
		if _, err := host.Click(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Click() dry-run error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("dry-run click dispatched %d actions", len(worker.actions))
		}
	})
}

func TestBrowserHostEnforcesLocalPrincipalLimitsAndSingleSession(t *testing.T) {
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerReady}
	factory := &fakeBrowserHostFactory{worker: worker}
	host := newTestBrowserHost(t, factory)

	denied := browserHostOpenFixture()
	denied.ActorID = "telegram:intruder"
	if _, err := host.Open(t.Context(), denied); !errors.Is(err, ErrBrowserHostDenied) ||
		len(factory.requests) != 0 {
		t.Fatalf("unauthorized Open() error = %v, requests = %d", err, len(factory.requests))
	}
	expanded := browserHostOpenFixture()
	expanded.Limits.Tabs++
	if _, err := host.Open(t.Context(), expanded); !errors.Is(err, ErrBrowserHostDenied) ||
		len(factory.requests) != 0 {
		t.Fatalf("expanded Open() error = %v, requests = %d", err, len(factory.requests))
	}
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	stolen := browserHostOpenFixture()
	stolen.BrowserPolicyRevision = strings.Repeat("d", 64)
	if _, err := host.Open(t.Context(), stolen); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-policy duplicate Open() error = %v", err)
	}
	stolen = browserHostOpenFixture()
	stolen.RoutedSessionID = "routed_session_2"
	if _, err := host.Open(t.Context(), stolen); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-session duplicate Open() error = %v", err)
	}
	second := browserHostOpenFixture()
	second.SessionID = "browser_session_2"
	if _, err := host.Open(t.Context(), second); !errors.Is(err, ErrBrowserHostBusy) ||
		len(factory.requests) != 1 {
		t.Fatalf("concurrent Open() error = %v, requests = %d", err, len(factory.requests))
	}
}

func TestBrowserHostBindsEveryCommandToRoutedSession(t *testing.T) {
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerReady}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	status := BrowserHostStatusRequest{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_2",
		ProfileRevision: "managed-v1", AgentID: "browser", ActorID: "telegram:owner",
	}
	if _, err := host.Status(t.Context(), status); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-session Status() error = %v", err)
	}
	if _, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_2",
		TabID: "tab_primary", SnapshotGeneration: 1,
		AgentID: "browser", ActorID: "telegram:owner",
	}); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-session Observe() error = %v", err)
	}
	navigate := browserHostNavigateFixture()
	navigate.RoutedSessionID = "routed_session_2"
	if _, err := host.Navigate(t.Context(), navigate); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-session Navigate() error = %v", err)
	}
	if _, err := host.Close(t.Context(), status); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("cross-session Close() error = %v", err)
	}
	if len(worker.actions) != 0 || worker.closeCalls != 0 {
		t.Fatalf("cross-session commands reached worker: actions=%d closes=%d",
			len(worker.actions), worker.closeCalls)
	}
}

func TestBrowserHostRechecksExecutableIdentityBeforeEveryOpen(t *testing.T) {
	factory := &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{}}
	host := newTestBrowserHost(t, factory)
	host.verifyProfile = func(companion.BrowserProfilePolicy) error {
		return errors.New("digest changed")
	}
	if _, err := host.Open(
		t.Context(),
		browserHostOpenFixture(),
	); !errors.Is(err, ErrBrowserHostDenied) ||
		len(factory.requests) != 0 {
		t.Fatalf("changed executable Open() error = %v, requests = %d", err, len(factory.requests))
	}
}

func TestBrowserHostFailedOpenCleansReturnedWorkerAndReportsSafeState(t *testing.T) {
	worker := &fakeBrowserHostWorker{}
	factory := &fakeBrowserHostFactory{
		worker: worker, err: browserworker.ErrDriverIncompatible,
	}
	host := newTestBrowserHost(t, factory)
	result, err := host.Open(t.Context(), browserHostOpenFixture())
	if !errors.Is(err, browserworker.ErrDriverIncompatible) || result.State != "lost" ||
		result.Reason != "driver_incompatible" || worker.closeCalls != 1 {
		t.Fatalf("failed Open() = %#v, %v, closes = %d", result, err, worker.closeCalls)
	}
}

func TestBrowserHostRetriesFailedStartupCleanupOnClose(t *testing.T) {
	worker := &fakeBrowserHostWorker{closeErr: errors.New("cleanup failed")}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{
		worker: worker, err: browserworker.ErrWorkerUnavailable,
	})
	result, err := host.Open(t.Context(), browserHostOpenFixture())
	if !errors.Is(err, browserworker.ErrWorkerUnavailable) || result.Reason != "cleanup_required" ||
		worker.closeCalls != 1 {
		t.Fatalf("failed Open() = %#v, %v, closes = %d", result, err, worker.closeCalls)
	}
	worker.closeErr = nil
	closed, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 2 {
		t.Fatalf("cleanup Close() = %#v, %v, closes = %d", closed, err, worker.closeCalls)
	}
}

func TestBrowserHostFailedCleanupKeepsProfileOccupied(t *testing.T) {
	worker := &fakeBrowserHostWorker{closeErr: errors.New("cleanup failed")}
	factory := &fakeBrowserHostFactory{
		worker: worker, err: browserworker.ErrWorkerUnavailable,
	}
	host := newTestBrowserHost(t, factory)
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); !errors.Is(err, browserworker.ErrWorkerUnavailable) {
		t.Fatal(err)
	}
	second := browserHostOpenFixture()
	second.SessionID = "browser_session_2"
	if _, err := host.Open(t.Context(), second); !errors.Is(err, ErrBrowserHostBusy) ||
		len(factory.requests) != 1 {
		t.Fatalf("Open() during unresolved cleanup error = %v, requests = %d", err, len(factory.requests))
	}
	worker.closeErr = nil
	if _, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Open(t.Context(), second); !errors.Is(err, browserworker.ErrWorkerUnavailable) ||
		len(factory.requests) != 2 {
		t.Fatalf("Open() after cleanup error = %v, requests = %d", err, len(factory.requests))
	}
}

func TestBrowserHostPreservesAdmittedIdleLimitOnActivity(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "https://example.com/", Origin: "https://example.com"},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	request := browserHostOpenFixture()
	request.Limits.IdleSeconds = 2
	if _, err := host.Open(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	host.now = func() time.Time { return time.Unix(101, 0).UTC() }
	observed, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	host.now = func() time.Time { return time.Unix(102, 0).UTC() }
	navigateRequest := browserHostNavigateFixture()
	navigateRequest.SnapshotGeneration = 1
	if _, err = host.Navigate(t.Context(), navigateRequest); err != nil {
		t.Fatal(err)
	}
	status, err := host.Status(t.Context(), BrowserHostStatusRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || observed.SnapshotGeneration != 1 || status.IdleExpiresAt != 104 {
		t.Fatalf("activity status = %#v, observation = %#v, error = %v", status, observed, err)
	}
}

func TestBrowserHostReservesInvocationAndQuarantinesAmbiguousExecute(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		executeErr:   errors.New("ambiguous driver failure"),
		observations: []browserworker.DriverObservation{{URL: "about:blank", Origin: "about:blank"}},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	request := browserHostNavigateFixture()
	if _, err := host.Navigate(t.Context(), request); !errors.Is(err, ErrBrowserHostLost) {
		t.Fatalf("ambiguous Navigate() error = %v", err)
	}
	status, err := host.Status(t.Context(), BrowserHostStatusRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || status.State != "lost" || status.Reason != "outcome_unknown" ||
		status.Recovery != "close" {
		t.Fatalf("quarantined Status() = %#v, %v", status, err)
	}
	if _, err = host.Navigate(t.Context(), request); !errors.Is(err, ErrBrowserHostLost) ||
		len(worker.actions) != 1 {
		t.Fatalf("replayed Navigate() error = %v, actions = %d", err, len(worker.actions))
	}
	session := host.sessions["browser_session_1"]
	session.mu.Lock()
	reservedHash := session.actionInvocations[request.ActionInvocationID]
	session.mu.Unlock()
	if reservedHash != request.PreparedActionHash {
		t.Fatalf("reserved invocation hash = %q", reservedHash)
	}
}

func TestBrowserHostRejectsChangedOriginBeforeActionAcceptance(t *testing.T) {
	worker := &fakeBrowserHostWorker{observations: []browserworker.DriverObservation{{
		URL: "https://changed.example/", Origin: "https://changed.example",
	}}}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	request := browserHostNavigateFixture()
	if _, err := host.Navigate(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
		t.Fatalf("changed-origin Navigate() error = %v", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("changed-origin driver actions = %d", len(worker.actions))
	}
	session := host.sessions[request.SessionID]
	session.mu.Lock()
	_, reserved := session.actionInvocations[request.ActionInvocationID]
	session.mu.Unlock()
	if reserved {
		t.Fatal("changed-origin action was reserved before rejection")
	}
}

func TestBrowserHostQuarantinesWhenPostActionObserveFails(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		observations: []browserworker.DriverObservation{{URL: "about:blank", Origin: "about:blank"}},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Navigate(t.Context(), browserHostNavigateFixture()); !errors.Is(err, ErrBrowserHostLost) {
		t.Fatalf("Navigate() with ambiguous observation error = %v", err)
	}
	if len(worker.actions) != 1 || worker.observeCalls != 1 {
		t.Fatalf("driver actions = %d, completed observations = %d", len(worker.actions), worker.observeCalls)
	}
}

func TestBrowserHostAppliesAdmittedActionDeadline(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		observations: []browserworker.DriverObservation{{URL: "about:blank", Origin: "about:blank"}},
		executeFunc: func(ctx context.Context, _ browserworker.DriverAction) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	request := browserHostOpenFixture()
	request.Limits.ActionSeconds = 1
	if _, err := host.Open(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := host.Navigate(t.Context(), browserHostNavigateFixture()); !errors.Is(err, ErrBrowserHostLost) {
		t.Fatalf("deadline Navigate() error = %v", err)
	}
	elapsed := time.Since(started)
	if elapsed < 500*time.Millisecond || elapsed > 2*time.Second || len(worker.actions) != 1 {
		t.Fatalf("deadline Navigate() elapsed = %s, actions = %d", elapsed, len(worker.actions))
	}
}

func TestBrowserHostCloseDuringOpenCannotResurrectWorker(t *testing.T) {
	worker := &fakeBrowserHostWorker{}
	factory := &blockingBrowserHostFactory{
		worker: worker, started: make(chan struct{}), release: make(chan struct{}),
	}
	host := newTestBrowserHost(t, factory)
	type openResult struct {
		session BrowserHostSession
		err     error
	}
	done := make(chan openResult, 1)
	go func() {
		session, err := host.Open(t.Context(), browserHostOpenFixture())
		done <- openResult{session: session, err: err}
	}()
	<-factory.started
	closed, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" {
		t.Fatalf("Close() during open = %#v, %v", closed, err)
	}
	close(factory.release)
	result := <-done
	if !errors.Is(result.err, ErrBrowserHostLost) || result.session.State != "closed" ||
		worker.closeCalls != 1 {
		t.Fatalf("raced Open() = %#v, %v, closes = %d", result.session, result.err, worker.closeCalls)
	}
}

func TestBrowserHostStatusNeverRecreatesLostWorker(t *testing.T) {
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerLost}
	factory := &fakeBrowserHostFactory{worker: worker}
	host := newTestBrowserHost(t, factory)
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	status, err := host.Status(t.Context(), BrowserHostStatusRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || status.State != "lost" || status.Recovery != "close" ||
		len(factory.requests) != 1 {
		t.Fatalf("Status() = %#v, %v, opens = %d", status, err, len(factory.requests))
	}
}

func TestBrowserHostExpiresAndClosesIdleWorker(t *testing.T) {
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerReady}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	host.now = func() time.Time {
		return time.Unix(100+int64(nodes.MaxBrowserIdleSeconds)+1, 0).UTC()
	}
	status, err := host.Status(t.Context(), BrowserHostStatusRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || status.State != "closed" || status.Reason != "session_expired" ||
		worker.closeCalls != 1 {
		t.Fatalf("expired Status() = %#v, %v, closes = %d", status, err, worker.closeCalls)
	}
}

func TestCompanionPlaywrightServerOwnsProfileAndTransportPolicy(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	profile := browserHostProfileFixture()
	profile.DriverExecutable = "/usr/local/lib/node_modules/npm/bin/npx-cli.js"
	profile.ProfileDirectory = "/Users/operator/.mintclaw/browser/managed"
	profile.LockFile = "/Users/operator/.mintclaw/browser.lock"
	profile.DriverArguments = []string{"-y", "@playwright/mcp@0.0.78", "--browser=chrome"}
	server, err := companionPlaywrightServer(profile)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(server.Args, "\x00")
	if server.Command != profile.DriverExecutable ||
		server.SessionLossReplay != "never" || server.ExclusiveLockFile != profile.LockFile ||
		server.Env["PATH"] != "/usr/local/lib/node_modules/npm/bin:/usr/bin:/bin" || len(server.Env) != 1 ||
		!strings.Contains(joined, "--user-data-dir\x00"+profile.ProfileDirectory) ||
		!strings.Contains(joined, "--output-mode\x00stdout") ||
		strings.Contains(joined, "--headless") {
		t.Fatalf("companion server = %#v", server)
	}
	profile.DriverArguments = append(profile.DriverArguments, "--cdp-endpoint=http://localhost:9222")
	if _, err = companionPlaywrightServer(profile); err == nil ||
		!strings.Contains(err.Error(), "host-managed option") || strings.Contains(err.Error(), "9222") {
		t.Fatalf("raw endpoint argument error = %v", err)
	}
}

func TestCompanionPlaywrightServerUsesNormalizedSymlinkLauncherDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("browser profile identity validation is admitted on Darwin and Linux")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	root := t.TempDir()
	brewBin := filepath.Join(root, "homebrew", "bin")
	npmBin := filepath.Join(root, "homebrew", "lib", "node_modules", "npm", "bin")
	profileDir := filepath.Join(root, "profile")
	lockDir := filepath.Join(root, "locks")
	for _, directory := range []string{brewBin, npmBin, profileDir, lockDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	canonical := filepath.Join(npmBin, "npx-cli.js")
	if err := os.WriteFile(canonical, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(brewBin, "npx")
	if err := os.Symlink(canonical, launcher); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("#!/bin/sh\n"))
	cfg, err := (companion.Config{
		GatewayURL: "wss://gateway.example",
		BrowserProfiles: map[string]companion.BrowserProfilePolicy{
			"managed": {
				Enabled: true, Revision: "managed-v1",
				AllowedAgents: []string{"browser"}, AllowedActors: []string{"owner"},
				Driver:           nodes.BrowserDriverPlaywrightMCP,
				DriverExecutable: launcher, DriverExecutableSHA256: hex.EncodeToString(digest[:]),
				DriverArguments: []string{"--browser=chrome"}, ProfileDirectory: profileDir,
				LockFile: filepath.Join(lockDir, "browser.lock"), Mode: nodes.BrowserProfileManaged,
				NetworkMode: nodes.BrowserNetworkAnyHTTP, DryRun: true,
				AllowedActions: []string{"navigate"}, Headed: true,
			},
		},
	}).Normalize(root)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.BrowserProfiles["managed"]
	server, err := companionPlaywrightServer(profile)
	if err != nil {
		t.Fatal(err)
	}
	canonicalReal, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	brewBinReal, err := filepath.EvalSymlinks(brewBin)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := brewBinReal + string(os.PathListSeparator) + "/usr/bin:/bin"
	if profile.DriverExecutable != canonicalReal ||
		server.Command != canonicalReal || server.Env["PATH"] != wantPath || len(server.Env) != 1 {
		t.Fatalf("normalized profile = %#v, server = %#v", profile, server)
	}
	if err = os.Chmod(brewBin, 0o777); err != nil {
		t.Fatal(err)
	}
	if err = companion.VerifyBrowserProfileRuntimeIdentity(profile); err == nil ||
		!strings.Contains(err.Error(), "executable identity changed") {
		t.Fatalf("unsafe launcher directory runtime identity error = %v", err)
	}
}

func TestNewBrowserHostBuildsPassiveFactoryFromNormalizedPolicy(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("browser profile identity validation is admitted on Darwin and Linux")
	}
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	profileDir := filepath.Join(root, "profile")
	lockDir := filepath.Join(root, "locks")
	if err = os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := (companion.Config{
		GatewayURL: "wss://gateway.example",
		BrowserProfiles: map[string]companion.BrowserProfilePolicy{
			"managed": {
				Enabled: true, Revision: "managed-v1",
				AllowedAgents: []string{"browser"}, AllowedActors: []string{"owner"},
				Driver:           nodes.BrowserDriverPlaywrightMCP,
				DriverExecutable: executable, DriverExecutableSHA256: hex.EncodeToString(digest[:]),
				DriverArguments:  []string{"--browser=chrome"},
				ProfileDirectory: profileDir, LockFile: filepath.Join(lockDir, "browser.lock"),
				Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
				DryRun: true, AllowedActions: []string{"navigate"}, Headed: true,
			},
		},
	}).Normalize(root)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewBrowserHost(cfg.BrowserProfiles)
	if err != nil || host == nil || len(host.factories) != 1 || len(host.sessions) != 0 {
		t.Fatalf("NewBrowserHost() = %#v, %v", host, err)
	}
}

func newTestBrowserHost(t *testing.T, factory browserHostFactory) *BrowserHost {
	t.Helper()
	host, err := newBrowserHost(
		map[string]companion.BrowserProfilePolicy{"managed": browserHostProfileFixture()},
		map[string]browserHostFactory{"managed": factory},
	)
	if err != nil {
		t.Fatal(err)
	}
	host.now = func() time.Time { return time.Unix(100, 0).UTC() }
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	return host
}

func TestBrowserHostOpensExplicitApprovedActionProfile(t *testing.T) {
	profile := browserHostProfileFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerReady}
	host, err := newBrowserHost(
		map[string]companion.BrowserProfilePolicy{"managed": profile},
		map[string]browserHostFactory{"managed": &fakeBrowserHostFactory{worker: worker}},
	)
	if err != nil {
		t.Fatal(err)
	}
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	request := browserHostOpenFixture()
	request.DryRun = false
	opened, err := host.Open(t.Context(), request)
	if err != nil || opened.State != "ready" {
		t.Fatalf("Open() approved-action profile = %#v, %v", opened, err)
	}
	request.DryRun = true
	request.SessionID = "browser_session_wrong_mode"
	if _, err = host.Open(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("Open() mismatched dry-run mode error = %v", err)
	}
}

func browserHostProfileFixture() companion.BrowserProfilePolicy {
	return companion.BrowserProfilePolicy{
		Enabled: true, Revision: "managed-v1",
		AllowedAgents: []string{"browser"}, AllowedActors: []string{"telegram:owner"},
		Driver: nodes.BrowserDriverPlaywrightMCP, Mode: nodes.BrowserProfileManaged,
		NetworkMode: nodes.BrowserNetworkAnyHTTP, DryRun: true,
		AllowedActions: []string{"download", "navigate", "scroll"}, Headed: true,
		Limits: nodes.BrowserLimits{}.Effective(),
	}
}

func browserHostOpenFixture() BrowserHostOpenRequest {
	return BrowserHostOpenRequest{
		SessionID: "browser_session_1", Profile: "managed", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), RoutedSessionID: "routed_session_1",
		AgentID: "browser",
		ActorID: "telegram:owner", DryRun: true, Limits: nodes.BrowserLimits{}.Effective(),
	}
}

func browserHostNavigateFixture() BrowserHostNavigateRequest {
	return BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 0,
		ActionInvocationID: "browser_action_1",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
}
