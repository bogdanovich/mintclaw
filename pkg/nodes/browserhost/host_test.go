package browserhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	status                  browserworker.WorkerStatus
	statusErr               error
	observations            []browserworker.DriverObservation
	observeCalls            int
	navigationIdentities    []string
	navigationIdentityCalls int
	dispatchNavigationID    string
	authorizeFillCalls      int
	authorizeFillErr        error
	beforeBoundDispatch     func()
	actions                 []browserworker.DriverAction
	executeErr              error
	executeFunc             func(context.Context, browserworker.DriverAction) error
	download                browserworker.DriverDownload
	downloadErr             error
	closeErr                error
	closeCalls              int
	contextCatalog          browserworker.ContextCatalog
	contextObservation      browserworker.DriverObservation
	contextSelectCalls      int
	contextSelectErr        error
	contextSelectMutates    bool
	diagnostics             browserworker.DiagnosticSummary
	diagnosticsErr          error
	diagnosticsCategories   []browserworker.DiagnosticCategory
	onDiagnostics           func()
}

func (worker *fakeBrowserHostWorker) Diagnostics(
	_ context.Context,
	categories []browserworker.DiagnosticCategory,
) (browserworker.DiagnosticSummary, error) {
	worker.diagnosticsCategories = append([]browserworker.DiagnosticCategory(nil), categories...)
	if worker.onDiagnostics != nil {
		worker.onDiagnostics()
	}
	return worker.diagnostics, worker.diagnosticsErr
}

func (worker *fakeBrowserHostWorker) AuthorizeFill(
	_ context.Context,
	expectedNavigationID string,
	_ string,
) error {
	worker.authorizeFillCalls++
	if worker.dispatchNavigationID != "" && worker.dispatchNavigationID != expectedNavigationID {
		return browserworker.ErrStale
	}
	return worker.authorizeFillErr
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

func (worker *fakeBrowserHostWorker) NavigationIdentity(context.Context) (string, error) {
	identity := "navigation_1"
	if worker.navigationIdentityCalls < len(worker.navigationIdentities) {
		identity = worker.navigationIdentities[worker.navigationIdentityCalls]
	}
	worker.navigationIdentityCalls++
	return identity, nil
}

func (*fakeBrowserHostWorker) CapturePageScreenshot(
	context.Context,
	string,
	int,
) (browserworker.DriverScreenshot, error) {
	return browserworker.DriverScreenshot{
		ContentType: "image/png",
		Data:        []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1},
	}, nil
}

func (*fakeBrowserHostWorker) CaptureElementScreenshot(
	context.Context,
	string,
	string,
	browserworker.DriverElement,
	int,
) (browserworker.DriverScreenshot, error) {
	return browserworker.DriverScreenshot{
		ContentType: "image/png",
		Data:        []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 2},
	}, nil
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

func (worker *fakeBrowserHostWorker) Upload(
	ctx context.Context,
	action browserworker.DriverAction,
) error {
	return worker.Execute(ctx, action)
}

func (worker *fakeBrowserHostWorker) Download(
	_ context.Context,
	action browserworker.DriverAction,
	_ int64,
) (browserworker.DriverDownload, error) {
	worker.actions = append(worker.actions, action)
	return worker.download, worker.downloadErr
}

func (worker *fakeBrowserHostWorker) UploadAfterNavigationCheck(
	ctx context.Context,
	expectedNavigationID string,
	action browserworker.DriverAction,
) error {
	return worker.ExecuteAfterNavigationCheck(ctx, expectedNavigationID, action)
}

func (worker *fakeBrowserHostWorker) ExecuteAfterNavigationCheck(
	ctx context.Context,
	expectedNavigationID string,
	action browserworker.DriverAction,
) error {
	if worker.beforeBoundDispatch != nil {
		worker.beforeBoundDispatch()
	}
	if worker.dispatchNavigationID != "" && worker.dispatchNavigationID != expectedNavigationID {
		return browserworker.ErrStale
	}
	return worker.Execute(ctx, action)
}

func (*fakeBrowserHostWorker) CatalogRevision() string { return "driver-v1" }

func (worker *fakeBrowserHostWorker) ContextCatalog(context.Context) (browserworker.ContextCatalog, error) {
	if worker.contextCatalog.ID == "" {
		worker.contextCatalog = browserHostContextCatalogFixture(false)
	}
	return worker.contextCatalog, nil
}

func (worker *fakeBrowserHostWorker) OpenTab(context.Context) (browserworker.ContextCatalog, error) {
	worker.contextCatalog = browserHostContextCatalogFixture(true)
	return worker.contextCatalog, nil
}

func (worker *fakeBrowserHostWorker) SelectContext(
	context.Context,
	browserworker.ContextMutationAuthority,
) (browserworker.DriverObservation, browserworker.ContextCatalog, error) {
	if worker.contextSelectErr != nil && !worker.contextSelectMutates {
		return browserworker.DriverObservation{}, worker.contextCatalog, worker.contextSelectErr
	}
	if worker.contextSelectCalls == 0 {
		worker.contextCatalog.Generation++
	}
	worker.contextSelectCalls++
	worker.contextCatalog.SelectedTabID = "context_tab_2"
	worker.contextCatalog.SelectedFrameID = "context_frame_1"
	return worker.contextObservation, worker.contextCatalog, worker.contextSelectErr
}

func (worker *fakeBrowserHostWorker) CloseTab(
	context.Context,
	browserworker.ContextMutationAuthority,
) (browserworker.ContextCatalog, error) {
	worker.contextCatalog.Generation++
	worker.contextCatalog.SelectedTabID = "context_tab_1"
	worker.contextCatalog.SelectedFrameID = ""
	worker.contextCatalog.Tabs = worker.contextCatalog.Tabs[:1]
	return worker.contextCatalog, nil
}

func browserHostContextCatalogFixture(opened bool) browserworker.ContextCatalog {
	catalog := browserworker.ContextCatalog{
		ID: "context_catalog_1", Generation: 1, SelectedTabID: "context_tab_1",
		Tabs: []browserworker.TabContext{{
			ID: "context_tab_1", Kind: browserworker.TabPrimary, CreationSequence: 1,
			DocumentGeneration: 1, URL: "about:blank", Origin: "about:blank",
		}},
	}
	if opened {
		catalog.Generation = 2
		catalog.SelectedTabID = "context_tab_2"
		catalog.Tabs = append(catalog.Tabs, browserworker.TabContext{
			ID: "context_tab_2", Kind: browserworker.TabOpened, CreationSequence: 2,
			DocumentGeneration: 1, URL: "https://example.com/", Origin: "https://example.com",
			Frames: []browserworker.FrameContext{{
				ID: "context_frame_1", CreationSequence: 1, Depth: 1, DocumentGeneration: 1,
				URL: "https://frame.example/", Origin: "https://frame.example",
				Availability: browserworker.FrameReady,
			}},
		})
	}
	return catalog
}

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
		opened.TabID != "tab_primary" || !opened.Features.Navigate || !opened.Features.Download {
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

	navigated, err := host.Act(t.Context(), BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_1",
		Action:             browserworker.Action{Kind: browserworker.ActionNavigate, URL: "https://example.com/"},
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
	if _, err = host.Act(t.Context(), replayed); !errors.Is(err, ErrBrowserHostStale) ||
		len(worker.actions) != 1 {
		t.Fatalf("replayed successful Navigate() error = %v, actions = %d", err, len(worker.actions))
	}
	replayed.PreparedActionHash = strings.Repeat("c", 64)
	if _, err = host.Act(t.Context(), replayed); !errors.Is(err, ErrBrowserHostDenied) ||
		len(worker.actions) != 1 {
		t.Fatalf("rebound Navigate() error = %v, actions = %d", err, len(worker.actions))
	}

	_, err = host.Act(t.Context(), BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_2",
		Action:             browserworker.Action{Kind: browserworker.ActionNavigate, URL: "https://example.com/stale"},
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

func TestBrowserHostDiagnosticsEnforcesFreshAuthorityAndSafeSummary(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "about:blank", Origin: "about:blank", Snapshot: "blank",
		}},
		navigationIdentities: []string{"navigation_1", "navigation_1", "navigation_1", "navigation_1"},
		diagnostics: browserworker.DiagnosticSummary{
			Categories: []browserworker.DiagnosticCategorySummary{{
				Category: browserworker.DiagnosticFailedRequests, Count: 1,
				Entries: []browserworker.DiagnosticEntry{{
					Timestamp: 1, ResourceClass: "fetch", FailureCode: "network_failed",
					Origin: "https://example.com", Path: "/api", MessageHash: strings.Repeat("a", 64),
				}},
			}},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	opened, err := host.Open(t.Context(), browserHostOpenFixture())
	if err != nil || !opened.Features.Diagnostics {
		t.Fatalf("Open() = %+v, %v", opened, err)
	}
	request := nodes.BrowserHostDiagnosticsRequest{
		BrowserDiagnosticsInput: nodes.BrowserDiagnosticsInput{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 0,
			Categories: []string{"failed_requests"},
		},
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	result, err := host.Diagnostics(t.Context(), request)
	if err != nil || result.Categories[0].Entries[0].Path != "/api" ||
		!reflect.DeepEqual(
			worker.diagnosticsCategories,
			[]browserworker.DiagnosticCategory{browserworker.DiagnosticFailedRequests},
		) {
		t.Fatalf("Diagnostics() = %+v, %v", result, err)
	}
	request.SnapshotGeneration++
	if _, err = host.Diagnostics(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
		t.Fatalf("stale Diagnostics() error = %v", err)
	}
	observation, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	request.SnapshotGeneration = observation.SnapshotGeneration
	worker.onDiagnostics = func() { worker.navigationIdentities[3] = "navigation_2" }
	if _, err = host.Diagnostics(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
		t.Fatalf("document-changed Diagnostics() error = %v", err)
	}
	worker.onDiagnostics = nil
	worker.diagnostics.Categories[0].Entries[0].Path = "/api?credential=canary"
	if _, err = host.Diagnostics(t.Context(), request); !errors.Is(err, ErrBrowserHostLost) {
		t.Fatalf("unsafe Diagnostics() error = %v", err)
	}
}

func TestBrowserHostCapturesApprovedDownloadAsPrivateOutput(t *testing.T) {
	payload := []byte("companion download fixture")
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	observation := browserworker.DriverObservation{
		URL: "https://example.com/download", Origin: "https://example.com", Title: "Download",
		Snapshot: "- button \"Download\" [ref=download_target]",
		Elements: []browserworker.DriverElement{{Target: "download_target", Role: "button", Name: "Download"}},
	}
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			observation, observation, observation,
		},
		download: browserworker.DriverDownload{
			Path: path, Filename: "fixture.txt", ContentType: "text/plain",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)),
		},
	}
	profile := browserHostProfileFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
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
	if _, err = host.Open(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || len(observed.Elements) != 1 {
		t.Fatalf("Observe() = %#v, %v", observed, err)
	}
	action := nodes.BrowserHostActRequest{
		SessionID: request.SessionID, RoutedSessionID: request.RoutedSessionID,
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		ActionInvocationID: "browser_download_1",
		Action:             browserworker.Action{Kind: "download", Ref: observed.Elements[0].Ref},
		Effect:             "external_commit", CurrentOrigin: observed.Origin,
		PreparedActionHash: strings.Repeat("c", 64), BrowserPolicyRevision: request.BrowserPolicyRevision,
		ProfileRevision: profile.Revision, ExpectedRole: "button", ExpectedName: "Download",
		WorkspaceID: "workspace_1", RouteID: "route_1", BrowserTarget: "companion",
		AgentID: request.AgentID, ActorID: request.ActorID,
	}
	action.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(action))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := host.Download(t.Context(), action)
	if err != nil || descriptor.Kind != nodes.BrowserOutputDownload ||
		descriptor.Size != uint64(len(payload)) || descriptor.SHA256 != hex.EncodeToString(digest[:]) ||
		descriptor.SnapshotGeneration != observed.SnapshotGeneration+1 ||
		descriptor.RouteID != action.RouteID || descriptor.TransferID == "" {
		t.Fatalf("Download() = %#v, %v", descriptor, err)
	}
	if len(worker.actions) != 1 || worker.actions[0].Kind != browserworker.DriverDownloadAction ||
		worker.actions[0].Target != "download_target" {
		t.Fatalf("driver actions = %#v", worker.actions)
	}
}

func TestBrowserHostPreservesDownloadSuccessWhenOutputRegistrationIsFull(t *testing.T) {
	payload := []byte("companion download fixture")
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	observation := browserworker.DriverObservation{
		URL: "https://example.com/download", Origin: "https://example.com", Title: "Download",
		Snapshot: "- button \"Download\" [ref=download_target]",
		Elements: []browserworker.DriverElement{{Target: "download_target", Role: "button", Name: "Download"}},
	}
	worker := &fakeBrowserHostWorker{
		status:       browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{observation, observation, observation},
		download: browserworker.DriverDownload{
			Path: path, Filename: "fixture.txt", ContentType: "text/plain",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)),
		},
	}
	profile := browserHostProfileFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
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
	if _, err = host.Open(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || len(observed.Elements) != 1 {
		t.Fatalf("Observe() = %#v, %v", observed, err)
	}
	host.transferMu.Lock()
	for index := range maxBrowserOutputArtifacts {
		host.outputArtifacts[fmt.Sprintf("prefill_%d", index)] = browserOutputArtifact{
			descriptor: nodes.BrowserOutputDescriptor{ExpiresAt: time.Now().Add(time.Hour).Unix()},
			expiryStop: func() {},
		}
	}
	host.transferMu.Unlock()
	action := nodes.BrowserHostActRequest{
		SessionID: request.SessionID, RoutedSessionID: request.RoutedSessionID,
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		ActionInvocationID: "browser_download_full",
		Action:             browserworker.Action{Kind: browserworker.ActionDownload, Ref: observed.Elements[0].Ref},
		Effect:             "unknown", CurrentOrigin: observed.Origin,
		PreparedActionHash: strings.Repeat("c", 64), BrowserPolicyRevision: request.BrowserPolicyRevision,
		ProfileRevision: profile.Revision, ExpectedRole: "button", ExpectedName: "Download",
		WorkspaceID: "workspace_1", RouteID: "route_1", BrowserTarget: "companion",
		AgentID: request.AgentID, ActorID: request.ActorID,
	}
	action.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(action))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor, downloadErr := host.Download(t.Context(), action); !errors.Is(
		downloadErr, nodes.ErrBrowserHostArtifactUnavailable,
	) || descriptor != (nodes.BrowserOutputDescriptor{}) {
		t.Fatalf("Download() = %#v, %v", descriptor, downloadErr)
	}
	if len(worker.actions) != 1 || worker.actions[0].Kind != browserworker.DriverDownloadAction {
		t.Fatalf("driver actions = %#v", worker.actions)
	}
}

func TestBrowserHostAdvancesAuthorityAfterPostClickDownloadArtifactFailure(t *testing.T) {
	for _, status := range []string{"oversize", "read_failed"} {
		t.Run(status, func(t *testing.T) {
			testBrowserHostPostClickDownloadArtifactFailure(t, status)
		})
	}
}

func testBrowserHostPostClickDownloadArtifactFailure(t *testing.T, status string) {
	t.Helper()
	observation := browserworker.DriverObservation{
		URL: "https://example.com/download", Origin: "https://example.com", Title: "Download",
		Snapshot: "- button \"Download\" [ref=download_target]",
		Elements: []browserworker.DriverElement{{Target: "download_target", Role: "button", Name: "Download"}},
	}
	worker := &fakeBrowserHostWorker{
		status:       browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{observation, observation, observation, observation},
		downloadErr: fmt.Errorf(
			"%s: %w", status,
			&browserworker.DownloadArtifactError{Err: browserworker.ErrDriverIncompatible},
		),
	}
	profile := browserHostProfileFixture()
	host, err := newBrowserHost(
		map[string]companion.BrowserProfilePolicy{"managed": profile},
		map[string]browserHostFactory{"managed": &fakeBrowserHostFactory{worker: worker}},
	)
	if err != nil {
		t.Fatal(err)
	}
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	request := browserHostOpenFixture()
	if _, err = host.Open(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	action := nodes.BrowserHostActRequest{
		SessionID: request.SessionID, RoutedSessionID: request.RoutedSessionID,
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		ActionInvocationID: "browser_download_capture_failure_" + status,
		Action:             browserworker.Action{Kind: browserworker.ActionDownload, Ref: observed.Elements[0].Ref},
		Effect:             "unknown", CurrentOrigin: observed.Origin,
		PreparedActionHash: strings.Repeat("d", 64), BrowserPolicyRevision: request.BrowserPolicyRevision,
		ProfileRevision: profile.Revision, ExpectedRole: "button", ExpectedName: "Download",
		WorkspaceID: "workspace_1", RouteID: "route_1", BrowserTarget: "companion",
		AgentID: request.AgentID, ActorID: request.ActorID,
	}
	action.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(action))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor, downloadErr := host.Download(t.Context(), action); !errors.Is(
		downloadErr, nodes.ErrBrowserHostArtifactUnavailable,
	) || descriptor != (nodes.BrowserOutputDescriptor{}) {
		t.Fatalf("Download() = %#v, %v", descriptor, downloadErr)
	}
	if len(worker.actions) != 1 {
		t.Fatalf("driver actions = %#v", worker.actions)
	}
	action.SnapshotGeneration++
	action.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(action))
	if err != nil {
		t.Fatal(err)
	}
	if _, replayErr := host.Download(t.Context(), action); !errors.Is(replayErr, ErrBrowserHostStale) {
		t.Fatalf("replayed Download() error = %v", replayErr)
	}
	if len(worker.actions) != 1 {
		t.Fatalf("driver replayed after artifact failure: %#v", worker.actions)
	}
	refreshedRequest := browserHostObserveFixture()
	refreshedRequest.SnapshotGeneration = action.SnapshotGeneration + 1
	refreshed, err := host.Observe(t.Context(), refreshedRequest)
	if err != nil || refreshed.SnapshotGeneration != refreshedRequest.SnapshotGeneration {
		t.Fatalf("Observe() after artifact failure = %#v, %v", refreshed, err)
	}
}

func TestBrowserHostAllowsOnlyUnknownEffectDownloadInDryRun(t *testing.T) {
	payload := []byte("dry-run companion download fixture")
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	observation := browserworker.DriverObservation{
		URL: "https://example.com/download", Origin: "https://example.com", Title: "Download",
		Snapshot: "- link \"Download\" [ref=download_target]",
		Elements: []browserworker.DriverElement{{Target: "download_target", Role: "link", Name: "Download"}},
	}
	worker := &fakeBrowserHostWorker{
		status:       browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{observation, observation, observation},
		download: browserworker.DriverDownload{
			Path: path, Filename: "fixture.txt", ContentType: "text/plain",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)),
		},
	}
	profile := browserHostProfileFixture()
	host, err := newBrowserHost(
		map[string]companion.BrowserProfilePolicy{"managed": profile},
		map[string]browserHostFactory{"managed": &fakeBrowserHostFactory{worker: worker}},
	)
	if err != nil {
		t.Fatal(err)
	}
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	open := browserHostOpenFixture()
	if _, err = host.Open(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || len(observed.Elements) != 1 {
		t.Fatalf("Observe() = %#v, %v", observed, err)
	}
	action := nodes.BrowserHostActRequest{
		SessionID: open.SessionID, RoutedSessionID: open.RoutedSessionID,
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		ActionInvocationID: "browser_download_dry_run",
		Action:             browserworker.Action{Kind: browserworker.ActionDownload, Ref: observed.Elements[0].Ref},
		Effect:             "unknown", CurrentOrigin: observed.Origin,
		PreparedActionHash: strings.Repeat("c", 64), BrowserPolicyRevision: open.BrowserPolicyRevision,
		ProfileRevision: profile.Revision, ExpectedRole: "link", ExpectedName: "Download",
		WorkspaceID: "workspace_1", RouteID: "route_1", BrowserTarget: "companion",
		AgentID: open.AgentID, ActorID: open.ActorID,
	}
	action.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(action))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := host.Download(t.Context(), action)
	if err != nil || descriptor.Kind != nodes.BrowserOutputDownload || len(worker.actions) != 1 {
		t.Fatalf("dry-run Download() = %#v, %v; actions = %#v", descriptor, err, worker.actions)
	}
	external := action
	external.SnapshotGeneration++
	external.ActionInvocationID = "browser_download_dry_run_external"
	external.Effect = "external_commit"
	external.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(external))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Download(t.Context(), external); !errors.Is(err, ErrBrowserHostDenied) ||
		len(worker.actions) != 1 {
		t.Fatalf("external-commit dry-run Download() error = %v; actions = %#v", err, worker.actions)
	}
}

func TestBrowserHostCapturesAndRetainsExactObservationIdempotently(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			{URL: "https://example.com/", Origin: "https://example.com", Snapshot: "page"},
			{URL: "https://example.com/", Origin: "https://example.com", Snapshot: "page"},
		},
		navigationIdentities: []string{"navigation_1", "navigation_1", "navigation_1", "navigation_1"},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(context.Background(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observation, err := host.Observe(context.Background(), browserHostObserveFixture())
	if err != nil || observation.DocumentID == "" {
		t.Fatalf("Observe() = %+v, %v", observation, err)
	}
	request := BrowserHostCaptureRequest{
		BrowserCaptureInput: nodes.BrowserCaptureInput{
			SessionID: observation.SessionID, TabID: observation.TabID,
			SnapshotID: "snapshot_gateway_1", SnapshotGeneration: observation.SnapshotGeneration,
			DocumentID: observation.DocumentID, InvocationID: "capture_request_1",
			WorkspaceID: "workspace_1", RouteID: "route_1", BrowserTarget: "companion",
			Target: "page", ProfileRevision: "managed-v1",
			BrowserPolicyRevision: strings.Repeat("a", 64),
		},
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	first, err := host.Capture(context.Background(), request)
	if err != nil || first.TransferID == "" || first.Size == 0 || first.SHA256 == "" {
		t.Fatalf("Capture() = %+v, %v", first, err)
	}
	second, err := host.Capture(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("idempotent Capture() = %+v, %v; want %+v", second, err, first)
	}
	rebound := request
	rebound.RouteID = "route_2"
	if _, err = host.Capture(context.Background(), rebound); !errors.Is(err, ErrBrowserHostDenied) {
		t.Fatalf("route-rebound Capture() error = %v", err)
	}
	host.transferMu.Lock()
	count := len(host.outputArtifacts)
	host.transferMu.Unlock()
	if count != 1 {
		t.Fatalf("retained output count = %d, want 1", count)
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
	result, err := host.Act(t.Context(), BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_scroll_1",
		Action:             browserworker.Action{Kind: browserworker.ActionScroll, Direction: "down", Amount: 2},
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
	newFixture := func(t *testing.T, dryRun bool) (*BrowserHost, *fakeBrowserHostWorker, BrowserHostActRequest) {
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
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_click_1",
			Action:             browserworker.Action{Kind: browserworker.ActionClick, Ref: initial.Elements[0].Ref},
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
		result, err := host.Act(t.Context(), request)
		if err != nil || result.SnapshotGeneration != 2 {
			t.Fatalf("Click() = %#v, %v", result, err)
		}
		if len(worker.actions) != 1 || worker.actions[0] != (browserworker.DriverAction{
			Kind: browserworker.DriverClick, Target: "driver_ref_1", Element: "Save",
		}) {
			t.Fatalf("driver actions = %#v", worker.actions)
		}
	})

	t.Run("declared safe click in dry run", func(t *testing.T) {
		host, worker, request := newFixture(t, true)
		request.Effect = "navigation"
		request.ApprovalDigest = ""
		result, err := host.Act(t.Context(), request)
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
		if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
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
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
			t.Fatalf("Click() duplicate target error = %v, want stale", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("duplicate target dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		host, worker, request := newFixture(t, false)
		request.ApprovalDigest = strings.Repeat("f", 64)
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Click() digest mismatch error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("digest mismatch dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		host, worker, request := newFixture(t, true)
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Click() dry-run error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("dry-run click dispatched %d actions", len(worker.actions))
		}
	})
}

func TestBrowserHostReferenceBindingPreservesProjectedSnapshotLimit(t *testing.T) {
	elements := make([]browserworker.DriverElement, 0, nodes.MaxBrowserSnapshotRefs)
	var snapshot strings.Builder
	rewrittenBytes := 0
	for index := 0; index < nodes.MaxBrowserSnapshotRefs; index++ {
		target := fmt.Sprintf("e%d", index+1)
		line := fmt.Sprintf("- region [ref=%s]\n", target)
		snapshot.WriteString(line)
		rewrittenBytes += len(line) - len(target) + len("ref_00000000000000000000000000000000")
		elements = append(elements, browserworker.DriverElement{
			Target: target, Role: "region", Name: fmt.Sprintf("Region %d", index+1),
		})
	}
	if rewrittenBytes >= nodes.MaxBrowserSnapshotBytes {
		t.Fatalf("reference fixture uses %d bytes before padding", rewrittenBytes)
	}
	snapshot.WriteString(strings.Repeat("x", nodes.MaxBrowserSnapshotBytes-rewrittenBytes))
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "https://example.com/", Origin: "https://example.com",
			Snapshot: snapshot.String(), Elements: elements,
		}},
		navigationIdentities: []string{"navigation_1", "navigation_1"},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || len(observed.Snapshot) != nodes.MaxBrowserSnapshotBytes ||
		len(observed.Elements) != nodes.MaxBrowserSnapshotRefs {
		t.Fatalf(
			"Observe() snapshot bytes=%d elements=%d, error=%v",
			len(observed.Snapshot), len(observed.Elements), err,
		)
	}
	for _, element := range observed.Elements {
		if len(element.Ref) != len("ref_00000000000000000000000000000000") ||
			!strings.Contains(observed.Snapshot, "[ref="+element.Ref+"]") {
			t.Fatalf("bound reference = %q", element.Ref)
		}
	}
	streamed, err := host.PrepareObservationOutput(nodes.BrowserHostObservationOutputRequest{
		SessionID: observed.SessionID, RoutedSessionID: "routed_session_1",
		InvocationID: "browser_observe_dense_refs_1", WorkspaceID: "workspace_1",
		BrowserTarget: "companion", AgentID: "browser", ActorID: "telegram:owner",
	}, observed)
	if err != nil || streamed.Output == nil {
		t.Fatalf("PrepareObservationOutput() = %#v, %v", streamed, err)
	}
	payload := downloadBrowserOutput(t, host, *streamed.Output, nil)
	decoded, err := nodes.DecodeBrowserSnapshotPayload(payload, nodes.BrowserLimits{}.Effective())
	if err != nil || decoded.Snapshot != observed.Snapshot || len(decoded.Elements) != len(observed.Elements) {
		t.Fatalf(
			"decoded dense snapshot bytes=%d elements=%d, error=%v",
			len(decoded.Snapshot),
			len(decoded.Elements),
			err,
		)
	}
}

func TestBrowserHostExecutesBoundProtectedDialog(t *testing.T) {
	message := "Type confirmation"
	pending := browserworker.DriverObservation{
		URL: "https://example.com/form", Origin: "https://example.com", Title: "Fixture",
		Snapshot:      "dialog pending",
		PendingDialog: &browserworker.DialogObservation{Type: "prompt", Message: message},
	}
	settled := pending
	settled.Snapshot = "dialog accepted"
	settled.PendingDialog = nil
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			pending, pending, settled,
		},
	}
	profile := browserHostProfileFixture()
	profile.AllowedActions = []string{"dialog", "navigate"}
	profile.DryRun = false
	profile.AllowApprovedActions = true
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
	open.DryRun = false
	if _, err = host.Open(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || initial.PendingDialog == nil {
		t.Fatalf("initial dialog = %#v, %v", initial, err)
	}
	secret := "dialog-prompt-canary"
	request := BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_dialog_1",
		Action: browserworker.Action{
			Kind: "dialog", DialogID: "dialog_authority_1", Decision: "accept",
			Value: secret, PromptProvided: true,
		},
		Effect: "external_commit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", DialogType: "prompt",
		DialogMessageDigest: nodes.BrowserDialogMessageDigest("prompt", message),
		DialogMessageBytes:  len(message), InputDigest: nodes.BrowserInputDigest(secret), InputBytes: len(secret),
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.Act(t.Context(), request)
	if err != nil || result.SnapshotGeneration != 2 || result.PendingDialog != nil {
		t.Fatalf("Dialog() = %#v, %v", result, err)
	}
	want := browserworker.DriverAction{
		Kind: browserworker.DriverDialog, Accept: true, Value: secret, PromptProvided: true,
	}
	if len(worker.actions) != 1 || worker.actions[0] != want {
		t.Fatalf("driver actions = %#v, want %#v", worker.actions, want)
	}
}

func TestBrowserHostRejectsReplacedDialogBeforeDispatch(t *testing.T) {
	first := browserworker.DriverObservation{
		URL: "https://example.com/form", Origin: "https://example.com", Snapshot: "first",
		PendingDialog: &browserworker.DialogObservation{Type: "confirm", Message: "First"},
	}
	replaced := first
	replaced.Snapshot = "replacement"
	replaced.PendingDialog = &browserworker.DialogObservation{Type: "confirm", Message: "Replacement"}
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady, observations: []browserworker.DriverObservation{first, replaced},
	}
	profile := browserHostProfileFixture()
	profile.AllowedActions = []string{"dialog", "navigate"}
	host, err := newBrowserHost(
		map[string]companion.BrowserProfilePolicy{"managed": profile},
		map[string]browserHostFactory{"managed": &fakeBrowserHostFactory{worker: worker}},
	)
	if err != nil {
		t.Fatal(err)
	}
	host.now = func() time.Time { return time.Unix(100, 0).UTC() }
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	if _, err = host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}); err != nil {
		t.Fatal(err)
	}
	request := BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_dialog_replaced",
		Action: browserworker.Action{
			Kind: "dialog", DialogID: "dialog_authority_1", Decision: "dismiss",
		},
		Effect: "read", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", DialogType: "confirm",
		DialogMessageDigest: nodes.BrowserDialogMessageDigest("confirm", "First"),
		DialogMessageBytes:  len("First"),
		RoutedSessionID:     "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
		t.Fatalf("Dialog(replaced) error = %v, want stale", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("replaced dialog reached driver: %#v", worker.actions)
	}
}

func TestBrowserHostExecutesTypedSelectAndDocumentPress(t *testing.T) {
	newFixture := func(
		t *testing.T,
		element browserworker.DriverElement,
	) (*BrowserHost, *fakeBrowserHostWorker, BrowserHostObservation) {
		t.Helper()
		observation := browserworker.DriverObservation{
			URL: "https://example.com/form", Origin: "https://example.com", Title: "Fixture",
			Snapshot: "- " + element.Role + " State", Elements: []browserworker.DriverElement{element},
		}
		worker := &fakeBrowserHostWorker{
			status: browserworker.WorkerReady,
			observations: []browserworker.DriverObservation{
				observation, observation, observation,
			},
		}
		profile := browserHostProfileFixture()
		profile.AllowedActions = []string{
			"check", "fill", "hover", "navigate", "press", "select", "uncheck",
		}
		profile.DryRun = false
		profile.AllowApprovedActions = true
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
		open.DryRun = false
		if _, err = host.Open(t.Context(), open); err != nil {
			t.Fatal(err)
		}
		initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return host, worker, initial
	}

	t.Run("select semantic option", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_select_1", Role: "combobox", Name: "State"}
		host, worker, initial := newFixture(t, element)
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_select_1",
			Action: browserworker.Action{
				Kind: browserworker.ActionSelect, Ref: initial.Elements[0].Ref, Value: "CA",
			},
			Effect: "local_edit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", ExpectedRole: "combobox", ExpectedName: "State",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		}
		result, err := host.Act(t.Context(), request)
		if err != nil || result.SnapshotGeneration != 2 {
			t.Fatalf("Select() = %#v, %v", result, err)
		}
		if want := (browserworker.DriverAction{
			Kind: browserworker.DriverSelect, Target: element.Target, Element: element.Name, Value: "CA",
		}); len(worker.actions) != 1 || worker.actions[0] != want {
			t.Fatalf("driver actions = %#v, want %#v", worker.actions, want)
		}
	})

	for _, test := range []struct {
		kind, role, effect string
		driverKind         browserworker.DriverActionKind
	}{
		{kind: "check", role: "radio", effect: "local_edit", driverKind: browserworker.DriverCheck},
		{kind: "uncheck", role: "checkbox", effect: "local_edit", driverKind: browserworker.DriverUncheck},
		{kind: "hover", role: "button", effect: "read", driverKind: browserworker.DriverHover},
	} {
		t.Run(test.kind+" semantic control", func(t *testing.T) {
			element := browserworker.DriverElement{
				Target: "driver_" + test.kind + "_1",
				Role:   test.role,
				Name:   "Control",
			}
			host, worker, initial := newFixture(t, element)
			request := BrowserHostActRequest{
				SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
				ActionInvocationID: "browser_" + test.kind + "_1",
				Action: browserworker.Action{
					Kind: browserworker.ActionKind(test.kind), Ref: initial.Elements[0].Ref,
				},
				Effect: test.effect, CurrentOrigin: "https://example.com",
				PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
				ProfileRevision: "managed-v1", ExpectedRole: test.role, ExpectedName: "Control",
				RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
			}
			result, err := host.Act(t.Context(), request)
			if err != nil || result.SnapshotGeneration != 2 {
				t.Fatalf("%s = %#v, %v", test.kind, result, err)
			}
			want := browserworker.DriverAction{Kind: test.driverKind, Target: element.Target, Element: element.Name}
			if len(worker.actions) != 1 || worker.actions[0] != want {
				t.Fatalf("driver actions = %#v, want %#v", worker.actions, want)
			}
		})
	}

	t.Run("deny radio uncheck", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_radio_1", Role: "radio", Name: "Primary"}
		host, worker, initial := newFixture(t, element)
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_uncheck_radio_1",
			Action:             browserworker.Action{Kind: browserworker.ActionUncheck, Ref: initial.Elements[0].Ref},
			Effect:             "local_edit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", ExpectedRole: "radio", ExpectedName: "Primary",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Uncheck(radio) error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("radio uncheck reached driver: %#v", worker.actions)
		}
	})

	t.Run("fill ordinary text", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Display name"}
		host, worker, initial := newFixture(t, element)
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_1",
			Action: browserworker.Action{
				Kind: "fill", Ref: initial.Elements[0].Ref, Value: "Ada",
			},
			Effect: "local_edit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", ExpectedRole: "textbox", ExpectedName: "Display name",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		}
		result, err := host.Act(t.Context(), request)
		if err != nil || result.SnapshotGeneration != 2 {
			t.Fatalf("Fill() = %#v, %v", result, err)
		}
		if want := (browserworker.DriverAction{
			Kind: browserworker.DriverFill, Target: element.Target, Element: element.Name, Value: "Ada",
		}); len(worker.actions) != 1 || worker.actions[0] != want {
			t.Fatalf("driver actions = %#v, want %#v", worker.actions, want)
		}
		if worker.authorizeFillCalls != 1 {
			t.Fatalf("fill authorization calls = %d, want 1", worker.authorizeFillCalls)
		}
	})

	t.Run("deny fill above profile text limit", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Display name"}
		host, worker, initial := newFixture(t, element)
		host.sessions["browser_session_1"].limits.TextInputBytes = 2
		request := BrowserHostActRequest{
			SessionID:          "browser_session_1",
			TabID:              "tab_primary",
			SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_over_profile_limit",
			Action: browserworker.Action{
				Kind:  browserworker.ActionFill,
				Ref:   initial.Elements[0].Ref,
				Value: "Ada",
			},
			Effect:                "local_edit",
			CurrentOrigin:         "https://example.com",
			PreparedActionHash:    strings.Repeat("b", 64),
			BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision:       "managed-v1",
			ExpectedRole:          "textbox",
			ExpectedName:          "Display name",
			RoutedSessionID:       "routed_session_1",
			AgentID:               "browser",
			ActorID:               "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Fill(over profile limit) error = %v, want denied", err)
		}
		if len(worker.actions) != 0 || worker.authorizeFillCalls != 0 {
			t.Fatalf(
				"over-limit fill reached driver: actions=%#v authorizations=%d",
				worker.actions,
				worker.authorizeFillCalls,
			)
		}
	})

	t.Run("deny operator configured sensitive field", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Display name"}
		host, worker, initial := newFixture(t, element)
		host.sessions["browser_session_1"].profile.SensitiveFields = []string{"display name"}
		request := BrowserHostActRequest{
			SessionID:          "browser_session_1",
			TabID:              "tab_primary",
			SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_configured_sensitive",
			Action: browserworker.Action{
				Kind:  browserworker.ActionFill,
				Ref:   initial.Elements[0].Ref,
				Value: "Ada",
			},
			Effect:                "local_edit",
			CurrentOrigin:         "https://example.com",
			PreparedActionHash:    strings.Repeat("b", 64),
			BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision:       "managed-v1",
			ExpectedRole:          "textbox",
			ExpectedName:          "Display name",
			RoutedSessionID:       "routed_session_1",
			AgentID:               "browser",
			ActorID:               "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Fill(configured sensitive) error = %v, want denied", err)
		}
		if len(worker.actions) != 0 || worker.authorizeFillCalls != 0 {
			t.Fatalf(
				"configured-sensitive fill reached driver: actions=%#v authorizations=%d",
				worker.actions,
				worker.authorizeFillCalls,
			)
		}
	})

	t.Run("private classifier denial remains definite", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Display name"}
		host, worker, initial := newFixture(t, element)
		worker.authorizeFillErr = browserworker.ErrDenied
		request := BrowserHostActRequest{
			SessionID:          "browser_session_1",
			TabID:              "tab_primary",
			SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_private_denied",
			Action: browserworker.Action{
				Kind:  browserworker.ActionFill,
				Ref:   initial.Elements[0].Ref,
				Value: "Ada",
			},
			Effect:                "local_edit",
			CurrentOrigin:         "https://example.com",
			PreparedActionHash:    strings.Repeat("b", 64),
			BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision:       "managed-v1",
			ExpectedRole:          "textbox",
			ExpectedName:          "Display name",
			RoutedSessionID:       "routed_session_1",
			AgentID:               "browser",
			ActorID:               "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Fill(private denial) error = %v, want denied", err)
		}
		session := host.sessions["browser_session_1"]
		if len(worker.actions) != 0 || worker.authorizeFillCalls != 1 || session.state != "ready" ||
			len(session.actionInvocations) != 0 {
			t.Fatalf("private denial state: actions=%#v authorizations=%d state=%s invocations=%#v",
				worker.actions, worker.authorizeFillCalls, session.state, session.actionInvocations)
		}
	})

	t.Run("final classifier denial does not quarantine", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Display name"}
		host, worker, initial := newFixture(t, element)
		worker.executeErr = browserworker.ErrDenied
		request := BrowserHostActRequest{
			SessionID:          "browser_session_1",
			TabID:              "tab_primary",
			SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_final_denied",
			Action: browserworker.Action{
				Kind:  browserworker.ActionFill,
				Ref:   initial.Elements[0].Ref,
				Value: "Ada",
			},
			Effect:                "local_edit",
			CurrentOrigin:         "https://example.com",
			PreparedActionHash:    strings.Repeat("b", 64),
			BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision:       "managed-v1",
			ExpectedRole:          "textbox",
			ExpectedName:          "Display name",
			RoutedSessionID:       "routed_session_1",
			AgentID:               "browser",
			ActorID:               "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Fill(final denial) error = %v, want denied", err)
		}
		session := host.sessions["browser_session_1"]
		if worker.authorizeFillCalls != 1 || session.state != "ready" || len(session.actionInvocations) != 0 {
			t.Fatalf("final denial state: authorizations=%d state=%s invocations=%#v",
				worker.authorizeFillCalls, session.state, session.actionInvocations)
		}
	})

	t.Run("deny sensitive fill before dispatch", func(t *testing.T) {
		element := browserworker.DriverElement{Target: "driver_fill_1", Role: "textbox", Name: "Password"}
		host, worker, initial := newFixture(t, element)
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_fill_sensitive_1",
			Action: browserworker.Action{
				Kind: "fill", Ref: initial.Elements[0].Ref, Value: "secret",
			},
			Effect: "local_edit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", ExpectedRole: "textbox", ExpectedName: "Password",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		}
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Fill(sensitive) error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("sensitive fill dispatched %d actions", len(worker.actions))
		}
	})

	t.Run("press document", func(t *testing.T) {
		host, worker, _ := newFixture(t, browserworker.DriverElement{})
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_press_1",
			Action:             browserworker.Action{Kind: browserworker.ActionPress, Target: "document", Key: "Enter"},
			Effect:             "unknown", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", RoutedSessionID: "routed_session_1",
			AgentID: "browser", ActorID: "telegram:owner",
		}
		var err error
		request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
		if err != nil {
			t.Fatal(err)
		}
		result, err := host.Act(t.Context(), request)
		if err != nil || result.SnapshotGeneration != 2 {
			t.Fatalf("Press() = %#v, %v", result, err)
		}
		if want := (browserworker.DriverAction{Kind: browserworker.DriverPress, Key: "Enter"}); len(
			worker.actions,
		) != 1 ||
			worker.actions[0] != want {
			t.Fatalf("driver actions = %#v, want %#v", worker.actions, want)
		}
	})

	t.Run("privileged shortcut denied", func(t *testing.T) {
		host, worker, _ := newFixture(t, browserworker.DriverElement{})
		request := BrowserHostActRequest{
			SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
			ActionInvocationID: "browser_press_denied",
			Action: browserworker.Action{
				Kind: browserworker.ActionPress, Target: "document", Key: "Control+L",
			},
			Effect: "unknown", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
			ProfileRevision: "managed-v1", RoutedSessionID: "routed_session_1",
			AgentID: "browser", ActorID: "telegram:owner",
		}
		request.ApprovalDigest, _ = nodes.BrowserApprovalDigest(browserHostActInput(request))
		if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostDenied) {
			t.Fatalf("Press(Control+L) error = %v, want denied", err)
		}
		if len(worker.actions) != 0 {
			t.Fatalf("denied shortcut reached driver: %#v", worker.actions)
		}
	})

	for _, transition := range []string{"document replacement", "same-document navigation"} {
		for _, action := range []string{"press", "select"} {
			t.Run(action+" rejects byte-identical "+transition, func(t *testing.T) {
				element := browserworker.DriverElement{
					Target: "driver_select_1", Role: "combobox", Name: "State",
				}
				host, worker, initial := newFixture(t, element)
				worker.navigationIdentities = []string{
					"navigation_1", "navigation_1", "navigation_2", "navigation_2",
				}
				request := BrowserHostActRequest{
					SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
					ActionInvocationID: "browser_replaced_" + action,
					Effect:             "local_edit", CurrentOrigin: "https://example.com",
					PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
					ProfileRevision: "managed-v1", ExpectedRole: "combobox", ExpectedName: "State",
					RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
				}
				if action == "select" {
					request.Action = browserworker.Action{
						Kind: "select", Ref: initial.Elements[0].Ref, Value: "CA",
					}
					if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
						t.Fatalf("Select(%s) error = %v, want stale", transition, err)
					}
				} else {
					request.Action = browserworker.Action{
						Kind: browserworker.ActionPress, Target: "document", Key: "Tab",
					}
					request.Effect = "unknown"
					request.ExpectedRole, request.ExpectedName = "", ""
					var err error
					request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
					if err != nil {
						t.Fatal(err)
					}
					if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
						t.Fatalf("Press(%s) error = %v, want stale", transition, err)
					}
				}
				if len(worker.actions) != 0 {
					t.Fatalf("%s reached transitioned page: %#v", action, worker.actions)
				}
			})
		}
	}

	for _, action := range []string{"press", "select"} {
		t.Run(action+" rejects navigation before driver input", func(t *testing.T) {
			element := browserworker.DriverElement{
				Target: "driver_select_1", Role: "combobox", Name: "State",
			}
			host, worker, initial := newFixture(t, element)
			worker.dispatchNavigationID = "navigation_1"
			worker.beforeBoundDispatch = func() {
				worker.dispatchNavigationID = "navigation_2"
			}
			request := BrowserHostActRequest{
				SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
				ActionInvocationID: "browser_dispatch_race_" + action,
				Effect:             "local_edit", CurrentOrigin: "https://example.com",
				PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
				ProfileRevision: "managed-v1", ExpectedRole: "combobox", ExpectedName: "State",
				RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
			}
			if action == "select" {
				request.Action = browserworker.Action{
					Kind: "select", Ref: initial.Elements[0].Ref, Value: "CA",
				}
				if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
					t.Fatalf("Select(dispatch navigation) error = %v, want stale", err)
				}
			} else {
				request.Action = browserworker.Action{
					Kind: browserworker.ActionPress, Target: "document", Key: "Tab",
				}
				request.Effect = "unknown"
				request.ExpectedRole, request.ExpectedName = "", ""
				var err error
				request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
					t.Fatalf("Press(dispatch navigation) error = %v, want stale", err)
				}
			}
			if len(worker.actions) != 0 {
				t.Fatalf("%s reached transitioned page: %#v", action, worker.actions)
			}
		})
	}
}

func TestBrowserHostExecutesApprovedDragWithFreshSourceAndDestination(t *testing.T) {
	source := browserworker.DriverElement{Target: "driver_source_1", Role: "listitem", Name: "Todo"}
	destination := browserworker.DriverElement{Target: "driver_destination_1", Role: "list", Name: "Done"}
	observation := browserworker.DriverObservation{
		URL: "https://example.com/board", Origin: "https://example.com", Title: "Fixture",
		Snapshot: "- listitem Todo\n- list Done", Elements: []browserworker.DriverElement{source, destination},
	}
	worker := &fakeBrowserHostWorker{
		status:       browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{observation, observation, observation},
	}
	profile := browserHostProfileFixture()
	profile.AllowedActions = []string{"drag", "navigate"}
	profile.DryRun = false
	profile.AllowApprovedActions = true
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
	open.DryRun = false
	if _, err = host.Open(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	initial, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || len(initial.Elements) != 2 {
		t.Fatalf("Observe() = %#v, %v", initial, err)
	}
	request := BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_drag_1",
		Action: browserworker.Action{
			Kind: "drag", SourceRef: initial.Elements[0].Ref, DestinationRef: initial.Elements[1].Ref,
		},
		Effect: "unknown", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", ExpectedRole: source.Role, ExpectedName: source.Name,
		DestinationExpectedRole: destination.Role, DestinationExpectedName: destination.Name,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	request.ApprovalDigest, err = nodes.BrowserApprovalDigest(browserHostActInput(request))
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.Act(t.Context(), request)
	want := browserworker.DriverAction{
		Kind: browserworker.DriverDrag, Target: source.Target, Element: source.Name,
		DestinationTarget: destination.Target, DestinationElement: destination.Name,
	}
	if err != nil || result.SnapshotGeneration != 2 || len(worker.actions) != 1 || worker.actions[0] != want {
		t.Fatalf("Drag() = %#v, %v; actions = %#v, want %#v", result, err, worker.actions, want)
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
	if _, err := host.Act(t.Context(), navigate); !errors.Is(err, ErrBrowserHostDenied) {
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

func TestBrowserHostRechecksExecutableAfterSessionReservationBeforeLaunch(t *testing.T) {
	factory := &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{}}
	host := newTestBrowserHost(t, factory)
	checks := 0
	host.verifyProfile = func(companion.BrowserProfilePolicy) error {
		checks++
		if checks == 2 {
			return errors.New("digest changed")
		}
		return nil
	}
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); !errors.Is(err, ErrBrowserHostDenied) ||
		checks != 2 || len(factory.requests) != 0 || len(host.sessions) != 0 {
		t.Fatalf("pre-launch replacement: error=%v checks=%d requests=%d sessions=%d",
			err, checks, len(factory.requests), len(host.sessions))
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
	if _, err = host.Act(t.Context(), navigateRequest); err != nil {
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
		executeErr: errors.New("ambiguous driver failure"),
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Observe(t.Context(), browserHostObserveFixture()); err != nil {
		t.Fatal(err)
	}
	request := browserHostNavigateFixture()
	request.SnapshotGeneration = 1
	if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostLost) {
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
	if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostLost) ||
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
	worker := &fakeBrowserHostWorker{observations: []browserworker.DriverObservation{
		{URL: "about:blank", Origin: "about:blank"},
		{URL: "https://changed.example/", Origin: "https://changed.example"},
	}}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Observe(t.Context(), browserHostObserveFixture()); err != nil {
		t.Fatal(err)
	}
	request := browserHostNavigateFixture()
	request.SnapshotGeneration = 1
	if _, err := host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) {
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
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Observe(t.Context(), browserHostObserveFixture()); err != nil {
		t.Fatal(err)
	}
	navigate := browserHostNavigateFixture()
	navigate.SnapshotGeneration = 1
	if _, err := host.Act(t.Context(), navigate); !errors.Is(err, ErrBrowserHostLost) {
		t.Fatalf("Navigate() with ambiguous observation error = %v", err)
	}
	if len(worker.actions) != 1 || worker.observeCalls != 2 {
		t.Fatalf("driver actions = %d, completed observations = %d", len(worker.actions), worker.observeCalls)
	}
}

func TestBrowserHostAppliesAdmittedActionDeadline(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		observations: []browserworker.DriverObservation{
			{URL: "about:blank", Origin: "about:blank"},
			{URL: "about:blank", Origin: "about:blank"},
		},
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
	if _, err := host.Observe(t.Context(), browserHostObserveFixture()); err != nil {
		t.Fatal(err)
	}
	navigate := browserHostNavigateFixture()
	navigate.SnapshotGeneration = 1
	started := time.Now()
	if _, err := host.Act(t.Context(), navigate); !errors.Is(err, ErrBrowserHostLost) {
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
		server.ExclusiveLockFile != profile.LockFile ||
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
	runtimeExecutable := filepath.Join(root, "browser-runtime")
	if err = os.WriteFile(runtimeExecutable, []byte("runtime"), 0o700); err != nil {
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
				DriverArguments: []string{
					"--browser=chrome", "--executable-path=" + runtimeExecutable,
				},
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
	if err = os.Remove(runtimeExecutable); err != nil {
		t.Fatal(err)
	}
	if host, err = NewBrowserHost(cfg.BrowserProfiles); err == nil || host != nil ||
		!strings.Contains(err.Error(), "runtime is unavailable") {
		t.Fatalf("NewBrowserHost() stale runtime = %#v, %v", host, err)
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

func TestBrowserHostExecutesContextLifecycleWithBoundAuthority(t *testing.T) {
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "https://example.com/", Origin: "https://example.com", Title: "Top level",
			Snapshot: "top-level-only",
		}},
		navigationIdentities: []string{"navigation_selected", "navigation_selected"},
		contextObservation: browserworker.DriverObservation{
			URL: "https://frame.example/", Origin: "https://frame.example", Title: "Selected frame",
			Snapshot: "frame-only [ref=frame_target]",
			Elements: []browserworker.DriverElement{{
				Target: "frame_target", Role: "button", Name: "Frame action",
			}},
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	opened, err := host.Open(t.Context(), browserHostOpenFixture())
	if err != nil || !opened.Features.Contexts {
		t.Fatalf("Open() = %#v, %v", opened, err)
	}
	request := nodes.BrowserHostContextRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", RequestID: "context_request_1",
		AgentID: "browser", ActorID: "telegram:owner", Operation: "list",
	}
	listed, err := host.Contexts(t.Context(), request)
	if err != nil || listed.Catalog.SelectedTabID != "context_tab_1" {
		t.Fatalf("Contexts(list) = %#v, %v", listed, err)
	}
	request.Operation = "open"
	request.RequestID = "context_request_2"
	openedContext, err := host.Contexts(t.Context(), request)
	if err != nil || len(openedContext.Catalog.Tabs) != 2 {
		t.Fatalf("Contexts(open) = %#v, %v", openedContext, err)
	}
	request.Operation = "select"
	request.RequestID = "context_request_3"
	request.Authority = &openedContext.Catalog
	request.TabID = "context_tab_2"
	request.FrameID = "context_frame_1"
	selected, err := host.Contexts(t.Context(), request)
	if err != nil || selected.Observation == nil || selected.Observation.SnapshotGeneration != 1 ||
		selected.Catalog.SelectedTabID != "context_tab_2" ||
		selected.Catalog.SelectedFrameID != "context_frame_1" ||
		selected.Observation.URL != "https://frame.example/" ||
		!strings.Contains(selected.Observation.Snapshot, "frame-only") || worker.observeCalls != 0 {
		t.Fatalf("Contexts(select) = %#v, %v", selected, err)
	}
	observed, err := host.Observe(t.Context(), BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 2,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	})
	if err != nil || observed.URL != "https://frame.example/" ||
		!strings.Contains(observed.Snapshot, "frame-only") || worker.observeCalls != 0 {
		t.Fatalf("Observe(selected frame) = %#v, %v", observed, err)
	}
	request.Operation = "close"
	request.RequestID = "context_request_4"
	request.Authority = &selected.Catalog
	request.FrameID = ""
	closed, err := host.Contexts(t.Context(), request)
	if err != nil || len(closed.Catalog.Tabs) != 1 || closed.Catalog.SelectedTabID != "context_tab_1" {
		t.Fatalf("Contexts(close) = %#v, %v", closed, err)
	}
}

func TestBrowserHostContextListPreservesEquivalentObservationAuthority(t *testing.T) {
	observation := browserworker.DriverObservation{
		URL: "about:blank", Origin: "about:blank", Snapshot: "stable blank page",
	}
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			observation, observation, observation,
		},
		navigationIdentities: []string{
			"navigation_stable", "navigation_stable",
			"navigation_stable", "navigation_stable",
			"navigation_stable", "navigation_stable",
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	contextRequest := nodes.BrowserHostContextRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", RequestID: "context_request_1",
		AgentID: "browser", ActorID: "telegram:owner", Operation: "list",
	}
	if _, err := host.Contexts(t.Context(), contextRequest); err != nil {
		t.Fatalf("first Contexts(list) error = %v", err)
	}
	if _, err := host.Observe(t.Context(), browserHostObserveFixture()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	contextRequest.RequestID = "context_request_2"
	if _, err := host.Contexts(t.Context(), contextRequest); err != nil {
		t.Fatalf("equivalent Contexts(list) error = %v", err)
	}
	navigate := browserHostNavigateFixture()
	navigate.SnapshotGeneration = 1
	result, err := host.Act(t.Context(), navigate)
	if err != nil || result.SnapshotGeneration != 2 || len(worker.actions) != 1 {
		t.Fatalf("Navigate() = %#v, %v; actions = %#v", result, err, worker.actions)
	}
}

func TestBrowserHostClassifiesContextSelectionStaleByDispatchCertainty(t *testing.T) {
	tests := []struct {
		name       string
		selectErr  error
		mutates    bool
		wantErr    error
		wantState  string
		wantReason string
	}{
		{
			name: "authority changed before dispatch",
			selectErr: errors.Join(
				browserworker.ErrStale,
				browserworker.ErrContextAuthorityStale,
			),
			wantErr: ErrBrowserHostStale, wantState: "ready",
		},
		{
			name: "plain stale after dispatch", selectErr: browserworker.ErrStale, mutates: true,
			wantErr: ErrBrowserHostLost, wantState: "lost", wantReason: "outcome_unknown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := &fakeBrowserHostWorker{
				status:           browserworker.WorkerReady,
				contextSelectErr: test.selectErr, contextSelectMutates: test.mutates,
			}
			host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
			if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			request := nodes.BrowserHostContextRequest{
				SessionID: "browser_session_1", ProfileRevision: "managed-v1",
				RoutedSessionID: "routed_session_1", RequestID: "context_request_1",
				AgentID: "browser", ActorID: "telegram:owner", Operation: "open",
			}
			opened, err := host.Contexts(t.Context(), request)
			if err != nil {
				t.Fatalf("Contexts(open) error = %v", err)
			}
			request.Operation = "select"
			request.RequestID = "context_request_2"
			request.Authority = &opened.Catalog
			request.TabID = "context_tab_2"
			request.FrameID = "context_frame_1"
			if _, err = host.Contexts(t.Context(), request); !errors.Is(err, test.wantErr) {
				t.Fatalf("Contexts(select) error = %v, want %v", err, test.wantErr)
			}
			status, statusErr := host.Status(t.Context(), BrowserHostStatusRequest{
				SessionID: "browser_session_1", ProfileRevision: "managed-v1",
				RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
			})
			if statusErr != nil || status.State != test.wantState || status.Reason != test.wantReason {
				t.Fatalf("Status() = %#v, %v", status, statusErr)
			}
		})
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

func browserHostNavigateFixture() BrowserHostActRequest {
	return BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 0,
		ActionInvocationID: "browser_action_1",
		Action:             browserworker.Action{Kind: browserworker.ActionNavigate, URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
}

func browserHostObserveFixture() BrowserHostObserveRequest {
	return BrowserHostObserveRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
}
