package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type agentCapabilityBrowser struct{ available bool }

func (browser *agentCapabilityBrowser) Available() bool { return browser.available }

func TestAgentLoopCapabilityReportTracksRunnerGeneration(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	client := &agentCapabilityBrowser{}
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoop(
		cfg,
		messageBus,
		&mockProvider{},
		WithIsolatedToolBootstrap(),
		WithBrowserCapabilityClient(client),
	)
	t.Cleanup(loop.Close)

	if loop.CapabilityReport().Runtime != runtimecap.KindGateway {
		t.Fatalf("runtime kind = %q, want gateway", loop.CapabilityReport().Runtime)
	}
	readFile, ok := loop.CapabilityReport().LookupTool("read_file")
	if !ok || !readFile.Available {
		t.Fatalf("admitted read_file tool = %#v, %t", readFile, ok)
	}
	availability, ok := loop.CapabilityReport().Lookup(runtimecap.CapabilityBrowserClient)
	if !ok || availability.Available || availability.Reason == nil ||
		availability.Reason.Code != runtimecap.ReasonServiceUnavailable {
		t.Fatalf("unready browser capability = %#v", availability)
	}
	client.available = true
	availability, ok = loop.CapabilityReport().Lookup(runtimecap.CapabilityBrowserClient)
	if !ok || !availability.Available {
		t.Fatalf("ready browser capability = %#v", availability)
	}
	loop.SetBrowserCapabilityClient(nil)
	availability, ok = loop.CapabilityReport().Lookup(runtimecap.CapabilityBrowserClient)
	if !ok || availability.Available || availability.Reason == nil ||
		availability.Reason.Code != runtimecap.ReasonNotConfigured {
		t.Fatalf("removed browser capability = %#v", availability)
	}
}

func TestRuntimeServiceCapabilityRefreshesCachedSkillCatalog(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	skillDirectory := filepath.Join(workspace, "skills", "browser-client")
	if err := os.MkdirAll(filepath.Join(skillDirectory, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDirectory, "SKILL.md"),
		[]byte("---\nname: browser-client\ndescription: browser client\n---\n\n# Browser client\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDirectory, "agents", "mintclaw.yaml"),
		[]byte("schema_version: 1\nproducts: [gateway]\nrequirements:\n  capabilities: [browser.client]\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ContextManager = "none"
	loop := NewAgentLoop(
		cfg,
		nil,
		&mockProvider{},
		WithIsolatedToolBootstrap(),
		WithIsolatedSkillBootstrap(),
	)
	t.Cleanup(loop.Close)
	agent := loop.GetRegistry().GetDefaultAgent()
	beforeRevision := agent.capabilityRevision.current()
	if prompt := agent.ContextBuilder.BuildSystemPromptWithCache(); strings.Contains(prompt, "browser-client") {
		t.Fatal("unconfigured browser capability admitted its skill")
	}
	loop.SetBrowserCapabilityClient(&agentCapabilityBrowser{available: true})
	if prompt := agent.ContextBuilder.BuildSystemPromptWithCache(); !strings.Contains(prompt, "browser-client") {
		t.Fatal("browser capability update left the compatible skill cache stale")
	}
	if got := agent.capabilityRevision.current(); got != beforeRevision+2 {
		t.Fatalf("browser capability revision = %d, want %d", got, beforeRevision+2)
	}
}

func TestGatewayRuntimePrincipalUsesCanonicalInboundActor(t *testing.T) {
	ts := &turnState{
		agentID:     "Main Agent",
		sessionKey:  "effective-session",
		executionID: "execution-1",
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{
			RouteSessionKey: "route-session",
			SessionKey:      "effective-session",
			InboundContext: &bus.InboundContext{
				Channel: "Telegram", SenderID: "transport-user", ActorID: "telegram:linked-user",
			},
		}}),
	}
	pipeline := &Pipeline{RuntimeCapabilities: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway})}

	got := runtimePrincipalForTurn(ts, pipeline)
	want := runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "telegram:linked-user", AgentID: "main-agent",
		SessionID: "route-session", ExecutionID: "execution-1",
	}
	if got != want {
		t.Fatalf("gateway runtime principal = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("gateway runtime principal validation = %v", err)
	}
}

func TestGatewayRuntimePrincipalKeepsApprovedOriginActor(t *testing.T) {
	ts := &turnState{
		agentID:     "main",
		sessionKey:  "continued-session",
		executionID: "continuation-execution",
		opts: freezeTurnInput(turnSpec{
			InteractionOriginExecution: "origin-execution",
			InteractionRouteKey:        "origin-route",
			InteractionOriginContext: &bus.InboundContext{
				Channel: "telegram", ActorID: "origin-actor",
			},
			Dispatch: DispatchRequest{
				RouteSessionKey: "route-session",
				SessionKey:      "continued-session",
				InboundContext: &bus.InboundContext{
					Channel: "telegram", ActorID: "answering-actor",
				},
			},
		}),
	}
	pipeline := &Pipeline{RuntimeCapabilities: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway})}

	got := runtimePrincipalForTurn(ts, pipeline)
	if got.ActorID != "telegram:origin-actor" || got.SessionID != "origin-route" ||
		got.ExecutionID != "origin-execution" {
		t.Fatalf("continued runtime principal = %+v", got)
	}
}

func TestCodingRuntimePrincipalIgnoresSyntheticChannelAndRepositoryFacts(t *testing.T) {
	ts := &turnState{
		agentID:     "main",
		sessionKey:  "thread-session",
		executionID: "execution-2",
		opts: freezeTurnInput(turnSpec{
			RuntimeActorID: "local:opaque-operator",
			Dispatch: DispatchRequest{
				RouteSessionKey: "thread-session",
				SessionKey:      "thread-session",
				InboundContext: &bus.InboundContext{
					Channel: "coding", SenderID: "coding", ChatID: "/private/repository/path",
				},
			},
		}),
	}
	pipeline := &Pipeline{RuntimeCapabilities: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding})}

	got := runtimePrincipalForTurn(ts, pipeline)
	want := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:opaque-operator", AgentID: "main",
		SessionID: "thread-session", ExecutionID: "execution-2",
	}
	if got != want {
		t.Fatalf("coding runtime principal = %+v, want %+v", got, want)
	}
}

func TestToolExecutionContextCarriesTurnBoundRuntimeCapabilities(t *testing.T) {
	base := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding})
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: "thread-session", ExecutionID: "execution-3",
	}
	ts := &turnState{
		agent:               &AgentInstance{ID: "main"},
		agentID:             "main",
		sessionKey:          "thread-session",
		executionID:         "execution-3",
		runtimeCapabilities: base.BindPrincipal(principal),
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{
			RouteSessionKey: "thread-session", SessionKey: "thread-session",
		}}),
	}

	toolContext := toolExecutionContextForTurn(context.Background(), ts)
	runtime, ok := toolshared.RuntimeCapabilities(toolContext)
	if !ok {
		t.Fatal("tool context is missing runtime capabilities")
	}
	got, ok := runtime.Principal()
	if !ok || got != principal {
		t.Fatalf("tool runtime principal = (%+v, %t), want %+v", got, ok, principal)
	}
	availability, ok := runtime.Report().Lookup(runtimecap.CapabilityRuntimePrincipal)
	if !ok || !availability.Available {
		t.Fatalf("turn-bound capability report = %#v", runtime.Report())
	}
}
