package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestLoopbackFixtureRealLibraryTransportAndEphemeralCleanup(t *testing.T) {
	runLoopbackFixtureRealLibrary(t, false)
}

func TestRealBrowserLoopbackFixtureLongPrivateRuntime(t *testing.T) {
	runLoopbackFixtureRealLibrary(t, true)
}

func runLoopbackFixtureRealLibrary(t *testing.T, longRuntime bool) {
	t.Helper()
	if os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER") != "1" ||
		os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER_MODE") != config.BrowserDriverPlaywrightLibrary {
		t.Skip("set the real Playwright library driver environment to run this fixture")
	}
	var foreignRequests, fixtureRequests, resourceRequests, redirectRequests atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(foreign.Close)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureRequests.Add(1)
		if r.URL.Path == "/redirect" {
			redirectRequests.Add(1)
			http.Redirect(w, r, foreign.URL+"/landing", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/subresource" {
			resourceRequests.Add(1)
			_, _ = fmt.Fprintf(w, `<!doctype html><img src="%s/pixel" alt="blocked foreign resource">`, foreign.URL)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><title>Loopback Fixture</title><main>Owned fixture boundary</main>`)
	}))
	t.Cleanup(fixture.Close)
	root, runtimeConfig := ephemeralPlaywrightConfig(t, false)
	if longRuntime {
		padding := strings.Repeat("p", max(1, 150-len(runtimeConfig.EphemeralRoot)))
		runtimeConfig.EphemeralRoot = filepath.Join(runtimeConfig.EphemeralRoot, padding)
		if err := os.Mkdir(runtimeConfig.EphemeralRoot, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configureRealPlaywrightLibraryDriver(t, root)
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := fixtureNetworkProfile(fixture.URL)
	profile.AllowedAgents, profile.AllowedActors = []string{"browser"}, []string{"telegram:owner"}
	profile.Runtime = runtimeConfig
	target.Profiles["ephemeral"] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	factory, err := NewPlaywrightProfileWorkerFactory(root, "gateway", "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	opened, err := factory.Open(ctx, WorkerOpenRequest{
		SessionID: "loopback_fixture_real", Target: "gateway", Profile: "ephemeral",
		ProfileRevision: profile.Revision, DryRun: true,
	})
	if opened.Owner != nil {
		t.Cleanup(func() { _ = opened.Owner.Close(context.Background()) })
	}
	if err != nil {
		t.Fatal("real fixture browser open failed", err)
	}
	worker := opened.Owner.(*playwrightWorker)
	if err = worker.Execute(ctx, DriverAction{Kind: DriverNavigate, URL: fixture.URL}); err != nil {
		// The native worker conservatively attributes overlapping denied browser
		// background traffic to an action. This test qualifies transport and
		// cleanup only, not broker acceptance or an eval scenario verdict.
		if !errors.Is(err, ErrDenied) || errors.Is(err, ErrDriverRejected) {
			t.Fatal("real fixture navigation failed", err, "fixture requests", fixtureRequests.Load())
		}
		t.Log("native action reported a proxy denial; only independent transport/observation evidence is qualified")
	}
	observation, err := worker.Observe(ctx)
	if err != nil || observation.Origin != fixture.URL ||
		!strings.Contains(observation.Snapshot, "Owned fixture boundary") {
		t.Fatal("real fixture observation missing", err)
	}
	// Denied traffic can return either a driver error or the proxy's 403.
	// Neither result is permission for traffic to reach the foreign listener.
	_ = worker.Execute(ctx, DriverAction{Kind: DriverNavigate, URL: fixture.URL + "/subresource"})
	_ = worker.Execute(ctx, DriverAction{Kind: DriverNavigate, URL: fixture.URL + "/redirect"})
	if err = worker.Close(ctx); err != nil {
		t.Fatal("real fixture browser did not close", err)
	}
	if resourceRequests.Load() != 1 || redirectRequests.Load() != 1 || foreignRequests.Load() != 0 {
		t.Fatal("real browser fixture boundary mismatch",
			resourceRequests.Load(), redirectRequests.Load(), foreignRequests.Load())
	}
	assertDirectoryEmpty(t, runtimeConfig.EphemeralRoot)
}
