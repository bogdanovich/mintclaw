package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
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
