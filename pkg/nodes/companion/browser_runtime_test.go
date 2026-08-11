package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

type fakeBrowserCommandHost struct {
	profiles       []nodes.BrowserProfileDescriptor
	opened         int
	observed       int
	navigated      int
	clicked        int
	selected       int
	pressed        int
	scrolled       int
	contextCalls   int
	contextError   error
	closed         int
	navigateError  error
	invalidAction  bool
	selectSnapshot string
	routedSessions []string
}

func (host *fakeBrowserCommandHost) BrowserProfiles() []nodes.BrowserProfileDescriptor {
	return nodes.CloneBrowserProfileDescriptors(host.profiles)
}

func (host *fakeBrowserCommandHost) Open(
	_ context.Context,
	request nodes.BrowserHostOpenRequest,
) (nodes.BrowserSessionResult, error) {
	host.opened++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "ready", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func (host *fakeBrowserCommandHost) Status(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "ready", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func (host *fakeBrowserCommandHost) Observe(
	_ context.Context,
	request nodes.BrowserHostObserveRequest,
) (nodes.BrowserObservationResult, error) {
	host.observed++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	return browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration), nil
}

func (host *fakeBrowserCommandHost) Navigate(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.navigated++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	if host.navigateError != nil {
		return nodes.BrowserObservationResult{}, host.navigateError
	}
	result := browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1)
	if host.invalidAction {
		result.SnapshotGeneration = 0
	}
	return result, nil
}

func (host *fakeBrowserCommandHost) Scroll(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.scrolled++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	result := browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1)
	return result, host.navigateError
}

func (host *fakeBrowserCommandHost) Click(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.clicked++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	result := browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1)
	return result, host.navigateError
}

func (host *fakeBrowserCommandHost) Select(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.selected++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	result := browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1)
	if host.selectSnapshot != "" {
		result.Snapshot = host.selectSnapshot
	}
	return result, host.navigateError
}

func (host *fakeBrowserCommandHost) Press(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.pressed++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	return browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1), host.navigateError
}

func (host *fakeBrowserCommandHost) Close(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	host.closed++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "closed", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func (host *fakeBrowserCommandHost) Contexts(
	_ context.Context,
	request nodes.BrowserHostContextRequest,
) (nodes.BrowserContextResult, error) {
	host.contextCalls++
	host.routedSessions = append(host.routedSessions, request.RoutedSessionID)
	result := nodes.BrowserContextResult{
		Operation: request.Operation,
		Catalog: nodes.BrowserContextCatalog{
			ID: "context_catalog_1", Generation: 1, SelectedTabID: "context_tab_1",
			Tabs: []nodes.BrowserTabContext{{
				ID: "context_tab_1", Kind: "primary", CreationSequence: 1,
				DocumentGeneration: 1, URL: "about:blank", Origin: "about:blank",
			}},
		},
	}
	if request.Operation == "select" {
		observation := browserRuntimeObservation(request.SessionID, "tab_primary", 1)
		observation.Snapshot = "transient_context_observation"
		result.Observation = &observation
	}
	return result, host.contextError
}

func browserRuntimeObservation(sessionID, tabID string, generation uint64) nodes.BrowserObservationResult {
	return nodes.BrowserObservationResult{
		SessionID: sessionID, TabID: tabID, SnapshotGeneration: generation,
		URL: "about:blank", Origin: "about:blank", Snapshot: "", Elements: []nodes.BrowserElement{},
	}
}

func TestRuntimeRegistersTypedBrowserCommandsWithoutModelContract(t *testing.T) {
	host := browserRuntimeHostFixture()
	policy := testRuntimePolicy(browserRuntimeCommands())
	policy.MaximumRisk = nodes.RiskWrite
	policy.MaxOutputBytes = nodes.MaxBrowserToolResultBytes
	runtime, err := NewRuntime(
		nodes.ID("node_test"), "test", policy, newMemoryInvocationLedger(), WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, descriptor := range runtime.Catalog().Commands {
		if !nodes.IsBrowserCommand(descriptor.Name) {
			continue
		}
		seen[descriptor.Name] = true
		if descriptor.ModelContract != nil {
			t.Fatalf("browser command %q has model contract %#v", descriptor.Name, descriptor.ModelContract)
		}
	}
	for _, command := range browserRuntimeCommands() {
		if !seen[command] {
			t.Fatalf("browser command %q was not registered", command)
		}
	}
}

func TestRuntimeExecutesTypedBrowserLifecycle(t *testing.T) {
	host := browserRuntimeHostFixture()
	runtime := newBrowserRuntimeFixture(t, host)
	limits := nodes.BrowserLimits{}.Effective()
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionOpen, nodes.BrowserSessionOpenInput{
		SessionID: "browser_session_1", Profile: "managed", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), DryRun: true, Limits: limits,
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionStatus, nodes.BrowserSessionStatusInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandObserve, nodes.BrowserObserveInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandAct, nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_1",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionClose, nodes.BrowserSessionStatusInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
	})
	if host.opened != 1 || host.observed != 1 || host.navigated != 1 || host.closed != 1 {
		t.Fatalf(
			"browser host calls = open %d observe %d navigate %d close %d",
			host.opened, host.observed, host.navigated, host.closed,
		)
	}
	if len(host.routedSessions) != 5 {
		t.Fatalf("routed session calls = %#v", host.routedSessions)
	}
	for _, routedSession := range host.routedSessions {
		if routedSession != "session_test" {
			t.Fatalf("routed session calls = %#v", host.routedSessions)
		}
	}
}

func TestRuntimeExecutesTypedBrowserContextCatalog(t *testing.T) {
	host := browserRuntimeHostFixture()
	runtime := newBrowserRuntimeFixture(t, host)
	result := invokeBrowserRuntime(t, runtime, nodes.BrowserCommandContexts, nodes.BrowserContextInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		Operation: "list", RequestID: "context_request_1",
	})
	var catalog nodes.BrowserContextResult
	if err := json.Unmarshal(result, &catalog); err != nil {
		t.Fatal(err)
	}
	if host.contextCalls != 1 || catalog.Operation != "list" ||
		catalog.Catalog.SelectedTabID != "context_tab_1" {
		t.Fatalf("Contexts() result = %#v, calls = %d", catalog, host.contextCalls)
	}
}

func TestRuntimeBindsTransientBrowserContextAuthorityAndRedactsObservation(t *testing.T) {
	host := browserRuntimeHostFixture()
	runtime := newBrowserRuntimeFixture(t, host)
	authority := nodes.BrowserContextCatalog{
		ID: "context_catalog_1", Generation: 1, SelectedTabID: "context_tab_1",
		Tabs: []nodes.BrowserTabContext{{
			ID: "context_tab_1", Kind: "primary", CreationSequence: 1,
			DocumentGeneration: 1, URL: "about:blank", Origin: "about:blank",
		}},
	}
	ephemeral, err := json.Marshal(struct {
		Authority nodes.BrowserContextCatalog `json:"authority"`
	}{Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := nodes.BrowserContextAuthorityDigest(authority)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(nodes.BrowserContextInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		Operation: "select", RequestID: "context_request_1",
		ContextCatalogID: authority.ID, ContextGeneration: authority.Generation,
		AuthorityDigest: digest, AuthorityBytes: len(ephemeral), TabID: "context_tab_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := testRuntimePlan(t, runtime, nodes.BrowserCommandContexts, input)
	result, err := runtime.InvokeWithEphemeral(t.Context(), plan, ephemeral)
	if err != nil || !bytes.Contains(result, []byte("transient_context_observation")) {
		t.Fatalf("InvokeWithEphemeral(context select) = %s, %v", result, err)
	}
	record, found := runtime.ledger.(*InvocationLedger).Get(plan.InvocationID)
	if !found || record.State != nodes.InvocationSucceeded ||
		bytes.Contains(record.Result, []byte("transient_context_observation")) {
		t.Fatalf("durable context record = %#v, found %v", record, found)
	}
}

func TestRuntimeExecutesTypedBrowserScroll(t *testing.T) {
	host := browserRuntimeHostFixture()
	runtime := newBrowserRuntimeFixture(t, host)
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandAct, nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_scroll_1",
		Action:             nodes.BrowserAction{Kind: "scroll", Direction: "down", Amount: 2},
		Effect:             "read", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("c", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
	})
	if host.scrolled != 1 || host.navigated != 0 {
		t.Fatalf("browser host calls = navigate %d scroll %d", host.navigated, host.scrolled)
	}
}

func TestRuntimeExecutesOnlyExactlyAttestedTypedBrowserClick(t *testing.T) {
	host := browserRuntimeHostFixture()
	host.profiles[0].DryRun = false
	host.profiles[0].AllowApprovedActions = true
	host.profiles[0].Actions = []string{"click", "navigate", "scroll"}
	runtime := newBrowserRuntimeFixture(t, host)
	input := nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_click_1",
		Action:             nodes.BrowserAction{Kind: "click", Ref: "host_ref_1"},
		Effect:             "external_commit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("c", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", ExpectedRole: "button", ExpectedName: "Save",
	}
	var err error
	input.ApprovalDigest, err = nodes.BrowserApprovalDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandAct, input)
	if host.clicked != 1 || host.navigated != 0 || host.scrolled != 0 {
		t.Fatalf("browser host calls = click %d navigate %d scroll %d", host.clicked, host.navigated, host.scrolled)
	}

	for _, test := range []struct {
		name   string
		mutate func(*nodes.BrowserActInput)
	}{
		{name: "semantic drift", mutate: func(input *nodes.BrowserActInput) { input.ExpectedName = "Delete" }},
		{name: "role drift", mutate: func(input *nodes.BrowserActInput) {
			input.ExpectedRole = "link"
			input.Effect = "unknown"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := input
			candidate.ActionInvocationID = "browser_click_denied_" + strings.ReplaceAll(test.name, " ", "_")
			candidate.ApprovalDigest, err = nodes.BrowserApprovalDigest(candidate)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&candidate)
			raw, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			plan := testRuntimePlan(t, runtime, nodes.BrowserCommandAct, raw)
			if _, invokeErr := runtime.Invoke(t.Context(), plan); invokeErr == nil {
				t.Fatal("unattested click was accepted")
			}
		})
	}
	if host.clicked != 1 {
		t.Fatalf("denied clicks reached host: %d", host.clicked)
	}
}

func TestRuntimeExecutesTypedSelectAndApprovedDocumentPress(t *testing.T) {
	selectHost := browserRuntimeHostFixture()
	selectHost.profiles[0].DryRun = false
	selectHost.profiles[0].AllowApprovedActions = true
	selectHost.profiles[0].Actions = []string{"navigate", "press", "select"}
	selectHost.selectSnapshot = `- combobox "State CA" [ref=host_ref_1]`
	selectRuntime := newBrowserRuntimeFixture(t, selectHost)
	selectInput := nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_select_1",
		Action:             nodes.BrowserAction{Kind: "select", Ref: "host_ref_1"},
		Effect:             "local_edit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("c", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", ExpectedRole: "combobox", ExpectedName: "State",
		InputDigest: nodes.BrowserInputDigest("CA"), InputBytes: 2,
	}
	selectRaw, err := json.Marshal(selectInput)
	if err != nil {
		t.Fatal(err)
	}
	selectPlan := testRuntimePlan(t, selectRuntime, nodes.BrowserCommandAct, selectRaw)
	if strings.Contains(string(selectPlan.Input), "CA") {
		t.Fatalf("durable select plan exposed option identity: %s", selectPlan.Input)
	}
	selectResult, err := selectRuntime.InvokeWithEphemeral(
		t.Context(), selectPlan, json.RawMessage(`{"value":"CA"}`),
	)
	if err != nil {
		t.Fatalf("InvokeWithEphemeral(select) error = %v", err)
	}
	if !strings.Contains(string(selectResult), `State CA`) {
		t.Fatalf("transient select result omitted fresh observation: %s", selectResult)
	}
	durable, found, err := selectRuntime.Invocation(selectPlan.InvocationID)
	if err != nil || !found || durable.State != nodes.InvocationSucceeded {
		t.Fatalf("durable select invocation = %+v, %v, %v", durable, found, err)
	}
	if bytes.Contains(durable.Result, []byte("CA")) {
		t.Fatalf("durable select receipt exposed option identity: %s", durable.Result)
	}
	if selectHost.selected != 1 {
		t.Fatalf("select calls = %d", selectHost.selected)
	}
	missingInput := selectInput
	missingInput.ActionInvocationID = "browser_select_missing"
	missingRaw, err := json.Marshal(missingInput)
	if err != nil {
		t.Fatal(err)
	}
	missingPlan := testRuntimePlan(t, selectRuntime, nodes.BrowserCommandAct, missingRaw)
	if _, err = selectRuntime.Invoke(t.Context(), missingPlan); err == nil {
		t.Fatal("select without transient option identity was accepted")
	}
	wrongInput := selectInput
	wrongInput.ActionInvocationID = "browser_select_wrong"
	wrongRaw, err := json.Marshal(wrongInput)
	if err != nil {
		t.Fatal(err)
	}
	wrongPlan := testRuntimePlan(t, selectRuntime, nodes.BrowserCommandAct, wrongRaw)
	if _, err = selectRuntime.InvokeWithEphemeral(
		t.Context(), wrongPlan, json.RawMessage(`{"value":"NY"}`),
	); err == nil {
		t.Fatal("select with a mismatched transient option identity was accepted")
	}
	if selectHost.selected != 1 {
		t.Fatalf("missing transient input reached host: %d", selectHost.selected)
	}

	pressHost := browserRuntimeHostFixture()
	pressHost.profiles[0].DryRun = false
	pressHost.profiles[0].AllowApprovedActions = true
	pressHost.profiles[0].Actions = []string{"navigate", "press", "select"}
	pressRuntime := newBrowserRuntimeFixture(t, pressHost)
	pressInput := nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 2,
		ActionInvocationID: "browser_press_1",
		Action:             nodes.BrowserAction{Kind: "press", Target: "document", Key: "Enter"},
		Effect:             "unknown", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("d", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1",
	}
	pressInput.ApprovalDigest, err = nodes.BrowserApprovalDigest(pressInput)
	if err != nil {
		t.Fatal(err)
	}
	invokeBrowserRuntime(t, pressRuntime, nodes.BrowserCommandAct, pressInput)
	if pressHost.pressed != 1 {
		t.Fatalf("press calls = %d", pressHost.pressed)
	}

	deniedHost := browserRuntimeHostFixture()
	deniedHost.profiles[0].DryRun = false
	deniedHost.profiles[0].AllowApprovedActions = true
	deniedHost.profiles[0].Actions = []string{"navigate", "press", "select"}
	deniedRuntime := newBrowserRuntimeFixture(t, deniedHost)
	denied := pressInput
	denied.ActionInvocationID = "browser_press_denied"
	denied.ApprovalDigest = strings.Repeat("f", 64)
	raw, err := json.Marshal(denied)
	if err != nil {
		t.Fatal(err)
	}
	plan := testRuntimePlan(t, deniedRuntime, nodes.BrowserCommandAct, raw)
	if _, err = deniedRuntime.Invoke(t.Context(), plan); err == nil {
		t.Fatal("unattested press was accepted")
	}
	if deniedHost.pressed != 0 {
		t.Fatalf("denied press reached host: %d", deniedHost.pressed)
	}
}

func TestRuntimeMarksAmbiguousOrInvalidBrowserActionUnknownWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*fakeBrowserCommandHost)
	}{
		{name: "host lost", configure: func(host *fakeBrowserCommandHost) {
			host.navigateError = nodes.ErrBrowserHostLost
		}},
		{name: "invalid terminal output", configure: func(host *fakeBrowserCommandHost) {
			host.invalidAction = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := browserRuntimeHostFixture()
			test.configure(host)
			runtime := newBrowserRuntimeFixture(t, host)
			input, err := json.Marshal(nodes.BrowserActInput{
				SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
				ActionInvocationID: "browser_action_1",
				Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
				Effect:             "navigation", CurrentOrigin: "about:blank",
				PreparedActionHash:    strings.Repeat("b", 64),
				BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			plan := testRuntimePlan(t, runtime, nodes.BrowserCommandAct, input)
			if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) {
				t.Fatalf("Invoke() error = %v, want unknown", err)
			}
			record, found := runtime.ledger.(*InvocationLedger).Get(plan.InvocationID)
			if !found || record.State != nodes.InvocationUnknown || host.navigated != 1 {
				t.Fatalf("record = %#v, found %v, navigate calls %d", record, found, host.navigated)
			}
			if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) ||
				host.navigated != 1 {
				t.Fatalf("replay error = %v, navigate calls = %d", err, host.navigated)
			}
		})
	}
}

func TestRuntimeMarksAmbiguousBrowserContextMutationUnknownWithoutReplay(t *testing.T) {
	host := browserRuntimeHostFixture()
	host.contextError = nodes.ErrBrowserHostLost
	runtime := newBrowserRuntimeFixture(t, host)
	input, err := json.Marshal(nodes.BrowserContextInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		Operation: "open", RequestID: "context_request_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := testRuntimePlan(t, runtime, nodes.BrowserCommandContexts, input)
	if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) {
		t.Fatalf("Invoke() error = %v, want unknown", err)
	}
	record, found := runtime.ledger.(*InvocationLedger).Get(plan.InvocationID)
	if !found || record.State != nodes.InvocationUnknown || host.contextCalls != 1 {
		t.Fatalf("record = %#v, found %v, context calls %d", record, found, host.contextCalls)
	}
	if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) ||
		host.contextCalls != 1 {
		t.Fatalf("replay error = %v, context calls = %d", err, host.contextCalls)
	}
}

func browserRuntimeHostFixture() *fakeBrowserCommandHost {
	return &fakeBrowserCommandHost{profiles: []nodes.BrowserProfileDescriptor{{
		Alias: "managed", Revision: "managed-v1", Driver: nodes.BrowserDriverPlaywrightMCP,
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		DryRun: true, Actions: []string{"navigate", "scroll"}, Limits: nodes.BrowserLimits{}.Effective(),
	}}}
}

func newBrowserRuntimeFixture(t *testing.T, host *fakeBrowserCommandHost) *Runtime {
	t.Helper()
	policy := testRuntimePolicy(browserRuntimeCommands())
	policy.MaximumRisk = nodes.RiskWrite
	policy.MaxOutputBytes = nodes.MaxBrowserToolResultBytes
	runtime, err := NewRuntime(
		nodes.ID("node_test"), "test", policy, newMemoryInvocationLedger(), WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func invokeBrowserRuntime(t *testing.T, runtime *Runtime, command string, input any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	plan := testRuntimePlan(t, runtime, command, raw)
	result, err := runtime.Invoke(t.Context(), plan)
	if err != nil {
		t.Fatalf("Invoke(%s) error = %v", command, err)
	}
	return result
}

func browserRuntimeCommands() []string {
	return []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandSessionStatus,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandAct,
		nodes.BrowserCommandContexts,
		nodes.BrowserCommandSessionClose,
	}
}
