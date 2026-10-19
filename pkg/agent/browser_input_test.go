package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	browserInputTestOrigin = "http://127.0.0.1:34567"
	browserInputCanary     = "PRIVATE_BROWSER_INPUT_419b79e8"
)

// Only the worker is synthetic: ownership, preparations, interaction claims,
// history projections, suspension and continuation use the production runtime.
type browserInputTestWorker struct {
	value  string
	fills  int
	closed int
}

func (*browserInputTestWorker) Status(context.Context) (browser.WorkerStatus, error) {
	return browser.WorkerReady, nil
}

func (worker *browserInputTestWorker) Close(context.Context) error {
	worker.closed++
	worker.value = ""
	return nil
}

func (worker *browserInputTestWorker) Observe(context.Context) (browser.DriverObservation, error) {
	return browser.DriverObservation{
		URL: browserInputTestOrigin + "/form", Origin: browserInputTestOrigin, Title: "Synthetic form",
		Snapshot: "- textbox \"Name\" [ref=e1]: " + worker.value,
		Elements: []browser.DriverElement{{Target: "e1", Role: "textbox", Name: "Name"}},
	}, nil
}

func (*browserInputTestWorker) Resolve(
	_ context.Context, target string,
) (browser.DriverElement, string, error) {
	if target != "e1" {
		return browser.DriverElement{}, "", browser.ErrNotFound
	}
	return browser.DriverElement{Target: "e1", Role: "textbox", Name: "Name"}, browserInputTestOrigin, nil
}

func (worker *browserInputTestWorker) Execute(_ context.Context, action browser.DriverAction) error {
	if action.Kind != browser.DriverFill || action.Target != "e1" {
		return browser.ErrInvalid
	}
	worker.fills++
	worker.value = action.Value
	return nil
}

func (*browserInputTestWorker) CatalogRevision() string { return strings.Repeat("c", 64) }

func (*browserInputTestWorker) NavigationIdentity(context.Context) (string, error) {
	return "navigation_1", nil
}

func (worker *browserInputTestWorker) ExecuteAfterNavigationCheck(
	ctx context.Context, expected string, action browser.DriverAction,
) error {
	if expected != "navigation_1" {
		return browser.ErrStale
	}
	return worker.Execute(ctx, action)
}

func (*browserInputTestWorker) AuthorizeFill(_ context.Context, expected, target string) error {
	if expected != "navigation_1" || target != "e1" {
		return browser.ErrStale
	}
	return nil
}

type browserInputTestFactory struct{ worker *browserInputTestWorker }

func (factory browserInputTestFactory) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: factory.worker, Capabilities: browser.WorkerCapabilityActions}, nil
}

type browserInputTestSource struct {
	*browser.Broker
	store                  browser.Store
	latestSession          browser.Session
	latestObservation      browser.Observation
	prepared               browser.Preparation
	observationUnavailable bool
}

var _ tools.BrowserProtectedInputSource = (*browserInputTestSource)(nil)

func (*browserInputTestSource) Available() bool                 { return true }
func (*browserInputTestSource) ScreenshotAvailable() bool       { return false }
func (*browserInputTestSource) ArtifactTransferAvailable() bool { return false }
func (*browserInputTestSource) DownloadAvailable() bool         { return false }
func (*browserInputTestSource) HandoffAvailable() bool          { return false }
func (*browserInputTestSource) LookupScreenshot(
	context.Context, browser.Owner, string, string,
) (browser.ScreenshotArtifact, bool, error) {
	return browser.ScreenshotArtifact{}, false, browser.ErrDenied
}

func (*browserInputTestSource) CaptureScreenshot(
	context.Context, browser.ScreenshotRequest,
) (browser.ScreenshotArtifact, error) {
	return browser.ScreenshotArtifact{}, browser.ErrDenied
}

func (*browserInputTestSource) ClaimScreenshotDelivery(context.Context, browser.ScreenshotDeliveryRequest) error {
	return browser.ErrDenied
}

func (*browserInputTestSource) ClaimDownloadDelivery(context.Context, browser.DownloadDeliveryRequest) error {
	return browser.ErrDenied
}

func (*browserInputTestSource) PassiveTargetDiagnostics(
	context.Context, string, []string,
) (tools.BrowserTargetDiagnostics, error) {
	return tools.BrowserTargetDiagnostics{}, browser.ErrDenied
}

func (source *browserInputTestSource) Open(ctx context.Context, request browser.OpenRequest) (browser.Session, error) {
	value, err := source.Broker.Open(ctx, request)
	if err == nil {
		source.latestSession = value
	}
	return value, err
}

func (source *browserInputTestSource) Observe(
	ctx context.Context, owner browser.Owner, sessionID, tabID string,
) (browser.Observation, error) {
	if source.observationUnavailable {
		return browser.Observation{}, browser.ErrWorkerUnavailable
	}
	value, err := source.Broker.Observe(ctx, owner, sessionID, tabID)
	if err == nil {
		source.latestObservation = value
	}
	return value, err
}

func (source *browserInputTestSource) ObserveContext(
	ctx context.Context, request browser.ObserveRequest,
) (browser.Observation, error) {
	value, err := source.Broker.ObserveContext(ctx, request)
	if err == nil {
		source.latestObservation = value
	}
	return value, err
}

func (source *browserInputTestSource) PrepareProtectedFill(
	ctx context.Context, owner browser.Owner, token, value string,
) (browser.Preparation, error) {
	prepared, err := source.Broker.PrepareProtectedFill(ctx, owner, token, value)
	if err == nil {
		source.prepared = prepared
	}
	return prepared, err
}

func (source *browserInputTestSource) CloseOwner(ctx context.Context, owner browser.Owner) ([]browser.Session, error) {
	return source.CloseOwnerSessions(ctx, owner)
}

func browserInputTestConfig(cfg *config.Config, decision string) {
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true, Agents: []string{"main"}, Targets: map[string]config.BrowserTargetConfig{
			"gateway": {
				Enabled: true, Driver: config.BrowserDriverPlaywrightLibrary, DriverExecutable: "node",
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {
						Enabled:              true,
						Revision:             "input-v1",
						Mode:                 config.BrowserProfileEphemeral,
						AllowedAgents:        []string{"main"},
						AllowedActors:        []string{"telegram:user-1"},
						NetworkMode:          config.BrowserNetworkLoopbackFixture,
						AllowedOrigins:       []string{browserInputTestOrigin},
						CapabilityMode:       config.BrowserCapabilityRestricted,
						ApprovalMode:         config.BrowserApprovalPolicy,
						AllowApprovedActions: true,
						Runtime: config.BrowserProfileRuntimeConfig{
							EphemeralRoot: filepath.Join(cfg.Agents.Defaults.Workspace, "browser-profiles"),
							LockFile:      filepath.Join(cfg.Agents.Defaults.Workspace, "browser-profile.lock"),
						},
						Policy: &browserpolicy.Policy{
							DefaultDecision: browserpolicy.DecisionDeny,
							Rules: []browserpolicy.Rule{{
								ID: "synthetic-field", Decision: decision, Match: browserpolicy.RuleMatch{
									Actions: []string{"fill"}, Origins: []string{browserInputTestOrigin},
								},
							}},
						},
					},
				},
			},
		},
	}
}

func newBrowserInputTestSource(
	t *testing.T,
	cfg *config.Config,
	workspace string,
) (*browserInputTestSource, *browserInputTestWorker) {
	t.Helper()
	stateDir := filepath.Join(workspace, "browser-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := browser.NewFileStore(filepath.Join(stateDir, "native-browser.json"), 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	worker := &browserInputTestWorker{}
	broker, err := browser.NewBroker(cfg, store, browserInputTestFactory{worker: worker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := broker.Shutdown(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return &browserInputTestSource{Broker: broker, store: store}, worker
}

func browserInputAskArguments(source *browserInputTestSource) (map[string]any, error) {
	observation := source.latestObservation
	match := regexp.MustCompile(`\[ref=([^\]]+)\]`).FindStringSubmatch(observation.Snapshot)
	if len(match) != 2 {
		return nil, errors.New("fresh native field reference unavailable")
	}
	return map[string]any{
		"operation": "ask", "question": "What test name should be entered?",
		"browser_session_id": source.latestSession.ID, "tab_id": observation.TabID,
		"snapshot_id": observation.SnapshotID, "snapshot_generation": int(observation.SnapshotGeneration),
		"ref": match[1],
	}, nil
}

type browserInputTestProvider struct {
	source *browserInputTestSource
	step   int
}

func (*browserInputTestProvider) GetDefaultModel() string { return "test-model" }

func (provider *browserInputTestProvider) Chat(
	_ context.Context, messages []providers.Message, definitions []providers.ToolDefinition, _ string, _ map[string]any,
) (*providers.LLMResponse, error) {
	if len(definitions) == 1 && definitions[0].Function.Name == interactionContinuationDecisionTool {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID: "browser-input-preflight", Name: interactionContinuationDecisionTool,
			Arguments: map[string]any{"action": "continue"},
		}}}, nil
	}
	var name string
	var args map[string]any
	switch provider.step {
	case 0:
		name, args = "browser_session", map[string]any{
			"operation": "open", "target": "gateway", "profile": "managed", "interaction_language": "en",
		}
	case 1:
		name, args = "browser_observe", map[string]any{
			"browser_session_id": provider.source.latestSession.ID, "tab_id": provider.source.latestSession.TabID,
		}
	case 2:
		var err error
		name = "browser_input"
		args, err = browserInputAskArguments(provider.source)
		if err != nil {
			return nil, err
		}
	case 3:
		if len(definitions) != 1 || definitions[0].Function.Name != "browser_input" || len(messages) == 0 ||
			!strings.Contains(messages[len(messages)-1].Content, "<runtime_protected_answer_continuation>") {
			return nil, errors.New("missing native browser protected continuation")
		}
		name, args = "browser_input", map[string]any{
			"operation": "resume", "answer_ref": "browser_fill." + provider.source.prepared.Action.ID,
		}
	case 4:
		name, args = "browser_session", map[string]any{
			"operation": "close", "browser_session_id": provider.source.latestSession.ID,
		}
	default:
		return &providers.LLMResponse{Content: "The requested field was filled.", FinishReason: "stop"}, nil
	}
	provider.step++
	return &providers.LLMResponse{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{
		ID: "browser-input-call-" + strconv.Itoa(provider.step), Name: name, Arguments: args,
	}}}, nil
}

func TestBrowserInputAgentLoopProtectedAnswerAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name     string
		cancel   bool
		approval bool
	}{
		{name: "accepted"}, {name: "separate native approval", approval: true}, {name: "canceled", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cancel := test.cancel
			provider := &browserInputTestProvider{}
			fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
				decision := browserpolicy.DecisionAllow
				if test.approval {
					decision = browserpolicy.DecisionAsk
				}
				browserInputTestConfig(cfg, decision)
				cfg.Diagnostics.TraceCapture = config.DiagnosticTraceCaptureConfig{
					Enabled: true, ContentMode: "redacted_content",
				}
				if test.name == "accepted" {
					cfg.Diagnostics.TraceCapture.ContentMode = "metadata_only"
				}
			})
			source, worker := newBrowserInputTestSource(t, fixture.Config, fixture.Agent.Workspace)
			provider.source = source
			loop := fixture.Loop
			loop.SetBrowserCapabilityClient(source)
			installInteractionChannelManager(t, loop, newInteractionChannelManager())
			options := tools.NewBrowserToolOptions(fixture.Config.Tools.Browser)
			fixture.Agent.Tools.Register(tools.NewBrowserSessionTool(options, source))
			fixture.Agent.Tools.Register(tools.NewBrowserObserveTool(options, source))
			fixture.Agent.Tools.Register(tools.NewBrowserInputTool(options, source))
			if err := loop.RegisterProtectedAnswerSink(tools.NewBrowserProtectedAnswerSink(
				func() (tools.BrowserProtectedInputSource, error) { return source, nil },
			)); err != nil {
				t.Fatal(err)
			}
			msg := testInboundMessage(bus.InboundMessage{
				SessionKey: session.BuildOpaqueSessionKey("agent:main:test:browser-input"),
				Context: bus.InboundContext{
					Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
				},
			})
			target, ok := loop.resolveSteeringTarget(msg)
			if !ok {
				t.Fatal("interaction route unavailable")
			}
			_, status, err := runAgentLoopWithStatusForTest(t.Context(), loop, fixture.Agent, turnSpec{
				Dispatch: DispatchRequest{
					RouteSessionKey: target.Allocation.RouteScopeKey,
					SessionKey:      target.SessionKey,
					UserMessage:     "Fill the synthetic name field. Ask me privately for its value.",
					InboundContext:  &msg.Context,
				},
				DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
			})
			if err != nil || status != TurnEndStatusSuspended {
				t.Fatalf("browser question did not suspend: %s, %v", status, err)
			}
			registry := loop.interactionRegistryForWorkspace(fixture.Agent.Workspace)
			record, ok := activeInteractionForSession(registry, target.SessionKey)
			if !ok || record.ProtectedAnswer == nil || record.Origin.ToolName != "browser_input" ||
				record.Origin.ExecutionID == "" || worker.fills != 0 {
				t.Fatal("native question lost protected ownership or dispatched early")
			}
			sessionState, err := source.Status(t.Context(), source.latestSession.Owner, source.latestSession.ID)
			if err != nil || sessionState.State != browser.SessionReady ||
				sessionState.EffectiveController() != browser.ControllerAgent {
				t.Fatal("waiting question closed the resource or transferred DOM control", err)
			}
			if record.Status != interactions.StatusWaiting {
				t.Fatal("native prompt delivery did not reach waiting state")
			}
			assertBrowserInputWaitingTargetTrace(t, fixture.Agent.Workspace, source, record)
			answer := msg
			answer.Content = answerCommand + " " + record.ShortID + " " + browserInputCanary
			answer.SpoolID = "browser-private-answer"
			answer.Context.MessageID = "browser-private-answer"
			if cancel {
				answer.Content = "/stop"
				command, valid := newCancelInteractionCommand(answer, target)
				if !valid {
					t.Fatal("native cancellation unavailable")
				}
				_, err = newInteractionService(loop).Cancel(t.Context(), command)
			} else {
				command, commandErr := newAnswerInteractionCommand(answer, target)
				if commandErr != nil {
					t.Fatal(commandErr)
				}
				_, err = newInteractionService(loop).Answer(t.Context(), command)
			}
			if err != nil {
				t.Fatal("native browser answer failed", err)
			}
			if test.approval {
				approval, found := activeInteractionForSession(registry, target.SessionKey)
				if !found || approval.Kind != interactions.KindApproval || worker.fills != 0 || worker.closed != 0 {
					t.Fatal("private input did not wait for separate native approval")
				}
				answer.Content = answerCommand + " " + approval.ShortID + " allow_once"
				answer.SpoolID = "browser-fill-approval"
				answer.Context.MessageID = "browser-fill-approval"
				command, commandErr := newAnswerInteractionCommand(answer, target)
				if commandErr != nil {
					t.Fatal(commandErr)
				}
				if _, err = newInteractionService(loop).Answer(t.Context(), command); err != nil {
					t.Fatal("separate native approval did not restore the protected fill", err)
				}
			}
			wantFills := 1
			if cancel {
				wantFills = 0
			}
			if worker.fills != wantFills || worker.closed != 1 || worker.value != "" {
				t.Fatalf("unexpected resource outcome: fills=%d closed=%d", worker.fills, worker.closed)
			}
			resolved, _ := registry.Get(record.ID)
			if !cancel && (resolved.Status != interactions.StatusResolved || resolved.Answer == nil ||
				resolved.Answer.Text != "") {
				t.Fatal("protected answer did not settle without plaintext")
			}
			if cancel && resolved.Status != interactions.StatusCancelled {
				t.Fatal("native cancellation did not settle")
			}
			if !cancel && (resolved.Answer.Protected == nil || provider.step != 5) {
				t.Fatal("protected receipt did not resume the exact native operation")
			}
			history := fixture.Agent.Sessions.GetHistory(target.SessionKey)
			fixture.Close()
			seenInput := false
			for _, path := range waitForTraceFiles(t, fixture.Agent.Workspace, 1) {
				for _, record := range readCapturedTrace(t, path).Records {
					if record.Kind != diagnostictrace.RecordToolCall {
						continue
					}
					var payload diagnostictrace.ToolPayload
					if err := json.Unmarshal(record.Data, &payload); err != nil {
						t.Fatal(err)
					}
					seenInput = seenInput || payload.Tool == "browser_input"
				}
			}
			if !seenInput {
				t.Fatal("privacy check had no captured native input tool evidence")
			}
			assertBrowserInputPrivateState(t, fixture.Agent.Workspace, history)
		})
	}
}

func assertBrowserInputWaitingTargetTrace(
	t *testing.T, workspace string, source *browserInputTestSource, interaction interactions.Record,
) {
	t.Helper()
	native, err := source.InspectProtectedFillBinding(
		t.Context(),
		source.latestSession.Owner,
		interaction.ProtectedAnswer.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, path := range waitForTraceFiles(t, workspace, 1) {
		trace := readCapturedTrace(t, path)
		if trace.Outcome == nil || trace.Outcome.Status != "suspended" || trace.Truncation.Incomplete ||
			trace.Truncation.DroppedRecords != 0 {
			t.Fatal("waiting input target had no complete suspended trace")
		}
		for _, record := range trace.Records {
			if record.Kind != diagnostictrace.RecordToolResult {
				continue
			}
			var payload diagnostictrace.ToolPayload
			if err := json.Unmarshal(record.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.BrowserInputTarget == nil {
				continue
			}
			if payload.Tool != "browser_input" || !payload.BrowserInputTarget.Valid() ||
				payload.BrowserInputTarget.InteractionID != interaction.ID ||
				payload.BrowserInputTarget.Target != native.Observation ||
				record.Correlation.ToolCallID != interaction.Origin.ToolCallID ||
				record.Scope.TurnID != interaction.Origin.TurnID || payload.IsError || !payload.Executed ||
				payload.ArgumentsPreview != "" || payload.ResultPreview != "" {
				t.Fatal("trace target diverged from the actual native question and broker binding")
			}
			seen++
		}
	}
	if seen != 1 {
		t.Fatal("waiting question did not publish exactly one native bound-target observation")
	}
}

func assertBrowserInputPrivateState(t *testing.T, workspace string, history []providers.Message) {
	t.Helper()
	data, err := json.Marshal(history)
	if err != nil || bytes.Contains(data, []byte(browserInputCanary)) {
		t.Fatal("canonical history retained private browser input")
	}
	if err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(browserInputCanary)) {
			t.Error("raw durable surface retained private browser input: " + filepath.Base(path))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func browserInputNativeTestContext(workspace string) context.Context {
	ctx := toolshared.WithToolInboundMetadata(context.Background(), bus.InboundContext{
		Channel: "telegram", SenderID: "user-1",
	})
	ctx = toolshared.WithToolSessionContext(ctx, "main", "history-input", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "route-input")
	ctx = toolshared.WithToolCallID(ctx, "call-input")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "execution-input")
	return toolshared.WithRuntimeCapabilities(
		ctx,
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
	)
}

type browserInputNativeFixture struct {
	workspace string
	config    *config.Config
	source    *browserInputTestSource
	worker    *browserInputTestWorker
	tool      *tools.BrowserInputTool
	sink      *tools.BrowserProtectedAnswerSink
	ctx       context.Context
	request   interactions.ProtectedAnswerSinkRequest
}

func newBrowserInputNativeFixture(t *testing.T, decision string) *browserInputNativeFixture {
	t.Helper()
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	browserInputTestConfig(cfg, decision)
	source, worker := newBrowserInputTestSource(t, cfg, workspace)
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID("telegram:user-1"), AgentID: browser.OpaqueAgentID("main"),
		SessionKey: browser.OpaqueSessionKey("route-input"), ExecutionID: browser.OpaqueExecutionID("execution-input"),
	}
	current, err := source.Open(t.Context(), browser.OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Observe(t.Context(), owner, current.ID, current.TabID); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewBrowserInputTool(tools.NewBrowserToolOptions(cfg.Tools.Browser), source)
	ctx := browserInputNativeTestContext(workspace)
	args, err := browserInputAskArguments(source)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(ctx, args)
	if result.IsError || result.Control.Suspension == nil || result.Control.Suspension.ProtectedAnswer == nil {
		t.Fatal("protected native question unavailable", result.ContentForLLM())
	}
	if result.Control.LiveHandoff == nil || result.Control.LiveHandoff.ResourceKind != "browser_input" {
		t.Fatal("question did not retain its exact live resource")
	}
	return &browserInputNativeFixture{
		workspace: workspace,
		config:    cfg,
		source:    source,
		worker:    worker,
		tool:      tool,
		ctx:       ctx,
		sink: tools.NewBrowserProtectedAnswerSink(
			func() (tools.BrowserProtectedInputSource, error) { return source, nil },
		),
		request: interactions.ProtectedAnswerSinkRequest{
			Workspace: workspace, InteractionID: "interaction-input", IdempotencyKey: "claim-input",
			Intent: interactions.ProtectedAnswerValue, Text: browserInputCanary,
			Binding: *result.Control.Suspension.ProtectedAnswer,
			Route: interactions.Route{
				AgentID: "main", SessionKey: "history-input", RouteSessionKey: "route-input",
				Channel: "telegram", SenderID: "user-1",
			},
			Origin: interactions.ProtectedAnswerOrigin{
				ToolName:    "browser_input",
				ToolCallID:  "call-input",
				ExecutionID: "execution-input",
				ActorID:     "telegram:user-1",
			},
		},
	}
}

func (fixture *browserInputNativeFixture) accept(t *testing.T) interactions.ProtectedAnswerReceipt {
	t.Helper()
	receipt, err := fixture.sink.Accept(fixture.ctx, fixture.request)
	if err != nil || receipt.Reference == "" || fixture.worker.fills != 0 {
		t.Fatal("protected input staging failed or dispatched before the native continuation", err)
	}
	return receipt
}

func (fixture *browserInputNativeFixture) commit(t *testing.T, receipt interactions.ProtectedAnswerReceipt) {
	t.Helper()
	request := fixture.request
	if err := fixture.sink.Commit(fixture.ctx, interactions.ProtectedAnswerCommitRequest{
		Workspace: request.Workspace, InteractionID: request.InteractionID, Route: request.Route,
		Origin: request.Origin, Binding: request.Binding, Receipt: receipt,
	}); err != nil || fixture.worker.fills != 0 {
		t.Fatal("protected claim validation failed or dispatched prematurely", err)
	}
}

func TestBrowserInputNativeStagesWithoutGrantAndRejectsInventedContinuation(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	receipt := fixture.accept(t)
	fixture.commit(t, receipt)
	args, err := fixture.tool.ProtectedAnswerContinuationArguments(receipt.Reference)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := fixture.tool.DurableArguments(args)
	if err != nil || projection["answer_ref"] != receipt.Reference {
		t.Fatal("value-free native receipt did not survive durable approval history")
	}
	for _, ctx := range []context.Context{
		fixture.ctx,
		toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, "browser_fill.prepared_wrong"),
		toolshared.WithToolApprovalContinuation(fixture.ctx, true),
	} {
		if result := fixture.tool.Execute(ctx, args); !result.IsError || fixture.worker.fills != 0 {
			t.Fatal("model reference or unbound approval executed private input")
		}
	}
	other := fixture.request
	other.Text = "different-answer"
	if _, err := fixture.sink.Accept(fixture.ctx, other); !errors.Is(err, browser.ErrConflict) {
		t.Fatal("competing answer replaced the first transient input", err)
	}
	if err := fixture.sink.Discard(fixture.ctx, interactions.ProtectedAnswerDiscardRequest{
		Workspace: fixture.workspace, Route: fixture.request.Route, Origin: fixture.request.Origin,
		Binding: fixture.request.Binding,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, receipt.Reference)
	for range 2 {
		if result := fixture.tool.Execute(ctx, args); result.IsError || fixture.worker.fills != 1 ||
			fixture.worker.value != browserInputCanary {
			t.Fatal("native continuation failed or replayed the mutation")
		}
	}
	assertBrowserInputPrivateState(t, fixture.workspace, nil)
}

func TestBrowserInputNativeRequiresSeparateRestrictedApproval(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAsk)
	receipt := fixture.accept(t)
	fixture.commit(t, receipt)
	args, _ := fixture.tool.ProtectedAnswerContinuationArguments(receipt.Reference)
	ctx := toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, receipt.Reference)
	result := fixture.tool.Execute(ctx, args)
	if result.IsError || result.Control.Suspension == nil ||
		result.Control.Suspension.Kind != interactions.KindApproval || fixture.worker.fills != 0 {
		t.Fatal("private answer bypassed native restricted policy")
	}
	approved := toolshared.WithToolApprovalContinuation(fixture.ctx, true)
	wrong := toolshared.WithToolApprovalArguments(approved, map[string]any{"answer_ref": receipt.Reference})
	if result = fixture.tool.Execute(wrong, args); !result.IsError || fixture.worker.fills != 0 {
		t.Fatal("mismatched approval dispatched protected input")
	}
	approved = toolshared.WithToolApprovalArguments(
		approved,
		tools.BrowserActionApprovalArguments(fixture.source.prepared),
	)
	if result = fixture.tool.Execute(approved, args); result.IsError || fixture.worker.fills != 1 {
		t.Fatal("exact native fill approval was not consumed")
	}
	assertBrowserInputPrivateState(t, fixture.workspace, nil)
}

func TestBrowserInputNativeKeepsKnownSuccessWhenObservationIsLost(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	receipt := fixture.accept(t)
	fixture.commit(t, receipt)
	args, _ := fixture.tool.ProtectedAnswerContinuationArguments(receipt.Reference)
	fixture.source.observationUnavailable = true
	result := fixture.tool.Execute(toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, receipt.Reference), args)
	var value struct {
		State                  browser.InvocationState `json:"state"`
		ObservationUnavailable bool                    `json:"observation_unavailable"`
	}
	if result.IsError || json.Unmarshal([]byte(result.ContentForLLM()), &value) != nil ||
		value.State != browser.InvocationSucceeded || !value.ObservationUnavailable || fixture.worker.fills != 1 {
		t.Fatal("lost observation erased known accepted fill outcome")
	}
}

func TestBrowserInputNativeRejectsForeignStaleAndRestartedAuthority(t *testing.T) {
	for _, change := range []string{"actor", "execution", "call", "route", "namespace", "token", "snapshot", "restart"} {
		t.Run(change, func(t *testing.T) {
			fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
			request := fixture.request
			switch change {
			case "actor":
				request.Origin.ActorID = "telegram:other"
			case "execution":
				request.Origin.ExecutionID = "other-execution"
			case "call":
				request.Origin.ToolCallID = "other-call"
			case "route":
				request.Route.RouteSessionKey = "other-route"
			case "namespace":
				request.Binding.Namespace = "document.form.v1"
			case "token":
				request.Binding.Token += "forged"
			case "snapshot":
				current := fixture.source.latestSession
				if _, err := fixture.source.Observe(fixture.ctx, current.Owner, current.ID, current.TabID); err != nil {
					t.Fatal(err)
				}
			case "restart":
				replacement, err := browser.NewBroker(fixture.config, fixture.source.store,
					browserInputTestFactory{worker: &browserInputTestWorker{}})
				if err != nil {
					t.Fatal(err)
				}
				fixture.source.Broker = replacement
				t.Cleanup(func() { _ = replacement.Shutdown(context.Background()) })
			}
			if _, err := fixture.sink.Accept(fixture.ctx, request); err == nil || fixture.worker.fills != 0 ||
				fixture.source.prepared.Action.ID != "" {
				t.Fatal("foreign or obsolete authority staged private input")
			}
			assertBrowserInputPrivateState(t, fixture.workspace, nil)
		})
	}
}

func TestBrowserInputNativeForcedDiscardErasesStagedValue(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	receipt := fixture.accept(t)
	request := fixture.request
	if err := fixture.sink.Discard(fixture.ctx, interactions.ProtectedAnswerDiscardRequest{
		Workspace: request.Workspace,
		Route:     request.Route,
		Origin:    request.Origin,
		Binding:   request.Binding,
		Force:     true,
	}); err != nil || fixture.worker.closed != 1 || fixture.worker.value != "" {
		t.Fatal("forced discard failed native owner cleanup", err)
	}
	args, _ := fixture.tool.ProtectedAnswerContinuationArguments(receipt.Reference)
	if result := fixture.tool.Execute(
		toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, receipt.Reference),
		args,
	); !result.IsError ||
		fixture.worker.fills != 0 {
		t.Fatal("discarded input was reconstructed or dispatched")
	}
	assertBrowserInputPrivateState(t, fixture.workspace, nil)
}

func TestBrowserInputNativeLostEphemeralInputCannotResumeAfterRestart(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	receipt := fixture.accept(t)
	fixture.commit(t, receipt)
	replacement, err := browser.NewBroker(fixture.config, fixture.source.store,
		browserInputTestFactory{worker: &browserInputTestWorker{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Shutdown(context.Background()) })
	fixture.source.Broker = replacement
	args, _ := fixture.tool.ProtectedAnswerContinuationArguments(receipt.Reference)
	if result := fixture.tool.Execute(
		toolshared.WithToolProtectedAnswerContinuation(fixture.ctx, receipt.Reference),
		args,
	); !result.IsError ||
		fixture.worker.fills != 0 {
		t.Fatal("restart reconstructed a lost transient value")
	}
	request := fixture.request
	if err := fixture.sink.Cancel(fixture.ctx, interactions.ProtectedAnswerCancelRequest{
		Workspace: request.Workspace, Route: request.Route, Origin: request.Origin, Binding: request.Binding,
		InteractionID: request.InteractionID, IdempotencyKey: "cancel-after-restart",
	}); err != nil {
		t.Fatal("owner cleanup incorrectly depended on decoding the old process token", err)
	}
	current, err := replacement.Status(fixture.ctx, fixture.source.latestSession.Owner, fixture.source.latestSession.ID)
	if err != nil || !current.State.Terminal() {
		t.Fatal("restarted owner did not reach terminal native cleanup", err)
	}
	assertBrowserInputPrivateState(t, fixture.workspace, nil)
}

func TestBrowserInputNativeCommitRevalidatesClaimedTarget(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	receipt := fixture.accept(t)
	current := fixture.source.latestSession
	if _, err := fixture.source.Observe(fixture.ctx, current.Owner, current.ID, current.TabID); err != nil {
		t.Fatal(err)
	}
	request := fixture.request
	if err := fixture.sink.Commit(fixture.ctx, interactions.ProtectedAnswerCommitRequest{
		Workspace: request.Workspace, InteractionID: request.InteractionID, Route: request.Route,
		Origin: request.Origin, Binding: request.Binding, Receipt: receipt,
	}); err == nil || fixture.worker.fills != 0 {
		t.Fatal("claim committed input after its target changed")
	}
	assertBrowserInputPrivateState(t, fixture.workspace, nil)
}

func TestBrowserInputQuestionRejectsStatelessCodingAndMalformedAuthority(t *testing.T) {
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	args, err := browserInputAskArguments(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	args["frame_id"] = nil
	args["context_catalog_id"] = nil
	args["context_generation"] = nil
	args["answer_ref"] = nil
	if result := fixture.tool.Execute(fixture.ctx, args); result.IsError || result.Control.Suspension == nil {
		t.Fatal("provider null placeholders changed a valid value-free question")
	}
	args, err = fixture.tool.CanonicalArguments(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{
		toolshared.WithToolHistoryDisabled(fixture.ctx, true),
		toolshared.WithRuntimeCapabilities(fixture.ctx, runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding})),
	} {
		if result := fixture.tool.Execute(ctx, args); !result.IsError {
			t.Fatal("unsupported runtime accepted a protected browser question")
		}
	}
	for _, extra := range []map[string]any{
		{"value": browserInputCanary},
		{"context_catalog_id": "catalog-without-generation"},
		{"context_generation": 1},
		{"context_catalog_id": 42, "context_generation": 1},
	} {
		changed := make(map[string]any, len(args)+len(extra))
		for key, value := range args {
			changed[key] = value
		}
		for key, value := range extra {
			changed[key] = value
		}
		if result := fixture.tool.Execute(fixture.ctx, changed); !result.IsError {
			t.Fatal("model-authored value or malformed authority accepted")
		}
	}
	args["question"] = browserInputCanary
	projection, err := fixture.tool.DurableArguments(args)
	encoded, encodeErr := json.Marshal(projection)
	if err != nil || encodeErr != nil || bytes.Contains(encoded, []byte(browserInputCanary)) ||
		!fixture.tool.ProtectedDurableArguments(args) || !fixture.tool.ProtectedDurableResult(args) {
		t.Fatal("model-authored private question entered durable projection")
	}
	logged, err := json.Marshal(tools.ToolLogArguments("browser_input", args))
	if err != nil || bytes.Contains(logged, []byte(browserInputCanary)) {
		t.Fatal("model-authored private question entered operational logs")
	}
	for _, reference := range []string{
		"browser_fill.prepared_" + browserInputCanary,
		"browser_fill.prepared_" + strings.Repeat("z", 32),
		"browser_fill.prepared_" + strings.Repeat("a", 31),
		"browser_fill.prepared_" + strings.Repeat("a", 33),
	} {
		if _, err := fixture.tool.ProtectedAnswerContinuationArguments(reference); err == nil {
			t.Fatal("malformed receipt was accepted as a native continuation")
		}
		projection, err := fixture.tool.DurableArguments(map[string]any{"operation": "resume", "answer_ref": reference})
		if err != nil || projection["answer_ref"] != "browser_fill.prepared_redacted" {
			t.Fatal("malformed model reference entered durable authority")
		}
	}
}

func TestConfiguredBrowserInputCapabilityRequiresGatewayQuestionWindow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	browserInputTestConfig(cfg, browserpolicy.DecisionAllow)
	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeGateway)
	if environment.CapabilityState("browser.input").State != skills.SkillRequirementAvailable ||
		environment.ToolState("browser_input") != skills.SkillRequirementAvailable {
		t.Fatal("configured protected browser input capability was not advertised")
	}
	coding := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)
	if coding.CapabilityState("browser.input").State == skills.SkillRequirementAvailable ||
		coding.ToolState("browser_input") == skills.SkillRequirementAvailable {
		t.Fatal("gateway protected input was advertised to a coding runtime")
	}
	cfg.Tools.Browser.Limits.PreparedSeconds = 60
	environment = ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeGateway)
	if environment.CapabilityState("browser.input").State != skills.SkillRequirementPolicyDisabled ||
		environment.ToolState("browser_input") != skills.SkillRequirementPolicyDisabled {
		t.Fatal("unusable native question deadline was advertised as available")
	}
}
