package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func systemSourceTestParts() []PromptPart {
	return []PromptPart{
		{
			ID: "kernel.test", Layer: PromptLayerKernel, Slot: PromptSlotIdentity,
			Source: PromptSource{ID: PromptSourceKernel}, Content: "kernel-text",
		},
		{
			ID: "instruction.test", Layer: PromptLayerInstruction, Slot: PromptSlotWorkspace,
			Source: PromptSource{ID: PromptSourceWorkspace}, Content: "workspace-text",
		},
		{
			ID: "catalog.test", Layer: PromptLayerCapability, Slot: PromptSlotSkillCatalog,
			Source: PromptSource{ID: PromptSourceSkillCatalog}, Content: "catalog-text",
		},
		{
			ID: "active.test", Layer: PromptLayerCapability, Slot: PromptSlotActiveSkill,
			Source: PromptSource{ID: PromptSourceActiveSkills}, Content: "active-text",
		},
	}
}

func systemSourceTestMessage(parts []PromptPart) providers.Message {
	text, sections := renderPromptPartsWithSections(parts)
	return providers.Message{Role: "system", Content: text, SystemParts: []providers.ContentBlock{{
		Type: "text", Text: text, PromptSections: sections,
	}}}
}

func TestSystemSourcesIdentifyActualSectionsWithoutPublishingContent(t *testing.T) {
	parts := systemSourceTestParts()
	message := systemSourceTestMessage(parts)
	settings := traceCaptureSettings{enabled: true}
	result := fingerprintSystemSources(settings, []providers.Message{message})
	if result == nil || result.Validate() != nil || result.Code != "observed" ||
		result.SystemMessages != 1 || result.SystemParts != 4 || result.Workspace.Parts != 1 ||
		result.SkillCatalog.Parts != 1 || result.ActiveSkills.Parts != 1 ||
		result.Workspace.SHA256 != safeJSONHash(traceCaptureSettings{}, []string{"workspace-text"}) {
		t.Fatal("actual source sections were not retained", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if strings.Contains(string(data), part.Content) {
			t.Fatal("source text published")
		}
	}
	parts[2].Content = "changed-catalog"
	changed := fingerprintSystemSources(settings, []providers.Message{systemSourceTestMessage(parts)})
	if changed.SkillCatalog == result.SkillCatalog || changed.Workspace != result.Workspace ||
		changed.ActiveSkills != result.ActiveSkills {
		t.Fatal("source drift affected unrelated groups")
	}
	if fingerprintSystemSources(traceCaptureSettings{}, []providers.Message{message}) != nil {
		t.Fatal("disabled capture added metadata")
	}
	empty := fingerprintSystemSources(settings, nil)
	if empty.Validate() != nil || empty.Code != "observed" || empty.SystemMessages != 0 ||
		empty.Workspace.Parts != 0 || empty.Workspace.SHA256 != diagnostictrace.FilteredHash("[]", nil) {
		t.Fatal("actual absence became unavailable or unknown became empty", empty)
	}
}

func TestSystemSourcesFilterDecodedLiteralsBeforeHashing(t *testing.T) {
	secret := `quoted-"secret\\literal`
	parts := systemSourceTestParts()
	parts[1].Content = "prefix " + secret + " suffix"
	settings := traceCaptureSettings{enabled: true, filter: strings.NewReplacer(secret, "[REDACTED]").Replace}
	result := fingerprintSystemSources(settings, []providers.Message{systemSourceTestMessage(parts)})
	if result.Code != "observed" || result.Workspace.SHA256 !=
		safeJSONHash(traceCaptureSettings{}, []string{"prefix [REDACTED] suffix"}) {
		t.Fatal("decoded credential was not filtered before hashing", result)
	}
}

func TestSystemSourceRenderingPreservesWireTextAndUTF8Boundaries(t *testing.T) {
	parts := systemSourceTestParts()
	parts[1].Content = "workspace \u043f\u0440\u0438\u0432\u0435\u0442"
	parts[3].Content = "active \U0001f4c4"
	var previous []string
	for _, part := range sortPromptParts(parts) {
		if strings.TrimSpace(part.Content) != "" {
			previous = append(previous, part.Content)
		}
	}
	message := systemSourceTestMessage(parts)
	if message.Content != strings.Join(previous, "\n\n---\n\n") {
		t.Fatal("source metadata changed the established renderer output")
	}
	result := fingerprintSystemSources(traceCaptureSettings{enabled: true}, []providers.Message{message})
	if result.Code != "observed" ||
		result.Workspace.SHA256 != safeJSONHash(traceCaptureSettings{}, []string{parts[1].Content}) {
		t.Fatal("UTF-8 section boundaries were not preserved", result)
	}
}

func TestSystemSourcesRefuseUnboundProtectedOrOversizeSections(t *testing.T) {
	for _, mode := range []string{"plain", "missing", "start", "end", "label", "placement", "content", "type", "limit", "protected"} {
		t.Run(mode, func(t *testing.T) {
			parts := systemSourceTestParts()
			message := systemSourceTestMessage(parts)
			switch mode {
			case "plain":
				message.SystemParts = nil
			case "missing":
				message.SystemParts[0].PromptSections = nil
			case "start":
				message.SystemParts[0].PromptSections[0].Start++
			case "end":
				message.SystemParts[0].PromptSections[0].End = len(message.Content) + 1
			case "label":
				message.SystemParts[0].PromptSections[0].Layer = "\x00"
			case "placement":
				message.SystemParts[0].PromptSections[1].Slot = string(PromptSlotRuntime)
			case "content":
				message.Content += "unbound suffix"
			case "type":
				message.SystemParts[0].Type = "image"
			case "limit":
				message.SystemParts = make([]providers.ContentBlock, diagnostictrace.MaxSystemSourceParts+1)
			case "protected":
				parts[1].Content = "Read /tmp/protected-input.pdf"
				message = systemSourceTestMessage(parts)
			}
			result := fingerprintSystemSources(traceCaptureSettings{enabled: true}, []providers.Message{message})
			if result == nil || result.Validate() != nil || result.Code != "unavailable" ||
				result.Workspace.SHA256 != "" {
				t.Fatal("unbound/protected system content qualified", mode, result)
			}
		})
	}
}

func TestSystemSourceCacheKeepsOneWireBlockAndDetachedProvenance(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENTS.md":               "Use synthetic context only.",
		"skills/example/SKILL.md": "---\nname: example\ndescription: sample catalog entry\n---\n# Example\n",
	})
	defer os.RemoveAll(workspace)
	cb := newContextBuilderWithMemoryStoreAndSkills(
		workspace,
		NewMemoryStore(workspace),
		[]skills.SkillRoot{skills.WorkspaceSkillRoot(workspace)},
	)
	text, blocks := cb.buildSystemPromptForRequest(PromptBuildRequest{})
	if len(blocks) != 1 || blocks[0].Text != text || blocks[0].PromptSource != string(PromptSourceKernel) ||
		blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Fatal("default cache provider shape changed")
	}
	value := fingerprintSystemSources(
		traceCaptureSettings{enabled: true},
		[]providers.Message{{Role: "system", Content: text, SystemParts: blocks}},
	)
	if value.Code != "observed" || value.Workspace.Parts != 1 || value.SkillCatalog.Parts != 1 {
		t.Fatal("collapsed cache lost effective source identities", value)
	}
	blocks[0].PromptSections[0].Source = "mutated-copy"
	_, next := cb.buildSystemPromptForRequest(PromptBuildRequest{})
	if next[0].PromptSections[0].Source == "mutated-copy" {
		t.Fatal("caller changed cached provenance")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 8 {
				text, parts := cb.buildSystemPromptForRequest(PromptBuildRequest{})
				observed := fingerprintSystemSources(
					traceCaptureSettings{enabled: true},
					[]providers.Message{{Role: "system", Content: text, SystemParts: parts}},
				)
				if observed.Code != "observed" || observed.Workspace != value.Workspace {
					t.Error("cache pair tore")
				}
			}
		})
	}
	wg.Wait()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Changed instructions."), 0o600); err != nil {
		t.Fatal(err)
	}
	changedAt := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(workspace, "AGENTS.md"), changedAt, changedAt); err != nil {
		t.Fatal(err)
	}
	text, next = cb.buildSystemPromptForRequest(PromptBuildRequest{})
	changed := fingerprintSystemSources(
		traceCaptureSettings{enabled: true},
		[]providers.Message{{Role: "system", Content: text, SystemParts: next}},
	)
	if changed.Workspace == value.Workspace || changed.SkillCatalog != value.SkillCatalog {
		t.Fatal("cache invalidation lost source attribution")
	}
	cb.InvalidateCache()
	text, next = cb.buildSystemPromptForRequest(PromptBuildRequest{})
	refreshed := fingerprintSystemSources(traceCaptureSettings{enabled: true}, []providers.Message{
		{Role: "system", Content: text, SystemParts: next},
	})
	if *refreshed != *changed {
		t.Fatal("explicit invalidation changed the same effective source content")
	}
}

func TestSystemSourceMetadataIsDetachedAndUnavailableAfterHookRewrite(t *testing.T) {
	before := []providers.Message{systemSourceTestMessage(systemSourceTestParts())}
	after := cloneProviderMessages(before)
	after[0].SystemParts[0].PromptSections[0].Source = "mutated"
	if before[0].SystemParts[0].PromptSections[0].Source == "mutated" {
		t.Fatal("request clone aliased provenance")
	}
	visible := providerVisibleMessage(before[0])
	if visible.SystemParts[0].PromptSections != nil {
		t.Fatal("hook-visible clone exposed provenance")
	}
	restoreSystemMessagePromptMetadata(before, []providers.Message{visible})
	if !messagesEquivalent(before[0], visible) {
		t.Fatal("metadata changed canonical message comparison")
	}
	visible.SystemParts[0].Text = "rewritten text"
	visible.Content = "rewritten text"
	visible.SystemParts[0].PromptSections = before[0].SystemParts[0].PromptSections
	restoreSystemMessagePromptMetadata(before, []providers.Message{visible})
	result := fingerprintSystemSources(traceCaptureSettings{enabled: true}, []providers.Message{visible})
	if result.Code != "unavailable" || before[0].SystemParts[0].PromptSections == nil {
		t.Fatal("hook rewrite retained source proof or cleared original", result)
	}
}

func TestSystemSourcesReachNativeRequestMetadataWithoutContent(t *testing.T) {
	loop, agent, closeFixture := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer closeFixture()
	state := newTurnState(agent, makeTestTurnSpec("system-source-request"), turnEventScope{})
	pipeline := newTestPipeline(loop)
	cfg := *pipeline.Cfg
	cfg.Diagnostics.TraceCapture.Enabled, cfg.Diagnostics.TraceCapture.ContentMode = true, "metadata_only"
	pipeline.Cfg = &cfg
	emitter := &captureRuntimeEmitter{}
	pipeline.events = emitter
	llm := newLLMIterationState(1)
	llm.callMessages = []providers.Message{systemSourceTestMessage(systemSourceTestParts())}
	llm.llmModel = "synthetic-model"
	if pipeline.emitLLMRequestAttempt(state, &turnExecution{}, llm, llm.callMessages) == "" ||
		len(emitter.events) != 1 {
		t.Fatal("native request event missing")
	}
	payload := emitter.events[0].payload.(LLMRequestPayload)
	if payload.SystemSources == nil || payload.SystemSources.Code != "observed" {
		t.Fatal("request lost source proof")
	}
	now := time.Now()
	record, _, ok := runtimeEventRecord(
		traceCaptureSettingsFromConfig(&cfg),
		&activeTraceCapture{startedAt: now},
		runtimeevents.Event{
			ID: "request-source-event", Kind: runtimeevents.KindAgentLLMRequest, Time: now, Payload: payload,
		},
	)
	var native diagnostictrace.ModelPayload
	if !ok || json.Unmarshal(record.Data, &native) != nil || native.SystemSources == nil ||
		!reflect.DeepEqual(native.SystemSources, payload.SystemSources) {
		t.Fatal("native projection lost source proof")
	}
	for _, part := range systemSourceTestParts() {
		if strings.Contains(string(record.Data), part.Content) {
			t.Fatal("metadata-only trace contains prompt text")
		}
	}
	malformed := *payload.SystemSources
	malformed.Workspace.SHA256 = "private-malformed-hash"
	guarded := diagnosticSystemSources(traceCaptureSettings{}, &malformed)
	if guarded.Code != "unavailable" || guarded.Validate() != nil {
		t.Fatal("malformed producer metadata qualified")
	}
}

func TestSystemSourcesSurviveOrdinaryNativeTurnAssembly(t *testing.T) {
	loop, agent, closeFixture := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer closeFixture()
	pipeline := newTestPipeline(loop)
	cfg := *pipeline.Cfg
	cfg.Diagnostics.TraceCapture.Enabled, cfg.Diagnostics.TraceCapture.ContentMode = true, "metadata_only"
	pipeline.Cfg = &cfg
	emitter := &captureRuntimeEmitter{}
	pipeline.events = emitter
	state := newTurnState(agent, makeTestTurnSpec("source-native-turn"), turnEventScope{})
	if _, err := runTestTurn(loop, t.Context(), state, pipeline); err != nil {
		t.Fatal(err)
	}
	requests := 0
	for _, event := range emitter.events {
		if event.kind != runtimeevents.KindAgentLLMRequest {
			continue
		}
		requests++
		payload := event.payload.(LLMRequestPayload)
		if payload.SystemSources == nil || payload.SystemSources.Validate() != nil ||
			payload.SystemSources.Code != "observed" || payload.SystemSources.SystemMessages == 0 {
			t.Fatal("ordinary native turn lost effective source provenance", payload.SystemSources)
		}
	}
	if requests != 1 {
		t.Fatal("native turn did not make the expected single stub call", requests)
	}
}
