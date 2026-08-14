package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
)

type browserNodeTestHandler struct {
	mu                       sync.Mutex
	registration             nodes.Registration
	commands                 []string
	contextOperations        []string
	actInputs                []nodes.BrowserActInput
	actPlanInputs            []json.RawMessage
	invocations              map[string]nodes.InvocationRecord
	currentURL               string
	currentOrigin            string
	elementRole              string
	elementName              string
	redactNextActObservation bool
	redactNextObservation    bool
	redactNextContext        bool
	dynamicContextCatalog    bool
}

func (*browserNodeTestHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (*browserNodeTestHandler) Close(context.Context) error { return nil }

func (handler *browserNodeTestHandler) WithPreparationAuthority(
	_ nodes.ID,
	_ string,
	command string,
	operation func(nodes.Registration, nodes.CommandApproval) error,
) (nodes.CommandApproval, error) {
	for _, descriptor := range handler.registration.Snapshot.Catalog.Commands {
		if descriptor.Name == command {
			approval := nodes.CommandApproval{Descriptor: descriptor}
			return approval, operation(handler.registration, approval)
		}
	}
	return nodes.CommandApproval{}, nodes.ErrCommandDenied
}

func (handler *browserNodeTestHandler) Invoke(
	_ context.Context,
	_ nodes.ID,
	plan nodes.ExecutionPlan,
	ephemeralInput json.RawMessage,
	commit func() error,
) (json.RawMessage, bool, error) {
	if err := commit(); err != nil {
		return nil, false, err
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.commands = append(handler.commands, plan.Command)
	var result any
	switch plan.Command {
	case nodes.BrowserCommandSessionOpen:
		var input nodes.BrowserSessionOpenInput
		if err := json.Unmarshal(plan.Input, &input); err != nil {
			return nil, true, err
		}
		result = nodes.BrowserSessionResult{
			SessionID: input.SessionID, State: "ready", TabID: "tab_primary", Controller: "agent",
			Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true, Contexts: true},
			ExpiresAt: time.Now().Add(time.Hour).Unix(), IdleExpiresAt: time.Now().Add(time.Minute).Unix(),
		}
	case nodes.BrowserCommandSessionStatus:
		var input nodes.BrowserSessionStatusInput
		_ = json.Unmarshal(plan.Input, &input)
		result = nodes.BrowserSessionResult{SessionID: input.SessionID, State: "ready"}
	case nodes.BrowserCommandObserve:
		var input nodes.BrowserObserveInput
		_ = json.Unmarshal(plan.Input, &input)
		url, origin := handler.currentURL, handler.currentOrigin
		if url == "" {
			url, origin = "about:blank", "about:blank"
		}
		observation := browserNodeTestObservation(input.SessionID, input.TabID, input.SnapshotGeneration, url, origin)
		if handler.elementRole != "" && len(observation.Elements) == 1 {
			observation.Elements[0].Role = handler.elementRole
			observation.Elements[0].Name = handler.elementName
			observation.Snapshot = "- " + handler.elementRole + " \"" + handler.elementName + "\" [ref=host_ref_1]"
		}
		result = observation
		if handler.redactNextObservation {
			result = nodes.BrowserObservationResult{ProtectedResult: true}
			handler.redactNextObservation = false
		}
	case nodes.BrowserCommandAct:
		var input nodes.BrowserActInput
		_ = json.Unmarshal(plan.Input, &input)
		handler.actPlanInputs = append(handler.actPlanInputs, append(json.RawMessage(nil), plan.Input...))
		if input.Action.Kind == "fill" || input.Action.Kind == "select" {
			var ephemeral struct {
				Value string `json:"value"`
			}
			_ = json.Unmarshal(ephemeralInput, &ephemeral)
			input.Action.Value = ephemeral.Value
		}
		handler.actInputs = append(handler.actInputs, input)
		if input.Action.Kind == "navigate" {
			handler.currentURL, handler.currentOrigin = "https://example.com/", "https://example.com"
		}
		observation := browserNodeTestObservation(
			input.SessionID, input.TabID, input.SnapshotGeneration+1,
			handler.currentURL, handler.currentOrigin,
		)
		if handler.elementRole != "" && len(observation.Elements) == 1 {
			observation.Elements[0].Role = handler.elementRole
			observation.Elements[0].Name = handler.elementName
			observation.Snapshot = "- " + handler.elementRole + " \"" + handler.elementName + "\" [ref=host_ref_1]"
		}
		result = nodes.BrowserActResult{
			ActionInvocationID: input.ActionInvocationID, State: "succeeded", Observation: &observation,
		}
		if handler.redactNextActObservation {
			result = nodes.BrowserActResult{
				ActionInvocationID: input.ActionInvocationID, State: "succeeded",
			}
			handler.redactNextActObservation = false
		}
	case nodes.BrowserCommandContexts:
		var input nodes.BrowserContextInput
		_ = json.Unmarshal(plan.Input, &input)
		handler.contextOperations = append(handler.contextOperations, input.Operation)
		catalog := browserNodeTestContextCatalog()
		if handler.dynamicContextCatalog && handler.currentURL != "" {
			catalog.Generation = 2
			catalog.Tabs[0].DocumentGeneration = 2
			catalog.Tabs[0].URL = handler.currentURL
			catalog.Tabs[0].Origin = handler.currentOrigin
		}
		result = nodes.BrowserContextResult{Operation: input.Operation, Catalog: catalog}
		if handler.redactNextContext {
			result = nodes.BrowserContextResult{
				Operation: input.Operation, ProtectedResult: true,
			}
			handler.redactNextContext = false
		}
	case nodes.BrowserCommandSessionClose:
		var input nodes.BrowserSessionStatusInput
		_ = json.Unmarshal(plan.Input, &input)
		result = nodes.BrowserSessionResult{SessionID: input.SessionID, State: "closed"}
	default:
		return nil, true, errors.New("unexpected command")
	}
	raw, err := json.Marshal(result)
	return raw, true, err
}

func browserNodeTestContextCatalog() nodes.BrowserContextCatalog {
	return nodes.BrowserContextCatalog{
		ID: "context_catalog_1", Generation: 1, SelectedTabID: "context_tab_1",
		Tabs: []nodes.BrowserTabContext{{
			ID: "context_tab_1", Kind: "primary", CreationSequence: 1,
			DocumentGeneration: 1, URL: "about:blank", Origin: "about:blank",
		}},
	}
}

func (handler *browserNodeTestHandler) Invocation(
	_ context.Context,
	_ nodes.ID,
	invocationID string,
) (nodes.InvocationRecord, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	record, ok := handler.invocations[invocationID]
	if !ok {
		return nodes.InvocationRecord{}, nodes.NewInvocationQueryError(nodes.InvocationQueryNotFound, nil)
	}
	return record, nil
}

func (*browserNodeTestHandler) CancelInvocation(
	context.Context,
	nodes.ID,
	string,
) (nodes.InvocationRecord, error) {
	return nodes.InvocationRecord{}, errors.New("cancellation unsupported")
}

func browserNodeTestObservation(
	sessionID, tabID string,
	generation uint64,
	url, origin string,
) nodes.BrowserObservationResult {
	title, snapshot := "Fixture", "- button \"Save\" [ref=host_ref_1]"
	elements := []nodes.BrowserElement{{Ref: "host_ref_1", Role: "button", Name: "Save"}}
	if url == "about:blank" {
		title, snapshot = "", ""
		elements = []nodes.BrowserElement{}
	}
	return nodes.BrowserObservationResult{
		SessionID: sessionID, TabID: tabID, SnapshotGeneration: generation,
		URL: url, Origin: origin, Title: title, Snapshot: snapshot,
		Elements: elements,
	}
}

func TestGatewayBrowserWorkerRoutesTypedLifecycleToCompanion(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_test", ExecutionID: "execution_test",
	}
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: "companion", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || initial.URL != "about:blank" || initial.SnapshotGeneration != 1 {
		t.Fatalf("initial observation = %#v, %v", initial, err)
	}
	preparation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_1", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate recovery from the companion's durable terminal receipt. The
	// receipt intentionally omits page data, so the worker must re-observe
	// without replaying the accepted navigation.
	handler.redactNextActObservation = true
	invocation, err := broker.ExecuteAction(t.Context(), owner, preparation.Action.ID, nil)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("ExecuteAction() = %#v, %v", invocation, err)
	}
	final, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || final.URL != "https://example.com/" || final.SnapshotGeneration != 2 {
		t.Fatalf("final observation = %#v, %v", final, err)
	}
	scroll, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_2", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: final.SnapshotID, SnapshotGeneration: final.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionScroll, Direction: "down", Amount: 2},
	})
	if err != nil || scroll.RequiresApproval || scroll.Action.Effect != browser.EffectRead {
		t.Fatalf("PrepareAction(scroll) = %#v, %v", scroll, err)
	}
	invocation, err = broker.ExecuteAction(t.Context(), owner, scroll.Action.ID, nil)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("ExecuteAction(scroll) = %#v, %v", invocation, err)
	}
	afterScroll, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || afterScroll.URL != "https://example.com/" || afterScroll.SnapshotGeneration != 3 {
		t.Fatalf("scroll observation = %#v, %v", afterScroll, err)
	}
	closed, err := broker.Close(t.Context(), owner, session.ID)
	if err != nil || closed.State != browser.SessionClosed {
		t.Fatalf("Close() = %#v, %v", closed, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandAct,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandAct,
		nodes.BrowserCommandSessionClose,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("companion commands = %#v, want %#v", commands, want)
	}
}

func TestGatewayBrowserWorkerRefreshesRecoveredObservationWithFreshInvocation(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
		Owner: browser.Owner{
			ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
			SessionKey: "session_test", ExecutionID: "execution_test",
		},
		SessionID: "browser_session_test", Target: "companion",
		Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := opened.Owner.(*nodeBrowserWorker)
	handler.redactNextObservation = true
	observation, err := worker.Observe(t.Context())
	if err != nil || observation.URL != "about:blank" {
		t.Fatalf("recovered observation = %#v, %v", observation, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandObserve,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestGatewayBrowserWorkerRefreshesRecoveredContextWithoutReplayingMutation(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
		Owner: browser.Owner{
			ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
			SessionKey: "session_test", ExecutionID: "execution_test",
		},
		SessionID: "browser_session_test", Target: "companion",
		Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := opened.Owner.(*nodeBrowserWorker)
	handler.redactNextContext = true
	catalog, err := worker.OpenTab(t.Context())
	if err != nil || catalog.ID != "context_catalog_1" {
		t.Fatalf("recovered context catalog = %#v, %v", catalog, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	operations := append([]string(nil), handler.contextOperations...)
	handler.mu.Unlock()
	want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandContexts,
		nodes.BrowserCommandContexts,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if wantOperations := []string{"open", "list"}; !slices.Equal(operations, wantOperations) {
		t.Fatalf("context operations = %#v, want %#v", operations, wantOperations)
	}
}

func TestGatewayBrowserWorkerInvalidatesCachedObservationWhenContextCatalogChanges(t *testing.T) {
	for _, test := range []struct {
		name                    string
		establishInitialCatalog bool
	}{
		{name: "changed catalog", establishInitialCatalog: true},
		{name: "first catalog refresh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, runtime, handler := browserNodeTestRuntime(t)
			handler.dynamicContextCatalog = true
			factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
				Owner: browser.Owner{
					ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
					SessionKey: "session_test", ExecutionID: "execution_test",
				},
				SessionID: "browser_session_test", Target: "companion",
				Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
			})
			if err != nil {
				t.Fatal(err)
			}
			worker := opened.Owner.(*nodeBrowserWorker)
			if test.establishInitialCatalog {
				if _, err = worker.ContextCatalog(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = worker.Observe(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = worker.ExecutePrepared(t.Context(), browser.WorkerPreparedAction{
				InvocationID: "invocation_navigate",
				Prepared: browser.PreparedAction{
					Action: browser.Action{Kind: browser.ActionNavigate},
					Effect: browser.EffectNavigation, CurrentOrigin: "about:blank",
					ActionHash: strings.Repeat("a", 64),
				},
				DriverAction: browser.DriverAction{Kind: browser.DriverNavigate, URL: "https://example.com/"},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = worker.ContextCatalog(t.Context()); err != nil {
				t.Fatal(err)
			}
			observation, err := worker.Observe(t.Context())
			if err != nil || observation.URL != "https://example.com/" {
				t.Fatalf("fresh post-navigation observation = %#v, %v", observation, err)
			}
			handler.mu.Lock()
			commands := append([]string(nil), handler.commands...)
			handler.mu.Unlock()
			want := []string{nodes.BrowserCommandSessionOpen}
			if test.establishInitialCatalog {
				want = append(want, nodes.BrowserCommandContexts)
			}
			want = append(
				want,
				nodes.BrowserCommandObserve,
				nodes.BrowserCommandAct,
				nodes.BrowserCommandContexts,
				nodes.BrowserCommandObserve,
			)
			if !slices.Equal(commands, want) {
				t.Fatalf("commands = %#v, want fresh remote observation %#v", commands, want)
			}
		})
	}
}

func TestGatewayBrowserWorkerRefreshesRecoveredSelectObservationWithoutReplay(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
		Owner: browser.Owner{
			ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
			SessionKey: "session_test", ExecutionID: "execution_test",
		},
		SessionID: "browser_session_test", Target: "companion",
		Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := opened.Owner.(*nodeBrowserWorker)
	catalog, err := gatewayBrowserContextCatalog(browserNodeTestContextCatalog())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := browser.ContextMutationAuthorityFromBinding(browser.ContextMutationBinding{
		Catalog: catalog, TabID: catalog.SelectedTabID,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.redactNextContext = true
	observation, recoveredCatalog, err := worker.SelectContext(t.Context(), authority)
	if err != nil || observation.URL != "about:blank" || recoveredCatalog.ID != catalog.ID {
		t.Fatalf("recovered select = %#v, %#v, %v", observation, recoveredCatalog, err)
	}
	handler.mu.Lock()
	operations := append([]string(nil), handler.contextOperations...)
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	if want := []string{"select", "list"}; !slices.Equal(operations, want) {
		t.Fatalf("context operations = %#v, want %#v", operations, want)
	}
	if want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandContexts,
		nodes.BrowserCommandContexts,
		nodes.BrowserCommandObserve,
	}; !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestGatewayBrowserWorkerRoutesApprovedTypedClickToCompanion(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	profile := cfg.Tools.Browser.Targets["companion"].Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target := cfg.Tools.Browser.Targets["companion"]
	target.Profiles["managed"] = profile
	cfg.Tools.Browser.Targets["companion"] = target
	registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		for index := range catalog.Commands {
			remote := &catalog.Commands[index].BrowserProfiles[0]
			remote.DryRun = false
			remote.AllowApprovedActions = true
			remote.Actions = []string{"click", "navigate", "scroll"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.registry.Approve(registration.Snapshot.ID, nodes.PairingApproval{
		Aliases:         []nodes.Alias{"ab-local-test"},
		AllowedCommands: registration.AllowedCommands,
		At:              registration.ApprovedAt + 1,
	}); err != nil {
		t.Fatal(err)
	}
	registration, found, err := runtime.registry.Registration(registration.Snapshot.ID)
	if err != nil || !found {
		t.Fatalf("approved click registration = %#v, %v, %v", registration, found, err)
	}
	handler.registration = registration
	handler.currentURL = "https://example.com/"
	handler.currentOrigin = "https://example.com"

	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := factory.(*gatewayBrowserWorkerFactory).PassiveTargetDiagnostics(
		t.Context(), "companion", []string{"managed"},
	)
	if err != nil || !slices.Equal(diagnostics.Actions, []browser.ActionKind{
		browser.ActionNavigate, browser.ActionClick, browser.ActionScroll,
	}) {
		t.Fatalf("click diagnostics = %#v, %v", diagnostics, err)
	}
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_test", ExecutionID: "execution_test",
	}
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: "companion", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	refStart := strings.Index(initial.Snapshot, "[ref=")
	refEnd := strings.Index(initial.Snapshot, "]")
	if err != nil || refStart < 0 || refEnd <= refStart+5 {
		t.Fatalf("initial observation = %#v, %v", initial, err)
	}
	visibleRef := initial.Snapshot[refStart+5 : refEnd]
	preparation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_click", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionClick, Ref: visibleRef},
	})
	if err != nil || !preparation.RequiresApproval || preparation.Action.Effect != browser.EffectExternalCommit {
		t.Fatalf("click preparation = %#v, %v", preparation, err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, preparation.Action.ID, &preparation.Approval)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("ExecuteAction(click) = %#v, %v", invocation, err)
	}
	final, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || final.SnapshotGeneration != 2 {
		t.Fatalf("post-click observation = %#v, %v", final, err)
	}
	handler.mu.Lock()
	inputs := append([]nodes.BrowserActInput(nil), handler.actInputs...)
	handler.mu.Unlock()
	if len(inputs) != 1 || inputs[0].Action.Kind != "click" || inputs[0].Action.Ref != "host_ref_1" ||
		inputs[0].ExpectedRole != "button" || inputs[0].ExpectedName != "Save" ||
		!nodes.BrowserApprovalDigestMatches(inputs[0]) {
		t.Fatalf("typed click inputs = %#v", inputs)
	}
}

func TestGatewayBrowserWorkerRoutesProtectedFillOnlyInEphemeralEnvelope(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	handler.elementRole, handler.elementName = "textbox", "Display name"
	registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		for index := range catalog.Commands {
			catalog.Commands[index].BrowserProfiles[0].Actions = []string{"fill", "navigate"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.registry.Approve(registration.Snapshot.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: registration.AllowedCommands,
		At: registration.ApprovedAt + 1,
	}); err != nil {
		t.Fatal(err)
	}
	registration, found, err := runtime.registry.Registration(registration.Snapshot.ID)
	if err != nil || !found {
		t.Fatalf("registration = %#v, %v, %v", registration, found, err)
	}
	handler.registration = registration

	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := factory.(*gatewayBrowserWorkerFactory).PassiveTargetDiagnostics(
		t.Context(), "companion", []string{"managed"},
	)
	if err != nil || !slices.Equal(
		diagnostics.Actions,
		[]browser.ActionKind{browser.ActionNavigate, browser.ActionFill},
	) {
		t.Fatalf("fill diagnostics = %#v, %v", diagnostics, err)
	}
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_test", ExecutionID: "execution_test",
	}
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: "companion", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	navigate, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_fill_navigate", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.ExecuteAction(t.Context(), owner, navigate.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	form, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	refStart := strings.Index(form.Snapshot, "[ref=")
	refEnd := strings.Index(form.Snapshot, "]")
	if err != nil || refStart < 0 || refEnd <= refStart+5 {
		t.Fatalf("form observation = %#v, %v", form, err)
	}
	visibleRef := form.Snapshot[refStart+5 : refEnd]
	secret := "gateway-companion-fill-canary"
	fill, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_protected_fill", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: form.SnapshotID, SnapshotGeneration: form.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionFill, Ref: visibleRef, Value: secret},
	})
	if err != nil || fill.RequiresApproval || fill.Action.Effect != browser.EffectLocalEdit {
		t.Fatalf("fill preparation = %#v, %v", fill, err)
	}
	if _, err = broker.ExecuteAction(t.Context(), owner, fill.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	handler.mu.Lock()
	inputs := append([]nodes.BrowserActInput(nil), handler.actInputs...)
	plans := append([]json.RawMessage(nil), handler.actPlanInputs...)
	handler.mu.Unlock()
	if len(inputs) != 2 || inputs[1].Action.Kind != "fill" || inputs[1].Action.Value != secret ||
		inputs[1].ExpectedRole != "textbox" || inputs[1].ExpectedName != "Display name" ||
		!nodes.BrowserInputDigestMatches(inputs[1].InputDigest, secret) || inputs[1].InputBytes != len(secret) {
		t.Fatalf("protected fill inputs = %#v", inputs)
	}
	if len(plans) != 2 || bytes.Contains(plans[1], []byte(secret)) {
		t.Fatalf("durable protected fill plan = %s", plans[1])
	}
}

func TestGatewayBrowserWorkerReadinessRequiresAllApprovedTypedCommands(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	readiness := factory.(*gatewayBrowserWorkerFactory).PassiveTargetReadiness(
		t.Context(), "companion", "managed",
	)
	if readiness.Status != browser.ReadinessReady {
		t.Fatalf("ready diagnostics = %#v", readiness)
	}
	diagnostics, err := factory.(*gatewayBrowserWorkerFactory).PassiveTargetDiagnostics(
		t.Context(), "companion", []string{"managed"},
	)
	if err != nil || !slices.Equal(
		diagnostics.Actions, []browser.ActionKind{browser.ActionNavigate, browser.ActionScroll},
	) || diagnostics.Profiles["managed"].Status != browser.ReadinessReady {
		t.Fatalf("target diagnostics = %#v, %v", diagnostics, err)
	}
	handler.registration.AllowedCommands = handler.registration.AllowedCommands[1:]
	if _, err = runtime.registry.Approve(handler.registration.Snapshot.ID, nodes.PairingApproval{
		Aliases:         []nodes.Alias{"ab-local-test"},
		AllowedCommands: handler.registration.AllowedCommands,
		At:              time.Now().Add(time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	readiness = factory.(*gatewayBrowserWorkerFactory).PassiveTargetReadiness(
		t.Context(), "companion", "managed",
	)
	if readiness.Status != browser.ReadinessUnavailable || readiness.Code != "command_unapproved" {
		t.Fatalf("unapproved diagnostics = %#v", readiness)
	}
	if diagnostics, err = factory.(*gatewayBrowserWorkerFactory).PassiveTargetDiagnostics(
		t.Context(), "companion", []string{"managed"},
	); err != nil || len(diagnostics.Actions) != 0 ||
		diagnostics.Profiles["managed"].Code != "command_unapproved" {
		t.Fatalf("unapproved target diagnostics = %#v, %v", diagnostics, err)
	}
}

func TestGatewayBrowserWorkerReadinessRejectsMismatchedCommandProfile(t *testing.T) {
	cfg, runtime, _ := browserNodeTestRuntime(t)
	registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		catalog.Commands[0].BrowserProfiles[0].Limits.SnapshotRefs--
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.registry.Approve(registration.Snapshot.ID, nodes.PairingApproval{
		Aliases:         []nodes.Alias{"ab-local-test"},
		AllowedCommands: registration.AllowedCommands,
		At:              registration.ApprovedAt + 1,
	}); err != nil {
		t.Fatal(err)
	}
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	readiness := factory.(*gatewayBrowserWorkerFactory).PassiveTargetReadiness(
		t.Context(), "companion", "managed",
	)
	if readiness.Status != browser.ReadinessUnavailable ||
		readiness.Code != "profile_policy_mismatch" {
		t.Fatalf("mismatched profile diagnostics = %#v", readiness)
	}
}

func TestGatewayBrowserWorkerPinsCompleteProfileAcrossActiveCommands(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		for index := range catalog.Commands {
			if catalog.Commands[index].Name != nodes.BrowserCommandAct {
				continue
			}
			catalog.Commands[index].BrowserProfiles[0].DryRun = false
			catalog.Commands[index].BrowserProfiles[0].AllowApprovedActions = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.registry.Approve(registration.Snapshot.ID, nodes.PairingApproval{
		Aliases:         []nodes.Alias{"ab-local-test"},
		AllowedCommands: registration.AllowedCommands,
		At:              registration.ApprovedAt + 1,
	}); err != nil {
		t.Fatal(err)
	}
	registration, found, err := runtime.registry.Registration(registration.Snapshot.ID)
	if err != nil || !found {
		t.Fatalf("mixed-mode registration = %#v, %v, %v", registration, found, err)
	}
	handler.registration = registration

	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
		Owner: browser.Owner{
			ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
			SessionKey: "session_test", ExecutionID: "execution_test",
		},
		SessionID: "browser_session_test", Target: "companion",
		Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, ok := opened.Owner.(*nodeBrowserWorker)
	if !ok {
		t.Fatalf("worker = %T", opened.Owner)
	}
	if _, _, err = worker.resolveAuthority(nodes.BrowserCommandAct); !errors.Is(err, browser.ErrDenied) {
		t.Fatalf("mixed-mode act authority error = %v, want denied", err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	if want := []string{nodes.BrowserCommandSessionOpen}; !slices.Equal(commands, want) {
		t.Fatalf("mixed-mode commands = %#v, want %#v", commands, want)
	}
}

func TestGatewayBrowserWorkerPinsSessionToResolvedNodeAuthority(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_test", ExecutionID: "execution_test",
	}
	opened, err := factory.Open(t.Context(), browser.WorkerOpenRequest{
		Owner: owner, SessionID: "browser_session_test", Target: "companion",
		Profile: "managed", DryRun: true, Limits: cfg.Tools.Browser.Limits.Effective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, ok := opened.Owner.(*nodeBrowserWorker)
	if !ok {
		t.Fatalf("worker = %T", opened.Owner)
	}
	first, found, err := runtime.registry.Resolve("ab-local-test")
	if err != nil || !found {
		t.Fatalf("resolve first node = %#v, %v, %v", first, found, err)
	}
	registration, found, err := runtime.registry.Registration(first.ID)
	if err != nil || !found {
		t.Fatalf("first registration = %#v, %v, %v", registration, found, err)
	}
	if _, err = runtime.registry.Approve(first.ID, nodes.PairingApproval{
		AllowedCommands: registration.AllowedCommands, At: registration.ApprovedAt + 1,
	}); err != nil {
		t.Fatal(err)
	}
	secondID := browserNodeTestRegisterReplacement(t, runtime, registration)
	if _, err = worker.Observe(t.Context()); !errors.Is(err, browser.ErrDenied) {
		t.Fatalf("Observe() after alias rebound to %s error = %v, want denied", secondID, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	if want := []string{nodes.BrowserCommandSessionOpen}; !slices.Equal(commands, want) {
		t.Fatalf("companion commands after alias rebound = %#v, want %#v", commands, want)
	}
}

func browserNodeTestMutateCatalog(
	t *testing.T,
	runtime *nodeAdmissionRuntime,
	mutate func(*nodes.CapabilityCatalog),
) (nodes.Registration, error) {
	t.Helper()
	snapshot, found, err := runtime.registry.Resolve("ab-local-test")
	if err != nil || !found {
		return nodes.Registration{}, fmt.Errorf("resolve fixture node: found=%v: %w", found, err)
	}
	registration, found, err := runtime.registry.Registration(snapshot.ID)
	if err != nil || !found {
		return nodes.Registration{}, fmt.Errorf("load fixture registration: found=%v: %w", found, err)
	}
	mutate(&snapshot.Catalog)
	for index, command := range snapshot.Catalog.Commands {
		descriptors, descriptorErr := nodes.BrowserCommandDescriptors(command.BrowserProfiles)
		if descriptorErr != nil {
			return nodes.Registration{}, descriptorErr
		}
		for _, descriptor := range descriptors {
			if descriptor.Name == command.Name {
				snapshot.Catalog.Commands[index] = descriptor
				break
			}
		}
	}
	snapshot.CatalogHash, err = snapshot.Catalog.Hash()
	if err != nil {
		return nodes.Registration{}, err
	}
	if err = runtime.registry.Upsert(snapshot); err != nil {
		return nodes.Registration{}, err
	}
	return registration, nil
}

func browserNodeTestRegisterReplacement(
	t *testing.T,
	runtime *nodeAdmissionRuntime,
	registration nodes.Registration,
) nodes.ID {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := nodes.DeriveID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registration.Snapshot
	snapshot.ID = nodeID
	snapshot.State = nodes.StatePendingPairing
	snapshot.Aliases = nil
	snapshot.LastSeenAt = time.Now().Unix()
	if err = runtime.registry.UpsertPending(nodes.PendingPairing{
		Node: snapshot, PublicKey: publicKey, RequestedRole: "companion",
		RequestedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.registry.Approve(nodeID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: registration.AllowedCommands,
		At: time.Now().Add(time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot.State = nodes.StateConnected
	if err = runtime.registry.Upsert(snapshot); err != nil {
		t.Fatal(err)
	}
	release, err := runtime.sessions.Claim(nodeID, &testNodeConnection{}, nil, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = release() })
	return nodeID
}

func browserNodeTestRuntime(
	t *testing.T,
) (*config.Config, *nodeAdmissionRuntime, *browserNodeTestHandler) {
	t.Helper()
	workspace := t.TempDir()
	profiles := []nodes.BrowserProfileDescriptor{{
		Alias: "managed", Revision: "managed-v1", Driver: nodes.BrowserDriverPlaywrightMCP,
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		DryRun: true, Actions: []string{"navigate", "scroll"}, Limits: nodes.BrowserLimits{}.Effective(),
	}}
	descriptors, err := nodes.BrowserCommandDescriptors(profiles)
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := nodes.DeriveID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := nodes.RegistryPath(workspace)
	registry, err := nodes.NewFileRegistry(registryPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID: nodeID, State: nodes.StatePendingPairing, ProtocolVersion: nodes.ProtocolV1,
		Platform: "darwin", Architecture: "amd64", SoftwareVersion: "test",
		CatalogHash: catalogHash, Catalog: catalog, Executor: "local", PolicyRevision: "policy-v1",
		LastSeenAt: time.Now().Unix(),
	}
	if err = registry.UpsertPending(nodes.PendingPairing{
		Node: snapshot, PublicKey: publicKey, RequestedRole: "companion", RequestedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		commands[index] = descriptor.Name
	}
	if _, err = registry.Approve(nodeID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot.State = nodes.StateConnected
	if err = registry.Upsert(snapshot); err != nil {
		t.Fatal(err)
	}
	registration, found, err := registry.Registration(nodeID)
	if err != nil || !found {
		t.Fatalf("registration = %#v, %v, %v", registration, found, err)
	}
	sessions := nodews.NewSessionHub()
	release, err := sessions.Claim(nodeID, &testNodeConnection{}, nil, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = release() })
	handler := &browserNodeTestHandler{
		registration: registration, invocations: make(map[string]nodes.InvocationRecord),
	}
	runtime := &nodeAdmissionRuntime{
		registry: registry, registryPath: registryPath, handler: handler,
		sessions: sessions, generation: 1, mounted: true,
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"ab-local-test": {Type: "node", Node: "ab-local-test", Executor: "local"},
	}
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true, Agents: []string{"browser"},
		Targets: map[string]config.BrowserTargetConfig{
			"companion": {
				Enabled: true, Placement: config.BrowserPlacementNode, NodeTarget: "ab-local-test",
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {
						Enabled: true, Mode: config.BrowserProfileManaged,
						NetworkMode: config.BrowserNetworkAnyHTTP, DryRun: true,
					},
				},
			},
		},
	}
	if err = cfg.ValidateBrowserConfig(); err != nil {
		t.Fatal(err)
	}
	return cfg, runtime, handler
}

func TestBrowserProfileIntersectionRequiresExactActionMode(t *testing.T) {
	limits := config.BrowserLimitsConfig{}
	remote := nodes.BrowserProfileDescriptor{
		Alias: "managed", Revision: "managed-v1", Driver: nodes.BrowserDriverPlaywrightMCP,
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		AllowApprovedActions: true, Actions: []string{"navigate"}, Limits: nodes.BrowserLimits{}.Effective(),
	}
	local := config.BrowserProfileConfig{
		Enabled: true, Mode: config.BrowserProfileManaged,
		NetworkMode: config.BrowserNetworkAnyHTTP, AllowApprovedActions: true,
	}
	if !browserProfileIntersects(local, limits, remote) {
		t.Fatal("matching approved-action profiles did not intersect")
	}
	local.DryRun = true
	local.AllowApprovedActions = false
	if browserProfileIntersects(local, limits, remote) {
		t.Fatal("mismatched action modes intersected")
	}
}

func TestBrowserInvocationDispatchDeniedPreservesTypedClassification(t *testing.T) {
	if !browserInvocationDispatchDenied(nodes.NewInvocationDispatchError(
		nodes.InvocationDispatchCommandDenied,
		errors.New("private remote detail"),
	)) {
		t.Fatal("typed command denial was not preserved")
	}
	if browserInvocationDispatchDenied(nodes.NewInvocationDispatchError(
		nodes.InvocationDispatchExecutionFailed,
		errors.New("private remote detail"),
	)) || browserInvocationDispatchDenied(errors.New("untyped transport failure")) {
		t.Fatal("non-policy dispatch failure was classified as a denial")
	}
}
