package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestPrepareDocumentTurnActivatesVerifiedPDFWithoutExposingPath(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	registry.Register(tools.NewBM25SearchTool(registry, 5, 5))
	agent := &AgentInstance{
		ID: "main", Workspace: workspace, Tools: registry, ContextBuilder: NewContextBuilder(workspace),
	}
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "untrusted name.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nPRIVATE_BYTES\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename: "untrusted name.pdf", ContentType: "application/octet-stream",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "turn")
	if err != nil {
		t.Fatal(err)
	}
	ts := documentTestTurnState(agent, ref)
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	pipeline := &Pipeline{Context: PipelineContextServices{MediaResolver: store}}
	pipeline.prepareDocumentTurn(ts)
	if len(ts.documentProjections) != 1 || !containsFold(ts.activeSkills, "pdf") {
		t.Fatalf("projections=%#v skills=%#v", ts.documentProjections, ts.activeSkills)
	}
	if skillContext := agent.ContextBuilder.buildActiveSkillsContext(ts.activeSkills); !strings.Contains(
		skillContext,
		"PDF test skill body",
	) {
		t.Fatalf("active PDF skill body is missing: %q", skillContext)
	}
	messages := pipeline.resolveDocumentTurnMedia([]providers.Message{{
		Role: "user", Content: "read it", Media: []string{ref},
	}}, ts, config.DefaultMaxMediaSize)
	if len(messages) != 1 || len(messages[0].Attachments) != 1 || messages[0].Attachments[0].Ref != ref ||
		strings.Contains(messages[0].Content, path) || strings.Contains(messages[0].Content, "PRIVATE_BYTES") ||
		strings.Contains(messages[0].Content, "[file:") || len(messages[0].Media) != 0 {
		t.Fatalf("unsafe projected messages: %#v", messages)
	}
}

func TestUnrelatedTurnHasNoPDFSkillBodyOrDocumentSchema(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	agent := &AgentInstance{
		ID: "main", Workspace: workspace, Tools: registry, ContextBuilder: NewContextBuilder(workspace),
	}
	ensureDocumentToolDiscovery(agent)
	ts := documentTestTurnState(agent, "")
	ts.media = nil
	(&Pipeline{}).prepareDocumentTurn(ts)
	if skillContext := agent.ContextBuilder.buildActiveSkillsContext(ts.activeSkills); strings.Contains(
		skillContext,
		"PDF test skill body",
	) {
		t.Fatalf("unrelated turn loaded PDF skill: %q", skillContext)
	}
	if providerDefsContainTool(registry.ToProviderDefs(), "document") {
		t.Fatal("unrelated turn exposed hidden document schema")
	}
}

func TestPrepareDocumentTurnRefusesFakePDFWithoutActivation(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	registry.Register(tools.NewBM25SearchTool(registry, 5, 5))
	agent := &AgentInstance{
		ID: "main", Workspace: workspace, Tools: registry, ContextBuilder: NewContextBuilder(workspace),
	}
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "fake.pdf")
	if err := os.WriteFile(path, []byte("definitely not PDF bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename: "fake.pdf", ContentType: "application/pdf", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "turn")
	if err != nil {
		t.Fatal(err)
	}
	ts := documentTestTurnState(agent, ref)
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	pipeline := &Pipeline{Context: PipelineContextServices{MediaResolver: store}}
	pipeline.prepareDocumentTurn(ts)
	if len(ts.documentProjections) != 0 || len(ts.documentRejections) != 1 || containsFold(ts.activeSkills, "pdf") {
		t.Fatalf(
			"projections=%#v rejections=%#v skills=%#v",
			ts.documentProjections,
			ts.documentRejections,
			ts.activeSkills,
		)
	}
	messages := pipeline.resolveDocumentTurnMedia([]providers.Message{{
		Role: "user", Content: "read it", Media: []string{ref},
	}}, ts, config.DefaultMaxMediaSize)
	if len(messages[0].Attachments) != 1 || !strings.Contains(messages[0].Content, "unsupported_type") ||
		strings.Contains(messages[0].Content, path) || len(messages[0].Media) != 0 {
		t.Fatalf("fake PDF was not safely refused: %#v", messages)
	}
}

func TestPrepareDocumentTurnKeepsSameNamePDFRefsAndDigestsDistinct(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	registry.Register(tools.NewBM25SearchTool(registry, 5, 5))
	agent := &AgentInstance{
		ID: "main", Workspace: workspace, Tools: registry, ContextBuilder: NewContextBuilder(workspace),
	}
	store := media.NewFileMediaStore()
	var refs []string
	for index, data := range []string{
		"%PDF-1.7\nFIRST_DOCUMENT\n%%EOF\n",
		"%PDF-1.7\nSECOND_DOCUMENT\n%%EOF\n",
	} {
		dir := filepath.Join(t.TempDir(), string(rune('a'+index)))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "same-name.pdf")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		ref, err := store.Store(path, media.MediaMeta{
			Filename:      "same-name.pdf",
			ContentType:   "application/pdf",
			CleanupPolicy: media.CleanupPolicyForgetOnly,
		}, "turn")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	ts := documentTestTurnState(agent, refs[0])
	ts.media = append([]string(nil), refs...)
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if err = store.BindOwner(ref, owner); err != nil {
			t.Fatal(err)
		}
	}
	pipeline := &Pipeline{Context: PipelineContextServices{MediaResolver: store}}
	pipeline.prepareDocumentTurn(ts)
	if len(ts.documentProjections) != 2 ||
		ts.documentProjections[0].Ref == ts.documentProjections[1].Ref ||
		ts.documentProjections[0].SourceSHA256 == ts.documentProjections[1].SourceSHA256 {
		t.Fatalf("same-name projections lost identity: %#v", ts.documentProjections)
	}
	messages := pipeline.resolveDocumentTurnMedia([]providers.Message{{
		Role: "user", Content: "compare them", Media: append([]string(nil), refs...),
	}}, ts, config.DefaultMaxMediaSize)
	if len(messages[0].Attachments) != 2 || !strings.Contains(messages[0].Content, refs[0]) ||
		!strings.Contains(messages[0].Content, refs[1]) || strings.Contains(messages[0].Content, "FIRST_DOCUMENT") ||
		strings.Contains(messages[0].Content, "SECOND_DOCUMENT") {
		t.Fatalf("same-name projected messages = %#v", messages)
	}
}

func TestDocumentWorkflowRespectsTurnToolAndSkillPolicy(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	registry.Register(tools.NewBM25SearchTool(registry, 5, 5))
	agent := &AgentInstance{
		ID: "main", Workspace: workspace, Tools: registry, ContextBuilder: NewContextBuilder(workspace),
	}
	base := documentTestTurnState(agent, "media://current")
	if !documentWorkflowAllowed(base) {
		t.Fatal("default profile denied document workflow")
	}
	base.profile = config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeOff, SkillsMode: config.TurnProfileModeDefault,
	}
	if documentWorkflowAllowed(base) {
		t.Fatal("tools-off profile admitted document workflow")
	}
	base.profile = config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeDefault, SkillsMode: config.TurnProfileModeOff,
	}
	if documentWorkflowAllowed(base) {
		t.Fatal("skills-off profile admitted document workflow")
	}
}

func TestSelectPrimaryCandidatesCannotUseLightRoute(t *testing.T) {
	manager := &modelExecutionManager{}
	primary := providers.FallbackCandidate{Model: "primary"}
	light := providers.FallbackCandidate{Model: "light"}
	decision := manager.selectPrimaryCandidates(effectiveExecutionState{
		Model:           "primary",
		Candidates:      []providers.FallbackCandidate{primary},
		LightCandidates: []providers.FallbackCandidate{light},
	}, "")
	if decision.usedLight || decision.model != "primary" || len(decision.activeCandidates) != 1 ||
		decision.activeCandidates[0].Model != "primary" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestDocumentRenderContextFailsClosedAfterBeforeLLMNonVisionRewrite(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "vision-model", MaxTokens: 4096, MaxToolIterations: 3,
		}},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "vision-model", Provider: "openai", Model: "vision-model", Enabled: true,
				Capabilities: &config.ModelCapabilities{Vision: &config.ModelCapabilityOverride{}},
			},
			{ModelName: "text-model", Provider: "openai", Model: "text-model", Enabled: true},
		},
	}
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{Content: "must not be called"}}}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	useTestSideQuestionProvider(loop, provider)
	if err := loop.MountHook(NamedHook("rewrite-document-model", modelRewriteHook{model: "text-model"})); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	pipeline := newTestPipeline(loop)
	ts := newTurnState(agent, makeTestTurnSpec("document-rewrite-session"), turnEventScope{
		turnID: "document-rewrite-turn", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	exec.messages = append(exec.messages, providers.Message{
		Role: "tool", ToolCallID: "render-call", Content: "rendered page",
		Media: []string{"data:image/png;base64,aGVsbG8="},
	})
	exec.liveToolContexts = []liveToolContextProjection{{
		toolCallID:             "render-call",
		requiresDocumentVision: true,
	}}
	llm := newLLMIterationState(2)
	if stage, prepareErr := pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); prepareErr != nil ||
		stage.disposition == llmStageComplete {
		t.Fatalf("prepareLLMRequest() stage=%#v error=%v", stage, prepareErr)
	}
	if !llm.requiresDocumentVision || exec.model.llmModelName != "text-model" {
		t.Fatalf(
			"rewritten document request = requires_vision:%v model:%q",
			llm.requiresDocumentVision,
			exec.model.llmModelName,
		)
	}
	if _, invokeErr := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); invokeErr == nil ||
		!strings.Contains(invokeErr.Error(), "configured vision-capable model route") {
		t.Fatalf("invokeLLMWithRetry() error = %v, want fail-closed vision route error", invokeErr)
	}
	if provider.callCount != 0 {
		t.Fatalf("non-vision rewritten provider calls = %d, want 0", provider.callCount)
	}
}

func TestDocumentRenderContextExcludesNonVisionFallbackCandidates(t *testing.T) {
	visionConfig := &config.ModelConfig{
		ModelName: "vision", Provider: "openai", Model: "vision", Enabled: true,
		Capabilities: &config.ModelCapabilities{Vision: &config.ModelCapabilityOverride{}},
	}
	textConfig := &config.ModelConfig{
		ModelName: "text", Provider: "openai", Model: "text", Enabled: true,
	}
	pipeline := &Pipeline{Cfg: &config.Config{ModelList: []*config.ModelConfig{visionConfig, textConfig}}}
	candidates := []providers.FallbackCandidate{
		{
			Provider: "openai", Model: "vision", IdentityKey: modelConfigIdentityKey(visionConfig), ConfigOrdinal: 1,
		},
		{
			Provider: "openai", Model: "text", IdentityKey: modelConfigIdentityKey(textConfig), ConfigOrdinal: 2,
		},
	}
	eligible := pipeline.documentVisionCandidates(t.TempDir(), candidates)
	if len(eligible) != 1 || eligible[0].StableKey() != candidates[0].StableKey() {
		t.Fatalf("eligible document render candidates = %#v, want only configured vision route", eligible)
	}
}

func TestDocumentRenderContextAcceptsAlreadyAppliedVisionOverride(t *testing.T) {
	workspace := t.TempDir()
	visionTarget := &config.ModelConfig{
		ModelName: "vision-target", Provider: "openai", Model: "vision-target", Enabled: true,
	}
	candidate := providers.FallbackCandidate{
		Provider: "openai", Model: "vision-target",
		IdentityKey: modelConfigIdentityKey(visionTarget), ConfigOrdinal: 1,
	}
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{Content: "page inspected"}}}
	pipeline := &Pipeline{Cfg: &config.Config{ModelList: []*config.ModelConfig{visionTarget}}}
	agent := &AgentInstance{ID: "main", Workspace: workspace}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "document-vision-override-turn", workspace: workspace,
		opts: freezeTurnInput(makeTestTurnSpec("document-vision-override-session")),
	}
	exec := &turnExecution{model: turnExecutionModel{
		activeCandidates: []providers.FallbackCandidate{candidate},
		activeProvider:   provider,
		candidateProviders: map[string]providers.LLMProvider{
			candidateProviderKey(candidate): provider,
		},
		llmModelName: "vision-target",
		visionRoute:  visionRouteModelOverride,
	}}
	llm := newLLMIterationState(2)
	llm.callMessages = []providers.Message{{
		Role: "tool", ToolCallID: "document-render", Content: "rendered page",
		Media: []string{"data:image/png;base64,aGVsbG8="},
	}}
	llm.llmModel = "vision-target"
	llm.llmOpts = map[string]any{}
	llm.requiresDocumentVision = true
	if _, err := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); err != nil {
		t.Fatalf("invokeLLMWithRetry() error = %v", err)
	}
	if provider.callCount != 1 || !llm.documentVisionResolved || !llm.documentVisionAvailable {
		t.Fatalf(
			"applied override capability = calls:%d resolved:%v vision:%v",
			provider.callCount,
			llm.documentVisionResolved,
			llm.documentVisionAvailable,
		)
	}
}

func TestActualModelCapabilityReplacesStaleDocumentRenderAuthority(t *testing.T) {
	workspace := t.TempDir()
	visionConfig := &config.ModelConfig{
		ModelName: "vision-primary", Provider: "openai", Model: "vision-primary", Enabled: true,
		Capabilities: &config.ModelCapabilities{Vision: &config.ModelCapabilityOverride{}},
	}
	textConfig := &config.ModelConfig{
		ModelName: "text-fallback", Provider: "openai", Model: "text-fallback", Enabled: true,
	}
	candidates := []providers.FallbackCandidate{
		{
			Provider: "openai", Model: "vision-primary",
			IdentityKey: modelConfigIdentityKey(visionConfig), ConfigOrdinal: 1,
		},
		{
			Provider: "openai", Model: "text-fallback",
			IdentityKey: modelConfigIdentityKey(textConfig), ConfigOrdinal: 2,
		},
	}
	primary := &sequenceProvider{errors: []error{errors.New("rate limit exceeded")}}
	fallback := &sequenceProvider{responses: []*providers.LLMResponse{{Content: "text-only fallback completed"}}}
	cfg := &config.Config{ModelList: []*config.ModelConfig{visionConfig, textConfig}}
	pipeline := &Pipeline{
		Cfg: cfg,
		Interaction: PipelineInteractionServices{Fallback: providers.NewFallbackChain(
			providers.NewCooldownTracker(),
			nil,
		)},
	}
	agent := &AgentInstance{ID: "main", Workspace: workspace}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "document-fallback-turn", workspace: workspace,
		documentVisionAvailable: true,
		opts:                    freezeTurnInput(makeTestTurnSpec("document-fallback-session")),
	}
	exec := &turnExecution{
		messages: []providers.Message{{Role: "user", Content: "inspect and render the PDF"}},
		model: turnExecutionModel{
			activeCandidates: candidates,
			activeProvider:   primary,
			candidateProviders: map[string]providers.LLMProvider{
				candidateProviderKey(candidates[0]): primary,
				candidateProviderKey(candidates[1]): fallback,
			},
			llmModelName: "vision-primary",
		},
	}
	llm := newLLMIterationState(1)
	llm.callMessages = append([]providers.Message(nil), exec.messages...)
	llm.llmModel = "vision-primary"
	llm.llmOpts = map[string]any{}
	if _, err := pipeline.invokeLLMWithRetry(t.Context(), t.Context(), ts, exec, llm); err != nil {
		t.Fatalf("invokeLLMWithRetry() error = %v", err)
	}
	if primary.callCount != 1 || fallback.callCount != 1 || !llm.documentVisionResolved ||
		llm.documentVisionAvailable {
		t.Fatalf(
			"actual fallback capability = primary:%d fallback:%d resolved:%v vision:%v",
			primary.callCount,
			fallback.callCount,
			llm.documentVisionResolved,
			llm.documentVisionAvailable,
		)
	}
	if _, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm); err != nil {
		t.Fatalf("normalizeAndDispatchLLMResponse() error = %v", err)
	}
	toolCtx := toolExecutionContextForTurn(context.Background(), ts)
	if toolshared.ToolDocumentVisionAvailable(toolCtx) {
		t.Fatal("stale initial-model vision authority survived the actual text-only model response")
	}
}

func TestDocumentSchemaIsDeferredUntilExistingToolSearchPromotesIt(t *testing.T) {
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	agent := &AgentInstance{
		ID: "main", Workspace: t.TempDir(), Tools: registry, ContextBuilder: NewContextBuilder(t.TempDir()),
	}
	ensureDocumentToolDiscovery(agent)
	if providerDefsContainTool(registry.ToProviderDefs(), "document") {
		t.Fatal("hidden document schema was visible before discovery")
	}
	search, ok := registry.Get(tools.BM25SearchToolName)
	if !ok {
		t.Fatal("existing BM25 discovery tool was not registered")
	}
	result := search.Execute(t.Context(), map[string]any{"query": "inspect extract render PDF document"})
	if result.IsError || !strings.Contains(result.ForLLM, `"name":"document"`) {
		t.Fatalf("discovery result = %#v", result)
	}
	if !providerDefsContainTool(registry.ToProviderDefs(), "document") {
		t.Fatal("document schema was not promoted by existing discovery")
	}
}

func providerDefsContainTool(definitions []providers.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Function.Name == name {
			return true
		}
	}
	return false
}

func documentTestTurnState(agent *AgentInstance, ref string) *turnState {
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat", SenderID: "sender", ActorID: "actor",
	}
	return &turnState{
		agent: agent,
		opts: turnInput{turnIdentity: turnIdentity{Dispatch: DispatchRequest{
			RouteSessionKey: "route", SessionKey: "session", InboundContext: inbound,
		}}},
		workspace: agent.Workspace,
		channel:   inbound.Channel,
		chatID:    inbound.ChatID,
		media:     []string{ref},
	}
}

func writePDFSkillForTest(t *testing.T, workspace string) {
	t.Helper()
	dir := filepath.Join(workspace, "skills", "pdf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: pdf
description: test PDF skill
---
PDF test skill body.
`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
