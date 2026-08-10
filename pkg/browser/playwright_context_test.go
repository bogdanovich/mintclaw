package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPlaywrightContextCatalogProjectsStableOpaqueTabsAndNestedFrames(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 7, Selected: "p1", Pages: []playwrightRawPage{{
		Token: "p1", Index: 0, Generation: 3, URL: "https://example.com/path?secret=value",
		Title: "Fixture", Frames: []playwrightRawFrame{
			{Token: "f1", Generation: 2, URL: "https://example.com/frame", Label: "outer"},
			{Token: "f2", Parent: "f1", Generation: 1, URL: "https://example.com/nested", Label: "inner"},
		},
	}}}
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {contextProbeResult(t, raw), contextProbeResult(t, raw)},
	}}
	worker := contextTestWorker(client)
	first, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Generation != second.Generation || first.SelectedTabID != second.SelectedTabID ||
		len(first.Tabs) != 1 || len(first.Tabs[0].Frames) != 2 {
		t.Fatalf("catalogs = %#v, %#v", first, second)
	}
	if strings.Contains(first.SelectedTabID, "driver") || strings.Contains(first.Tabs[0].Frames[0].ID, "driver") ||
		first.Tabs[0].URL != "https://example.com/path" || first.Tabs[0].Kind != TabPrimary ||
		first.Tabs[0].Frames[0].Depth != 1 || first.Tabs[0].Frames[1].Depth != 2 ||
		first.Tabs[0].Frames[1].ParentFrameID != first.Tabs[0].Frames[0].ID {
		t.Fatalf("projected catalog = %#v", first)
	}
	if err = first.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestPlaywrightSelectContextKeepsIndexesPrivateAndObservesFrame(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 4, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 2, URL: "https://example.com/page", Title: "Page", Frames: []playwrightRawFrame{
			{Token: "f2", Generation: 3, URL: "https://frame.example/inside", Label: "child"},
		}},
	}}
	selected := raw
	selected.Selected = "p2"
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, raw), contextProbeResult(t, raw), contextProbeResult(t, selected),
			contextFrameResult(t, "https://frame.example/inside", "Page", "- heading \"Inside\""),
		},
	}}
	worker := contextTestWorker(client)
	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tabID := catalog.Tabs[1].ID
	frameID := catalog.Tabs[1].Frames[0].ID
	observation, selectedCatalog, err := worker.SelectContext(t.Context(), tabID, frameID)
	if err != nil {
		t.Fatal(err)
	}
	if selectedCatalog.SelectedTabID != tabID || selectedCatalog.SelectedFrameID != frameID ||
		observation.URL != "https://frame.example/inside" || observation.Origin != "https://frame.example" ||
		observation.Snapshot != "- heading \"Inside\"" {
		t.Fatalf("selection = %#v, observation = %#v", selectedCatalog, observation)
	}
	var tabCall *playwrightCall
	for index := range client.calls {
		if client.calls[index].tool == "browser_tabs" {
			tabCall = &client.calls[index]
		}
	}
	if tabCall == nil || tabCall.arguments["action"] != "select" || tabCall.arguments["index"] != 1 {
		t.Fatalf("private tab selection call = %#v", tabCall)
	}
	encodedCalls, _ := json.Marshal(client.calls)
	if strings.Contains(string(encodedCalls), tabID) || strings.Contains(string(encodedCalls), frameID) {
		t.Fatal("opaque context IDs crossed the Playwright boundary")
	}
}

func TestPlaywrightOpenAndCloseTabsEnforceBoundsAndFinalTab(t *testing.T) {
	initial := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
	}}
	opened := initial
	opened.Generation = 3
	opened.Selected = "p2"
	opened.Pages = append(opened.Pages, playwrightRawPage{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin})
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {contextProbeResult(t, initial), contextProbeResult(t, opened), contextProbeResult(t, opened)},
	}}
	worker := contextTestWorker(client)
	catalog, err := worker.OpenTab(t.Context())
	if err != nil || len(catalog.Tabs) != 2 || catalog.SelectedTabID != catalog.Tabs[1].ID {
		t.Fatalf("OpenTab() = %#v, %v", catalog, err)
	}
	if len(client.calls) < 2 || client.calls[1].tool != "browser_tabs" ||
		client.calls[1].arguments["action"] != "new" {
		t.Fatalf("open calls = %#v", client.calls)
	}

	finalClient := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": contextProbeResult(t, initial),
	}}
	finalWorker := contextTestWorker(finalClient)
	finalCatalog, err := finalWorker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = finalWorker.CloseTab(t.Context(), finalCatalog.SelectedTabID); !errors.Is(err, ErrDenied) {
		t.Fatalf("CloseTab(final) error = %v", err)
	}
	for _, call := range finalClient.calls {
		if call.tool == "browser_tabs" {
			t.Fatal("final tab reached the driver close boundary")
		}
	}
}

func TestParsePlaywrightContextProbeFailsClosed(t *testing.T) {
	for _, text := range []string{
		"missing header",
		"### Result\nMINTCLAW_CONTEXT_V1|ok|%7B%7D",
		"### Result\nMINTCLAW_CONTEXT_V1|error|private-handle",
	} {
		if _, err := parsePlaywrightContextProbe(text); !errors.Is(err, ErrDriverIncompatible) {
			t.Fatalf("parsePlaywrightContextProbe(%q) error = %v", text, err)
		}
	}
}

func TestPlaywrightContextWorkerRealBrowserTabsAndNestedFrames(t *testing.T) {
	if os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER") != "1" {
		t.Skip("set MINTCLAW_BROWSER_REAL_DRIVER=1 to run the pinned Playwright MCP fixture")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/frame":
			_, _ = fmt.Fprint(writer, `<!doctype html><title>Frame</title><main><h1>Nested frame content</h1></main>`)
		default:
			_, _ = fmt.Fprintf(writer, `<!doctype html><title>Context Fixture</title><main>root</main><iframe title="Child" src="%s/frame"></iframe>`, fixtureURLForRequest(request))
		}
	}))
	defer fixture.Close()

	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	profile.NetworkMode = config.BrowserNetworkPublicWeb
	profile.AllowedOrigins = nil
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	server := root.Tools.MCP.Servers["playwright"]
	driverTemp := t.TempDir()
	driverOutput := filepath.Join(driverTemp, "output")
	if mkdirErr := os.Mkdir(driverOutput, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	server.ExclusiveLockFile = filepath.Join(driverTemp, "playwright.lock")
	server.Args = []string{
		"-y", "@playwright/mcp@0.0.78", "--headless", "--browser=chrome", "--isolated",
		"--output-mode=stdout", "--output-dir=" + driverOutput,
	}
	if runtime.GOOS == "darwin" {
		server.Args = append(server.Args, "--executable-path=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	}
	root.Tools.MCP.Servers["playwright"] = server
	factory, err := NewPlaywrightWorkerFactory(root)
	if err != nil {
		t.Fatal(err)
	}
	factory.proxyLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	factory.proxyDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}
	fixtureURL, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	fixtureURL.Host = "browser-context-fixture.test:" + fixtureURL.Port()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opened, err := factory.Open(ctx, WorkerOpenRequest{SessionID: "context_real_fixture",
		Target: "gateway", Profile: "managed", DryRun: true, Limits: config.BrowserLimitsConfig{}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	worker := opened.Owner.(*playwrightWorker)
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	if err = worker.Execute(ctx, DriverAction{Kind: DriverNavigate, URL: fixtureURL.String()}); err != nil {
		t.Fatalf("navigate error = %v", err)
	}
	catalog, err := worker.ContextCatalog(ctx)
	if err != nil || len(catalog.Tabs) != 1 || len(catalog.Tabs[0].Frames) != 1 {
		t.Fatalf("initial ContextCatalog() = %#v, %v", catalog, err)
	}
	frame := catalog.Tabs[0].Frames[0]
	observation, selected, err := worker.SelectContext(ctx, catalog.Tabs[0].ID, frame.ID)
	if err != nil || selected.SelectedFrameID != frame.ID ||
		!strings.Contains(observation.Snapshot, "Nested frame content") {
		t.Fatalf("SelectContext(frame) = %#v, %#v, %v", observation, selected, err)
	}
	openedCatalog, err := worker.OpenTab(ctx)
	if err != nil || len(openedCatalog.Tabs) != 2 ||
		openedCatalog.SelectedTabID == catalog.SelectedTabID {
		t.Fatalf("OpenTab() = %#v, %v", openedCatalog, err)
	}
	closedCatalog, err := worker.CloseTab(ctx, openedCatalog.SelectedTabID)
	if err != nil || len(closedCatalog.Tabs) != 1 || closedCatalog.SelectedTabID != catalog.SelectedTabID {
		t.Fatalf("CloseTab() = %#v, %v", closedCatalog, err)
	}
}

func fixtureURLForRequest(request *http.Request) string {
	return "http://" + request.Host
}

func contextTestWorker(client *fakePlaywrightClient) *playwrightWorker {
	return &playwrightWorker{client: client, networkProxy: &browserNetworkProxy{},
		limits: config.BrowserLimitsConfig{}.Effective(), contextSessionID: "session_context_test",
		contextSecret: []byte("01234567890123456789012345678901")}
}

func contextProbeResult(t *testing.T, catalog playwrightRawContextCatalog) *sdkmcp.CallToolResult {
	t.Helper()
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return playwrightTextResult("### Result\n\"" + playwrightContextMarker + "|ok|" + url.QueryEscape(string(encoded)) + "\"")
}

func contextFrameResult(t *testing.T, rawURL, title, snapshot string) *sdkmcp.CallToolResult {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"url": rawURL, "title": title, "snapshot": snapshot})
	if err != nil {
		t.Fatal(err)
	}
	return playwrightTextResult("### Result\n\"" + playwrightContextMarker + "|frame|" + url.QueryEscape(string(encoded)) + "\"")
}
