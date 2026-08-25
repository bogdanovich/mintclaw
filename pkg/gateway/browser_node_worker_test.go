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
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
)

type browserNodeTestHandler struct {
	mu                       sync.Mutex
	registration             nodes.Registration
	commands                 []string
	contextOperations        []string
	observeInputs            []nodes.BrowserObserveInput
	actInputs                []nodes.BrowserActInput
	actPlanInputs            []json.RawMessage
	invocations              map[string]nodes.InvocationRecord
	currentURL               string
	currentOrigin            string
	elementRole              string
	elementName              string
	pendingDialog            *nodes.BrowserDialogObservation
	redactNextActObservation bool
	redactNextObservation    bool
	staleNextObservation     bool
	redactNextContext        bool
	redactNextDiagnostics    bool
	dynamicContextCatalog    bool
	closeFailureCode         string
}

type browserNodeRecordingFactory struct {
	browser.WorkerFactory
	worker *nodeBrowserWorker
}

func (factory *browserNodeRecordingFactory) Open(
	ctx context.Context,
	request browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	opened, err := factory.WorkerFactory.Open(ctx, request)
	if err == nil {
		factory.worker, _ = opened.Owner.(*nodeBrowserWorker)
	}
	return opened, err
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
			Features: nodes.BrowserHostFeatures{
				Observe: true, Navigate: true, Contexts: true, Download: true, Diagnostics: true,
			},
			ExpiresAt: time.Now().Add(time.Hour).Unix(), IdleExpiresAt: time.Now().Add(time.Minute).Unix(),
		}
	case nodes.BrowserCommandSessionStatus:
		var input nodes.BrowserSessionStatusInput
		_ = json.Unmarshal(plan.Input, &input)
		result = nodes.BrowserSessionResult{SessionID: input.SessionID, State: "ready"}
	case nodes.BrowserCommandObserve:
		var input nodes.BrowserObserveInput
		_ = json.Unmarshal(plan.Input, &input)
		handler.observeInputs = append(handler.observeInputs, input)
		if handler.staleNextObservation {
			handler.staleNextObservation = false
			now := time.Now().UnixNano()
			handler.invocations[plan.InvocationID] = nodes.InvocationRecord{
				InvocationID: plan.InvocationID, IdempotencyKey: plan.IdempotencyKey,
				PlanHash: plan.PlanHash, NodeID: plan.NodeID, CatalogHash: plan.CatalogHash,
				Command: plan.Command, Risk: plan.Risk, State: nodes.InvocationFailed,
				AcceptedAt: now, UpdatedAt: now, CompletedAt: now, ExpiresAt: plan.ExpiresAt,
				Failure: &nodes.InvocationFailure{
					Code: "STALE_BROWSER_STATE", Message: "browser state is stale",
				},
			}
			return nil, true, nodes.NewInvocationDispatchError(
				"STALE_BROWSER_STATE",
				errors.New("transient private browser authority changed"),
			)
		}
		url, origin := handler.currentURL, handler.currentOrigin
		if url == "" {
			url, origin = "about:blank", "about:blank"
		}
		observation := browserNodeTestObservation(input.SessionID, input.TabID, input.SnapshotGeneration, url, origin)
		observation.DocumentID = browserNodeStableID("document", input.SessionID, url)
		if handler.elementRole != "" && len(observation.Elements) == 1 {
			observation.Elements[0].Role = handler.elementRole
			observation.Elements[0].Name = handler.elementName
			observation.Snapshot = "- " + handler.elementRole + " \"" + handler.elementName + "\" [ref=host_ref_1]"
		}
		observation.PendingDialog = handler.pendingDialog
		result = observation
		if handler.redactNextObservation {
			result = nodes.BrowserObservationResult{ProtectedResult: true}
			handler.redactNextObservation = false
		}
	case nodes.BrowserCommandDiagnostics:
		var input nodes.BrowserDiagnosticsInput
		_ = json.Unmarshal(plan.Input, &input)
		categories := make([]nodes.BrowserDiagnosticCategory, len(input.Categories))
		for index, category := range input.Categories {
			categories[index] = nodes.BrowserDiagnosticCategory{
				Category: category, Entries: []nodes.BrowserDiagnosticEntry{},
			}
			if category == "console_errors" {
				categories[index].Count = 1
				categories[index].Entries = []nodes.BrowserDiagnosticEntry{{
					Timestamp: 1, Severity: "error", Origin: "https://example.com", Path: "/safe",
					MessageHash: strings.Repeat("a", 64),
				}}
			}
		}
		result = nodes.BrowserDiagnosticsResult{
			SessionID: input.SessionID, TabID: input.TabID,
			SnapshotGeneration: input.SnapshotGeneration, Categories: categories,
		}
		if handler.redactNextDiagnostics {
			result = nodes.BrowserDiagnosticsResult{ProtectedResult: true}
			handler.redactNextDiagnostics = false
		}
	case nodes.BrowserCommandCapture:
		// The focused worker tests only need to prove that capture passes local
		// generation preflight and reaches companion dispatch. An empty output
		// descriptor then fails closed before any transfer is attempted.
		result = nodes.BrowserOutputDescriptor{}
	case nodes.BrowserCommandAct:
		var input nodes.BrowserActInput
		_ = json.Unmarshal(plan.Input, &input)
		handler.actPlanInputs = append(handler.actPlanInputs, append(json.RawMessage(nil), plan.Input...))
		if input.Action.Kind == "fill" || input.Action.Kind == "select" ||
			(input.Action.Kind == "dialog" && input.Action.PromptProvided) {
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
		if input.Action.Kind == "dialog" {
			handler.pendingDialog = nil
		}
		observation := browserNodeTestObservation(
			input.SessionID, input.TabID, input.SnapshotGeneration+1,
			handler.currentURL, handler.currentOrigin,
		)
		observation.DocumentID = browserNodeStableID("document", input.SessionID, handler.currentURL)
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
		if handler.closeFailureCode != "" {
			return nil, true, nodes.NewInvocationDispatchError(
				handler.closeFailureCode,
				errors.New("private companion failure"),
			)
		}
		var input nodes.BrowserSessionStatusInput
		_ = json.Unmarshal(plan.Input, &input)
		result = nodes.BrowserSessionResult{SessionID: input.SessionID, State: "closed"}
	default:
		return nil, true, errors.New("unexpected command")
	}
	raw, err := json.Marshal(result)
	return raw, true, err
}

func TestGatewayBrowserWorkerCloseAcceptsConfirmedMissingCompanionSession(t *testing.T) {
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
	handler.mu.Lock()
	handler.closeFailureCode = nodes.InvocationDispatchBrowserSessionNotFound
	handler.mu.Unlock()
	worker := opened.Owner.(*nodeBrowserWorker)
	if err = worker.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	status, err := worker.Status(t.Context())
	if err != nil || status != browser.WorkerLost {
		t.Fatalf("Status() = %q, %v", status, err)
	}
}

func TestBrowserInvocationSessionNotFoundRequiresTypedClassification(t *testing.T) {
	if !browserInvocationSessionNotFound(nodes.NewInvocationDispatchError(
		nodes.InvocationDispatchBrowserSessionNotFound,
		errors.New("private remote detail"),
	)) {
		t.Fatal("typed missing browser session was not preserved")
	}
	if browserInvocationSessionNotFound(nodes.NewInvocationDispatchError(
		nodes.InvocationDispatchExecutionFailed,
		errors.New("private remote detail"),
	)) || browserInvocationSessionNotFound(errors.New("untyped transport failure")) {
		t.Fatal("non-terminal companion failure was classified as a missing session")
	}
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

func TestGatewayBrowserWorkerKeepsCaptureAuthorityAfterPrivateScrollValidation(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	baseFactory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	factory := &browserNodeRecordingFactory{WorkerFactory: baseFactory}
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
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	navigate, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_navigate", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.ExecuteAction(t.Context(), owner, navigate.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	observation, err = broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_scroll", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionScroll, Direction: "down", Amount: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if factory.worker == nil {
		t.Fatal("node browser worker was not recorded")
	}
	factory.worker.mu.Lock()
	remoteGeneration := factory.worker.snapshotGeneration
	publicGeneration := factory.worker.publicSnapshotGeneration
	factory.worker.mu.Unlock()
	if remoteGeneration != 3 || publicGeneration != 2 {
		t.Fatalf(
			"post-validation generations remote=%d public=%d, want remote=3 public=2",
			remoteGeneration,
			publicGeneration,
		)
	}
	captureOwner := nodes.TransferArtifactOwner{
		WorkspaceID: "workspace_capture", AgentID: "agent_capture", ActorID: "actor_capture",
		RouteID: "route_capture", SessionID: session.ID, ToolCallID: "capture_after_scroll_validation",
	}
	_, captureErr := broker.CaptureScreenshot(t.Context(), browser.ScreenshotRequest{
		Owner: owner, RequestID: captureOwner.ToolCallID, SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Target: browser.ScreenshotTargetPage,
		Retention: &browser.ScreenshotRetentionAuthority{
			WorkspaceID: captureOwner.WorkspaceID, AgentID: captureOwner.AgentID,
			ActorID: captureOwner.ActorID, RouteID: captureOwner.RouteID,
			SessionID: captureOwner.SessionID, ToolCallID: captureOwner.ToolCallID,
		},
	})
	if captureErr == nil || errors.Is(captureErr, browser.ErrStale) {
		t.Fatalf("capture after private scroll validation error = %v, want dispatched non-stale failure", captureErr)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	if !slices.Contains(commands, nodes.BrowserCommandCapture) {
		t.Fatalf("capture did not reach companion dispatch: %#v", commands)
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

func TestGatewayBrowserWorkerRetriesTransientStaleObservationWithFreshInvocation(t *testing.T) {
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
	handler.staleNextObservation = true
	observation, err := worker.Observe(t.Context())
	if err != nil || observation.URL != "about:blank" {
		t.Fatalf("recovered observation = %#v, %v", observation, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	observeInputs := append([]nodes.BrowserObserveInput(nil), handler.observeInputs...)
	handler.mu.Unlock()
	want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandObserve,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if len(observeInputs) != 2 || observeInputs[0].SnapshotGeneration != 1 ||
		observeInputs[1].SnapshotGeneration != observeInputs[0].SnapshotGeneration {
		t.Fatalf("observe inputs = %#v, want two reads at generation 1", observeInputs)
	}
}

func TestGatewayBrowserWorkerRefreshesProtectedDiagnosticsWithFreshInvocation(t *testing.T) {
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
	handler.redactNextDiagnostics = true
	summary, err := worker.Diagnostics(t.Context(), []browser.DiagnosticCategory{browser.DiagnosticConsoleErrors})
	if err != nil || len(summary.Categories) != 1 || summary.Categories[0].Entries[0].Path != "/safe" {
		t.Fatalf("recovered diagnostics = %#v, %v", summary, err)
	}
	handler.mu.Lock()
	commands := append([]string(nil), handler.commands...)
	handler.mu.Unlock()
	want := []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandDiagnostics,
		nodes.BrowserCommandDiagnostics,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestGatewayBrowserWorkerAdvancesAfterDownloadWithoutOutput(t *testing.T) {
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
	if _, err = worker.Observe(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler.redactNextActObservation = true
	request := browser.WorkerPreparedAction{
		InvocationID: "invocation_download",
		Prepared: browser.PreparedAction{
			ID: "prepared_download", RequestID: "request_download",
			SessionID: worker.sessionID, TabID: worker.tabID,
			Action: browser.Action{Kind: browser.ActionDownload, Ref: "host_ref_1"},
			Effect: browser.EffectUnknown, CurrentOrigin: "https://example.com",
			ActionHash: strings.Repeat("a", 64), ElementRole: "button", ElementName: "Save",
		},
		Action:       browser.Action{Kind: browser.ActionDownload, Ref: "host_ref_1"},
		DriverAction: browser.DriverAction{Kind: browser.DriverDownloadAction, Target: "host_ref_1"},
	}
	ctx := gatewayBrowserArtifactContext(cfg.WorkspacePath())
	var artifactErr *browser.DownloadArtifactError
	if _, downloadErr := worker.DownloadPrepared(
		ctx, request, int64(cfg.Tools.Browser.Limits.Effective().DownloadBytes),
	); !errors.As(downloadErr, &artifactErr) {
		t.Fatalf("DownloadPrepared() error = %v, want artifact unavailable", downloadErr)
	}
	worker.mu.Lock()
	generation := worker.snapshotGeneration
	worker.mu.Unlock()
	if generation != 2 {
		t.Fatalf("post-download generation = %d, want 2", generation)
	}
	if _, err = worker.Observe(t.Context()); err != nil {
		t.Fatalf("Observe() after unavailable download error = %v", err)
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
			worker.mu.Lock()
			initialPublicGeneration := worker.publicSnapshotGeneration
			worker.mu.Unlock()
			if initialPublicGeneration != 1 {
				t.Fatalf("initial public generation = %d, want 1", initialPublicGeneration)
			}
			if err = worker.ExecutePrepared(t.Context(), browser.WorkerPreparedAction{
				InvocationID: "invocation_navigate",
				Action:       browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
				Prepared: browser.PreparedAction{
					Action: browser.Action{Kind: browser.ActionNavigate},
					Effect: browser.EffectNavigation, CurrentOrigin: "about:blank",
					ActionHash: strings.Repeat("a", 64),
				},
				DriverAction: browser.DriverAction{Kind: browser.DriverNavigate, URL: "https://example.com/"},
			}); err != nil {
				t.Fatal(err)
			}
			worker.mu.Lock()
			postActionPublicGeneration := worker.publicSnapshotGeneration
			worker.mu.Unlock()
			if postActionPublicGeneration != 1 {
				t.Fatalf(
					"private action observation advanced public generation to %d, want 1",
					postActionPublicGeneration,
				)
			}
			if _, err = worker.ContextCatalog(t.Context()); err != nil {
				t.Fatal(err)
			}
			observation, err := worker.Observe(t.Context())
			if err != nil || observation.URL != "https://example.com/" {
				t.Fatalf("fresh post-navigation observation = %#v, %v", observation, err)
			}
			worker.mu.Lock()
			finalPublicGeneration := worker.publicSnapshotGeneration
			worker.mu.Unlock()
			if finalPublicGeneration != 2 {
				t.Fatalf(
					"fresh broker-visible observation public generation = %d, want 2",
					finalPublicGeneration,
				)
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
		Action: browser.Action{Kind: browser.ActionClick, Ref: visibleRef}, DeclaredEffect: browser.EffectNavigation,
	})
	if err != nil || preparation.RequiresApproval || preparation.Action.Effect != browser.EffectNavigation {
		t.Fatalf("click preparation = %#v, %v", preparation, err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, preparation.Action.ID, nil)
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
		inputs[0].Effect != "navigation" || inputs[0].ApprovalDigest != "" {
		t.Fatalf("typed click inputs = %#v", inputs)
	}
}

func TestGatewayBrowserWorkerRoutesTypedCheckUncheckAndHover(t *testing.T) {
	for _, test := range []struct {
		kind, role, effect string
	}{
		{kind: "check", role: "radio", effect: "local_edit"},
		{kind: "uncheck", role: "checkbox", effect: "local_edit"},
		{kind: "hover", role: "button", effect: "read"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			cfg, runtime, handler := browserNodeTestRuntime(t)
			handler.elementRole, handler.elementName = test.role, "Control"
			registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
				for index := range catalog.Commands {
					catalog.Commands[index].BrowserProfiles[0].Actions = []string{
						"check", "hover", "navigate", "uncheck",
					}
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
			if err != nil || !slices.Equal(diagnostics.Actions, []browser.ActionKind{
				browser.ActionNavigate, browser.ActionCheck, browser.ActionUncheck, browser.ActionHover,
			}) {
				t.Fatalf("ordinary interaction diagnostics = %#v, %v", diagnostics, err)
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
				Owner: owner, RequestID: "request_ordinary_navigate", SessionID: session.ID, TabID: session.TabID,
				SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
				Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = broker.ExecuteAction(t.Context(), owner, navigate.Action.ID, nil); err != nil {
				t.Fatal(err)
			}
			observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			refStart := strings.Index(observation.Snapshot, "[ref=")
			refEnd := strings.Index(observation.Snapshot, "]")
			if err != nil || refStart < 0 || refEnd <= refStart+5 {
				t.Fatalf("%s observation = %#v, %v", test.kind, observation, err)
			}
			preparation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
				Owner: owner, RequestID: "request_" + test.kind, SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
				Action: browser.Action{
					Kind: browser.ActionKind(test.kind), Ref: observation.Snapshot[refStart+5 : refEnd],
				},
			})
			if err != nil || preparation.RequiresApproval {
				t.Fatalf("%s preparation = %#v, %v", test.kind, preparation, err)
			}
			if _, err = broker.ExecuteAction(t.Context(), owner, preparation.Action.ID, nil); err != nil {
				t.Fatalf("%s execution error = %v", test.kind, err)
			}
			handler.mu.Lock()
			inputs := append([]nodes.BrowserActInput(nil), handler.actInputs...)
			handler.mu.Unlock()
			input := inputs[len(inputs)-1]
			if input.Action.Kind != browser.ActionKind(test.kind) || input.Action.Ref != "host_ref_1" ||
				input.ExpectedRole != test.role || input.ExpectedName != "Control" || input.Effect != test.effect ||
				input.ApprovalDigest != "" || input.InputDigest != "" || input.InputBytes != 0 {
				t.Fatalf("typed %s input = %#v", test.kind, input)
			}
		})
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

func TestGatewayBrowserWorkerRoutesProtectedDialogOnlyInEphemeralEnvelope(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	handler.currentURL, handler.currentOrigin = "https://example.com/form", "https://example.com"
	handler.pendingDialog = &nodes.BrowserDialogObservation{Type: "prompt", Message: "Type confirmation"}
	profile := cfg.Tools.Browser.Targets["companion"].Profiles["managed"]
	profile.DryRun = false
	profile.AllowApprovedActions = true
	target := cfg.Tools.Browser.Targets["companion"]
	target.Profiles["managed"] = profile
	cfg.Tools.Browser.Targets["companion"] = target
	registration, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		for index := range catalog.Commands {
			catalog.Commands[index].BrowserProfiles[0].Actions = []string{"dialog", "navigate"}
			catalog.Commands[index].BrowserProfiles[0].DryRun = false
			catalog.Commands[index].BrowserProfiles[0].AllowApprovedActions = true
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
		[]browser.ActionKind{browser.ActionNavigate, browser.ActionDialog},
	) {
		t.Fatalf("dialog diagnostics = %#v, %v", diagnostics, err)
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
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil || observation.PendingDialog == nil || observation.PendingDialog.ID == "" {
		t.Fatalf("dialog observation = %#v, %v", observation, err)
	}
	secret := "gateway-companion-dialog-canary"
	preparation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "request_protected_dialog", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{
			Kind: browser.ActionDialog, DialogID: observation.PendingDialog.ID,
			Decision: "accept", Value: secret, PromptProvided: true,
		},
	})
	if err != nil || !preparation.RequiresApproval || preparation.Action.Effect != browser.EffectExternalCommit {
		t.Fatalf("dialog preparation = %#v, %v", preparation, err)
	}
	if _, err = broker.ExecuteAction(
		t.Context(), owner, preparation.Action.ID, &preparation.Approval,
	); err != nil {
		t.Fatal(err)
	}
	handler.mu.Lock()
	inputs := append([]nodes.BrowserActInput(nil), handler.actInputs...)
	plans := append([]json.RawMessage(nil), handler.actPlanInputs...)
	handler.mu.Unlock()
	if len(inputs) != 1 || inputs[0].Action.Kind != "dialog" || inputs[0].Action.Value != secret ||
		inputs[0].Action.DialogID != observation.PendingDialog.ID || !inputs[0].Action.PromptProvided ||
		inputs[0].DialogType != "prompt" || inputs[0].DialogMessageBytes != len("Type confirmation") ||
		!nodes.BrowserDialogMessageDigestMatches(
			inputs[0].DialogMessageDigest,
			"prompt",
			"Type confirmation",
		) || !nodes.BrowserInputDigestMatches(inputs[0].InputDigest, secret) ||
		!nodes.BrowserApprovalDigestMatches(func() nodes.BrowserActInput {
			candidate := inputs[0]
			candidate.Action.Value = ""
			return candidate
		}()) {
		t.Fatalf("protected dialog inputs = %#v", inputs)
	}
	if len(plans) != 1 || bytes.Contains(plans[0], []byte(secret)) {
		t.Fatalf("durable protected dialog plan = %s", plans[0])
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
		diagnostics.Actions, []browser.ActionKind{
			browser.ActionNavigate, browser.ActionDownload, browser.ActionScroll,
		},
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

func TestGatewayBrowserDiagnosticsCapabilityIsIndependentFromCoreReadiness(t *testing.T) {
	cfg, runtime, handler := browserNodeTestRuntime(t)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	allowed := make([]string, 0, len(handler.registration.AllowedCommands)-1)
	for _, command := range handler.registration.AllowedCommands {
		if command != nodes.BrowserCommandDiagnostics {
			allowed = append(allowed, command)
		}
	}
	if _, err = runtime.registry.Approve(handler.registration.Snapshot.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: allowed,
		At: time.Now().Add(time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := factory.(*gatewayBrowserWorkerFactory).PassiveTargetDiagnostics(
		t.Context(), "companion", []string{"managed"},
	)
	if err != nil || diagnostics.Profiles["managed"].Status != browser.ReadinessReady ||
		!diagnostics.Contexts || diagnostics.Diagnostics || len(diagnostics.Actions) == 0 {
		t.Fatalf("target diagnostics = %#v, %v", diagnostics, err)
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
		t.Fatalf("core browser open failed without diagnostics approval: %v", err)
	}
	if _, err = opened.Owner.(*nodeBrowserWorker).Diagnostics(
		t.Context(), []browser.DiagnosticCategory{browser.DiagnosticConsoleErrors},
	); !errors.Is(err, browser.ErrDenied) {
		t.Fatalf("unapproved Diagnostics() error = %v", err)
	}
}

type readyLocalBrowserFactory struct{}

func (*readyLocalBrowserFactory) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
}

func (*readyLocalBrowserFactory) PassiveReadiness() browser.DriverReadiness {
	return browser.DriverReadiness{
		Status: browser.ReadinessReady, Driver: browser.ReadinessReady,
		Browser: browser.ReadinessReady, Proxy: browser.ReadinessReady,
		Compatibility: browser.CompatibilityCompatible,
	}
}

func (*readyLocalBrowserFactory) DiagnosticsAvailable() bool { return true }

func TestGatewayLocalDiagnosticsHideDragFromDryRunAndMixedProfiles(t *testing.T) {
	cfg, _, _ := browserNodeTestRuntime(t)
	target := cfg.Tools.Browser.Targets["companion"]
	target.Placement = config.BrowserPlacementGateway
	target.NodeTarget = ""
	target.Profiles["active"] = config.BrowserProfileConfig{
		Enabled: true, Mode: config.BrowserProfileManaged,
		NetworkMode: config.BrowserNetworkAnyHTTP, DryRun: false, AllowApprovedActions: true,
	}
	cfg.Tools.Browser.Targets = map[string]config.BrowserTargetConfig{"gateway": target}
	factory := &gatewayBrowserWorkerFactory{config: cfg, local: &readyLocalBrowserFactory{}}

	dryRun, err := factory.PassiveTargetDiagnostics(t.Context(), "gateway", []string{"managed"})
	if err != nil || slices.Contains(dryRun.Actions, browser.ActionDrag) {
		t.Fatalf("dry-run diagnostics = %#v, %v", dryRun, err)
	}
	active, err := factory.PassiveTargetDiagnostics(t.Context(), "gateway", []string{"active"})
	if err != nil || !slices.Contains(active.Actions, browser.ActionDrag) {
		t.Fatalf("active diagnostics = %#v, %v", active, err)
	}
	mixed, err := factory.PassiveTargetDiagnostics(t.Context(), "gateway", []string{"active", "managed"})
	if err != nil || slices.Contains(mixed.Actions, browser.ActionDrag) {
		t.Fatalf("mixed diagnostics = %#v, %v", mixed, err)
	}
}

func TestGatewayBrowserCatalogRejectsMismatchedCommandProfile(t *testing.T) {
	_, runtime, _ := browserNodeTestRuntime(t)
	_, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		catalog.Commands[0].BrowserProfiles[0].Limits.SnapshotRefs--
	})
	if !errors.Is(err, nodes.ErrInvalidCapability) {
		t.Fatalf("mutate mismatched profile catalog error = %v", err)
	}
}

func TestGatewayAcceptsSingleChunkSnapshotAboveNegotiatedResultBudget(t *testing.T) {
	limits := config.BrowserLimitsConfig{}.Effective()
	limits.ToolResultBytes = 150 * 1024
	size := uint64(160 * 1024)
	if size >= uint64(protocol.MaxTransferChunkBytes) || size <= uint64(limits.ToolResultBytes) {
		t.Fatal("invalid single-chunk snapshot fixture")
	}
	if !validBrowserSnapshotOutputSize(size, limits) {
		t.Fatal("gateway rejected a bounded single-chunk snapshot descriptor")
	}
}

func TestGatewayBrowserCatalogRejectsMixedActionModesAcrossCommands(t *testing.T) {
	_, runtime, _ := browserNodeTestRuntime(t)
	_, err := browserNodeTestMutateCatalog(t, runtime, func(catalog *nodes.CapabilityCatalog) {
		for index := range catalog.Commands {
			if catalog.Commands[index].Name != nodes.BrowserCommandAct {
				continue
			}
			catalog.Commands[index].BrowserProfiles[0].DryRun = false
			catalog.Commands[index].BrowserProfiles[0].AllowApprovedActions = true
		}
	})
	if !errors.Is(err, nodes.ErrInvalidCapability) {
		t.Fatalf("mutate mixed-mode catalog error = %v", err)
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
		DryRun: true, Actions: []string{"download", "navigate", "scroll"}, Limits: nodes.BrowserLimits{}.Effective(),
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
