package eval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	nativemcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

func newTestActiveToolFixture(t *testing.T) *activeToolFixture {
	t.Helper()
	fixture, err := newActiveToolFixture("127.0.0.1:0", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.shutdown(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func TestActiveToolFixtureNativeMCPWaitsForDriver(t *testing.T) {
	fixture := newTestActiveToolFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	manager := nativemcp.NewManager()
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ConnectServer(ctx, "fixture", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: fixture.url,
		Headers: map[string]string{"Authorization": "Bearer " + fixture.token},
	}); err != nil {
		t.Fatal(err)
	}
	if tools := manager.GetAllTools()["fixture"]; len(tools) != 1 || tools[0].Name != activeToolName {
		t.Fatal("fixture exposed extra model authority", tools)
	}
	args := activeToolArgs{TrialID: "trial-1", Slot: 1}
	if err := fixture.register(args.TrialID, "session-a", args.Slot); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, err := manager.CallTool(ctx, "fixture", activeToolName,
			map[string]any{"trial_id": args.TrialID, "slot": args.Slot})
		if err == nil && (response == nil || response.IsError) {
			err = errActiveToolFixture
		}
		result <- err
	}()
	if err := fixture.waitEntered(ctx, args); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
		t.Fatal("native tool returned before driver release")
	default:
	}
	if err := fixture.release(args); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	observation, err := fixture.snapshot(args)
	if err != nil || observation.State != "released" || observation.SessionID != "session-a" ||
		observation.EnteredSequence != 1 || observation.ReleaseSequence != 2 || observation.SettledSequence != 3 {
		t.Fatal("missing ordered fixture observation", observation, err)
	}
	if _, _, err := fixture.call(ctx, nil, args); err == nil {
		t.Fatal("completed trial was replayed")
	}
	if fixture.release(args) == nil || fixture.register(args.TrialID, "session-b", args.Slot) == nil {
		t.Fatal("retired identity was adopted")
	}
}

func TestActiveToolFixtureMissingCanceledAndConcurrentControls(t *testing.T) {
	fixture := newTestActiveToolFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	missing := activeToolArgs{TrialID: "missing", Slot: 1}
	if _, _, err := fixture.call(ctx, nil, missing); err == nil || fixture.release(missing) == nil ||
		fixture.waitEntered(ctx, missing) == nil {
		t.Fatal("unknown fixture produced active evidence")
	}
	results := make(chan error, 2)
	for _, id := range []string{"trial-a", "trial-b"} {
		args := activeToolArgs{TrialID: id, Slot: 1}
		if err := fixture.register(id, "session-"+id, 1); err != nil || fixture.release(args) == nil {
			t.Fatal("invalid registration or premature release", err)
		}
		go func() {
			_, _, err := fixture.call(ctx, nil, args)
			results <- err
		}()
		if err := fixture.waitEntered(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	third := activeToolArgs{TrialID: "third", Slot: 1}
	if fixture.register(third.TrialID, "session-third", 1) != nil {
		t.Fatal("third identity could not be registered")
	}
	unknownCtx, cancelUnknown := context.WithCancel(ctx)
	cancelUnknown()
	if fixture.waitEntered(unknownCtx, third) == nil {
		t.Fatal("registered but uncalled tool produced active evidence")
	}
	if _, _, err := fixture.call(ctx, nil, third); err == nil {
		t.Fatal("more than two concurrent native calls admitted")
	}
	if _, _, err := fixture.call(ctx, nil, activeToolArgs{TrialID: "trial-a", Slot: 1}); err == nil {
		t.Fatal("active call replayed")
	}
	cancel()
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("canceled read became a successful release")
		}
	}
	observation, err := fixture.snapshot(activeToolArgs{TrialID: "trial-a", Slot: 1})
	if err != nil || observation.State != "canceled" || observation.ReleaseSequence != 0 {
		t.Fatal("cancellation receipt changed", observation, err)
	}
}

func TestActiveToolFixtureShutdownUnblocksOwnedCalls(t *testing.T) {
	fixture := newTestActiveToolFixture(t)
	args := activeToolArgs{TrialID: "shutdown", Slot: 1}
	if fixture.register(args.TrialID, "session-shutdown", 1) != nil {
		t.Fatal("register")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := fixture.call(ctx, nil, args)
		result <- err
	}()
	if fixture.waitEntered(ctx, args) != nil || fixture.shutdown() != nil {
		t.Fatal("owned shutdown failed")
	}
	if err := <-result; err == nil {
		t.Fatal("shutdown was confused with driver release")
	}
	observation, err := fixture.snapshot(args)
	if err != nil || observation.State != "closed" || observation.ReleaseSequence != 0 {
		t.Fatal("missing closed observation", observation, err)
	}
	if fixture.register("after-close", "session-a", 1) == nil {
		t.Fatal("closed host admitted a trial")
	}
}

func TestActiveToolFixtureHTTPAndSerializationBoundary(t *testing.T) {
	fixture := newTestActiveToolFixture(t)
	for _, test := range []struct {
		path, token, host, origin string
		status                    int
	}{
		{path: "/mcp", status: http.StatusUnauthorized},
		{path: "/release", token: fixture.token, status: http.StatusBadRequest},
		{path: "/mcp?release=1", token: fixture.token, status: http.StatusBadRequest},
		{path: "/mcp", token: fixture.token, host: "example.invalid", status: http.StatusBadRequest},
		{path: "/mcp", token: fixture.token, origin: "https://example.invalid", status: http.StatusBadRequest},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			strings.TrimSuffix(fixture.url, "/mcp")+test.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+test.token)
		request.Header.Set("Origin", test.origin)
		if test.host != "" {
			request.Host = test.host
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatal(test.path, response.StatusCode)
		}
	}
	if _, err := json.Marshal(fixture); err == nil {
		t.Fatal("private host/token serialized")
	}
	for _, address := range []string{"0.0.0.0:0", "localhost:1234", "127.0.0.1:80", "127.0.0.1:01234"} {
		if host, err := newActiveToolFixture(address, fixture.token); err == nil {
			_ = host.shutdown()
			t.Fatal("unsafe listener accepted", address)
		}
	}
}
