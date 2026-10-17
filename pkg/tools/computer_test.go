package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type computerToolTestSource struct {
	core            *computer.ReadOnlyHost
	target          computer.Target
	principal       runtimecap.Principal
	sessions        []string
	captures        int
	inputCalls      int
	applicationOpen bool
	image           []byte
}

func (source *computerToolTestSource) Capture(_ context.Context, target computer.Target) (computer.Capture, error) {
	source.captures++
	return computer.Capture{
		Scope: target.Scope, Geometry: computer.Geometry{Width: 2, Height: 2, Scale: 1},
		ContentType: "image/png", Screenshot: source.image, AccessibilityText: "private-computer-tool-canary",
	}, nil
}
func (*computerToolTestSource) Available() bool { return true }

func (source *computerToolTestSource) Targets(
	ctx context.Context,
	principal runtimecap.Principal,
	_ string,
) ([]computer.NamedTarget, error) {
	targets, err := source.core.Targets(ctx, principal)
	result := []computer.NamedTarget{}
	for _, target := range targets {
		result = append(result, computer.NamedTarget{Alias: "desktop", Target: target})
	}
	return result, err
}

func (source *computerToolTestSource) Open(
	ctx context.Context,
	principal runtimecap.Principal,
	target, _ string,
) (computer.Session, error) {
	if target != "desktop" {
		return computer.Session{}, computer.ErrDenied
	}
	session, err := source.core.Open(ctx, computer.OpenRequest{Principal: principal, Target: source.target})
	if err == nil {
		source.sessions = append(source.sessions, session.Binding.SessionID)
	}
	return session, err
}

func (source *computerToolTestSource) Status(
	ctx context.Context,
	principal runtimecap.Principal,
	id, _ string,
) (computer.Session, error) {
	return source.core.Status(ctx, principal, id)
}

func (source *computerToolTestSource) Control(
	ctx context.Context,
	principal runtimecap.Principal,
	id, _ string,
	event computer.ControlEvent,
) (computer.Session, error) {
	session, err := source.core.Status(ctx, principal, id)
	if err != nil {
		return computer.Session{}, err
	}
	switch event {
	case computer.EventHandoff:
		return source.core.Handoff(ctx, session.Binding)
	case computer.EventRequestResume:
		return source.core.RequestResume(ctx, session.Binding)
	case computer.EventResume:
		return source.core.Resume(ctx, session.Binding)
	case computer.EventRelease:
		return source.core.Release(ctx, session.Binding)
	default:
		return computer.Session{}, computer.ErrDenied
	}
}

func (source *computerToolTestSource) Observe(
	ctx context.Context,
	principal runtimecap.Principal,
	id, _ string,
) (computer.Observation, error) {
	session, err := source.core.Status(ctx, principal, id)
	if err != nil {
		return computer.Observation{}, err
	}
	return source.core.Observe(ctx, session)
}

func (source *computerToolTestSource) OwnedSessions(
	ctx context.Context,
	principal runtimecap.Principal,
) ([]computer.Session, error) {
	sessions := []computer.Session{}
	for _, id := range source.sessions {
		if session, err := source.core.Status(ctx, principal, id); err == nil {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func computerToolsFixture(t *testing.T) (ComputerToolOptions, *computerToolTestSource, context.Context) {
	t.Helper()
	principal := runtimecap.Principal{
		Runtime:     runtimecap.KindGateway,
		ActorID:     "actor",
		AgentID:     "agent",
		SessionID:   "thread",
		ExecutionID: "turn",
	}
	target := computer.Target{
		Alias: "selected", Placement: computer.PlacementNode, Node: "node_test", OS: computer.OSLinux,
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "fixture",
		Scope: computer.Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "window", WindowGeneration: 7},
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	source := &computerToolTestSource{
		target:          target,
		principal:       principal,
		applicationOpen: true,
		image:           encoded.Bytes(),
	}
	core, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
		WorkerID: "worker", Revision: strings.Repeat("a", 64),
		Targets: []computer.Target{target}, LeaseTTL: time.Minute, ObservationTTL: 10 * time.Second,
		Admit: func(candidate runtimecap.Principal, _ computer.Target) bool { return candidate == principal },
	}, source, computer.NewArbiter())
	if err != nil {
		t.Fatal(err)
	}
	source.core = core
	options := NewComputerToolOptions(
		config.ComputerToolsConfig{Enabled: true, Targets: map[string]config.ComputerTargetConfig{
			"desktop": {
				Agents:   []string{"agent"},
				Actors:   []string{"actor"},
				Runtimes: []runtimecap.Kind{runtimecap.KindGateway},
			},
		}},
	)
	ctx := toolshared.WithRuntimeCapabilities(
		t.Context(),
		runtimecap.NewContext(runtimecap.Inputs{Kind: principal.Runtime}).BindPrincipal(principal),
	)
	return options, source, ctx
}

func executeComputerTestTool(
	t *testing.T,
	tool toolshared.Tool,
	ctx context.Context,
	request string,
	args map[string]any,
) *toolshared.ToolResult {
	t.Helper()
	result := tool.Execute(toolshared.WithToolCallID(ctx, request), args)
	if result == nil || result.IsError {
		t.Fatalf("computer fixture %s failed: %v", request, result)
	}
	return result
}

func openComputerTestTool(t *testing.T, tool *ComputerSessionTool, ctx context.Context) string {
	t.Helper()
	result := executeComputerTestTool(t, tool, ctx, "open", map[string]any{"operation": "open", "target": "desktop"})
	var view struct {
		SessionID string `json:"computer_session_id"`
	}
	if json.Unmarshal([]byte(result.ForLLM), &view) != nil || view.SessionID == "" {
		t.Fatal("session identity missing")
	}
	return view.SessionID
}

func computerTestHandoffArgs(id string) map[string]any {
	return map[string]any{
		"operation": "handoff", "computer_session_id": id, "interaction_language": "ru",
		"handoff_prompt": map[string]any{"header": "Управление", "question": "Проверка завершена. Что сделать дальше?"},
	}
}

func TestComputerToolsProtectedObservationAndSameResourceHandoff(t *testing.T) {
	options, source, ctx := computerToolsFixture(t)
	targets := NewComputerTargetsTool(options, source)
	sessionTool := NewComputerSessionTool(options, source)
	observe := NewComputerObserveTool(options, source)
	if !sessionTool.ToolEnabledForAgent("agent") || sessionTool.ToolEnabledForAgent("someone-else") {
		t.Fatal("agent grant widened")
	}
	executeComputerTestTool(t, targets, ctx, "targets", map[string]any{})
	if source.captures != 0 {
		t.Fatal("discovery captured desktop content")
	}
	id := openComputerTestTool(t, sessionTool, ctx)
	result := executeComputerTestTool(t, observe, ctx, "observe-first", map[string]any{"computer_session_id": id})
	if source.captures != 1 || len(result.ContextMedia) != 1 || !result.ContextMediaRequiresVision ||
		result.ContextText != "private-computer-tool-canary" || len(result.Media) != 0 || result.Deliverable != nil {
		t.Fatal("protected observation was not one-shot model context")
	}
	pixels, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result.ContextMedia[0], "data:image/png;base64,"))
	if err != nil || !bytes.Equal(pixels, source.image) {
		t.Fatal("model did not receive exact fixture PNG")
	}
	durable, err := json.Marshal(result)
	if err != nil || bytes.Contains(durable, []byte("private-computer-tool-canary")) ||
		bytes.Contains(durable, []byte("base64,")) {
		t.Fatal("protected observation entered ordinary result serialization")
	}
	before, err := source.core.Status(ctx, source.principal, id)
	if err != nil {
		t.Fatal(err)
	}
	handoff := executeComputerTestTool(t, sessionTool, ctx, "handoff", computerTestHandoffArgs(id))
	if handoff.Control.Suspension == nil || handoff.Control.LiveHandoff == nil ||
		handoff.Control.Suspension.PromptLanguage != "ru" || handoff.Control.LiveHandoff.ResourceID != id ||
		handoff.Control.Suspension.Questions[0].Question != "Проверка завершена. Что сделать дальше?" {
		t.Fatal("handoff lost localized useful prompt or original resource")
	}
	if result := observe.Execute(
		toolshared.WithToolCallID(ctx, "observe-while-human"),
		map[string]any{"computer_session_id": id},
	); !result.IsError ||
		source.captures != 1 {
		t.Fatal("observation ran during human control")
	}
	if err := handoff.Control.ResolveSuspension(ctx, interactions.OutcomeAnswered); err != nil {
		t.Fatal(err)
	}
	after, err := source.core.Status(ctx, source.principal, id)
	if err != nil || after.Control != computer.ControlAgent || after.Binding.WorkerID != before.Binding.WorkerID ||
		after.Binding.Target != before.Binding.Target || after.Binding.SessionID != before.Binding.SessionID || source.captures != 1 {
		t.Fatal("handoff resolution rebound or implicitly captured the application", err)
	}
	executeComputerTestTool(t, observe, ctx, "observe-resumed", map[string]any{"computer_session_id": id})
	if source.captures != 2 {
		t.Fatal("resumed observation was not fresh")
	}
	if _, needed, err := sessionTool.TurnFinalizationRequirement(ctx); err != nil || !needed {
		t.Fatal("live computer resource bypassed finalization")
	}
	released := executeComputerTestTool(
		t,
		sessionTool,
		ctx,
		"release",
		map[string]any{"operation": "release", "computer_session_id": id},
	)
	verified := sessionTool.VerifyResourceDisposition(
		ctx,
		map[string]any{"operation": "release", "computer_session_id": id},
		released,
	)
	if !verified ||
		!strings.Contains(released.ForLLM, "not_checked") ||
		!source.applicationOpen ||
		source.inputCalls != 0 {
		t.Fatal("release did not produce truthful automation-only evidence")
	}
	if _, needed, err := sessionTool.TurnFinalizationRequirement(ctx); err != nil || needed {
		t.Fatal("terminal computer session still blocked finalization")
	}
}

func TestComputerHandoffAbandonReleasesWithoutObservationOrPhraseMatching(t *testing.T) {
	options, source, ctx := computerToolsFixture(t)
	tool := NewComputerSessionTool(options, source)
	id := openComputerTestTool(t, tool, ctx)
	handoff := executeComputerTestTool(t, tool, ctx, "handoff", computerTestHandoffArgs(id))
	cleanup, err := tool.ResolveLiveResourceHandoffWithResult(
		ctx,
		*handoff.Control.LiveHandoff,
		toolshared.LiveResourceHandoffAbandon,
	)
	if err != nil || len(cleanup.Receipts) != 1 || source.captures != 0 || !source.applicationOpen {
		t.Fatal("abandon captured/closed the application or lacked terminal evidence", err)
	}
	if strings.Contains(cleanup.Receipts[0].Summary, "remains open") {
		t.Fatal("cleanup claimed unobserved application lifetime")
	}
	if _, err := tool.ResolveLiveResourceHandoffWithResult(
		ctx,
		*handoff.Control.LiveHandoff,
		toolshared.LiveResourceHandoffResume,
	); err == nil {
		t.Fatal("terminal resource resumed")
	}
}

func TestComputerToolsRequireTrustedPrincipalAndRejectRawNativeInput(t *testing.T) {
	options, source, ctx := computerToolsFixture(t)
	tool := NewComputerSessionTool(options, source)
	if result := tool.Execute(
		toolshared.WithToolCallID(t.Context(), "unbound"),
		map[string]any{"operation": "open", "target": "desktop"},
	); !result.IsError {
		t.Fatal("unbound caller acquired computer authority")
	}
	for _, field := range []string{"pid", "display", "window_id", "principal", "actor_id", "event", "argv"} {
		args := map[string]any{"operation": "open", "target": "desktop", field: "forged"}
		if result := tool.Execute(toolshared.WithToolCallID(ctx, "forged-"+field), args); !result.IsError {
			t.Fatal("raw authority admitted", field)
		}
	}
	id := openComputerTestTool(t, tool, ctx)
	released := executeComputerTestTool(
		t,
		tool,
		ctx,
		"release",
		map[string]any{"operation": "release", "computer_session_id": id},
	)
	other := source.principal
	other.ExecutionID = "another-turn"
	otherCtx := toolshared.WithRuntimeCapabilities(
		ctx,
		runtimecap.NewContext(runtimecap.Inputs{Kind: other.Runtime}).BindPrincipal(other),
	)
	if tool.VerifyResourceDisposition(
		otherCtx,
		map[string]any{"operation": "release", "computer_session_id": id},
		released,
	) {
		t.Fatal("cleanup receipt certified another owner")
	}
	for _, kind := range []string{taskresult.ObjectiveKindLiveHandoff, taskresult.ObjectiveKindResourceDisposition} {
		schema, available := tool.ObjectiveRecoveryParameters(kind)
		encoded, err := json.Marshal(schema)
		if err != nil || !available || bytes.Contains(encoded, []byte(`"open"`)) ||
			bytes.Contains(encoded, []byte(`"target"`)) {
			t.Fatal("bounded resource recovery admitted replacement open")
		}
	}
}
