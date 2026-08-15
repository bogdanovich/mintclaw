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
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPlaywrightContextCatalogProjectsStableOpaqueTabsAndNestedFrames(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 7, Selected: "p1", Pages: []playwrightRawPage{{
		Token: "p1", Index: 0, Generation: 3, URL: "https://example.com/path?secret=value",
		Title: "Fixture", Frames: []playwrightRawFrame{
			{Token: "f1", Generation: 2, URL: "https://example.com/frame", Label: "outer"},
			{Token: "f2", Parent: "f1", Generation: 1, URL: "https://example.com/nested", Label: "inner"},
		},
	}}}
	changedRaw := raw
	changedRaw.Generation++
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, raw), contextProbeResult(t, raw), contextProbeResult(t, changedRaw),
		},
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
	if first.Generation != 1 {
		t.Fatalf("initial broker-visible generation = %d, want 1", first.Generation)
	}
	changed, err := worker.ContextCatalog(t.Context())
	if err != nil || changed.Generation != 2 {
		t.Fatalf("changed ContextCatalog() = %#v, %v", changed, err)
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

func TestPlaywrightPendingDialogPreservesCachedContextAuthority(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{{
		Token: "p1", Index: 0, Generation: 1, URL: "https://example.com/form", Title: "Fixture",
	}}}
	client := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": contextProbeResult(t, raw),
	}}
	worker := contextTestWorker(client)
	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	identity := playwrightNavigationIdentity{frameID: "frame-1", loaderID: "loader-1", generation: 1}
	worker.navigationID = identity
	worker.navigationToken = identity.token()
	worker.lastObservation = DriverObservation{
		URL: "https://example.com/form", Origin: "https://example.com", Title: "Fixture",
	}
	worker.pendingDialog = &DialogObservation{Type: "confirm", Message: "Continue?"}
	callsBefore := len(client.calls)

	cached, err := worker.ContextCatalog(t.Context())
	if err != nil || cached.ID != catalog.ID || cached.Generation != catalog.Generation {
		t.Fatalf("ContextCatalog(pending dialog) = %#v, %v; want cached %#v", cached, err, catalog)
	}
	if token, identityErr := worker.NavigationIdentity(t.Context()); identityErr != nil || token != identity.token() {
		t.Fatalf("NavigationIdentity(pending dialog) = %q, %v", token, identityErr)
	}
	observation, err := worker.Observe(t.Context())
	if err != nil || observation.PendingDialog == nil ||
		*observation.PendingDialog != (DialogObservation{Type: "confirm", Message: "Continue?"}) {
		t.Fatalf("Observe(pending dialog) = %#v, %v", observation, err)
	}
	if len(client.calls) != callsBefore {
		t.Fatalf("pending dialog authority called blocked MCP tools: %#v", client.calls[callsBefore:])
	}

	cached.Tabs[0].Title = "mutated caller copy"
	again, err := worker.ContextCatalog(t.Context())
	if err != nil || again.Tabs[0].Title == cached.Tabs[0].Title {
		t.Fatalf("cached context authority was mutable: %#v, %v", again, err)
	}
	worker.pendingDialog = nil
	if _, err = worker.ContextCatalog(t.Context()); err != nil {
		t.Fatalf("ContextCatalog(after dialog) error = %v", err)
	}
	if len(client.calls) != callsBefore+1 {
		t.Fatalf("context authority did not resume live probing after dialog: %#v", client.calls[callsBefore:])
	}
}

func TestPlaywrightPendingDialogWithoutCachedAuthorityFailsClosed(t *testing.T) {
	worker := contextTestWorker(&fakePlaywrightClient{})
	worker.pendingDialog = &DialogObservation{Type: "alert", Message: "Blocked"}
	if _, err := worker.ContextCatalog(t.Context()); !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("ContextCatalog(pending dialog without cache) error = %v, want ErrWorkerUnavailable", err)
	}
	if _, err := worker.NavigationIdentity(t.Context()); !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("NavigationIdentity(pending dialog without cache) error = %v, want ErrWorkerUnavailable", err)
	}
}

func TestContextMutationBindingRoundTripRetainsImmutableAuthority(t *testing.T) {
	catalog := ContextCatalog{
		ID: "context_catalog_1", Generation: 1, SelectedTabID: "context_tab_1",
		Tabs: []TabContext{{
			ID: "context_tab_1", Kind: TabPrimary, CreationSequence: 1,
			DocumentGeneration: 1, URL: "about:blank", Origin: "about:blank",
		}},
	}
	authority := newContextMutationAuthority(catalog, "context_tab_1", "")
	binding, err := authority.Binding()
	if err != nil {
		t.Fatal(err)
	}
	binding.Catalog.Tabs[0].Title = "mutated transport copy"
	fresh, err := authority.Binding()
	if err != nil || fresh.Catalog.Tabs[0].Title != "" {
		t.Fatalf("authority binding was mutated: %#v, %v", fresh, err)
	}
	reconstructed, err := ContextMutationAuthorityFromBinding(fresh)
	if err != nil || reconstructed.validateLive(catalog) != nil {
		t.Fatalf("reconstructed authority = %#v, %v", reconstructed, err)
	}
}

func TestPlaywrightContextCatalogRejectsConfiguredTabLimitOverflow(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
	}}
	client := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": contextProbeResult(t, raw),
	}}
	worker := contextTestWorker(client)
	worker.limits.Tabs = 1
	if _, err := worker.ContextCatalog(t.Context()); !errors.Is(err, ErrDriverIncompatible) {
		t.Fatalf("ContextCatalog(over limit) error = %v", err)
	}
	if !worker.lost {
		t.Fatal("configured tab limit overflow did not retire the worker")
	}
}

func TestPlaywrightContextCatalogAdvancesGenerationForExternalSelection(t *testing.T) {
	firstRaw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: "https://example.com/second"},
	}}
	secondRaw := firstRaw
	secondRaw.Selected = "p2"
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, firstRaw), contextProbeResult(t, secondRaw), contextProbeResult(t, secondRaw),
		},
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
	stable, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || second.Generation != 2 || stable.Generation != second.Generation ||
		first.SelectedTabID == second.SelectedTabID || stable.SelectedTabID != second.SelectedTabID {
		t.Fatalf("selection generations = %#v, %#v, %#v", first, second, stable)
	}
}

func TestPlaywrightSelectContextKeepsIndexesPrivateAndObservesFrame(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 4, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{
			Token:      "p2",
			Index:      1,
			Generation: 2,
			URL:        "https://example.com/page",
			Title:      "Page",
			Frames: []playwrightRawFrame{
				{Token: "f2", Generation: 3, URL: "https://frame.example/inside", Label: "child"},
			},
		},
	}}
	selected := raw
	selected.Selected = "p2"
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, raw), contextProbeResult(t, raw), contextArmResult("p2"),
			contextProbeResult(t, selected),
			contextFrameResult(t, "https://frame.example/inside", "Page", "- heading \"Inside\""),
			contextProbeResult(t, selected),
		},
	}}
	worker := contextTestWorker(client)
	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tabID := catalog.Tabs[1].ID
	frameID := catalog.Tabs[1].Frames[0].ID
	authority := newContextMutationAuthority(catalog, tabID, frameID)
	observation, selectedCatalog, err := worker.SelectContext(t.Context(), authority)
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
	opened.Pages = append(
		opened.Pages,
		playwrightRawPage{
			Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin,
			Frames: []playwrightRawFrame{{Token: "f2", Generation: 1, URL: initialBlankOrigin}},
		},
	)
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, initial),
			contextProbeResult(t, opened),
			contextProbeResult(t, opened),
			contextCloseResult("p2"),
			contextProbeResult(t, initial),
		},
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
	closed, err := worker.CloseTab(
		t.Context(), newContextMutationAuthority(catalog, catalog.SelectedTabID, ""),
	)
	if err != nil || len(closed.Tabs) != 1 || closed.SelectedTabID != closed.Tabs[0].ID {
		t.Fatalf("CloseTab() = %#v, %v", closed, err)
	}
	if len(worker.contextState.tabs) != 1 || len(worker.contextState.tabIndexes) != 1 ||
		len(worker.contextState.tabSequence) != 1 || len(worker.contextState.frames) != 0 ||
		len(worker.contextState.frameSequence) != 0 {
		t.Fatalf("post-close tab registries = %#v", worker.contextState)
	}
	for _, call := range client.calls {
		if call.tool == "browser_tabs" && call.arguments["action"] == "close" {
			t.Fatal("close crossed the Playwright boundary by stale index")
		}
		if call.tool == "browser_run_code_unsafe" {
			code, _ := call.arguments["code"].(string)
			if strings.Contains(code, "await target.close()") &&
				(!strings.Contains(code, "state.generation !== 3") ||
					!strings.Contains(code, "mainRecord.generation !== 1")) {
				t.Fatalf("close lacks atomic catalog/document guards: %s", code)
			}
		}
	}

	finalClient := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": contextProbeResult(t, initial),
	}}
	finalWorker := contextTestWorker(finalClient)
	finalCatalog, err := finalWorker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = finalWorker.CloseTab(
		t.Context(), newContextMutationAuthority(finalCatalog, finalCatalog.SelectedTabID, ""),
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("CloseTab(final) error = %v", err)
	}
	for _, call := range finalClient.calls {
		if call.tool == "browser_tabs" {
			t.Fatal("final tab reached the driver close boundary")
		}
	}
}

func TestPlaywrightContextMutationsRejectStaleAuthorityBeforeDispatch(t *testing.T) {
	initial := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
	}}
	changed := initial
	changed.Generation++
	changed.Pages = append([]playwrightRawPage(nil), initial.Pages...)
	changed.Pages[1].Generation++

	t.Run("select", func(t *testing.T) {
		client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": {contextProbeResult(t, initial), contextProbeResult(t, changed)},
		}}
		worker := contextTestWorker(client)
		catalog, err := worker.ContextCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		authority := newContextMutationAuthority(catalog, catalog.Tabs[1].ID, "")
		if _, _, err = worker.SelectContext(t.Context(), authority); !errors.Is(err, ErrStale) {
			t.Fatalf("SelectContext(stale authority) error = %v", err)
		}
		for _, call := range client.calls {
			if call.tool == "browser_tabs" {
				t.Fatalf("stale authority reached tab selection: %#v", call)
			}
		}
	})

	t.Run("close", func(t *testing.T) {
		client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": {contextProbeResult(t, initial), contextProbeResult(t, changed)},
		}}
		worker := contextTestWorker(client)
		catalog, err := worker.ContextCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		authority := newContextMutationAuthority(catalog, catalog.Tabs[1].ID, "")
		if _, err = worker.CloseTab(t.Context(), authority); !errors.Is(err, ErrStale) {
			t.Fatalf("CloseTab(stale authority) error = %v", err)
		}
		for _, call := range client.calls {
			if call.tool != "browser_run_code_unsafe" {
				continue
			}
			code, _ := call.arguments["code"].(string)
			if strings.Contains(code, "await target.close()") {
				t.Fatalf("stale authority reached close dispatch: %#v", call)
			}
		}
	})
}

func TestPlaywrightOpenTabRejectsAmbiguousTerminalState(t *testing.T) {
	oneTab := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
	}}
	twoTabs := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
	}}
	for _, test := range []struct {
		name   string
		before playwrightRawContextCatalog
		after  playwrightRawContextCatalog
	}{
		{name: "no new tab", before: oneTab, after: oneTab},
		{name: "new tab not selected", before: oneTab, after: playwrightRawContextCatalog{
			Generation: 3, Selected: "p1", Pages: []playwrightRawPage{
				{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
				{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
			},
		}},
		{name: "popup substituted", before: oneTab, after: playwrightRawContextCatalog{
			Generation: 3, Selected: "p2", Pages: []playwrightRawPage{
				{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
				{Token: "p2", Index: 1, Opener: "p1", Generation: 1, URL: initialBlankOrigin},
			},
		}},
		{name: "multiple new tabs", before: oneTab, after: playwrightRawContextCatalog{
			Generation: 4, Selected: "p3", Pages: []playwrightRawPage{
				{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
				{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
				{Token: "p3", Index: 2, Generation: 1, URL: initialBlankOrigin},
			},
		}},
		{name: "old tab replaced", before: twoTabs, after: playwrightRawContextCatalog{
			Generation: 4, Selected: "p4", Pages: []playwrightRawPage{
				{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
				{Token: "p3", Index: 1, Generation: 1, URL: initialBlankOrigin},
				{Token: "p4", Index: 2, Generation: 1, URL: initialBlankOrigin},
			},
		}},
		{name: "new tab navigated", before: oneTab, after: playwrightRawContextCatalog{
			Generation: 3, Selected: "p2", Pages: []playwrightRawPage{
				{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
				{Token: "p2", Index: 1, Generation: 2, URL: "https://example.com/"},
			},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
				"browser_run_code_unsafe": {
					contextProbeResult(t, test.before), contextProbeResult(t, test.after),
				},
			}}
			worker := contextTestWorker(client)
			if _, err := worker.OpenTab(t.Context()); !errors.Is(err, ErrDriverIncompatible) {
				t.Fatalf("OpenTab(ambiguous) error = %v", err)
			}
			if !worker.lost {
				t.Fatal("ambiguous open did not retire the worker")
			}
		})
	}
}

func TestPlaywrightContextCatalogBoundsSequenceRegistriesUnderChurn(t *testing.T) {
	worker := contextTestWorker(&fakePlaywrightClient{})
	for generation := uint64(1); generation <= 32; generation++ {
		token := fmt.Sprintf("p%d", generation)
		frameToken := fmt.Sprintf("f%d", generation)
		raw := playwrightRawContextCatalog{Generation: generation, Selected: token, Pages: []playwrightRawPage{{
			Token: token, Index: 0, Generation: 1, URL: initialBlankOrigin,
			Frames: []playwrightRawFrame{{
				Token: frameToken, Generation: 1, URL: initialBlankOrigin,
			}},
		}}}
		if _, err := worker.projectContextCatalog(raw); err != nil {
			t.Fatalf("project generation %d: %v", generation, err)
		}
		if len(worker.contextState.tabs) != 1 || len(worker.contextState.tabIndexes) != 1 ||
			len(worker.contextState.tabSequence) != 1 || len(worker.contextState.frames) != 1 ||
			len(worker.contextState.frameSequence) != 1 {
			t.Fatalf("generation %d registries = %#v", generation, worker.contextState)
		}
	}
}

func TestPlaywrightContextMutationsRetireWorkerAfterUncertainDispatch(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		initial := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
			{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		}}
		client := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": contextProbeResult(t, initial),
		}}
		worker := contextTestWorker(client)
		client.onCall = func(tool string) {
			if tool == "browser_tabs" {
				worker.networkProxy.denials.Add(1)
			}
		}
		if _, err := worker.OpenTab(t.Context()); !errors.Is(err, ErrDenied) {
			t.Fatalf("OpenTab(uncertain) error = %v", err)
		}
		if !worker.lost {
			t.Fatal("uncertain open did not retire the worker")
		}
	})

	t.Run("close", func(t *testing.T) {
		raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
			{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
			{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
		}}
		client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": {
				contextProbeResult(t, raw), contextProbeResult(t, raw),
			},
		}}
		worker := contextTestWorker(client)
		catalog, err := worker.ContextCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var runCodeCalls int
		client.onCall = func(tool string) {
			if tool != "browser_run_code_unsafe" {
				return
			}
			runCodeCalls++
			if runCodeCalls == 2 {
				worker.networkProxy.denials.Add(1)
			}
		}
		if _, err = worker.CloseTab(
			t.Context(), newContextMutationAuthority(catalog, catalog.Tabs[1].ID, ""),
		); !errors.Is(err, ErrDenied) {
			t.Fatalf("CloseTab(uncertain) error = %v", err)
		}
		if !worker.lost {
			t.Fatal("uncertain close did not retire the worker")
		}
	})

	t.Run("select", func(t *testing.T) {
		raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
			{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
			{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
		}}
		client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": {
				contextProbeResult(t, raw), contextProbeResult(t, raw), contextArmResult("p2"),
			},
		}}
		worker := contextTestWorker(client)
		catalog, err := worker.ContextCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		client.onCall = func(tool string) {
			if tool == "browser_tabs" {
				worker.networkProxy.denials.Add(1)
			}
		}
		if _, _, err = worker.SelectContext(
			t.Context(), newContextMutationAuthority(catalog, catalog.Tabs[1].ID, ""),
		); !errors.Is(err, ErrDenied) {
			t.Fatalf("SelectContext(uncertain) error = %v", err)
		}
		if !worker.lost {
			t.Fatal("uncertain selection did not retire the worker")
		}
	})
}

func TestPlaywrightContextCatalogDoesNotAttributeAmbientProxyDenial(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
	}}
	proxy := &browserNetworkProxy{}
	client := &fakePlaywrightClient{
		callResults: map[string]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": contextProbeResult(t, raw),
		},
		onCall: func(string) { proxy.denials.Add(1) },
	}
	worker := contextTestWorker(client)
	worker.networkProxy = proxy

	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil || catalog.SelectedTabID == "" || len(catalog.Tabs) != 1 || proxy.Denials() != 1 {
		t.Fatalf("ContextCatalog() = %#v, %v; denials = %d", catalog, err, proxy.Denials())
	}
}

func TestPlaywrightSelectContextRejectsIndexRaceBeforeObservation(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: "https://example.com/second"},
	}}
	client := &fakePlaywrightClient{callQueues: map[string][]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": {
			contextProbeResult(t, raw), contextProbeResult(t, raw), contextArmResult("p2"),
			contextProbeResult(t, raw),
		},
	}}
	worker := contextTestWorker(client)
	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = worker.SelectContext(
		t.Context(),
		newContextMutationAuthority(catalog, catalog.Tabs[1].ID, ""),
	); !errors.Is(err, ErrStale) ||
		!worker.lost {
		t.Fatalf("SelectContext(index race) error = %v, lost = %t", err, worker.lost)
	}
	for _, call := range client.calls {
		if call.tool == "browser_snapshot" {
			t.Fatal("index race reached observation")
		}
	}
}

func TestPlaywrightSelectContextMapsAtomicGuardRejectionToStale(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{
		{Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin},
		{Token: "p2", Index: 1, Generation: 1, URL: initialBlankOrigin},
	}}
	rejected := playwrightTextResult("Error: " + playwrightContextSelectStale)
	rejected.IsError = true
	client := &fakePlaywrightClient{
		callQueues: map[string][]*sdkmcp.CallToolResult{
			"browser_run_code_unsafe": {
				contextProbeResult(t, raw), contextProbeResult(t, raw), contextArmResult("p2"),
			},
		},
		callResults: map[string]*sdkmcp.CallToolResult{"browser_tabs": rejected},
	}
	worker := contextTestWorker(client)
	catalog, err := worker.ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	authority := newContextMutationAuthority(catalog, catalog.Tabs[1].ID, "")
	if _, _, err = worker.SelectContext(t.Context(), authority); !errors.Is(err, ErrStale) || worker.lost {
		t.Fatalf("SelectContext(guard stale) error = %v, lost = %t", err, worker.lost)
	}
}

func TestPlaywrightContextCatalogTruncatesUnicodeLabelsSafely(t *testing.T) {
	raw := playwrightRawContextCatalog{Generation: 2, Selected: "p1", Pages: []playwrightRawPage{{
		Token: "p1", Index: 0, Generation: 1, URL: initialBlankOrigin,
		Title: strings.Repeat("🦞", MaxContextLabelBytes), Frames: []playwrightRawFrame{{
			Token: "f1", Generation: 1, URL: initialBlankOrigin, Label: strings.Repeat("界", MaxContextLabelBytes),
		}},
	}}}
	client := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": contextProbeResult(t, raw),
	}}
	catalog, err := contextTestWorker(client).ContextCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tabs[0].Title) > MaxContextLabelBytes || !utf8.ValidString(catalog.Tabs[0].Title) ||
		len(
			catalog.Tabs[0].Frames[0].Label,
		) > MaxContextLabelBytes || !utf8.ValidString(catalog.Tabs[0].Frames[0].Label) {
		t.Fatalf("bounded labels = %q, %q", catalog.Tabs[0].Title, catalog.Tabs[0].Frames[0].Label)
	}
	if err = catalog.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
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
			_, _ = fmt.Fprintf(
				writer,
				`<!doctype html><title>Context Fixture</title><main>root</main><iframe title="Child" src="%s/frame"></iframe>`,
				fixtureURLForRequest(request),
			)
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
		server.Args = append(
			server.Args,
			"--executable-path=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		)
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
	opened, err := factory.Open(ctx, WorkerOpenRequest{
		SessionID: "context_real_fixture",
		Target:    "gateway", Profile: "managed", DryRun: true, Limits: config.BrowserLimitsConfig{},
	})
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
	observation, selected, err := worker.SelectContext(
		ctx, newContextMutationAuthority(catalog, catalog.Tabs[0].ID, frame.ID),
	)
	if err != nil || selected.SelectedFrameID != frame.ID ||
		!strings.Contains(observation.Snapshot, "Nested frame content") {
		t.Fatalf("SelectContext(frame) = %#v, %#v, %v", observation, selected, err)
	}
	openedCatalog, err := worker.OpenTab(ctx)
	if err != nil || len(openedCatalog.Tabs) != 2 ||
		openedCatalog.SelectedTabID == catalog.SelectedTabID {
		t.Fatalf("OpenTab() = %#v, %v", openedCatalog, err)
	}
	if err = worker.Execute(
		ctx,
		DriverAction{Kind: DriverNavigate, URL: fixtureURL.String() + "/secondary"},
	); err != nil {
		t.Fatalf("navigate selected secondary tab error = %v", err)
	}
	secondaryCatalog, err := worker.ContextCatalog(ctx)
	if err != nil || secondaryCatalog.SelectedTabID != openedCatalog.SelectedTabID ||
		len(secondaryCatalog.Tabs) != 2 || secondaryCatalog.Tabs[1].URL != fixtureURL.String()+"/secondary" ||
		len(secondaryCatalog.Tabs[1].Frames) != 1 {
		t.Fatalf("secondary ContextCatalog() = %#v, %v", secondaryCatalog, err)
	}
	closedCatalog, err := worker.CloseTab(
		ctx, newContextMutationAuthority(secondaryCatalog, secondaryCatalog.SelectedTabID, ""),
	)
	if err != nil || len(closedCatalog.Tabs) != 1 || closedCatalog.SelectedTabID != catalog.SelectedTabID {
		t.Fatalf("CloseTab() = %#v, %v", closedCatalog, err)
	}
}

func fixtureURLForRequest(request *http.Request) string {
	return "http://" + request.Host
}

func contextTestWorker(client *fakePlaywrightClient) *playwrightWorker {
	return &playwrightWorker{
		client: client, networkProxy: &browserNetworkProxy{},
		limits: config.BrowserLimitsConfig{}.Effective(), contextSessionID: "session_context_test",
		contextSecret: []byte("01234567890123456789012345678901"),
	}
}

func contextProbeResult(t *testing.T, catalog playwrightRawContextCatalog) *sdkmcp.CallToolResult {
	t.Helper()
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return playwrightTextResult(
		"### Result\n\"" + playwrightContextMarker + "|ok|" + url.QueryEscape(string(encoded)) + "\"",
	)
}

func contextFrameResult(t *testing.T, rawURL, title, snapshot string) *sdkmcp.CallToolResult {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"url": rawURL, "title": title, "snapshot": snapshot})
	if err != nil {
		t.Fatal(err)
	}
	return playwrightTextResult(
		"### Result\n\"" + playwrightContextMarker + "|frame|" + url.QueryEscape(string(encoded)) + "\"",
	)
}

func contextCloseResult(rawToken string) *sdkmcp.CallToolResult {
	return playwrightTextResult("### Result\n\"" + playwrightContextMarker + "|closed|" + rawToken + "\"")
}

func contextArmResult(rawToken string) *sdkmcp.CallToolResult {
	return playwrightTextResult("### Result\n\"" + playwrightContextMarker + "|armed|" + rawToken + "\"")
}
