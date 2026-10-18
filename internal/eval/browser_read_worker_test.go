package eval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type browserReadWorker struct {
	page   *browserFixture
	origin string
	closed bool
}

func (worker *browserReadWorker) Open(context.Context, browser.WorkerOpenRequest) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: worker, Capabilities: browser.WorkerCapabilityActions}, nil
}

func (*browserReadWorker) Status(context.Context) (browser.WorkerStatus, error) {
	return browser.WorkerReady, nil
}
func (worker *browserReadWorker) Close(context.Context) error { worker.closed = true; return nil }
func (worker *browserReadWorker) Observe(context.Context) (browser.DriverObservation, error) {
	return browser.DriverObservation{URL: worker.origin, Origin: worker.origin}, nil
}

func (worker *browserReadWorker) Resolve(context.Context, string) (browser.DriverElement, string, error) {
	return browser.DriverElement{}, worker.origin, browser.ErrNotFound
}
func (*browserReadWorker) CatalogRevision() string { return strings.Repeat("c", 64) }
func (worker *browserReadWorker) NavigationIdentity(context.Context) (string, error) {
	if worker.origin == "about:blank" {
		return "navigation_blank", nil
	}
	return "navigation_fixture", nil
}

func (worker *browserReadWorker) Execute(_ context.Context, action browser.DriverAction) error {
	if action.Kind != browser.DriverNavigate || action.URL != worker.page.origin+worker.page.path {
		return browser.ErrDenied
	}
	response := httptest.NewRecorder()
	worker.page.ServeHTTP(response, httptest.NewRequest(http.MethodGet, action.URL, nil))
	if response.Code != http.StatusOK {
		return browser.ErrWorkerUnavailable
	}
	worker.origin = worker.page.origin
	return nil
}

func (worker *browserReadWorker) ExecuteAfterNavigationCheck(
	ctx context.Context, expected string, action browser.DriverAction,
) error {
	current, _ := worker.NavigationIdentity(ctx)
	if expected != current {
		return browser.ErrStale
	}
	return worker.Execute(ctx, action)
}

func (control *browserReadControl) produceNativeState(t *testing.T, owner browser.Owner) {
	t.Helper()
	store, err := browser.NewFileStore(filepath.Join(control.fixture.workspace, "state/browser/browser.json"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := &browserReadWorker{page: control.page, origin: "about:blank"}
	broker, err := browser.NewBroker(control.fixture.cfg, store, worker)
	if err != nil {
		t.Fatal(err)
	}
	session, err := broker.Open(
		t.Context(),
		browser.OpenRequest{Owner: owner, Target: config.BrowserDefaultTarget, Profile: "eval-browser"},
	)
	if err != nil {
		t.Fatal("native open control", err)
	}
	t.Cleanup(func() { _ = broker.CloseOwner(context.Background(), owner) })
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal("native observe control", err)
	}
	prepared, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "native_read_request", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: control.page.origin + control.page.path},
	})
	if err != nil {
		t.Fatal("native prepare control", err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, nil)
	if err != nil {
		t.Fatal("native execute control", err)
	}
	session, err = broker.Close(t.Context(), owner, session.ID)
	if err != nil || !worker.closed {
		t.Fatal("native close control", err)
	}
	control.session, control.action, control.invocation = session, prepared.Action, invocation
}
