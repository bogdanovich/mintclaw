package browserhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	_ context.Context,
	action browserworker.DriverAction,
) error {
	worker.actions = append(worker.actions, action)
	return worker.executeErr
}

func (*fakeBrowserHostWorker) CatalogRevision() string { return "driver-v1" }

func TestBrowserHostReusesWorkerForTypedLifecycle(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
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
	if len(factory.requests) != 1 || factory.requests[0].Target != companionBrowserTarget ||
		factory.requests[0].Profile != nodes.BrowserProfileManaged {
		t.Fatalf("worker open requests = %#v", factory.requests)
	}

	initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || initial.SnapshotGeneration != 1 || initial.URL != "about:blank" {
		t.Fatalf("initial Observe() = %#v, %v", initial, err)
	}

	navigated, err := host.Navigate(t.Context(), BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_1", URL: "https://example.com/",
		Effect: "navigation", PreparedActionHash: strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || navigated.SnapshotGeneration != 2 || navigated.Title != "Example" ||
		len(navigated.Elements) != 1 || navigated.Elements[0].Ref != "e1" {
		t.Fatalf("Navigate() = %#v, %v", navigated, err)
	}
	if len(worker.actions) != 1 || worker.actions[0].Kind != browserworker.DriverNavigate ||
		worker.actions[0].URL != "https://example.com/" {
		t.Fatalf("driver actions = %#v", worker.actions)
	}

	_, err = host.Navigate(t.Context(), BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_2", URL: "https://example.com/stale",
		Effect: "navigation", PreparedActionHash: strings.Repeat("c", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if !errors.Is(err, ErrBrowserHostStale) || len(worker.actions) != 1 {
		t.Fatalf("stale Navigate() error = %v, actions = %d", err, len(worker.actions))
	}

	closed, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 1 {
		t.Fatalf("Close() = %#v, %v, calls = %d", closed, err, worker.closeCalls)
	}
	closed, err = host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 1 {
		t.Fatalf("repeated Close() = %#v, %v, calls = %d", closed, err, worker.closeCalls)
	}
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
	second := browserHostOpenFixture()
	second.SessionID = "browser_session_2"
	if _, err := host.Open(t.Context(), second); !errors.Is(err, ErrBrowserHostBusy) ||
		len(factory.requests) != 1 {
		t.Fatalf("concurrent Open() error = %v, requests = %d", err, len(factory.requests))
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
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || closed.State != "closed" || worker.closeCalls != 2 {
		t.Fatalf("cleanup Close() = %#v, %v, closes = %d", closed, err, worker.closeCalls)
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
		AgentID: "browser", ActorID: "telegram:owner",
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
		AgentID: "browser", ActorID: "telegram:owner",
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
		AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || status.State != "closed" || status.Reason != "session_expired" ||
		worker.closeCalls != 1 {
		t.Fatalf("expired Status() = %#v, %v, closes = %d", status, err, worker.closeCalls)
	}
}

func TestCompanionPlaywrightServerOwnsProfileAndTransportPolicy(t *testing.T) {
	profile := browserHostProfileFixture()
	profile.DriverExecutable = "/usr/local/bin/npx"
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

func browserHostProfileFixture() companion.BrowserProfilePolicy {
	return companion.BrowserProfilePolicy{
		Enabled: true, Revision: "managed-v1",
		AllowedAgents: []string{"browser"}, AllowedActors: []string{"telegram:owner"},
		Driver: nodes.BrowserDriverPlaywrightMCP, Mode: nodes.BrowserProfileManaged,
		NetworkMode: nodes.BrowserNetworkAnyHTTP, DryRun: true,
		AllowedActions: []string{"download", "navigate"}, Headed: true,
		Limits: nodes.BrowserLimits{}.Effective(),
	}
}

func browserHostOpenFixture() BrowserHostOpenRequest {
	return BrowserHostOpenRequest{
		SessionID: "browser_session_1", Profile: "managed", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), AgentID: "browser",
		ActorID: "telegram:owner", DryRun: true, Limits: nodes.BrowserLimits{}.Effective(),
	}
}
