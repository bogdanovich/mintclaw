package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestBrokerClickNotDispatchedPreservesSessionWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		preserved bool
	}{
		{"proven preflight", ErrClickNotDispatched, true},
		{"transport loss", errors.Join(ErrClickNotDispatched, ErrWorkerUnavailable), false},
		{"driver rejection", errors.Join(ErrClickNotDispatched, ErrDriverRejected), false},
		{"incompatible", errors.Join(ErrClickNotDispatched, ErrDriverIncompatible), false},
		{"canceled", errors.Join(ErrClickNotDispatched, context.Canceled), false},
		{"deadline", errors.Join(ErrClickNotDispatched, context.DeadlineExceeded), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			request := PrepareActionRequest{
				Owner:              testOwner(),
				RequestID:          "click_preflight",
				SessionID:          session.ID,
				TabID:              session.TabID,
				SnapshotID:         observed.SnapshotID,
				SnapshotGeneration: observed.SnapshotGeneration,
				Action: Action{
					Kind: ActionClick,
					Ref:  onlyVisibleRef(t, observed.Snapshot),
				},
				DeclaredEffect: EffectRead,
			}
			prepared, err := broker.PrepareAction(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			worker.executeErr = test.err
			invocation, err := broker.ExecuteAction(t.Context(), testOwner(), prepared.Action.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, err := broker.Status(t.Context(), testOwner(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !test.preserved {
				if invocation.State != InvocationUnknown || status.State != SessionLost || worker.closed != 1 {
					t.Fatalf("uncertain click = %#v; session = %#v", invocation, status)
				}
				return
			}
			if invocation.State != InvocationFailed || invocation.SafeFailure != "click_not_dispatched" ||
				invocation.Diagnostic == nil || status.State != SessionReady || worker.closed != 0 {
				t.Fatalf("preflight failure = %#v; session = %#v", invocation, status)
			}
			replayed, err := broker.ExecuteAction(t.Context(), testOwner(), prepared.Action.ID, nil)
			if err != nil || replayed.State != InvocationFailed || replayed.Diagnostic == nil ||
				len(worker.actions) != 1 {
				t.Fatalf("replay = %#v, %v; calls = %d", replayed, err, len(worker.actions))
			}
			request.RequestID = "old_click_snapshot"
			if _, err = broker.PrepareAction(t.Context(), request); !errors.Is(err, ErrStale) {
				t.Fatalf("old snapshot accepted: %v", err)
			}
			fresh, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			request.RequestID, request.SnapshotID, request.SnapshotGeneration = "fresh_click", fresh.SnapshotID, fresh.SnapshotGeneration
			request.Action.Ref = onlyVisibleRef(t, fresh.Snapshot)
			prepared, err = broker.PrepareAction(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			worker.executeErr = nil
			invocation, err = broker.ExecuteAction(t.Context(), testOwner(), prepared.Action.ID, nil)
			if err != nil || invocation.State != InvocationSucceeded || len(worker.actions) != 2 {
				t.Fatalf("recovery = %#v, %v", invocation, err)
			}
		})
	}
}

func TestPlaywrightClickPreflightSeparatesDispatchAndDialog(t *testing.T) {
	for _, test := range []struct {
		name    string
		results []*sdkmcp.CallToolResult
		want    error
		calls   int
	}{
		{"blocked", []*sdkmcp.CallToolResult{playwrightTextResult("### Result\n\"MINTCLAW_NAV_ACT_V1|click_not_dispatched\"\n### Page\nignored")}, ErrClickNotDispatched, 1},
		{"stale before trial", []*sdkmcp.CallToolResult{playwrightTextResult("### Result\n\"MINTCLAW_NAV_ACT_V1|stale\"")}, ErrClickNotDispatched, 1},
		{"success", []*sdkmcp.CallToolResult{playwrightTextResult("### Result\n\"MINTCLAW_NAV_ACT_V1|ok\""), playwrightTextResult("### Result\n\"MINTCLAW_NAV_ACT_V1|ok\"")}, nil, 2},
		{"rejection after trial", []*sdkmcp.CallToolResult{playwrightTextResult("### Result\n\"MINTCLAW_NAV_ACT_V1|ok\""), {IsError: true}}, ErrDriverRejected, 2},
		{"malformed", []*sdkmcp.CallToolResult{playwrightTextResult("page says click_not_dispatched")}, ErrDriverIncompatible, 1},
		{"trial dialog", []*sdkmcp.CallToolResult{playwrightTextResult("### Modal state\n- [\"alert\" dialog with message \"Hello\"]: can be handled by browser_handle_dialog")}, ErrDriverRejected, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakePlaywrightClient{
				callQueues: map[string][]*sdkmcp.CallToolResult{"browser_run_code_unsafe": test.results},
			}
			identity := playwrightNavigationIdentity{frameID: "frame-1", loaderID: "loader-1", generation: 1}
			worker := &playwrightWorker{
				client:          client,
				limits:          config.BrowserLimitsConfig{ActionSeconds: 4}.Effective(),
				navigationID:    identity,
				navigationToken: identity.token(),
				lastObservation: DriverObservation{URL: "https://example.com", Origin: "https://example.com"},
			}
			err := worker.ExecuteAfterNavigationCheck(
				t.Context(),
				identity.token(),
				DriverAction{Kind: DriverClick, Target: "e4"},
			)
			if !errors.Is(err, test.want) || len(client.calls) != test.calls {
				t.Fatalf("click = %v, calls = %d; want %v, %d", err, len(client.calls), test.want, test.calls)
			}
			code := client.calls[0].arguments["code"].(string)
			if !strings.Contains(code, "trial: true, timeout: 1000") || strings.Contains(code, "noWaitAfter") ||
				strings.Contains(code, "force:") {
				t.Fatalf("unsafe preflight: %s", code)
			}
			if test.calls == 2 && strings.Contains(client.calls[1].arguments["code"].(string), "trial:") {
				t.Fatal("real dispatch replaced by trial")
			}
		})
	}
}

func TestRealBrowserBlockedPhotoClickRecoversInSameSession(t *testing.T) {
	if os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER") != "1" ||
		os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER_MODE") != config.BrowserDriverPlaywrightLibrary {
		t.Skip("set the real Playwright library driver environment")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Photo click recovery</title>
<div style="position:relative;width:100px;height:100px">
<img alt="Blocked photo" style="width:100px;height:100px" onclick="document.querySelector('output').textContent='BAD_CLICK'" src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='100' height='100'/%3E">
<div style="position:absolute;inset:0;background:transparent"></div></div>
<button onclick="document.querySelector('output').textContent='PHOTO_OPENED_ONCE'">Open photo</button><output>NO_PHOTO_CLICK</output>`)
	}))
	defer fixture.Close()
	root := runtimeAdmittedBrowserConfig(t, false)
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.NetworkMode, profile.AllowedOrigins = config.BrowserNetworkAnyHTTP, nil
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	root.Tools.Browser.Limits.ActionSeconds = 8
	configureRealPlaywrightLibraryDriver(t, root)
	factory, err := NewPlaywrightWorkerFactory(root)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(root, NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	session, err := broker.Open(ctx, OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = broker.Close(context.Background(), testOwner(), session.ID) })
	act := func(id string, action Action, effect Effect) Invocation {
		t.Helper()
		observation, observeErr := broker.Observe(ctx, testOwner(), session.ID, session.TabID)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if action.Kind == ActionClick {
			pattern := `img "Blocked photo" \[ref=(ref_[a-z0-9]+)\]`
			if id == "recovery" {
				pattern = `button "Open photo" \[ref=(ref_[a-z0-9]+)\]`
			}
			action.Ref = mustSnapshotRef(t, observation.Snapshot, pattern)
		}
		prepared, prepareErr := broker.PrepareAction(
			ctx,
			PrepareActionRequest{
				Owner:              testOwner(),
				RequestID:          id,
				SessionID:          session.ID,
				TabID:              session.TabID,
				SnapshotID:         observation.SnapshotID,
				SnapshotGeneration: observation.SnapshotGeneration,
				Action:             action,
				DeclaredEffect:     effect,
			},
		)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		invocation, executeErr := broker.ExecuteAction(ctx, testOwner(), prepared.Action.ID, nil)
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		return invocation
	}
	if result := act(
		"navigate",
		Action{Kind: ActionNavigate, URL: fixture.URL},
		"",
	); result.State != InvocationSucceeded {
		t.Fatalf("navigate: %#v", result)
	}
	if result := act(
		"blocked",
		Action{Kind: ActionClick},
		EffectRead,
	); result.State != InvocationFailed ||
		result.SafeFailure != "click_not_dispatched" {
		t.Fatalf("blocked: %#v", result)
	}
	status, err := broker.Status(ctx, testOwner(), session.ID)
	if err != nil || status.State != SessionReady {
		t.Fatalf("status: %#v, %v", status, err)
	}
	if result := act("recovery", Action{Kind: ActionClick}, EffectLocalEdit); result.State != InvocationSucceeded {
		t.Fatalf("recovery: %#v", result)
	}
	observed, err := broker.Observe(ctx, testOwner(), session.ID, session.TabID)
	if err != nil || !strings.Contains(observed.Snapshot, "PHOTO_OPENED_ONCE") {
		t.Fatalf("observation: %#v, %v", observed, err)
	}
	closed, err := broker.Close(ctx, testOwner(), session.ID)
	if err != nil || closed.State != SessionClosed {
		t.Fatalf("close: %#v, %v", closed, err)
	}
}
