package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

type actionTestWorker struct {
	observation            DriverObservation
	observeErr             error
	observeCalls           int
	resolveElement         DriverElement
	resolveElements        map[string]DriverElement
	resolveOrigin          string
	resolveErr             error
	resolveCalls           int
	actions                []DriverAction
	onExecute              func(DriverAction)
	navigationID           string
	catalogCalls           int
	beforeNavCheck         func()
	authorizeErr           error
	authorizeCalls         int
	executeErr             error
	screenshot             DriverScreenshot
	screenshotErr          error
	screenshotElement      DriverElement
	screenshotNavigationID string
	beforeScreenshot       func()
	uploads                []DriverAction
	download               DriverDownload
	closed                 int
	statusCalls            int
	humanControl           bool
	beginHumanErr          error
	endHumanErr            error
	diagnostics            DiagnosticSummary
	diagnosticsErr         error
	diagnosticsCalls       int
	diagnosticCategories   []DiagnosticCategory
}

func (worker *actionTestWorker) Diagnostics(
	_ context.Context,
	categories []DiagnosticCategory,
) (DiagnosticSummary, error) {
	worker.diagnosticsCalls++
	worker.diagnosticCategories = append([]DiagnosticCategory(nil), categories...)
	return worker.diagnostics, worker.diagnosticsErr
}

func (worker *actionTestWorker) AuthorizeFill(
	ctx context.Context,
	expected string,
	_ string,
) error {
	worker.authorizeCalls++
	current, err := worker.NavigationIdentity(ctx)
	if err != nil {
		return err
	}
	if expected == "" || expected != current {
		return ErrStale
	}
	return worker.authorizeErr
}

func (worker *actionTestWorker) BeginHumanControl(context.Context) error {
	if worker.beginHumanErr != nil {
		return worker.beginHumanErr
	}
	if worker.humanControl {
		return ErrConflict
	}
	worker.humanControl = true
	return nil
}

func (worker *actionTestWorker) EndHumanControl(context.Context) error {
	if worker.endHumanErr != nil {
		return worker.endHumanErr
	}
	if !worker.humanControl {
		return ErrConflict
	}
	worker.humanControl = false
	return nil
}

func (worker *actionTestWorker) Status(context.Context) (WorkerStatus, error) {
	worker.statusCalls++
	return WorkerReady, nil
}

func (worker *actionTestWorker) Close(context.Context) error {
	worker.closed++
	return nil
}

func (worker *actionTestWorker) Observe(context.Context) (DriverObservation, error) {
	worker.observeCalls++
	return worker.observation, worker.observeErr
}

func (worker *actionTestWorker) Resolve(_ context.Context, target string) (DriverElement, string, error) {
	worker.resolveCalls++
	if worker.resolveErr != nil {
		return DriverElement{}, "", worker.resolveErr
	}
	if element, ok := worker.resolveElements[target]; ok {
		return element, worker.resolveOrigin, nil
	}
	return worker.resolveElement, worker.resolveOrigin, nil
}

func (worker *actionTestWorker) Execute(_ context.Context, action DriverAction) error {
	worker.actions = append(worker.actions, action)
	if worker.onExecute != nil {
		worker.onExecute(action)
	}
	return worker.executeErr
}

func (worker *actionTestWorker) NavigationIdentity(context.Context) (string, error) {
	if worker.navigationID == "" {
		return "navigation_1", nil
	}
	return worker.navigationID, nil
}

func (worker *actionTestWorker) ExecuteAfterNavigationCheck(
	ctx context.Context,
	expected string,
	action DriverAction,
) error {
	if worker.beforeNavCheck != nil {
		worker.beforeNavCheck()
	}
	current, err := worker.NavigationIdentity(ctx)
	if err != nil {
		return err
	}
	if expected == "" || expected != current {
		return ErrStale
	}
	return worker.Execute(ctx, action)
}

func (worker *actionTestWorker) CatalogRevision() string {
	worker.catalogCalls++
	return strings.Repeat("c", 64)
}

func (worker *actionTestWorker) CaptureScreenshot(context.Context, int) (DriverScreenshot, error) {
	return worker.screenshot, worker.screenshotErr
}

func (worker *actionTestWorker) CapturePageScreenshot(
	_ context.Context,
	expectedNavigationID string,
	_ int,
) (DriverScreenshot, error) {
	if worker.beforeScreenshot != nil {
		worker.beforeScreenshot()
	}
	if expectedNavigationID == "" ||
		(expectedNavigationID != worker.navigationID && expectedNavigationID != "navigation_1") {
		return DriverScreenshot{}, ErrStale
	}
	worker.screenshotNavigationID = expectedNavigationID
	return worker.screenshot, worker.screenshotErr
}

func (worker *actionTestWorker) CaptureElementScreenshot(
	_ context.Context,
	expectedNavigationID string,
	expectedOrigin string,
	element DriverElement,
	_ int,
) (DriverScreenshot, error) {
	if expectedNavigationID == "" ||
		(expectedNavigationID != worker.navigationID && expectedNavigationID != "navigation_1") ||
		expectedOrigin != worker.resolveOrigin ||
		element != worker.resolveElement {
		return DriverScreenshot{}, ErrStale
	}
	worker.screenshotNavigationID = expectedNavigationID
	worker.screenshotElement = element
	return worker.screenshot, worker.screenshotErr
}

func (worker *actionTestWorker) Upload(_ context.Context, action DriverAction) error {
	worker.uploads = append(worker.uploads, action)
	return nil
}

func (worker *actionTestWorker) UploadAfterNavigationCheck(
	ctx context.Context,
	expected string,
	action DriverAction,
) error {
	current, err := worker.NavigationIdentity(ctx)
	if err != nil {
		return err
	}
	if expected == "" || expected != current {
		return ErrStale
	}
	return worker.Upload(ctx, action)
}

func (worker *actionTestWorker) Download(_ context.Context, action DriverAction, _ int64) (DriverDownload, error) {
	worker.actions = append(worker.actions, action)
	return worker.download, nil
}

type actionTestFactory struct {
	worker *actionTestWorker
}

func (factory *actionTestFactory) Open(
	context.Context,
	WorkerOpenRequest,
) (WorkerOpenResult, error) {
	return WorkerOpenResult{Owner: factory.worker}, nil
}

type preparedStageTestWorker struct {
	*actionTestWorker
	stageErr     error
	stageCalls   int
	executeCalls int
}

func (*preparedStageTestWorker) SupportsPreparedAction(kind ActionKind) bool {
	return kind == ActionFileChooser
}

func (worker *preparedStageTestWorker) StagePreparedAction(
	_ context.Context,
	_ WorkerPreparedAction,
) error {
	worker.stageCalls++
	return worker.stageErr
}

func (worker *preparedStageTestWorker) ExecutePrepared(
	_ context.Context,
	request WorkerPreparedAction,
) error {
	worker.executeCalls++
	worker.uploads = append(worker.uploads, request.DriverAction)
	return nil
}

type preparedStageTestFactory struct{ worker *preparedStageTestWorker }

func (factory *preparedStageTestFactory) Open(
	context.Context,
	WorkerOpenRequest,
) (WorkerOpenResult, error) {
	return WorkerOpenResult{Owner: factory.worker}, nil
}

type preparedDragTestWorker struct {
	*actionTestWorker
	executePreparedCalls int
}

func (*preparedDragTestWorker) SupportsPreparedAction(kind ActionKind) bool {
	return kind == ActionDrag
}

func (worker *preparedDragTestWorker) ExecutePrepared(
	_ context.Context,
	request WorkerPreparedAction,
) error {
	worker.executePreparedCalls++
	worker.actions = append(worker.actions, request.DriverAction)
	return worker.executeErr
}

type preparedDragTestFactory struct{ worker *preparedDragTestWorker }

func (factory *preparedDragTestFactory) Open(
	context.Context,
	WorkerOpenRequest,
) (WorkerOpenResult, error) {
	return WorkerOpenResult{Owner: factory.worker}, nil
}

type failOnceStagedAcceptanceStore struct {
	*MemoryStore
	failures int
}

func (store *failOnceStagedAcceptanceStore) UpdateInvocation(
	ctx context.Context,
	expected uint64,
	next Invocation,
) error {
	if next.State == InvocationAccepted && store.failures > 0 {
		store.failures--
		return ErrStale
	}
	return store.MemoryStore.UpdateInvocation(ctx, expected, next)
}

func TestBrokerObservationScopesOpaqueReferencesToFreshGeneration(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	owner := testOwner()
	first, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Snapshot, "ref=e1") || !strings.Contains(first.Snapshot, "ref=ref_") {
		t.Fatalf("model-visible snapshot contains an unscoped driver ref: %q", first.Snapshot)
	}
	firstRef := onlyVisibleRef(t, first.Snapshot)
	second, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotGeneration != first.SnapshotGeneration+1 ||
		onlyVisibleRef(t, second.Snapshot) == firstRef || first.PageStateHash == "" ||
		second.PageStateHash != first.PageStateHash {
		t.Fatalf("observations did not rotate authority: first=%+v second=%+v", first, second)
	}
	_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_old", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: first.SnapshotID, SnapshotGeneration: first.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: firstRef, Value: "Ada"},
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("PrepareAction() stale generation error = %v, want ErrStale", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("stale preparation dispatched actions: %+v", worker.actions)
	}
}

func TestBrokerBlocksThirdEquivalentApprovedActionOnUnchangedPage(t *testing.T) {
	store := NewMemoryStore()
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
	owner := testOwner()
	search := DriverElement{Target: "search", Role: "button", Name: "Search"}
	worker.observation = driverObservationFixture(search)
	worker.resolveElement = search
	worker.resolveOrigin = worker.observation.Origin

	var pageStateHash string
	for attempt := 1; attempt <= 2; attempt++ {
		observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
		if err != nil {
			t.Fatal(err)
		}
		if pageStateHash == "" {
			pageStateHash = observed.PageStateHash
		} else if observed.PageStateHash != pageStateHash {
			t.Fatalf(
				"page state hash changed across equivalent snapshots: %q != %q",
				observed.PageStateHash,
				pageStateHash,
			)
		}
		prepared, prepareErr := broker.PrepareAction(t.Context(), PrepareActionRequest{
			Owner: owner, RequestID: fmt.Sprintf("request_search_%d", attempt),
			SessionID: session.ID, TabID: session.TabID,
			SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
			Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
		})
		if prepareErr != nil || !prepared.RequiresApproval {
			t.Fatalf("PrepareAction(attempt %d) = %+v, %v", attempt, prepared, prepareErr)
		}
		invocation, executeErr := broker.ExecuteAction(
			t.Context(), owner, prepared.Action.ID, &prepared.Approval,
		)
		if executeErr != nil || invocation.State != InvocationSucceeded {
			t.Fatalf("ExecuteAction(attempt %d) = %+v, %v", attempt, invocation, executeErr)
		}
	}

	observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_search_3", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
	})
	if !errors.Is(err, ErrNoProgress) || len(worker.actions) != 2 {
		t.Fatalf("third equivalent preparation error = %v; actions = %+v", err, worker.actions)
	}
	stored, getErr := store.GetSession(t.Context(), session.ID)
	if getErr != nil || stored.ProgressSignature == "" || stored.ProgressCount != 2 {
		t.Fatalf("stored progress = %+v, %v", stored, getErr)
	}

	worker.observation.Title = "All postings"
	changed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || changed.PageStateHash == pageStateHash {
		t.Fatalf("changed observation = %+v, %v", changed, err)
	}
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_search_changed_scope",
		SessionID: session.ID, TabID: session.TabID,
		SnapshotID: changed.SnapshotID, SnapshotGeneration: changed.SnapshotGeneration,
		Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, changed.Snapshot)},
	})
	if err != nil || !prepared.RequiresApproval {
		t.Fatalf("changed-page preparation = %+v, %v", prepared, err)
	}
}

func TestBrokerHonorsDeclaredClickNavigationWithoutApproval(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	owner := testOwner()
	control := DriverElement{Target: "all", Role: "button", Name: "all postings"}
	worker.observation = driverObservationFixture(control)
	worker.resolveElement = control
	worker.resolveOrigin = worker.observation.Origin

	observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_all_postings", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action:         Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
		DeclaredEffect: EffectNavigation,
	})
	if err != nil || prepared.RequiresApproval || prepared.Action.Effect != EffectNavigation {
		t.Fatalf("PrepareAction() = %+v, %v", prepared, err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded {
		t.Fatalf("ExecuteAction() = %+v, %v", invocation, err)
	}
	if len(worker.actions) != 1 || worker.actions[0] != (DriverAction{
		Kind: DriverClick, Target: "all", Element: "all postings",
	}) {
		t.Fatalf("actions = %+v", worker.actions)
	}
}

func TestBrokerBindsModelDeclaredClickEffectsToApprovalPolicy(t *testing.T) {
	tests := []struct {
		effect           Effect
		requiresApproval bool
	}{
		{effect: EffectRead},
		{effect: EffectNavigation},
		{effect: EffectLocalEdit},
		{effect: EffectExternalCommit, requiresApproval: true},
		{effect: EffectUnknown, requiresApproval: true},
	}
	for _, test := range tests {
		t.Run(string(test.effect), func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			owner := testOwner()
			control := DriverElement{Target: "control", Role: "button", Name: "Continue"}
			worker.observation = driverObservationFixture(control)
			worker.resolveElement = control
			worker.resolveOrigin = worker.observation.Origin
			observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
				Owner: owner, RequestID: "request_declared_effect", SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
				Action:         Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
				DeclaredEffect: test.effect,
			})
			if err != nil || prepared.Action.Effect != test.effect ||
				prepared.RequiresApproval != test.requiresApproval {
				t.Fatalf("PrepareAction(%s) = %+v, %v", test.effect, prepared, err)
			}
		})
	}
}

func TestBrowserActionProgressSignatureDistinguishesDeclaredEffect(t *testing.T) {
	prepared := PreparedAction{
		Action: Action{Kind: ActionClick}, ElementRole: "button", ElementName: "Continue",
		ElementPosition: 1, Effect: EffectNavigation,
	}
	first, err := browserActionProgressSignature(strings.Repeat("a", 64), prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Effect = EffectExternalCommit
	second, err := browserActionProgressSignature(strings.Repeat("a", 64), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("progress signatures conflate declared effects: %q", first)
	}
}

func TestBrokerBlocksAlternatingApprovedActionCycleAcrossObservations(t *testing.T) {
	store := NewMemoryStore()
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
	owner := testOwner()

	type page struct {
		title   string
		element DriverElement
	}
	pages := []page{
		{title: "Active postings", element: DriverElement{Target: "all", Role: "button", Name: "all postings"}},
		{title: "All postings", element: DriverElement{Target: "active", Role: "button", Name: "active"}},
	}
	for attempt := 0; attempt < 4; attempt++ {
		current := pages[attempt%len(pages)]
		worker.observation = driverObservationFixture(current.element)
		worker.observation.Title = current.title
		worker.resolveElement = current.element
		worker.resolveOrigin = worker.observation.Origin
		observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
			Owner: owner, RequestID: fmt.Sprintf("request_cycle_%d", attempt),
			SessionID: session.ID, TabID: session.TabID,
			SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
			Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
		})
		if err != nil || !prepared.RequiresApproval {
			t.Fatalf("PrepareAction(attempt %d) = %+v, %v", attempt, prepared, err)
		}
		if _, err = broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, &prepared.Approval); err != nil {
			t.Fatalf("ExecuteAction(attempt %d) error = %v", attempt, err)
		}
	}

	worker.observation = driverObservationFixture(pages[0].element)
	worker.observation.Title = pages[0].title
	worker.resolveElement = pages[0].element
	observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_cycle_blocked", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observed.Snapshot)},
	})
	if !errors.Is(err, ErrNoProgress) || len(worker.actions) != 4 {
		t.Fatalf("alternating cycle error = %v; actions = %+v", err, worker.actions)
	}
	stored, getErr := store.GetSession(t.Context(), session.ID)
	if getErr != nil || stored.ProgressCount != 2 || stored.PriorProgressCount != 2 {
		t.Fatalf("stored alternating progress = %+v, %v", stored, getErr)
	}
}

func TestBrokerDistinguishesSameLabeledControlsByStructuralPosition(t *testing.T) {
	store := NewMemoryStore()
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
	owner := testOwner()
	controls := []DriverElement{
		{Target: "open_first", Role: "button", Name: "Open"},
		{Target: "open_second", Role: "button", Name: "Open"},
		{Target: "open_third", Role: "button", Name: "Open"},
	}
	worker.observation = driverObservationFixture(controls...)
	worker.resolveElements = map[string]DriverElement{
		controls[0].Target: controls[0],
		controls[1].Target: controls[1],
		controls[2].Target: controls[2],
	}
	worker.resolveOrigin = worker.observation.Origin

	for index := range controls {
		observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
		if err != nil {
			t.Fatal(err)
		}
		refs := visibleRefs(t, observed.Snapshot)
		prepared, prepareErr := broker.PrepareAction(t.Context(), PrepareActionRequest{
			Owner: owner, RequestID: fmt.Sprintf("request_open_%d", index+1),
			SessionID: session.ID, TabID: session.TabID,
			SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
			Action: Action{Kind: ActionClick, Ref: refs[index]},
		})
		if prepareErr != nil || !prepared.RequiresApproval {
			t.Fatalf("PrepareAction(control %d) = %+v, %v", index+1, prepared, prepareErr)
		}
		stored, getErr := store.GetPreparedAction(t.Context(), prepared.Action.ID)
		if getErr != nil || stored.ElementPosition != uint32(index+1) {
			t.Fatalf("stored control %d = %+v, %v", index+1, stored, getErr)
		}
		invocation, executeErr := broker.ExecuteAction(
			t.Context(), owner, prepared.Action.ID, &prepared.Approval,
		)
		if executeErr != nil || invocation.State != InvocationSucceeded {
			t.Fatalf("ExecuteAction(control %d) = %+v, %v", index+1, invocation, executeErr)
		}
	}
	if len(worker.actions) != len(controls) {
		t.Fatalf("actions = %+v, want all distinct controls executed", worker.actions)
	}
}

func TestBrowserActionProgressSignatureDistinguishesDragEndpointPositions(t *testing.T) {
	prepared := PreparedAction{
		Action: Action{Kind: ActionDrag}, ElementRole: "listitem", ElementName: "Item",
		ElementPosition: 1, DestinationElementRole: "listitem", DestinationElementName: "Item",
		DestinationElementPosition: 2,
	}
	first, err := browserActionProgressSignature(strings.Repeat("a", 64), prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared.DestinationElementPosition = 3
	second, err := browserActionProgressSignature(strings.Repeat("a", 64), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("drag signatures conflate distinct destination positions: %q", first)
	}
}

func TestBrokerObservationWorkerLossQuarantinesSessionAndReleasesProfile(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	worker.observeErr = ErrWorkerLost

	_, err := broker.Observe(context.Background(), testOwner(), session.ID, session.TabID)
	if !errors.Is(err, ErrWorkerLost) {
		t.Fatalf("Observe() error = %v, want ErrWorkerLost", err)
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionLost || stored.SafeFailure != "worker_lost" ||
		worker.closed != 1 {
		t.Fatalf("worker-loss quarantine = %+v, %v; worker = %+v", stored, getErr, worker)
	}
	availability, availabilityErr := broker.ProfileAvailability(
		context.Background(),
		"gateway",
		"managed",
	)
	if availabilityErr != nil || availability.Status != "ready" {
		t.Fatalf("ProfileAvailability() = %+v, %v, want ready", availability, availabilityErr)
	}
}

func TestBrokerObservationTransientWorkerUnavailablePreservesSession(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	worker.observeErr = ErrWorkerUnavailable

	_, err := broker.Observe(context.Background(), testOwner(), session.ID, session.TabID)
	if !errors.Is(err, ErrWorkerUnavailable) || errors.Is(err, ErrWorkerLost) {
		t.Fatalf("Observe() error = %v, want transient ErrWorkerUnavailable", err)
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionReady || worker.closed != 0 {
		t.Fatalf("transient observation failure = %+v, %v; worker = %+v", stored, getErr, worker)
	}
	availability, availabilityErr := broker.ProfileAvailability(
		context.Background(),
		"gateway",
		"managed",
	)
	if availabilityErr != nil || availability.Status != "busy" {
		t.Fatalf("ProfileAvailability() = %+v, %v, want busy", availability, availabilityErr)
	}
}

func TestBrokerUnknownActionOutcomeQuarantinesSessionAndReleasesProfile(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observed, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_unknown", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.executeErr = ErrWorkerUnavailable
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationUnknown || invocation.SafeFailure != "outcome_unknown" ||
		invocation.Diagnostic == nil ||
		invocation.Diagnostic.FailureClass != OutcomeFailureWorkerUnavailable {
		t.Fatalf("ExecuteAction() = %+v, %v, want unknown outcome", invocation, err)
	}
	storedInvocation, getInvocationErr := store.GetInvocation(context.Background(), invocation.ID)
	if getInvocationErr != nil || storedInvocation.Diagnostic != nil ||
		storedInvocation.State != InvocationUnknown || storedInvocation.SafeFailure != "outcome_unknown" {
		t.Fatalf(
			"stored invocation = %+v, %v; want durable outcome without diagnostic",
			storedInvocation,
			getInvocationErr,
		)
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionLost || stored.SafeFailure != "outcome_unknown" ||
		worker.closed != 1 {
		t.Fatalf("unknown-outcome quarantine = %+v, %v; worker = %+v", stored, getErr, worker)
	}
	availability, availabilityErr := broker.ProfileAvailability(
		context.Background(),
		"gateway",
		"managed",
	)
	if availabilityErr != nil || availability.Status != "ready" {
		t.Fatalf("ProfileAvailability() = %+v, %v, want ready", availability, availabilityErr)
	}
}

func TestBrokerAcceptedActionRetryQuarantinesWithoutReplay(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observed, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_accepted_retry", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocationID := derivedIdentifier("invocation", owner, session.ID, "request_accepted_retry")
	accepted, err := store.GetInvocation(context.Background(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	accepted.State = InvocationAccepted
	accepted.AcceptedAt = accepted.UpdatedAt + 1
	accepted.UpdatedAt = accepted.AcceptedAt
	accepted.Revision++
	if err = store.UpdateInvocation(context.Background(), accepted.Revision-1, accepted); err != nil {
		t.Fatal(err)
	}

	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationUnknown || invocation.Diagnostic == nil ||
		invocation.Diagnostic.FailureClass != OutcomeFailureWorkerUnavailable || len(worker.actions) != 0 {
		t.Fatalf("accepted retry = %+v, %v; worker actions = %+v", invocation, err, worker.actions)
	}
	lost, statusErr := store.GetSession(context.Background(), session.ID)
	if statusErr != nil || lost.State != SessionLost || lost.SafeFailure != "worker_lost" || worker.closed != 1 {
		t.Fatalf("accepted retry quarantine = %+v, %v; worker = %+v", lost, statusErr, worker)
	}
}

func TestBrokerUnknownActionRetryCompletesFailedQuarantineWithoutReplay(t *testing.T) {
	store := &failNextSessionUpdateStore{MemoryStore: NewMemoryStore()}
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observed, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_retry_quarantine", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.executeErr = ErrDriverRejected
	store.failAfter = 2
	first, firstErr := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if firstErr == nil || first.State != InvocationUnknown || len(worker.actions) != 1 {
		t.Fatalf("first execution = %+v, %v; actions = %+v", first, firstErr, worker.actions)
	}
	second, secondErr := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if secondErr != nil || second.State != InvocationUnknown || len(worker.actions) != 1 || worker.closed != 1 {
		t.Fatalf(
			"retry finalization = %+v, %v; actions = %+v; closes = %d",
			second,
			secondErr,
			worker.actions,
			worker.closed,
		)
	}
	lost, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || lost.State != SessionLost || lost.SafeFailure != "outcome_unknown" {
		t.Fatalf("retried quarantine = %+v, %v", lost, getErr)
	}
}

func TestBrokerHumanHandoffIsExclusiveAndResumeRequiresFreshObservation(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	owner := testOwner()
	observed, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_before_handoff", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	human, err := broker.Handoff(context.Background(), owner, session.ID)
	if err != nil || human.Controller != ControllerHuman || human.ControllerGeneration != 2 ||
		human.ControllerExpiresAt <= human.UpdatedAt || human.SnapshotID != "" || !worker.humanControl {
		t.Fatalf("Handoff() = %#v, %v; worker = %#v", human, err, worker)
	}
	status, err := broker.Status(context.Background(), owner, session.ID)
	if err != nil || status.Controller != ControllerHuman || status.State != SessionReady || worker.closed != 0 {
		t.Fatalf("Status() during human control = %#v, %v; worker = %#v", status, err, worker)
	}
	if _, err = broker.Observe(
		context.Background(),
		owner,
		session.ID,
		session.TabID,
	); !errors.Is(
		err,
		ErrWorkerUnavailable,
	) {
		t.Fatalf("Observe() during human control error = %v", err)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationCanceled || len(worker.actions) != 0 {
		t.Fatalf("old prepared action = %#v, %v; actions = %#v", invocation, err, worker.actions)
	}
	released, err := broker.ReleaseHandoff(context.Background(), owner, session.ID)
	if err != nil || released.Controller != ControllerResumePending || worker.humanControl {
		t.Fatalf("ReleaseHandoff() = %#v, %v; worker = %#v", released, err, worker)
	}
	resumed, err := broker.Resume(context.Background(), owner, session.ID)
	if err != nil || resumed.Controller != ControllerAgent || resumed.ControllerGeneration != 3 ||
		resumed.ControllerExpiresAt != 0 || resumed.SnapshotID != "" || worker.humanControl {
		t.Fatalf("Resume() = %#v, %v; worker = %#v", resumed, err, worker)
	}
	if _, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_stale_after_resume", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot), Value: "Grace"},
	}); !errors.Is(err, ErrStale) {
		t.Fatalf("PrepareAction() after resume error = %v", err)
	}
	fresh, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil || fresh.SnapshotGeneration != observed.SnapshotGeneration+1 ||
		fresh.SnapshotID == observed.SnapshotID {
		t.Fatalf("fresh Observe() = %#v, %v; prior = %#v", fresh, err, observed)
	}
}

func TestBrokerHumanHandoffReconcilesCommittedWriteWarnings(t *testing.T) {
	store := &committedWarningSessionUpdateStore{
		MemoryStore: NewMemoryStore(), warnControllers: make(map[ControllerState]int),
	}
	broker, worker, session := openActionTestBroker(t, store)
	store.warnControllers = map[ControllerState]int{
		ControllerHumanPending:  1,
		ControllerHuman:         1,
		ControllerResumePending: 1,
		ControllerAgent:         1,
	}
	human, err := broker.Handoff(context.Background(), testOwner(), session.ID)
	if err != nil || human.Controller != ControllerHuman || !worker.humanControl {
		t.Fatalf("Handoff() = %#v, %v; worker = %#v", human, err, worker)
	}
	pending, err := broker.ReleaseHandoff(context.Background(), testOwner(), session.ID)
	if err != nil || pending.Controller != ControllerResumePending || worker.humanControl {
		t.Fatalf("ReleaseHandoff() = %#v, %v; worker = %#v", pending, err, worker)
	}
	resumed, err := broker.Resume(context.Background(), testOwner(), session.ID)
	if err != nil || resumed.Controller != ControllerAgent || resumed.ControllerGeneration != 3 {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
}

func TestBrokerRecoveryRevokesHumanController(t *testing.T) {
	store := NewMemoryStore()
	broker, _, session := openActionTestBroker(t, store)
	human, err := broker.Handoff(context.Background(), testOwner(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered := newTestBroker(t, admittedBrowserConfig(), store, &actionTestFactory{worker: &actionTestWorker{}})
	if err = recovered.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	lost, err := store.GetSession(context.Background(), session.ID)
	if err != nil || lost.State != SessionLost || lost.Controller != ControllerAgent ||
		lost.ControllerExpiresAt != 0 || lost.ControllerGeneration != human.ControllerGeneration+1 ||
		lost.SafeFailure != "gateway_restarted" {
		t.Fatalf("recovered human session = %#v, %v", lost, err)
	}
}

func TestBrokerHumanHandoffExpiryClosesExclusiveController(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	human, err := broker.Handoff(context.Background(), testOwner(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	broker.now = func() time.Time { return time.Unix(0, human.ControllerExpiresAt) }
	expired, err := broker.Status(context.Background(), testOwner(), session.ID)
	if err != nil || expired.State != SessionExpired || expired.Controller != ControllerAgent ||
		expired.ControllerExpiresAt != 0 || worker.closed != 1 {
		t.Fatalf("expired handoff = %#v, %v; worker = %#v", expired, err, worker)
	}
}

func TestBrokerHumanHandoffDriverFailuresCloseSession(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		broker, worker, session := openActionTestBroker(t, NewMemoryStore())
		worker.beginHumanErr = errors.New("begin failed")
		failed, err := broker.Handoff(context.Background(), testOwner(), session.ID)
		if err == nil || failed.State != SessionLost || failed.Controller != ControllerAgent ||
			failed.SafeFailure != "handoff_failed" || worker.closed != 1 {
			t.Fatalf("Handoff() = %#v, %v; worker = %#v", failed, err, worker)
		}
	})
	t.Run("release", func(t *testing.T) {
		broker, worker, session := openActionTestBroker(t, NewMemoryStore())
		if _, err := broker.Handoff(context.Background(), testOwner(), session.ID); err != nil {
			t.Fatal(err)
		}
		worker.endHumanErr = errors.New("release failed")
		failed, err := broker.ReleaseHandoff(context.Background(), testOwner(), session.ID)
		if err == nil || failed.State != SessionLost || failed.Controller != ControllerAgent ||
			failed.SafeFailure != "resume_failed" || worker.closed != 1 {
			t.Fatalf("ReleaseHandoff() = %#v, %v; worker = %#v", failed, err, worker)
		}
	})
}

func TestBrokerBindsFileChooserArtifactAndRequiresApprovalForDownloadClick(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	owner := testOwner()
	worker.observation = driverObservationFixture(DriverElement{Target: "e2", Role: "button", Name: "Choose file"})
	worker.resolveElement = worker.observation.Elements[0]
	uploadObservation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_upload", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: uploadObservation.SnapshotID, SnapshotGeneration: uploadObservation.SnapshotGeneration,
		Action: Action{
			Kind: ActionFileChooser, Ref: onlyVisibleRef(t, uploadObservation.Snapshot),
			ArtifactRef: "transfer-artifact://opaque",
		},
		Upload: &UploadBinding{
			Ref: "transfer-artifact://opaque", SHA256: strings.Repeat("a", 64), Size: 7,
			Filename: "input.txt", ContentType: "text/plain", Path: "/private/retained/input.txt",
		},
	})
	if err != nil || upload.RequiresApproval {
		t.Fatalf("PrepareAction(file chooser) = %#v, %v", upload, err)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, upload.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded || len(worker.uploads) != 1 ||
		worker.uploads[0].Value != "/private/retained/input.txt" ||
		worker.uploads[0].ArtifactSHA256 != strings.Repeat("a", 64) || worker.uploads[0].ArtifactBytes != 7 {
		t.Fatalf("ExecuteAction(file chooser) = %#v, %v; uploads = %#v", invocation, err, worker.uploads)
	}

	worker.observation = driverObservationFixture(DriverElement{Target: "e3", Role: "link", Name: "Download"})
	worker.resolveElement = worker.observation.Elements[0]
	worker.download = DriverDownload{
		Path: "/private/output/result.txt", Filename: "result.txt", ContentType: "text/plain",
		SHA256: strings.Repeat("b", 64), Size: 9,
	}
	downloadObservation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	download, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_download", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: downloadObservation.SnapshotID, SnapshotGeneration: downloadObservation.SnapshotGeneration,
		Action: Action{Kind: ActionDownload, Ref: onlyVisibleRef(t, downloadObservation.Snapshot)},
	})
	if err != nil || !download.RequiresApproval || download.Action.Effect != EffectUnknown {
		t.Fatalf("PrepareAction(download) = %#v, %v", download, err)
	}
	if _, err = broker.ExecuteActionWithDownloadSink(
		context.Background(), owner, download.Action.ID, nil,
		func(context.Context, PreparedAction, DriverDownload) (json.RawMessage, error) {
			return json.RawMessage(`{"artifact":{"ref":"transfer-artifact://retained"}}`), nil
		},
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ExecuteAction(download without approval) error = %v, want ErrApprovalRequired", err)
	}
	invocation, err = broker.ExecuteActionWithDownloadSink(
		context.Background(), owner, download.Action.ID, &download.Approval,
		func(_ context.Context, prepared PreparedAction, retained DriverDownload) (json.RawMessage, error) {
			if prepared.Action.Kind != ActionDownload || retained != worker.download {
				t.Fatalf("download sink input = %#v, %#v", prepared, retained)
			}
			return json.RawMessage(`{"artifact":{"ref":"transfer-artifact://retained"}}`), nil
		},
	)
	if err != nil || invocation.State != InvocationSucceeded || len(worker.actions) != 1 ||
		worker.actions[0].Kind != DriverDownloadAction {
		t.Fatalf("approved dry-run download = %#v, %v; actions = %#v", invocation, err, worker.actions)
	}
}

func TestBrokerPreservesDownloadSuccessWhenRetentionSinkFails(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	owner := testOwner()
	worker.observation = driverObservationFixture(DriverElement{Target: "e3", Role: "link", Name: "Download"})
	worker.resolveElement = worker.observation.Elements[0]
	worker.download = DriverDownload{
		Path: "/private/output/result.txt", Filename: "result.txt", ContentType: "text/plain",
		SHA256: strings.Repeat("b", 64), Size: 9,
	}
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_download_sink_failure", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDownload, Ref: onlyVisibleRef(t, observation.Snapshot)},
	})
	if err != nil || !prepared.RequiresApproval {
		t.Fatalf("PrepareAction(download) = %#v, %v", prepared, err)
	}
	sinkCalls := 0
	failingSink := func(context.Context, PreparedAction, DriverDownload) (json.RawMessage, error) {
		sinkCalls++
		return nil, errors.New("retention unavailable")
	}
	invocation, err := broker.ExecuteActionWithDownloadSink(
		t.Context(), owner, prepared.Action.ID, &prepared.Approval, failingSink,
	)
	if err != nil || invocation.State != InvocationSucceeded ||
		!bytes.Contains(invocation.TerminalResult, []byte(`"artifact_state":"unavailable"`)) ||
		len(worker.actions) != 1 || sinkCalls != 1 {
		t.Fatalf(
			"download with failed sink = %#v, %v; actions = %#v; sink calls = %d",
			invocation, err, worker.actions, sinkCalls,
		)
	}
	replayed, err := broker.ExecuteActionWithDownloadSink(
		t.Context(), owner, prepared.Action.ID, &prepared.Approval, failingSink,
	)
	if err != nil || replayed.State != InvocationSucceeded || len(worker.actions) != 1 || sinkCalls != 1 {
		t.Fatalf(
			"replayed download with failed sink = %#v, %v; actions = %#v; sink calls = %d",
			replayed, err, worker.actions, sinkCalls,
		)
	}
}

func TestBrokerStagesPreparedArtifactBeforeDurableAcceptance(t *testing.T) {
	store := NewMemoryStore()
	baseWorker := &actionTestWorker{observation: driverObservationFixture(
		DriverElement{Target: "e2", Role: "button", Name: "Choose file"},
	)}
	baseWorker.resolveElement = baseWorker.observation.Elements[0]
	baseWorker.resolveOrigin = baseWorker.observation.Origin
	worker := &preparedStageTestWorker{
		actionTestWorker: baseWorker,
		stageErr:         ErrWorkerUnavailable,
	}
	broker := newTestBroker(t, admittedBrowserConfig(), store, &preparedStageTestFactory{worker: worker})
	now := time.Now().UTC()
	broker.now = func() time.Time {
		now = now.Add(time.Nanosecond)
		return now
	}
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_staging_boundary", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{
			Kind: ActionFileChooser, Ref: onlyVisibleRef(t, observation.Snapshot),
			ArtifactRef: "transfer-artifact://staging-boundary",
		},
		Upload: &UploadBinding{
			Ref: "transfer-artifact://staging-boundary", SHA256: strings.Repeat("a", 64), Size: 7,
			Filename: "input.txt", ContentType: "text/plain", Path: "/private/retained/input.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if !errors.Is(err, ErrWorkerUnavailable) || invocation.State != InvocationPrepared ||
		invocation.AcceptedAt != 0 || worker.stageCalls != 1 || worker.executeCalls != 0 {
		t.Fatalf(
			"failed pre-acceptance stage = %#v, %v; stage=%d execute=%d",
			invocation,
			err,
			worker.stageCalls,
			worker.executeCalls,
		)
	}
	stored, err := store.GetInvocation(context.Background(), invocation.ID)
	if err != nil || stored.State != InvocationPrepared || stored.AcceptedAt != 0 {
		t.Fatalf("durable invocation after staging failure = %#v, %v", stored, err)
	}
	storedSession, err := store.GetSession(context.Background(), session.ID)
	if err != nil || storedSession.State != SessionReady || storedSession.SafeFailure != "" {
		t.Fatalf("session after staging failure = %#v, %v", storedSession, err)
	}
	worker.stageErr = nil
	invocation, err = broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded || worker.stageCalls != 2 ||
		worker.executeCalls != 1 || len(worker.uploads) != 1 {
		t.Fatalf(
			"retried staged invocation = %#v, %v; stage=%d execute=%d uploads=%#v",
			invocation,
			err,
			worker.stageCalls,
			worker.executeCalls,
			worker.uploads,
		)
	}
}

func TestBrokerRetriesStagingAfterDurableAcceptanceFailure(t *testing.T) {
	store := &failOnceStagedAcceptanceStore{MemoryStore: NewMemoryStore(), failures: 1}
	baseWorker := &actionTestWorker{observation: driverObservationFixture(
		DriverElement{Target: "e2", Role: "button", Name: "Choose file"},
	)}
	baseWorker.resolveElement = baseWorker.observation.Elements[0]
	baseWorker.resolveOrigin = baseWorker.observation.Origin
	worker := &preparedStageTestWorker{actionTestWorker: baseWorker}
	broker := newTestBroker(t, admittedBrowserConfig(), store, &preparedStageTestFactory{worker: worker})
	now := time.Now().UTC()
	broker.now = func() time.Time {
		now = now.Add(time.Nanosecond)
		return now
	}
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_staging_acceptance_retry",
		SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{
			Kind: ActionFileChooser, Ref: onlyVisibleRef(t, observation.Snapshot),
			ArtifactRef: "transfer-artifact://acceptance-retry",
		},
		Upload: &UploadBinding{
			Ref: "transfer-artifact://acceptance-retry", SHA256: strings.Repeat("a", 64), Size: 7,
			Filename: "input.txt", ContentType: "text/plain", Path: "/private/retained/input.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation, executeErr := broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(executeErr, ErrStale) || invocation.ID != "" || worker.stageCalls != 1 ||
		worker.executeCalls != 0 {
		t.Fatalf(
			"first acceptance attempt = %#v, %v; stage=%d execute=%d",
			invocation,
			executeErr,
			worker.stageCalls,
			worker.executeCalls,
		)
	}
	invocationID := derivedIdentifier(
		"invocation", owner, session.ID, "request_staging_acceptance_retry",
	)
	stored, err := store.GetInvocation(context.Background(), invocationID)
	if err != nil || stored.State != InvocationPrepared || stored.AcceptedAt != 0 {
		t.Fatalf("invocation after failed acceptance = %#v, %v", stored, err)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded || worker.stageCalls != 2 ||
		worker.executeCalls != 1 || len(worker.uploads) != 1 {
		t.Fatalf(
			"retried acceptance = %#v, %v; stage=%d execute=%d uploads=%#v",
			invocation,
			err,
			worker.stageCalls,
			worker.executeCalls,
			worker.uploads,
		)
	}
}

func TestBrokerRecoversCommittedAcceptedDownloadWithoutDriverReplay(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	worker.observation = driverObservationFixture(DriverElement{Target: "e3", Role: "link", Name: "Download"})
	worker.resolveElement = worker.observation.Elements[0]
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_recover_download", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDownload, Ref: onlyVisibleRef(t, observation.Snapshot)},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := store.GetInvocation(
		context.Background(), derivedIdentifier("invocation", owner, session.ID, "request_recover_download"),
	)
	if err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationAccepted
	invocation.Revision++
	invocation.AcceptedAt = invocation.CreatedAt + 1
	invocation.UpdatedAt = invocation.AcceptedAt
	if err = store.UpdateInvocation(context.Background(), invocation.Revision-1, invocation); err != nil {
		t.Fatal(err)
	}
	if err = broker.Recover(
		context.Background(),
		func(_ context.Context, action PreparedAction) (bool, error) {
			return action.ID == prepared.Action.ID, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	recovered, err := broker.RecoverAcceptedDownload(
		context.Background(), owner, prepared.Action.ID,
		json.RawMessage(`{"status":"completed","artifact":{"ref":"transfer-artifact://retained"}}`),
	)
	if err != nil || recovered.State != InvocationSucceeded || len(worker.actions) != 0 {
		t.Fatalf("RecoverAcceptedDownload() = %#v, %v; actions = %#v", recovered, err, worker.actions)
	}
	status, err := broker.Status(context.Background(), owner, session.ID)
	if err != nil || status.SnapshotID != "" || status.SnapshotOrigin != "" {
		t.Fatalf("recovered session = %#v, %v", status, err)
	}
}

func TestBrokerRestartMakesUncommittedAcceptedDownloadUnknown(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	worker.observation = driverObservationFixture(DriverElement{Target: "e3", Role: "link", Name: "Download"})
	worker.resolveElement = worker.observation.Elements[0]
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_uncommitted_download", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDownload, Ref: onlyVisibleRef(t, observation.Snapshot)},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocationID := derivedIdentifier(
		"invocation", owner, session.ID, "request_uncommitted_download",
	)
	invocation, err := store.GetInvocation(context.Background(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationAccepted
	invocation.Revision++
	invocation.AcceptedAt = invocation.CreatedAt + 1
	invocation.UpdatedAt = invocation.AcceptedAt
	if err = store.UpdateInvocation(context.Background(), invocation.Revision-1, invocation); err != nil {
		t.Fatal(err)
	}
	if err = broker.Recover(
		context.Background(),
		func(_ context.Context, action PreparedAction) (bool, error) {
			if action.ID != prepared.Action.ID {
				t.Fatalf("verified prepared action = %q", action.ID)
			}
			return false, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetInvocation(context.Background(), invocationID)
	if err != nil || recovered.State != InvocationUnknown ||
		recovered.SafeFailure != "gateway_restarted" || recovered.CompletedAt == 0 {
		t.Fatalf("uncommitted recovered invocation = %#v, %v", recovered, err)
	}
	projected, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || projected.Diagnostic == nil ||
		projected.Diagnostic.FailureClass != OutcomeFailureWorkerUnavailable {
		t.Fatalf("recovered action diagnostic = %#v, %v", projected, err)
	}
}

func TestBrokerBlankSnapshotAuthorizesOnlyAllowedNavigation(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	worker.observation = DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin}

	blank, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if blank.URL != initialBlankOrigin || blank.Origin != initialBlankOrigin || blank.Snapshot != "" ||
		blank.SnapshotID == "" || blank.SnapshotGeneration != 1 {
		t.Fatalf("blank observation = %+v", blank)
	}
	_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_blank_scroll", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: blank.SnapshotID, SnapshotGeneration: blank.SnapshotGeneration,
		Action: Action{Kind: ActionScroll, Direction: "down", Amount: 1},
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("PrepareAction(blank scroll) error = %v, want ErrDenied", err)
	}

	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_blank_navigate", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: blank.SnapshotID, SnapshotGeneration: blank.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "https://example.com/form"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Action.CurrentOrigin != initialBlankOrigin ||
		prepared.Action.DestinationOrigin != "https://example.com" {
		t.Fatalf("prepared blank navigation = %+v", prepared.Action)
	}
	worker.onExecute = func(DriverAction) {
		worker.observation = driverObservationFixture(
			DriverElement{Target: "e1", Role: "textbox", Name: "Name"},
		)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded {
		t.Fatalf("ExecuteAction(blank navigation) = %+v, %v", invocation, err)
	}
	if len(worker.actions) != 1 || worker.actions[0].Kind != DriverNavigate ||
		worker.actions[0].URL != "https://example.com/form" {
		t.Fatalf("blank navigation driver actions = %+v", worker.actions)
	}
	observed, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil || observed.Origin != "https://example.com" || observed.SnapshotGeneration != 2 {
		t.Fatalf("Observe(after blank navigation) = %+v, %v", observed, err)
	}
}

func TestBrokerRejectsNonEmptyBlankDriverObservation(t *testing.T) {
	dialog := &DialogObservation{Type: "alert", Message: "unexpected"}
	tests := []struct {
		name        string
		observation DriverObservation
	}{
		{name: "different URL", observation: DriverObservation{URL: "about:srcdoc", Origin: initialBlankOrigin}},
		{
			name: "blank URL with HTTP origin",
			observation: DriverObservation{
				URL: initialBlankOrigin, Origin: "https://example.com",
			},
		},
		{
			name:        "title",
			observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin, Title: "Blank"},
		},
		{
			name:        "snapshot",
			observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin, Snapshot: "text"},
		},
		{
			name: "element",
			observation: DriverObservation{
				URL:      initialBlankOrigin,
				Origin:   initialBlankOrigin,
				Elements: []DriverElement{{Target: "e1"}},
			},
		},
		{
			name:        "dialog",
			observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin, PendingDialog: dialog},
		},
		{
			name:        "truncated",
			observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin, Truncated: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			worker.observation = test.observation
			_, err := broker.Observe(context.Background(), testOwner(), session.ID, session.TabID)
			if !errors.Is(err, ErrDriverIncompatible) {
				t.Fatalf("Observe() error = %v, want ErrDriverIncompatible", err)
			}
		})
	}
}

func TestBrokerRejectsBlankMutationAtActionObservationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		execute bool
	}{
		{name: "prepare"},
		{name: "execute", execute: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			owner := testOwner()
			worker.observation = DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin}
			blank, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			request := PrepareActionRequest{
				Owner: owner, RequestID: "request_blank_mutation", SessionID: session.ID,
				TabID: session.TabID, SnapshotID: blank.SnapshotID,
				SnapshotGeneration: blank.SnapshotGeneration,
				Action:             Action{Kind: ActionNavigate, URL: "https://example.com/form"},
			}
			if test.execute {
				prepared, prepareErr := broker.PrepareAction(context.Background(), request)
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				worker.observation.Title = "mutated"
				_, err = broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
			} else {
				worker.observation.Origin = "https://example.com"
				_, err = broker.PrepareAction(context.Background(), request)
			}
			if !errors.Is(err, ErrDriverIncompatible) {
				t.Fatalf("action boundary error = %v, want ErrDriverIncompatible", err)
			}
			if len(worker.actions) != 0 {
				t.Fatalf("malformed blank observation dispatched actions: %+v", worker.actions)
			}
		})
	}
}

func TestBrokerObservationDeniesPrivateDNSResolutionAndClosesSession(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	if _, err := broker.Observe(
		context.Background(), testOwner(), session.ID, session.TabID,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("Observe() private DNS error = %v, want ErrDenied", err)
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil || stored.State != SessionLost || stored.SafeFailure != "network_denied" || worker.closed != 1 {
		t.Fatalf("session after private DNS = %+v, %v; worker = %+v", stored, err, worker)
	}
}

func TestBrokerPublicWebOriginPolicyAllowsPublicSyntaxButRejectsPrivateLiterals(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkPublicWeb
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	broker := &Broker{config: root.Tools.Browser}
	session := Session{Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile}
	if !broker.originAllowed(session, "https://public.example") {
		t.Fatal("public_web rejected a normalized public origin")
	}
	for _, denied := range []string{
		"http://127.0.0.1", "http://169.254.169.254", "https://public.example:443",
	} {
		if broker.originAllowed(session, denied) {
			t.Fatalf("public_web allowed non-public or non-canonical origin %q", denied)
		}
	}
}

func TestBrokerAnyHTTPObservationAdmitsPrivateOrigin(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkAnyHTTP
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	store := NewMemoryStore()
	broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("169.254.169.254")}, nil
	}
	worker.observation.URL = "http://private.internal/status"
	worker.observation.Origin = "http://private.internal"
	worker.resolveOrigin = worker.observation.Origin

	observation, err := broker.Observe(
		context.Background(), testOwner(), session.ID, session.TabID,
	)
	if err != nil {
		t.Fatalf("Observe() private origin error = %v", err)
	}
	if observation.URL != "http://private.internal/status" ||
		observation.Origin != "http://private.internal" {
		t.Fatalf("private observation = %+v", observation)
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil || stored.State != SessionReady || stored.SnapshotOrigin != "http://private.internal" {
		t.Fatalf("stored private session = %+v, %v", stored, err)
	}
}

func TestBrokerAnyHTTPRejectsNavigationWithEmptyPort(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkAnyHTTP
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	observation, err := broker.Observe(
		context.Background(), testOwner(), session.ID, session.TabID,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_empty_port", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "http://127.0.0.1:/health"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("PrepareAction() empty-port error = %v, want ErrInvalid", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("empty-port navigation dispatched actions: %+v", worker.actions)
	}
}

func TestNavigationOriginPreservesMappedIPv6AddressFamily(t *testing.T) {
	origin, err := originFromURL("http://[::ffff:7f00:1]/health")
	if err != nil || origin != "http://[::ffff:127.0.0.1]" {
		t.Fatalf("originFromURL() = %q, %v", origin, err)
	}
}

func TestNavigationOriginCanonicalizesIPv4RootDotButRejectsShorthand(t *testing.T) {
	origin, err := originFromURL("http://127.0.0.1./health")
	if err != nil || origin != "http://127.0.0.1" {
		t.Fatalf("originFromURL(canonical IPv4) = %q, %v", origin, err)
	}
	if _, err = originFromURL("http://127.1./health"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("originFromURL(shorthand IPv4) error = %v, want ErrInvalid", err)
	}
}

func TestNavigationOriginPreservesScopedIPv6Zone(t *testing.T) {
	normalized, err := normalizeDriverNavigationURL("http://[FE80::1%25EtherNet]:8080/health")
	if err != nil || normalized != "http://[fe80::1%25EtherNet]:8080/health" {
		t.Fatalf("normalizeDriverNavigationURL() = %q, %v", normalized, err)
	}
	origin, err := originFromURL(normalized)
	if err != nil || origin != "http://[fe80::1%25EtherNet]:8080" {
		t.Fatalf("originFromURL() = %q, %v", origin, err)
	}
}

func TestBrokerAnyHTTPAdmitsScopedIPv6NavigationAndObservation(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkAnyHTTP
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	broker.lookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		if host == "example.com" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		t.Fatalf("scoped IPv6 literal used DNS for %q", host)
		return nil, nil
	}
	observation, err := broker.Observe(context.Background(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_scoped_ipv6", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "http://[FE80::1%25EtherNet]:8080/health"},
	})
	if err != nil || prepared.Action.DestinationOrigin != "http://[fe80::1%25EtherNet]:8080" {
		t.Fatalf("PrepareAction() scoped destination = %q, %v", prepared.Action.DestinationOrigin, err)
	}

	worker.observation.URL = "http://[fe80::1%25EtherNet]:8080/health"
	worker.observation.Origin = "http://[fe80::1%25EtherNet]:8080"
	if _, err = broker.Observe(context.Background(), testOwner(), session.ID, session.TabID); err != nil {
		t.Fatalf("Observe() scoped IPv6 error = %v", err)
	}
}

func TestBrokerAnyHTTPAdmitsPercentEncodedScopedIPv6NavigationAndObservation(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkAnyHTTP
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	broker.lookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		if host == "example.com" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		t.Fatalf("scoped IPv6 literal used DNS for %q", host)
		return nil, nil
	}
	observation, err := broker.Observe(context.Background(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_encoded_scoped_ipv6", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "http://[FE80::1%25Ether%20Net]:8080/health"},
	})
	if err != nil || prepared.Action.DestinationOrigin != "http://[fe80::1%25Ether%20Net]:8080" {
		t.Fatalf("PrepareAction() encoded scoped destination = %q, %v", prepared.Action.DestinationOrigin, err)
	}
	prepared, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_trailing_dot_scoped_ipv6", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "http://[FE80::1%25Ether%2E]:8080/health"},
	})
	if err != nil || prepared.Action.DestinationOrigin != "http://[fe80::1%25Ether.]:8080" {
		t.Fatalf("PrepareAction() trailing-dot scoped destination = %q, %v", prepared.Action.DestinationOrigin, err)
	}

	worker.observation.URL = "http://[fe80::1%25Ether.]:8080/health"
	worker.observation.Origin = "http://[fe80::1%25Ether.]:8080"
	if _, err = broker.Observe(context.Background(), testOwner(), session.ID, session.TabID); err != nil {
		t.Fatalf("Observe() trailing-dot scoped IPv6 error = %v", err)
	}
}

func TestBrokerPreparationQuarantinesDeniedDNSResolution(t *testing.T) {
	t.Run("current origin", func(t *testing.T) {
		store := NewMemoryStore()
		broker, worker, session := openActionTestBroker(t, store)
		owner := testOwner()
		observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
		if err != nil {
			t.Fatal(err)
		}
		broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("100.64.0.1")}, nil
		}
		_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
			Owner: owner, RequestID: "request_prepare_rebind", SessionID: session.ID, TabID: session.TabID,
			SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
			Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
		})
		assertNetworkQuarantine(t, store, worker, session, err)
	})

	t.Run("navigation destination", func(t *testing.T) {
		root := admittedBrowserConfig()
		target := root.Tools.Browser.Targets["gateway"]
		profile := target.Profiles["managed"]
		profile.AllowedOrigins = append(profile.AllowedOrigins, "https://private.example")
		target.Profiles["managed"] = profile
		root.Tools.Browser.Targets["gateway"] = target
		store := NewMemoryStore()
		broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
		broker.lookupIP = func(_ context.Context, _ string, host string) ([]net.IP, error) {
			if host == "private.example" {
				return []net.IP{net.ParseIP("198.18.0.1")}, nil
			}
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		owner := testOwner()
		observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
			Owner: owner, RequestID: "request_private_destination", SessionID: session.ID,
			TabID: session.TabID, SnapshotID: observation.SnapshotID,
			SnapshotGeneration: observation.SnapshotGeneration,
			Action:             Action{Kind: ActionNavigate, URL: "https://private.example/listing"},
		})
		assertNetworkQuarantine(t, store, worker, session, err)
	})
}

func TestBrokerPreparesRuntimeEffectAndExecutesLocalEditOnce(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	ref := onlyVisibleRef(t, observation.Snapshot)
	worker.resolveElement = DriverElement{Target: "e1", Role: "textbox", Name: "Name"}
	worker.resolveOrigin = "https://example.com"
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_fill", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: ref, Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Action.Effect != EffectLocalEdit || prepared.RequiresApproval {
		t.Fatalf("fill preparation = %+v", prepared)
	}
	first, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || first.State != InvocationSucceeded || len(worker.actions) != 1 ||
		worker.actions[0].Target != "e1" || worker.actions[0].Value != "Ada" {
		t.Fatalf("ExecuteAction() = %+v, %v; actions = %+v", first, err, worker.actions)
	}
	second, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || !reflect.DeepEqual(second, first) || len(worker.actions) != 1 {
		t.Fatalf("idempotent ExecuteAction() = %+v, %v; actions = %+v", second, err, worker.actions)
	}
	storedSession, err := store.GetSession(context.Background(), session.ID)
	if err != nil || storedSession.SnapshotID != "" || storedSession.SnapshotGeneration == 0 {
		t.Fatalf("session after action = %+v, %v", storedSession, err)
	}
}

func TestBrokerDeniesSensitiveOrAmbiguousFillBeforePreparation(t *testing.T) {
	for _, field := range []DriverElement{
		{Target: "e1", Role: "textbox", Name: "Password"},
		{Target: "e1", Role: "textbox", Name: "Card number"},
		{Target: "e1", Role: "textbox", Name: "One-time code"},
		{Target: "e1", Role: "textbox", Name: ""},
		{Target: "e1", Role: "combobox", Name: "Unclassified"},
	} {
		t.Run(field.Role+"_"+field.Name, func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			owner := testOwner()
			worker.observation.Elements = []DriverElement{field}
			worker.observation.Snapshot = "- " + field.Role + " [ref=" + field.Target + "]"
			observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			worker.resolveElement = field
			worker.resolveOrigin = "https://example.com"
			_, err = broker.PrepareAction(t.Context(), PrepareActionRequest{
				Owner: owner, RequestID: "request_sensitive_fill", SessionID: session.ID,
				TabID: session.TabID, SnapshotID: observation.SnapshotID,
				SnapshotGeneration: observation.SnapshotGeneration,
				Action: Action{
					Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "canary",
				},
			})
			if !errors.Is(err, ErrDenied) || len(worker.actions) != 0 {
				t.Fatalf("PrepareAction(%+v) = %v; actions = %+v", field, err, worker.actions)
			}
		})
	}
}

func TestBrokerPrivateFillDenialFailsClosedBeforeDispatch(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	worker.resolveElement = DriverElement{Target: "e1", Role: "textbox", Name: "Name"}
	worker.resolveOrigin = "https://example.com"
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_private_fill_denial", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "canary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.authorizeErr = ErrDenied
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, nil)
	if !errors.Is(err, ErrDenied) || invocation.State != InvocationFailed ||
		invocation.SafeFailure != "policy_denied" || worker.authorizeCalls != 1 || len(worker.actions) != 0 {
		t.Fatalf("ExecuteAction() = %+v, %v; authorizations=%d actions=%#v",
			invocation, err, worker.authorizeCalls, worker.actions)
	}
	if _, retained := broker.slots[session.ID].inputs[prepared.Action.ID]; retained {
		t.Fatal("private classifier denial retained live fill input")
	}
	storedSession, err := store.GetSession(t.Context(), session.ID)
	if err != nil || storedSession.State != SessionReady || worker.closed != 0 {
		t.Fatalf("session after definite denial = %+v, %v; closes=%d", storedSession, err, worker.closed)
	}
}

func TestBrokerQuarantinesSessionWhenPostActionSnapshotInvalidationFails(t *testing.T) {
	store := &failNextSessionUpdateStore{MemoryStore: NewMemoryStore()}
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	ref := onlyVisibleRef(t, observation.Snapshot)
	worker.resolveElement = DriverElement{Target: "e1", Role: "textbox", Name: "Name"}
	worker.resolveOrigin = "https://example.com"
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_invalidation_failure", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             Action{Kind: ActionFill, Ref: ref, Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first update records action activity; fail the following snapshot
	// invalidation write.
	store.failAfter = 2
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if !errors.Is(err, ErrSnapshotInvalidation) || invocation.State != InvocationSucceeded ||
		len(worker.actions) != 1 || worker.closed != 1 {
		stored, storedErr := store.GetSession(context.Background(), session.ID)
		t.Fatalf(
			"ExecuteAction() = %+v, %v (snapshot failure = %t); actions = %+v; closes = %d; session = %+v, %v",
			invocation,
			err,
			errors.Is(err, ErrSnapshotInvalidation),
			worker.actions,
			worker.closed,
			stored,
			storedErr,
		)
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionLost || stored.SnapshotID != "" ||
		stored.SafeFailure != "snapshot_invalidation_failed" {
		t.Fatalf("quarantined session = %+v, %v", stored, getErr)
	}
	_, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_reuse_old_snapshot", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             Action{Kind: ActionFill, Ref: ref, Value: "Grace"},
	})
	if !errors.Is(err, ErrWorkerUnavailable) || len(worker.actions) != 1 {
		t.Fatalf("old snapshot reuse error = %v; actions = %+v", err, worker.actions)
	}
}

func TestFileStoreQuarantinesCommittedSnapshotInvalidationWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	ref := onlyVisibleRef(t, observation.Snapshot)
	worker.resolveElement = DriverElement{Target: "e1", Role: "textbox", Name: "Name"}
	worker.resolveOrigin = "https://example.com"
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_committed_invalidation", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             Action{Kind: ActionFill, Ref: ref, Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := store.writeFile
	committedWarning := false
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if writeErr := originalWrite(path, data, mode); writeErr != nil {
			return writeErr
		}
		var document fileStoreDocument
		if jsonErr := json.Unmarshal(data, &document); jsonErr != nil {
			return jsonErr
		}
		persisted := document.Sessions[session.ID]
		if !committedWarning && persisted.State == SessionReady && persisted.SnapshotID == "" {
			committedWarning = true
			return &fileutil.CommittedWriteError{Err: errors.New("directory sync warning")}
		}
		return nil
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if !committedWarning || !errors.Is(err, ErrSnapshotInvalidation) ||
		invocation.State != InvocationSucceeded || len(worker.actions) != 1 || worker.closed != 1 {
		t.Fatalf(
			"ExecuteAction() = %+v, %v; warning = %t; actions = %+v; closes = %d",
			invocation, err, committedWarning, worker.actions, worker.closed,
		)
	}
	persisted, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || persisted.State != SessionLost ||
		persisted.SafeFailure != "snapshot_invalidation_failed" || persisted.SnapshotID != "" {
		t.Fatalf("quarantined session = %+v, %v", persisted, getErr)
	}
}

func TestBrokerRequiresExactApprovalAndDryRunStillDeniesCommit(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	worker.observation = driverObservationFixture(DriverElement{Target: "e2", Role: "button", Name: "Save"})
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	worker.resolveElement = DriverElement{Target: "e2", Role: "button", Name: "Save"}
	worker.resolveOrigin = "https://example.com"
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_submit", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionClick, Ref: onlyVisibleRef(t, observation.Snapshot)},
	})
	if err != nil || prepared.Action.Effect != EffectExternalCommit || !prepared.RequiresApproval {
		t.Fatalf("PrepareAction() = %+v, %v", prepared, err)
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ExecuteAction() without approval error = %v", err)
	}
	forged := prepared.Approval
	forged.PolicyRevision = strings.Repeat("a", 64)
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, &forged,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ExecuteAction() with forged approval error = %v", err)
	}
	denied, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, &prepared.Approval)
	if !errors.Is(err, ErrDenied) || denied.State != InvocationCanceled ||
		denied.SafeFailure != "dry_run_denied" || len(worker.actions) != 0 {
		t.Fatalf("approved dry-run ExecuteAction() = %+v, %v; actions = %+v", denied, err, worker.actions)
	}
	stored, getErr := store.GetInvocation(context.Background(), denied.ID)
	if getErr != nil || !reflect.DeepEqual(stored, denied) {
		t.Fatalf("stored dry-run denial = %+v, %v", stored, getErr)
	}
}

func TestBrokerRevalidatesResolvedSemanticsBeforeAcceptance(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_changed", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.resolveElement = DriverElement{Target: "e1", Role: "button", Name: "Name"}
	worker.resolveOrigin = "https://example.com"
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrStale) {
		t.Fatalf("ExecuteAction() changed semantics error = %v, want ErrStale", err)
	}
	invocation, err := store.GetInvocation(
		context.Background(),
		derivedIdentifier("invocation", owner, session.ID, "request_changed"),
	)
	if err != nil || invocation.State != InvocationPrepared || len(worker.actions) != 0 {
		t.Fatalf("invocation after stale semantics = %+v, %v; actions = %+v", invocation, err, worker.actions)
	}
}

func TestBrokerRevalidatesDNSBeforeAcceptance(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_dns_rebind", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.8")}, nil
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("ExecuteAction() rebound DNS error = %v, want ErrDenied", err)
	}
	invocation, err := store.GetInvocation(
		context.Background(),
		derivedIdentifier("invocation", owner, session.ID, "request_dns_rebind"),
	)
	if err != nil || invocation.State != InvocationCanceled || invocation.SafeFailure != "network_denied" ||
		len(worker.actions) != 0 || worker.closed != 1 {
		t.Fatalf("invocation after rebound DNS = %+v, %v; actions = %+v", invocation, err, worker.actions)
	}
}

func TestFileStorePersistsPreparedApprovalBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	broker, _, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "credential-that-must-never-reach-browser-state"
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_durable", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state, []byte(secret)) || prepared.Action.Action.Value != "" ||
		!validDigest(prepared.Action.InputDigest) || prepared.Action.InputBytes != len(secret) {
		t.Fatalf("durable prepared action exposed raw fill input: %+v", prepared.Action)
	}
	store.Close()
	reopened, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetPreparedAction(context.Background(), prepared.Action.ID)
	if err != nil || got != prepared.Action {
		t.Fatalf("reopened prepared action = %+v, %v; want %+v", got, err, prepared.Action)
	}
}

func TestBrokerExecutesAdmittedOrdinaryActions(t *testing.T) {
	tests := []struct {
		name       string
		element    DriverElement
		action     Action
		wantEffect Effect
		wantDriver DriverAction
	}{
		{
			name: "select", element: DriverElement{Target: "e1", Role: "combobox", Name: "State"},
			action: Action{Kind: ActionSelect, Value: "CA"}, wantEffect: EffectLocalEdit,
			wantDriver: DriverAction{Kind: DriverSelect, Target: "e1", Element: "State", Value: "CA"},
		},
		{
			name: "check", element: DriverElement{Target: "e1", Role: "checkbox", Name: "Notify"},
			action: Action{Kind: ActionCheck}, wantEffect: EffectLocalEdit,
			wantDriver: DriverAction{Kind: DriverCheck, Target: "e1", Element: "Notify"},
		},
		{
			name: "uncheck", element: DriverElement{Target: "e1", Role: "switch", Name: "Dark mode"},
			action: Action{Kind: ActionUncheck}, wantEffect: EffectLocalEdit,
			wantDriver: DriverAction{Kind: DriverUncheck, Target: "e1", Element: "Dark mode"},
		},
		{
			name: "hover", element: DriverElement{Target: "e1", Role: "button", Name: "Menu"},
			action: Action{Kind: ActionHover}, wantEffect: EffectRead,
			wantDriver: DriverAction{Kind: DriverHover, Target: "e1", Element: "Menu"},
		},
		{
			name: "scroll", action: Action{Kind: ActionScroll, Direction: "down", Amount: 3},
			wantEffect: EffectRead,
			wantDriver: DriverAction{Kind: DriverScroll, Direction: "down", Amount: 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			broker, worker, session := openActionTestBroker(t, store)
			if test.element.Target != "" {
				worker.observation = driverObservationFixture(test.element)
				worker.resolveElement = test.element
			}
			owner := testOwner()
			observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			action := test.action
			if test.element.Target != "" {
				action.Ref = onlyVisibleRef(t, observation.Snapshot)
			}
			prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
				Owner: owner, RequestID: "request_" + test.name, SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
				Action: action,
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.RequiresApproval || prepared.Action.Effect != test.wantEffect {
				t.Fatalf("preparation = %+v", prepared)
			}
			if test.name == "select" && (prepared.Action.Action.Value != "" ||
				!validDigest(prepared.Action.InputDigest)) {
				t.Fatalf("durable selection exposed input: %+v", prepared.Action)
			}
			invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
			if err != nil || invocation.State != InvocationSucceeded ||
				!reflect.DeepEqual(worker.actions, []DriverAction{test.wantDriver}) {
				t.Fatalf("execution = %+v, %v; driver actions = %+v", invocation, err, worker.actions)
			}
		})
	}
}

func TestBrokerExecutesApprovedDragWithTwoFreshSemanticBindings(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	source := DriverElement{Target: "e1", Role: "listitem", Name: "Todo"}
	destination := DriverElement{Target: "e2", Role: "list", Name: "Done"}
	worker.observation = driverObservationFixture(source, destination)
	worker.resolveElements = map[string]DriverElement{source.Target: source, destination.Target: destination}
	worker.resolveErr = ErrDriverRejected
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	refs := visibleRefs(t, observation.Snapshot)
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_drag", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDrag, SourceRef: refs[0], DestinationRef: refs[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.RequiresApproval || prepared.Action.Effect != EffectUnknown ||
		prepared.Action.ElementRole != source.Role || prepared.Action.ElementName != source.Name ||
		prepared.Action.DestinationElementRole != destination.Role ||
		prepared.Action.DestinationElementName != destination.Name {
		t.Fatalf("drag preparation = %+v", prepared)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, &prepared.Approval)
	want := DriverAction{
		Kind: DriverDrag, Target: source.Target, Element: source.Name,
		DestinationTarget: destination.Target, DestinationElement: destination.Name,
	}
	if err != nil || invocation.State != InvocationSucceeded ||
		!reflect.DeepEqual(worker.actions, []DriverAction{want}) || worker.resolveCalls != 0 ||
		worker.observeCalls != 3 {
		t.Fatalf("drag execution = %+v, %v; driver actions = %+v", invocation, err, worker.actions)
	}
}

func TestBrokerPreparedDragPreservesSnapshotBoundRemoteReferences(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target

	source := DriverElement{Target: "node_ref_source_1", Role: "listitem", Name: "Todo"}
	destination := DriverElement{Target: "node_ref_destination_1", Role: "list", Name: "Done"}
	actionWorker := &actionTestWorker{
		observation: driverObservationFixture(source, destination),
		resolveElements: map[string]DriverElement{
			source.Target: source, destination.Target: destination,
		},
		resolveOrigin: "https://example.com",
	}
	worker := &preparedDragTestWorker{actionTestWorker: actionWorker}
	broker, err := NewBroker(root, NewMemoryStore(), &preparedDragTestFactory{worker: worker})
	if err != nil {
		t.Fatal(err)
	}
	owner := testOwner()
	session, err := broker.Open(t.Context(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	refs := visibleRefs(t, observation.Snapshot)

	// Node-hosted observations intentionally rotate their private references
	// on every snapshot. The outer broker must not request another observation
	// while preparing a drag that the node broker will revalidate itself.
	worker.observation = driverObservationFixture(
		DriverElement{Target: "node_ref_source_2", Role: source.Role, Name: source.Name},
		DriverElement{Target: "node_ref_destination_2", Role: destination.Role, Name: destination.Name},
	)
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_remote_drag", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDrag, SourceRef: refs[0], DestinationRef: refs[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, &prepared.Approval)
	want := DriverAction{
		Kind: DriverDrag, Target: source.Target, Element: source.Name,
		DestinationTarget: destination.Target, DestinationElement: destination.Name,
	}
	if err != nil || invocation.State != InvocationSucceeded || worker.executePreparedCalls != 1 ||
		worker.observeCalls != 1 || worker.resolveCalls != 4 ||
		!reflect.DeepEqual(worker.actions, []DriverAction{want}) {
		t.Fatalf(
			"remote drag execution = %+v, %v; prepared calls = %d; observe calls = %d; resolve calls = %d; actions = %+v",
			invocation,
			err,
			worker.executePreparedCalls,
			worker.observeCalls,
			worker.resolveCalls,
			worker.actions,
		)
	}
}

func TestBrokerRejectsDragWhenDestinationBindingChanges(t *testing.T) {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	source := DriverElement{Target: "e1", Role: "listitem", Name: "Todo"}
	destination := DriverElement{Target: "e2", Role: "list", Name: "Done"}
	worker.observation = driverObservationFixture(source, destination)
	worker.resolveElements = map[string]DriverElement{source.Target: source, destination.Target: destination}
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	refs := visibleRefs(t, observation.Snapshot)
	prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_drag_stale", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDrag, SourceRef: refs[0], DestinationRef: refs[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	changedDestination := DriverElement{
		Target: destination.Target, Role: destination.Role, Name: "Archive",
	}
	worker.observation = driverObservationFixture(source, changedDestination)
	if _, err = broker.ExecuteAction(
		t.Context(),
		owner,
		prepared.Action.ID,
		&prepared.Approval,
	); !errors.Is(
		err,
		ErrStale,
	) {
		t.Fatalf("ExecuteAction(stale drag destination) error = %v, want ErrStale", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("stale drag reached driver: %+v", worker.actions)
	}
}

func TestBrokerRejectsUncheckForRadioControl(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	element := DriverElement{Target: "e1", Role: "radio", Name: "Primary"}
	worker.observation = driverObservationFixture(element)
	worker.resolveElement = element
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: owner, RequestID: "request_uncheck_radio", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionUncheck, Ref: onlyVisibleRef(t, observation.Snapshot)},
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("PrepareAction(uncheck radio) error = %v, want denied", err)
	}
}

func TestBrokerLocalCheckedActionsRejectNavigationBeforeDriverInput(t *testing.T) {
	for _, actionKind := range []ActionKind{
		ActionSelect, ActionCheck, ActionUncheck, ActionHover, ActionDrag, ActionPress,
	} {
		t.Run(string(actionKind), func(t *testing.T) {
			root := admittedBrowserConfig()
			target := root.Tools.Browser.Targets["gateway"]
			profile := target.Profiles["managed"]
			profile.DryRun = false
			profile.AllowApprovedActions = true
			target.Profiles["managed"] = profile
			root.Tools.Browser.Targets["gateway"] = target
			store := NewMemoryStore()
			broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
			element := DriverElement{Target: "e1", Role: "checkbox", Name: "Notify"}
			switch actionKind {
			case ActionSelect:
				element = DriverElement{Target: "e1", Role: "combobox", Name: "State"}
			case ActionHover:
				element = DriverElement{Target: "e1", Role: "button", Name: "Menu"}
			}
			elements := []DriverElement{element}
			if actionKind == ActionDrag {
				destination := DriverElement{Target: "e2", Role: "list", Name: "Done"}
				element = DriverElement{Target: "e1", Role: "listitem", Name: "Todo"}
				elements = []DriverElement{element, destination}
				worker.resolveElements = map[string]DriverElement{
					element.Target: element, destination.Target: destination,
				}
			} else {
				worker.resolveElement = element
			}
			worker.observation = driverObservationFixture(elements...)
			owner := testOwner()
			observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			action := Action{Kind: actionKind}
			if actionKind == ActionDrag {
				refs := visibleRefs(t, observation.Snapshot)
				action.SourceRef, action.DestinationRef = refs[0], refs[1]
			} else if actionKind != ActionPress {
				action.Ref = onlyVisibleRef(t, observation.Snapshot)
				if actionKind == ActionSelect {
					action.Value = "CA"
				}
			} else {
				action.Target = "document"
				action.Key = "Tab"
			}
			prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
				Owner: owner, RequestID: "request_checked_" + string(actionKind),
				SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
				Action: action,
			})
			if err != nil {
				t.Fatal(err)
			}
			worker.beforeNavCheck = func() { worker.navigationID = "navigation_2" }
			var approval *ApprovalBinding
			if prepared.RequiresApproval {
				approval = &prepared.Approval
			}
			invocation, executeErr := broker.ExecuteAction(
				t.Context(), owner, prepared.Action.ID, approval,
			)
			if executeErr != nil || invocation.State != InvocationUnknown || len(worker.actions) != 0 {
				t.Fatalf(
					"ExecuteAction() = %+v, %v; actions = %+v",
					invocation,
					executeErr,
					worker.actions,
				)
			}
			stored, err := store.GetSession(t.Context(), session.ID)
			if err != nil || stored.State != SessionLost || stored.SafeFailure != "outcome_unknown" {
				t.Fatalf("quarantined session = %+v, %v", stored, err)
			}
		})
	}
}

func TestBrokerTreatsGlobalPressAsUnknownAndDryRunDenied(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	pageHandlerCommitted := false
	worker.onExecute = func(DriverAction) { pageHandlerCommitted = true }
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_press", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionPress, Target: "document", Key: "Tab"},
	})
	if err != nil || !prepared.RequiresApproval || prepared.Action.Effect != EffectUnknown {
		t.Fatalf("PrepareAction(Tab) = %+v, %v", prepared, err)
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ExecuteAction(Tab) error = %v, want ErrApprovalRequired", err)
	}
	if len(worker.actions) != 0 || pageHandlerCommitted {
		t.Fatalf("unapproved global press reached driver: %+v", worker.actions)
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, &prepared.Approval,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("ExecuteAction(Tab, approved dry-run) error = %v, want ErrDenied", err)
	}
	if len(worker.actions) != 0 || pageHandlerCommitted {
		t.Fatalf("dry-run global press reached driver: %+v", worker.actions)
	}
}

func TestBrokerObservesAndDismissesBoundDialog(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	worker.observation = driverObservationFixture()
	worker.observation.PendingDialog = &DialogObservation{Type: "confirm", Message: "Discard draft?"}
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil || observation.PendingDialog == nil || observation.PendingDialog.ID == "" ||
		observation.PendingDialog.Type != "confirm" || observation.PendingDialog.Message != "Discard draft?" {
		t.Fatalf("Observe() dialog = %+v, %v", observation, err)
	}
	if _, err = broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_wrong_dialog", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDialog, DialogID: "dialog_wrong", Decision: "dismiss"},
	}); !errors.Is(err, ErrStale) {
		t.Fatalf("PrepareAction(wrong dialog) error = %v, want ErrStale", err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_dismiss", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDialog, DialogID: observation.PendingDialog.ID, Decision: "dismiss"},
	})
	if err != nil || prepared.RequiresApproval || prepared.Action.Effect != EffectRead ||
		prepared.Action.DialogType != "confirm" || !validDigest(prepared.Action.DialogMessageDigest) ||
		prepared.Action.DialogMessageBytes != len("Discard draft?") {
		t.Fatalf("PrepareAction(dismiss) = %+v, %v", prepared, err)
	}
	invocation, err := broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != InvocationSucceeded || !reflect.DeepEqual(
		worker.actions, []DriverAction{{Kind: DriverDialog}},
	) {
		t.Fatalf("ExecuteAction(dismiss) = %+v, %v; actions = %+v", invocation, err, worker.actions)
	}
}

func TestBrokerBindsDialogPromptAndProtectsAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker, worker, session := openActionTestBroker(t, store)
	worker.observation = driverObservationFixture()
	worker.observation.PendingDialog = &DialogObservation{Type: "prompt", Message: "Type confirmation"}
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_accept", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{
			Kind: ActionDialog, DialogID: observation.PendingDialog.ID,
			Decision: "accept", Value: "prompt-secret", PromptProvided: true,
		},
	})
	if err != nil || !prepared.RequiresApproval || prepared.Action.Effect != EffectExternalCommit ||
		prepared.Action.Action.Value != "" || !validDigest(prepared.Action.InputDigest) {
		t.Fatalf("PrepareAction(accept) = %+v, %v", prepared, err)
	}
	state, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state, []byte("prompt-secret")) || bytes.Contains(state, []byte("Type confirmation")) {
		t.Fatalf("durable browser state exposed dialog prompt: %s", state)
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ExecuteAction(accept) error = %v, want ErrApprovalRequired", err)
	}
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, &prepared.Approval,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("ExecuteAction(approved dry-run accept) error = %v, want ErrDenied", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("protected dialog acceptance reached driver: %+v", worker.actions)
	}
}

func TestBrokerRejectsChangedDialogBeforeDispatch(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	worker.observation = driverObservationFixture()
	worker.observation.PendingDialog = &DialogObservation{Type: "confirm", Message: "First"}
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_changed_dialog", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDialog, DialogID: observation.PendingDialog.ID, Decision: "dismiss"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.observation.PendingDialog.Message = "Replacement"
	if _, err = broker.ExecuteAction(
		context.Background(), owner, prepared.Action.ID, nil,
	); !errors.Is(err, ErrStale) {
		t.Fatalf("ExecuteAction(changed dialog) error = %v, want ErrStale", err)
	}
	if len(worker.actions) != 0 {
		t.Fatalf("changed dialog reached driver: %+v", worker.actions)
	}
}

func TestFileStoreRejectsLegacyDialogMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	broker, worker, session := openActionTestBroker(t, store)
	worker.observation = driverObservationFixture()
	worker.observation.PendingDialog = &DialogObservation{Type: "confirm", Message: "Current dialog"}
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil || observation.PendingDialog == nil {
		t.Fatalf("Observe() = %#v, %v", observation, err)
	}
	preparation, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_current_dialog", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{
			Kind: ActionDialog, DialogID: observation.PendingDialog.ID, Decision: "dismiss",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	preparedActions, ok := document["prepared_actions"].(map[string]any)
	if !ok {
		t.Fatalf("prepared_actions = %#v", document["prepared_actions"])
	}
	prepared, ok := preparedActions[preparation.Action.ID].(map[string]any)
	if !ok {
		t.Fatalf("prepared action = %#v", preparedActions[preparation.Action.ID])
	}
	prepared["dialog_message"] = "obsolete plaintext"
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = NewFileStore(path, 0, 0); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("NewFileStore() error = %v, want unknown-field rejection", err)
	}
}

func TestActionValidationRejectsUnadmittedKeyAndUnboundedScroll(t *testing.T) {
	for _, action := range []Action{
		{Kind: ActionPress, Key: "a"},
		{Kind: ActionPress, Key: "Control+L"},
		{Kind: ActionPress, Key: "Enter"},
		{Kind: ActionScroll, Direction: "left", Amount: 1},
		{Kind: ActionScroll, Direction: "down", Amount: MaxScrollAmount + 1},
		{Kind: ActionDialog, Decision: "dismiss", Value: "unexpected"},
		{Kind: ActionDialog, Decision: "accept", Value: "unmarked-prompt"},
		{Kind: ActionDialog, Decision: "later"},
	} {
		if err := action.Validate(config.BrowserMaxTextInputBytes); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Action.Validate(%+v) error = %v, want ErrInvalid", action, err)
		}
	}
}

func TestExecuteFillFailsClosedBeforeDriverCallWithoutExactLiveInput(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]string
	}{
		{name: "missing"},
		{name: "mismatched", input: map[string]string{"prepared": "different-secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			broker, worker, session := openActionTestBroker(t, store)
			owner := testOwner()
			observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
				Owner: owner, RequestID: "request_live_input", SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
				Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "secret"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.input == nil {
				broker.slots[session.ID].inputs = nil
			} else {
				broker.slots[session.ID].inputs = map[string]string{
					prepared.Action.ID: test.input["prepared"],
				}
			}
			resolveCalls := worker.resolveCalls
			if _, err = broker.ExecuteAction(
				context.Background(), owner, prepared.Action.ID, nil,
			); !errors.Is(err, ErrStale) {
				t.Fatalf("ExecuteAction() live input error = %v, want ErrStale", err)
			}
			if worker.resolveCalls != resolveCalls || len(worker.actions) != 0 {
				t.Fatalf(
					"driver reached with invalid live input: resolves %d -> %d, actions %+v",
					resolveCalls,
					worker.resolveCalls,
					worker.actions,
				)
			}
		})
	}
}

func TestFileStorePreparationIsAtomicAtRecordBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker, _, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareActionRequest{
		Owner: owner, RequestID: "request_bounded", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
	}
	if _, err = broker.PrepareAction(context.Background(), request); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("PrepareAction() bound error = %v, want ErrStoreFull", err)
	}
	preparedID := derivedIdentifier("prepared", owner, session.ID, request.RequestID)
	invocationID := derivedIdentifier("invocation", owner, session.ID, request.RequestID)
	if _, err = store.GetPreparedAction(context.Background(), preparedID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial prepared record error = %v, want ErrNotFound", err)
	}
	if _, err = store.GetInvocation(context.Background(), invocationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial invocation record error = %v, want ErrNotFound", err)
	}
}

func TestFileStorePreparationCommittedWarningRetainsCompletePair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker, _, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareActionRequest{
		Owner: owner, RequestID: "request_committed", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
	}
	originalWrite := store.writeFile
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if err := originalWrite(path, data, mode); err != nil {
			return err
		}
		return &fileutil.CommittedWriteError{Err: errors.New("directory sync warning")}
	}
	if _, err = broker.PrepareAction(context.Background(), request); !fileutil.IsCommittedWriteError(err) {
		t.Fatalf("PrepareAction() committed warning = %v", err)
	}
	preparedID := derivedIdentifier("prepared", owner, session.ID, request.RequestID)
	invocationID := derivedIdentifier("invocation", owner, session.ID, request.RequestID)
	prepared, preparedErr := store.GetPreparedAction(context.Background(), preparedID)
	invocation, invocationErr := store.GetInvocation(context.Background(), invocationID)
	if preparedErr != nil || invocationErr != nil || invocation.PreparedActionID != prepared.ID ||
		invocation.ActionHash != prepared.ActionHash {
		t.Fatalf(
			"committed pair = prepared %+v (%v), invocation %+v (%v)",
			prepared,
			preparedErr,
			invocation,
			invocationErr,
		)
	}
	retried, err := broker.PrepareAction(context.Background(), request)
	if err != nil || retried.Action != prepared {
		t.Fatalf("idempotent PrepareAction() = %+v, %v", retried, err)
	}
}

func TestPreparedRetentionWaitsForInvocationRetention(t *testing.T) {
	store := NewMemoryStore()
	broker, _, session := openActionTestBroker(t, store)
	owner := testOwner()
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := broker.PrepareAction(context.Background(), PrepareActionRequest{
		Owner: owner, RequestID: "request_retention", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.ExecuteAction(context.Background(), owner, prepared.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = store.PrunePreparedActions(context.Background(), int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetPreparedAction(context.Background(), prepared.Action.ID); err != nil {
		t.Fatalf("prepared action pruned while invocation retained: %v", err)
	}
	if err = store.PruneInvocations(context.Background(), int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	if err = store.PrunePreparedActions(context.Background(), int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetPreparedAction(
		context.Background(), prepared.Action.ID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unreferenced prepared action prune error = %v, want ErrNotFound", err)
	}
}

func TestObserveDoesNotProbeWorkerStatusBeforeReconnectableFailure(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	worker.observeErr = ErrWorkerUnavailable
	if _, err := broker.Observe(
		t.Context(),
		testOwner(),
		session.ID,
		session.TabID,
	); !errors.Is(
		err,
		ErrWorkerUnavailable,
	) {
		t.Fatalf("Observe() error = %v, want worker unavailable", err)
	}
	if worker.statusCalls != 0 {
		t.Fatalf("Observe() probed worker status %d times", worker.statusCalls)
	}
	stored, err := store.GetSession(t.Context(), session.ID)
	if err != nil || stored.State != SessionReady || stored.SafeFailure != "" {
		t.Fatalf("reconnectable observation failure changed session = %#v, %v", stored, err)
	}
}

func openActionTestBroker(t *testing.T, store Store) (*Broker, *actionTestWorker, Session) {
	t.Helper()
	return openActionTestBrokerWithConfig(t, admittedBrowserConfig(), store)
}

func TestBrokerDiagnosticsUsesFreshOwnedSessionWithoutDurableMutation(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	owner := testOwner()
	worker.diagnostics = DiagnosticSummary{Categories: []DiagnosticCategorySummary{
		{
			Category: DiagnosticConsoleErrors, Count: 1,
			Entries: []DiagnosticEntry{{
				Timestamp: 1, Severity: "error", Origin: "https://example.com", Path: "/safe",
				MessageHash: strings.Repeat("a", 64),
			}},
		},
		{Category: DiagnosticPageCrashes, Entries: []DiagnosticEntry{}},
	}}
	before, err := broker.Status(context.Background(), owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := broker.Diagnostics(context.Background(), DiagnosticsRequest{
		Owner: owner, SessionID: session.ID,
		Categories: []DiagnosticCategory{DiagnosticPageCrashes, DiagnosticConsoleErrors},
	})
	if err != nil {
		t.Fatalf("Diagnostics() error = %v", err)
	}
	after, err := broker.Status(context.Background(), owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || before.UpdatedAt != after.UpdatedAt {
		t.Fatalf("diagnostics mutated durable session: before=%+v after=%+v", before, after)
	}
	wantCategories := []DiagnosticCategory{DiagnosticConsoleErrors, DiagnosticPageCrashes}
	if !reflect.DeepEqual(worker.diagnosticCategories, wantCategories) || worker.diagnosticsCalls != 1 {
		t.Fatalf("worker diagnostics = %#v, calls %d", worker.diagnosticCategories, worker.diagnosticsCalls)
	}
	if summary.SessionID != session.ID || summary.TabID != session.TabID ||
		len(summary.Categories) != 2 || summary.Categories[0].Category != DiagnosticConsoleErrors {
		t.Fatalf("Diagnostics() = %+v", summary)
	}
	observation, err := broker.Observe(context.Background(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	worker.diagnostics.Categories = worker.diagnostics.Categories[:1]
	bound := DiagnosticsRequest{
		Owner: owner, SessionID: session.ID, TabID: observation.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Categories: []DiagnosticCategory{DiagnosticConsoleErrors},
	}
	if _, err = broker.Diagnostics(context.Background(), bound); err != nil {
		t.Fatalf("fresh bound Diagnostics() error = %v", err)
	}
	bound.SnapshotGeneration++
	if _, err = broker.Diagnostics(context.Background(), bound); !errors.Is(err, ErrStale) {
		t.Fatalf("stale bound Diagnostics() error = %v", err)
	}
	wrongOwner := owner
	wrongOwner.ExecutionID = "other_execution"
	if _, err = broker.Diagnostics(context.Background(), DiagnosticsRequest{
		Owner: wrongOwner, SessionID: session.ID,
		Categories: []DiagnosticCategory{DiagnosticConsoleErrors},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-owner Diagnostics() error = %v", err)
	}
}

func openActionTestBrokerWithConfig(
	t *testing.T,
	root *config.Config,
	store Store,
) (*Broker, *actionTestWorker, Session) {
	t.Helper()
	worker := &actionTestWorker{observation: driverObservationFixture(
		DriverElement{Target: "e1", Role: "textbox", Name: "Name"},
	)}
	worker.screenshot = DriverScreenshot{
		Data: append(append([]byte(nil), pngSignature...), []byte("fixture")...), ContentType: "image/png",
	}
	worker.resolveElement = worker.observation.Elements[0]
	worker.resolveOrigin = worker.observation.Origin
	broker := newTestBroker(t, root, store, &actionTestFactory{worker: worker})
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	now := time.Now().UTC()
	broker.now = func() time.Time {
		now = now.Add(time.Nanosecond)
		return now
	}
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: testOwner(), Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker, worker, session
}

func assertNetworkQuarantine(
	t *testing.T,
	store *MemoryStore,
	worker *actionTestWorker,
	session Session,
	err error,
) {
	t.Helper()
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("network policy error = %v, want ErrDenied", err)
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionLost || stored.SafeFailure != "network_denied" ||
		worker.closed != 1 {
		t.Fatalf("network quarantine = %+v, %v; worker = %+v", stored, getErr, worker)
	}
}

func TestWorkerActionForPreparedUsesCanonicalAction(t *testing.T) {
	tests := []struct {
		name     string
		prepared Action
		driver   DriverAction
		want     Action
	}{
		{name: "navigate", prepared: Action{Kind: ActionNavigate}, driver: DriverAction{
			Kind: DriverNavigate, URL: "https://example.com/",
		}, want: Action{Kind: ActionNavigate, URL: "https://example.com/"}},
		{name: "click", prepared: Action{Kind: ActionClick}, driver: DriverAction{
			Kind: DriverClick, Target: "host_ref_click",
		}, want: Action{Kind: ActionClick, Ref: "host_ref_click"}},
		{name: "fill", prepared: Action{Kind: ActionFill}, driver: DriverAction{
			Kind: DriverFill, Target: "host_ref_fill", Value: "private",
		}, want: Action{Kind: ActionFill, Ref: "host_ref_fill"}},
		{name: "select", prepared: Action{Kind: ActionSelect}, driver: DriverAction{
			Kind: DriverSelect, Target: "host_ref_select", Value: "private",
		}, want: Action{Kind: ActionSelect, Ref: "host_ref_select"}},
		{name: "check", prepared: Action{Kind: ActionCheck}, driver: DriverAction{
			Kind: DriverCheck, Target: "host_ref_check",
		}, want: Action{Kind: ActionCheck, Ref: "host_ref_check"}},
		{name: "uncheck", prepared: Action{Kind: ActionUncheck}, driver: DriverAction{
			Kind: DriverUncheck, Target: "host_ref_uncheck",
		}, want: Action{Kind: ActionUncheck, Ref: "host_ref_uncheck"}},
		{name: "hover", prepared: Action{Kind: ActionHover}, driver: DriverAction{
			Kind: DriverHover, Target: "host_ref_hover",
		}, want: Action{Kind: ActionHover, Ref: "host_ref_hover"}},
		{name: "download", prepared: Action{Kind: ActionDownload, Deliver: true}, driver: DriverAction{
			Kind: DriverDownloadAction, Target: "host_ref_download",
		}, want: Action{Kind: ActionDownload, Ref: "host_ref_download", Deliver: true}},
		{name: "press", prepared: Action{Kind: ActionPress, Target: "document"}, driver: DriverAction{
			Kind: DriverPress, Key: "Enter",
		}, want: Action{Kind: ActionPress, Target: "document", Key: "Enter"}},
		{name: "scroll", prepared: Action{Kind: ActionScroll}, driver: DriverAction{
			Kind: DriverScroll, Direction: "down", Amount: 2,
		}, want: Action{Kind: ActionScroll, Direction: "down", Amount: 2}},
		{name: "dialog", prepared: Action{
			Kind: ActionDialog, DialogID: "dialog_1", Decision: "accept",
		}, driver: DriverAction{
			Kind: DriverDialog, PromptProvided: true,
		}, want: Action{
			Kind: ActionDialog, DialogID: "dialog_1", Decision: "accept", PromptProvided: true,
		}},
		{name: "drag", prepared: Action{Kind: ActionDrag}, driver: DriverAction{
			Kind: DriverDrag, Target: "host_ref_source", DestinationTarget: "host_ref_destination",
		}, want: Action{
			Kind: ActionDrag, SourceRef: "host_ref_source", DestinationRef: "host_ref_destination",
		}},
		{name: "file chooser", prepared: Action{
			Kind: ActionFileChooser, ArtifactRef: "transfer-artifact://artifact_1",
		}, driver: DriverAction{
			Kind: DriverUpload, Target: "host_ref_file",
		}, want: Action{
			Kind: ActionFileChooser, Ref: "host_ref_file", ArtifactRef: "transfer-artifact://artifact_1",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := workerActionForPrepared(test.prepared, test.driver)
			if err != nil || got != test.want {
				t.Fatalf("worker action = %#v, %v; want %#v", got, err, test.want)
			}
			request := WorkerPreparedAction{
				Prepared: PreparedAction{Action: test.prepared}, Action: got, DriverAction: test.driver,
			}
			if err = request.Validate(1024); err != nil {
				t.Fatalf("WorkerPreparedAction.Validate() error = %v", err)
			}
		})
	}
	if _, err := workerActionForPrepared(
		Action{Kind: ActionNavigate},
		DriverAction{Kind: DriverClick},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched worker action error = %v", err)
	}
}

func driverObservationFixture(elements ...DriverElement) DriverObservation {
	lines := make([]string, 0, len(elements))
	for _, element := range elements {
		lines = append(lines, "- "+element.Role+" \""+element.Name+"\" [ref="+element.Target+"]")
	}
	return DriverObservation{
		URL: "https://example.com/form", Origin: "https://example.com", Title: "Fixture",
		Snapshot: strings.Join(lines, "\n"), Elements: elements,
	}
}

func onlyVisibleRef(t *testing.T, snapshot string) string {
	t.Helper()
	start := strings.Index(snapshot, "[ref=")
	if start < 0 {
		t.Fatalf("snapshot has no ref: %q", snapshot)
	}
	start += len("[ref=")
	end := strings.Index(snapshot[start:], "]")
	if end < 0 {
		t.Fatalf("snapshot has malformed ref: %q", snapshot)
	}
	return snapshot[start : start+end]
}

func visibleRefs(t *testing.T, snapshot string) []string {
	t.Helper()
	refs := make([]string, 0, 2)
	for remaining := snapshot; ; {
		start := strings.Index(remaining, "[ref=")
		if start < 0 {
			break
		}
		start += len("[ref=")
		end := strings.Index(remaining[start:], "]")
		if end < 0 {
			t.Fatalf("snapshot has malformed ref: %q", snapshot)
		}
		refs = append(refs, remaining[start:start+end])
		remaining = remaining[start+end+1:]
	}
	if len(refs) < 2 {
		t.Fatalf("snapshot has fewer than two refs: %q", snapshot)
	}
	return refs
}
