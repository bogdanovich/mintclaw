package browser

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type contextBrokerTestWorker struct {
	*actionTestWorker
	catalog      ContextCatalog
	openCalls    int
	selections   [][2]string
	closes       []string
	beforeSelect func()
	beforeClose  func()
}

func (worker *contextBrokerTestWorker) ContextCatalog(context.Context) (ContextCatalog, error) {
	return cloneContextCatalog(worker.catalog), nil
}

func (worker *contextBrokerTestWorker) OpenTab(context.Context) (ContextCatalog, error) {
	worker.openCalls++
	worker.catalog.Generation++
	worker.catalog.Tabs = append(worker.catalog.Tabs, TabContext{
		ID: "tab_secondary", Kind: TabOpened, CreationSequence: 2, DocumentGeneration: 1,
		URL: initialBlankOrigin, Origin: initialBlankOrigin,
	})
	worker.catalog.SelectedTabID = "tab_secondary"
	worker.catalog.SelectedFrameID = ""
	return cloneContextCatalog(worker.catalog), nil
}

func (worker *contextBrokerTestWorker) SelectContext(
	_ context.Context,
	authority ContextMutationAuthority,
) (DriverObservation, ContextCatalog, error) {
	if worker.beforeSelect != nil {
		worker.beforeSelect()
	}
	if err := authority.validateLive(worker.catalog); err != nil {
		return DriverObservation{}, ContextCatalog{}, err
	}
	tabID, frameID := authority.tabID, authority.frameID
	worker.selections = append(worker.selections, [2]string{tabID, frameID})
	worker.catalog.Generation++
	worker.catalog.SelectedTabID = tabID
	worker.catalog.SelectedFrameID = frameID
	return worker.observation, cloneContextCatalog(worker.catalog), nil
}

func (worker *contextBrokerTestWorker) SelectContextWithNavigationIdentity(
	ctx context.Context,
	authority ContextMutationAuthority,
) (DriverObservation, ContextCatalog, string, error) {
	observation, catalog, err := worker.SelectContext(ctx, authority)
	if err != nil {
		return DriverObservation{}, ContextCatalog{}, "", err
	}
	navigationID, err := worker.NavigationIdentity(ctx)
	return observation, catalog, navigationID, err
}

func (worker *contextBrokerTestWorker) CloseTab(
	_ context.Context,
	authority ContextMutationAuthority,
) (ContextCatalog, error) {
	if worker.beforeClose != nil {
		worker.beforeClose()
	}
	if err := authority.validateLive(worker.catalog); err != nil {
		return ContextCatalog{}, err
	}
	tabID := authority.tabID
	worker.closes = append(worker.closes, tabID)
	remaining := make([]TabContext, 0, len(worker.catalog.Tabs)-1)
	for _, tab := range worker.catalog.Tabs {
		if tab.ID != tabID {
			remaining = append(remaining, tab)
		}
	}
	if len(remaining) == len(worker.catalog.Tabs) {
		return ContextCatalog{}, ErrNotFound
	}
	worker.catalog.Generation++
	worker.catalog.Tabs = remaining
	worker.catalog.SelectedTabID = remaining[0].ID
	worker.catalog.SelectedFrameID = ""
	return cloneContextCatalog(worker.catalog), nil
}

type contextBrokerTestFactory struct{ worker *contextBrokerTestWorker }

func (factory *contextBrokerTestFactory) Open(
	context.Context,
	WorkerOpenRequest,
) (WorkerOpenResult, error) {
	return WorkerOpenResult{Owner: factory.worker}, nil
}

func openContextBrokerTest(t *testing.T, dryRun bool) (*Broker, *MemoryStore, *contextBrokerTestWorker, Session) {
	t.Helper()
	root := admittedBrowserConfig()
	profile := root.Tools.Browser.Targets["gateway"].Profiles["managed"]
	profile.DryRun = dryRun
	profile.AllowApprovedActions = !dryRun
	target := root.Tools.Browser.Targets["gateway"]
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	store := NewMemoryStore()
	actionWorker := &actionTestWorker{observation: driverObservationFixture(
		DriverElement{Target: "e1", Role: "button", Name: "Continue"},
	)}
	actionWorker.resolveElement = actionWorker.observation.Elements[0]
	actionWorker.resolveOrigin = actionWorker.observation.Origin
	actionWorker.screenshot = DriverScreenshot{
		Data: append(append([]byte(nil), pngSignature...), []byte("fixture")...), ContentType: "image/png",
	}
	worker := &contextBrokerTestWorker{actionTestWorker: actionWorker, catalog: ContextCatalog{
		ID: "catalog_gateway", Generation: 9, SelectedTabID: "tab_primary",
		Tabs: []TabContext{{
			ID: "tab_primary", Kind: TabPrimary, CreationSequence: 1, DocumentGeneration: 1,
			URL: "https://example.com/form", Origin: "https://example.com", Title: "Fixture",
			Frames: []FrameContext{{
				ID: "frame_child", CreationSequence: 1, Depth: 1, DocumentGeneration: 1,
				URL: "https://example.com/frame", Origin: "https://example.com",
				Label: "Child", Availability: FrameReady,
			}, {
				ID: "frame_external", CreationSequence: 2, Depth: 1, DocumentGeneration: 1,
				URL: "https://outside.example/frame", Origin: "https://outside.example",
				Label: "External", Availability: FrameReady,
			}},
		}},
	}}
	broker := newTestBroker(t, root, store, &contextBrokerTestFactory{worker: worker})
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	now := time.Now().UTC()
	broker.now = func() time.Time { now = now.Add(time.Nanosecond); return now }
	session, err := broker.Open(t.Context(), OpenRequest{
		Owner: testOwner(), Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker, store, worker, session
}

func TestContextBrokerSelectedFrameScreenshotBindsDocumentAndCatalog(t *testing.T) {
	broker, _, worker, session := openContextBrokerTest(t, false)
	catalog, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := broker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_select_frame_capture", SessionID: session.ID,
		Operation: ContextSelect, ContextCatalogID: catalog.ID,
		ContextGeneration: catalog.Generation, TabID: "tab_primary", FrameID: "frame_child",
	})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := broker.ExecuteContext(t.Context(), preparation, nil)
	if err != nil || selected.Observation == nil {
		t.Fatalf("ExecuteContext(select) = %#v, %v", selected, err)
	}
	observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
		Owner: testOwner(), SessionID: session.ID, TabID: selected.Catalog.SelectedTabID,
		FrameID: selected.Catalog.SelectedFrameID, ContextCatalogID: selected.Catalog.ID,
		ContextGeneration: selected.Catalog.Generation,
	})
	if err != nil {
		t.Fatalf("ObserveContext(selected frame) error = %v", err)
	}
	capture, err := broker.CaptureScreenshot(t.Context(), ScreenshotRequest{
		Owner: testOwner(), RequestID: "request_capture_selected_frame", SessionID: session.ID,
		TabID: observation.TabID, FrameID: observation.FrameID,
		ContextCatalogID: observation.ContextCatalogID, ContextGeneration: observation.ContextGeneration,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Target: ScreenshotTargetPage,
	})
	if err != nil || capture.FrameID != "frame_child" ||
		capture.ContextCatalogID != observation.ContextCatalogID ||
		worker.screenshotNavigationID == "" {
		t.Fatalf("CaptureScreenshot(selected frame) = %#v, %v", capture, err)
	}
}

func TestContextBrokerScreenshotRejectsCatalogMutationDuringCapture(t *testing.T) {
	broker, store, worker, session := openContextBrokerTest(t, false)
	if _, err := broker.ListContexts(t.Context(), testOwner(), session.ID); err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	worker.beforeScreenshot = func() {
		worker.beforeScreenshot = nil
		worker.catalog.Generation++
		worker.catalog.Tabs[0].Frames[0].DocumentGeneration++
	}
	_, err = broker.CaptureScreenshot(t.Context(), ScreenshotRequest{
		Owner: testOwner(), RequestID: "request_capture_catalog_race", SessionID: session.ID,
		TabID: observation.TabID, FrameID: observation.FrameID,
		ContextCatalogID: observation.ContextCatalogID, ContextGeneration: observation.ContextGeneration,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Target: ScreenshotTargetPage,
	})
	if !errors.Is(err, ErrStale) {
		stored, _ := store.GetSession(t.Context(), session.ID)
		t.Fatalf(
			"CaptureScreenshot(catalog mutation) error = %v, want ErrStale; worker catalog = %#v; stored = %#v",
			err,
			worker.catalog,
			stored.ContextAuthority,
		)
	}
	stored, getErr := store.GetSession(t.Context(), session.ID)
	if getErr != nil || stored.ContextAuthority == nil ||
		stored.ContextAuthority.Generation == observation.ContextGeneration {
		t.Fatalf("persisted changed catalog = %#v, %v", stored.ContextAuthority, getErr)
	}
}

func TestContextBrokerListOpenSelectCloseLifecycle(t *testing.T) {
	broker, store, worker, session := openContextBrokerTest(t, false)
	catalog, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
	if err != nil || catalog.Generation != 1 || catalog.SelectedTabID != "tab_primary" {
		t.Fatalf("ListContexts() = %#v, %v", catalog, err)
	}
	if external := catalog.Tabs[0].Frames[1]; external.Availability != FrameUnavailable ||
		external.SafeFailure != "frame_policy_denied" {
		t.Fatalf("off-policy frame projection = %#v", external)
	}
	stored, _ := store.GetSession(t.Context(), session.ID)
	if stored.ContextAuthority == nil || stored.TabID != "tab_primary" {
		t.Fatalf("stored initial context = %#v", stored)
	}

	openRequest := ContextRequest{
		Owner: testOwner(), RequestID: "request_open_context", SessionID: session.ID, Operation: ContextOpen,
	}
	openedPreparation, err := broker.PrepareContext(t.Context(), openRequest)
	if err != nil || openedPreparation.RequiresApproval {
		t.Fatalf("PrepareContext(open) = %#v, %v", openedPreparation, err)
	}
	opened, err := broker.ExecuteContext(t.Context(), openedPreparation, nil)
	if err != nil || opened.Invocation.State != InvocationSucceeded ||
		opened.Catalog.SelectedTabID != "tab_secondary" || opened.Catalog.Generation != 2 || worker.openCalls != 1 {
		t.Fatalf("ExecuteContext(open) = %#v, %v; calls = %d", opened, err, worker.openCalls)
	}
	replayedPreparation, err := broker.PrepareContext(t.Context(), openRequest)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := broker.ExecuteContext(t.Context(), replayedPreparation, nil)
	if err != nil || replayed.Invocation.ID != opened.Invocation.ID || worker.openCalls != 1 {
		t.Fatalf("replayed open = %#v, %v; calls = %d", replayed, err, worker.openCalls)
	}

	selectRequest := ContextRequest{
		Owner: testOwner(), RequestID: "request_select_context", SessionID: session.ID,
		Operation: ContextSelect, ContextCatalogID: opened.Catalog.ID,
		ContextGeneration: opened.Catalog.Generation, TabID: "tab_primary", FrameID: "frame_child",
	}
	selectedPreparation, err := broker.PrepareContext(t.Context(), selectRequest)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := broker.ExecuteContext(t.Context(), selectedPreparation, nil)
	if err != nil || selected.Observation == nil || selected.Observation.FrameID != "frame_child" ||
		selected.Catalog.Generation != 3 || len(worker.selections) != 1 {
		t.Fatalf("ExecuteContext(select) = %#v, %v; selections = %#v", selected, err, worker.selections)
	}
	replayedSelection, err := broker.ExecuteContext(t.Context(), selectedPreparation, nil)
	if err != nil || replayedSelection.Observation != nil ||
		replayedSelection.Catalog.Generation != selected.Catalog.Generation || len(worker.selections) != 1 {
		t.Fatalf(
			"replayed select = %#v, %v; selections = %#v",
			replayedSelection,
			err,
			worker.selections,
		)
	}

	closeRequest := ContextRequest{
		Owner: testOwner(), RequestID: "request_close_context", SessionID: session.ID,
		Operation: ContextClose, ContextCatalogID: selected.Catalog.ID,
		ContextGeneration: selected.Catalog.Generation, TabID: "tab_secondary",
	}
	closePreparation, err := broker.PrepareContext(t.Context(), closeRequest)
	if err != nil || !closePreparation.RequiresApproval {
		t.Fatalf("PrepareContext(close) = %#v, %v", closePreparation, err)
	}
	if _, err = broker.ExecuteContext(t.Context(), closePreparation, nil); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("unapproved close error = %v", err)
	}
	forged := closePreparation
	forged.RequiresApproval = false
	if _, err = broker.ExecuteContext(t.Context(), forged, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged close preparation error = %v", err)
	}
	closed, err := broker.ExecuteContext(t.Context(), closePreparation, &closePreparation.Approval)
	if err != nil || closed.Catalog.SelectedTabID != "tab_primary" || len(closed.Catalog.Tabs) != 1 ||
		len(worker.closes) != 1 {
		t.Fatalf("ExecuteContext(close) = %#v, %v; closes = %#v", closed, err, worker.closes)
	}
}

func TestContextBrokerRevalidatesLiveCatalogBeforeMutationAcceptance(t *testing.T) {
	broker, store, worker, session := openContextBrokerTest(t, false)
	if _, err := broker.ListContexts(t.Context(), testOwner(), session.ID); err != nil {
		t.Fatal(err)
	}
	openPreparation, err := broker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_open_for_revalidation", SessionID: session.ID,
		Operation: ContextOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := broker.ExecuteContext(t.Context(), openPreparation, nil)
	if err != nil {
		t.Fatal(err)
	}
	selectPreparation, err := broker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_select_revalidation", SessionID: session.ID,
		Operation: ContextSelect, ContextCatalogID: opened.Catalog.ID,
		ContextGeneration: opened.Catalog.Generation, TabID: "tab_primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.catalog.Generation++
	worker.catalog.Tabs[0].DocumentGeneration++
	if _, err = broker.ExecuteContext(t.Context(), selectPreparation, nil); !errors.Is(err, ErrStale) {
		t.Fatalf("select after live context change error = %v, want stale", err)
	}
	if len(worker.selections) != 0 {
		t.Fatalf("stale select reached driver: %#v", worker.selections)
	}
	storedSelect, err := store.GetInvocation(t.Context(), selectPreparation.Invocation.ID)
	if err != nil || storedSelect.State != InvocationPrepared {
		t.Fatalf("stale select invocation = %#v, %v", storedSelect, err)
	}

	fresh, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	closePreparation, err := broker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_close_revalidation", SessionID: session.ID,
		Operation: ContextClose, ContextCatalogID: fresh.ID,
		ContextGeneration: fresh.Generation, TabID: "tab_secondary",
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.catalog.Generation++
	worker.catalog.Tabs[1].DocumentGeneration++
	if _, err = broker.ExecuteContext(
		t.Context(), closePreparation, &closePreparation.Approval,
	); !errors.Is(err, ErrStale) {
		t.Fatalf("close after live context change error = %v, want stale", err)
	}
	if len(worker.closes) != 0 {
		t.Fatalf("stale close reached driver: %#v", worker.closes)
	}
	storedClose, err := store.GetInvocation(t.Context(), closePreparation.Invocation.ID)
	if err != nil || storedClose.State != InvocationPrepared {
		t.Fatalf("stale close invocation = %#v, %v", storedClose, err)
	}
}

func TestContextBrokerBindsMutationToWorkerLockedCatalog(t *testing.T) {
	t.Run("select", func(t *testing.T) {
		broker, store, worker, session := openContextBrokerTest(t, false)
		catalog, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		preparation, err := broker.PrepareContext(t.Context(), ContextRequest{
			Owner: testOwner(), RequestID: "request_select_worker_boundary", SessionID: session.ID,
			Operation: ContextSelect, ContextCatalogID: catalog.ID,
			ContextGeneration: catalog.Generation, TabID: catalog.SelectedTabID,
		})
		if err != nil {
			t.Fatal(err)
		}
		worker.beforeSelect = func() {
			worker.beforeSelect = nil
			worker.catalog.Tabs[0].DocumentGeneration++
		}
		if _, err = broker.ExecuteContext(t.Context(), preparation, nil); !errors.Is(err, ErrStale) {
			t.Fatalf("worker-boundary select error = %v, want stale", err)
		}
		if len(worker.selections) != 0 {
			t.Fatalf("stale worker-boundary select reached driver: %#v", worker.selections)
		}
		invocation, getErr := store.GetInvocation(t.Context(), preparation.Invocation.ID)
		if getErr != nil || invocation.State != InvocationFailed || invocation.SafeFailure != "context_stale" {
			t.Fatalf("stale select invocation = %#v, %v", invocation, getErr)
		}
	})

	t.Run("close", func(t *testing.T) {
		broker, store, worker, session := openContextBrokerTest(t, false)
		worker.catalog.Tabs = append(worker.catalog.Tabs, TabContext{
			ID: "tab_secondary", Kind: TabOpened, CreationSequence: 2, DocumentGeneration: 1,
			URL: initialBlankOrigin, Origin: initialBlankOrigin,
		})
		catalog, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		preparation, err := broker.PrepareContext(t.Context(), ContextRequest{
			Owner: testOwner(), RequestID: "request_close_worker_boundary", SessionID: session.ID,
			Operation: ContextClose, ContextCatalogID: catalog.ID,
			ContextGeneration: catalog.Generation, TabID: "tab_secondary",
		})
		if err != nil {
			t.Fatal(err)
		}
		worker.beforeClose = func() {
			worker.beforeClose = nil
			worker.catalog.Tabs[1].DocumentGeneration++
		}
		if _, err = broker.ExecuteContext(
			t.Context(), preparation, &preparation.Approval,
		); !errors.Is(err, ErrStale) {
			t.Fatalf("worker-boundary close error = %v, want stale", err)
		}
		if len(worker.closes) != 0 {
			t.Fatalf("stale worker-boundary close reached driver: %#v", worker.closes)
		}
		invocation, getErr := store.GetInvocation(t.Context(), preparation.Invocation.ID)
		if getErr != nil || invocation.State != InvocationFailed || invocation.SafeFailure != "context_stale" {
			t.Fatalf("stale close invocation = %#v, %v", invocation, getErr)
		}
	})
}

func TestContextBrokerRejectsStaleAndDryRunClose(t *testing.T) {
	broker, _, _, session := openContextBrokerTest(t, false)
	catalog, err := broker.ListContexts(t.Context(), testOwner(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_stale_context", SessionID: session.ID,
		Operation: ContextSelect, ContextCatalogID: catalog.ID,
		ContextGeneration: catalog.Generation + 1, TabID: catalog.SelectedTabID,
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale context error = %v", err)
	}

	dryBroker, _, dryWorker, drySession := openContextBrokerTest(t, true)
	_, err = dryBroker.ListContexts(t.Context(), testOwner(), drySession.ID)
	if err != nil {
		t.Fatal(err)
	}
	dryWorker.catalog.Tabs = append(dryWorker.catalog.Tabs, TabContext{
		ID: "tab_secondary", Kind: TabOpened, CreationSequence: 2, DocumentGeneration: 1,
		URL: initialBlankOrigin, Origin: initialBlankOrigin,
	})
	dryWorker.catalog.Generation++
	dryCatalog, err := dryBroker.ListContexts(t.Context(), testOwner(), drySession.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dryBroker.PrepareContext(t.Context(), ContextRequest{
		Owner: testOwner(), RequestID: "request_dry_close", SessionID: drySession.ID,
		Operation: ContextClose, ContextCatalogID: dryCatalog.ID,
		ContextGeneration: dryCatalog.Generation, TabID: "tab_secondary",
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("dry-run close error = %v", err)
	}
}
