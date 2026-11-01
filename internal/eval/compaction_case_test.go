package eval

import (
	"encoding/json"
	"errors"
	"testing"
)

func compactionDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"general.context.compaction"}})
	if err != nil || len(selected) != 1 {
		t.Fatal("compaction definition", err)
	}
	return selected[0]
}

func enableTestCompaction(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	enableTestRestart(t, fixture)
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, compactionCapability}
	fixture.preset.Compaction = &CompactionProfile{
		HistoryMaxTokens: 128, SummaryMaxTokens: 256, FreshTailMaxTokens: 128, RecentTailTurns: 0,
	}
	fixture.preset.TargetCallBudget.MaxCalls = 32
	fixture.preset.TargetCallBudget.MaxOutputTokens = 4096
	fixture.cfg.Diagnostics.TraceCapture = evalTargetMetadataCapture(fixture.preset)
	fixture.cfg.Agents.Defaults.MaxTokens = 128
	fixture.cfg.Agents.Defaults.ContextWindow = 16384
	fixture.cfg.Agents.Defaults.MaxLLMRetries = 1
	fixture.cfg.Gateway.LogLevel, fixture.cfg.Events.Logging = "info", compactionLogging()
	raw, err := json.Marshal(fixture.preset.Compaction)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Agents.Defaults.ContextManagerConfig = raw
}

func TestCompactionCaseAndScopedNumericPolicy(t *testing.T) {
	item := compactionDefinition(t)
	fixture, _ := preparationFixture(t)
	enableTestCompaction(t, fixture)
	if item.Case.Validate() != nil || fixture.preset.Validate() != nil ||
		validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace) != nil ||
		validateCompactionBinding(fixture.preset, item.Case) != nil {
		t.Fatal("compaction admission")
	}
	if !errors.Is(CheckRunAvailability([]LoadedCase{item}, false), ErrPendingCases) {
		t.Fatal("unqualified compaction became default")
	}
	for _, kind := range []string{
		"db_path", "ignore", "stateless", "unknown", "mismatch", "duplicate", "budget_output",
		"omitted_tail", "alias_tail", "one_tail",
		"no_budget", "no_metadata", "tool", "always_skill", "payload", "model_user", "streaming", "no_profile", "ordinary_case",
	} {
		t.Run(kind, func(t *testing.T) {
			fixture, _ := preparationFixture(t)
			enableTestCompaction(t, fixture)
			cfg, preset := fixture.cfg, fixture.preset
			candidate := item.Case
			switch kind {
			case "db_path":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(
					`{"historyMaxTokens":128,"summaryMaxTokens":256,"freshTailMaxTokens":128,"recentTailTurns":0,"dbPath":"/foreign"}`,
				)
			case "ignore":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(`{"ignoreSessionPatterns":[".*"]}`)
			case "stateless":
				cfg.Agents.Defaults.ContextManager = "none"
			case "unknown":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(`{"newAuthority":true}`)
			case "mismatch":
				preset.Compaction.HistoryMaxTokens = 256
			case "duplicate":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(
					`{"historyMaxTokens":128,"historyMaxTokens":256,"summaryMaxTokens":256,"freshTailMaxTokens":128,"recentTailTurns":0}`,
				)
			case "omitted_tail":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(
					`{"historyMaxTokens":128,"summaryMaxTokens":256,"freshTailMaxTokens":128}`,
				)
			case "alias_tail":
				cfg.Agents.Defaults.ContextManagerConfig = []byte(
					`{"historyMaxTokens":128,"summaryMaxTokens":256,"freshTailMaxTokens":128,"RecentTailTurns":0}`,
				)
			case "one_tail":
				preset.Compaction.RecentTailTurns = 1
			case "budget_output":
				preset.TargetCallBudget.MaxOutputTokens = 128
			case "no_budget":
				preset.TargetCallBudget = nil
			case "no_metadata":
				preset.CaptureModelMetadata = false
			case "tool":
				cfg.Agents.List[0].ToolPolicy.Allow = []string{"read_file"}
			case "always_skill":
				cfg.Agents.List[0].Skills = []string{"some-skill"}
			case "payload":
				cfg.Events.Logging.IncludePayload = true
			case "model_user":
				preset.UserModel = "eval-model"
			case "streaming":
				cfg.ModelList[0].Streaming.Enabled = true
			case "no_profile":
				preset.Compaction = nil
			case "ordinary_case":
				candidate.Compaction = nil
			}
			if candidate.Validate() == nil && preset.Validate() == nil &&
				validateConversationProfile(cfg, preset, fixture.workspace) == nil &&
				validateCompactionBinding(preset, candidate) == nil {
				t.Fatal("unsafe compaction admitted", kind)
			}
		})
	}
	ordinary := fixture.preset
	ordinary.Compaction = nil
	ordinary.Capabilities = []string{"conversation", boundedCallsCapability}
	if validEvalContextPolicy(fixture.cfg, ordinary) {
		t.Fatal("ordinary profile gained raw context configuration authority")
	}
}

func TestCompactionTraceRetentionIsProfileScoped(t *testing.T) {
	ordinary := Preset{CaptureModelMetadata: true, Capabilities: []string{"conversation", boundedCallsCapability}}
	compaction := ordinary
	compaction.Capabilities = append(append([]string{}, ordinary.Capabilities...), compactionCapability)
	standard, expanded := evalTargetMetadataCapture(ordinary), evalTargetMetadataCapture(compaction)
	if standard != evalModelMetadataCapture() || standard.MaxTraces != 16 || expanded.MaxTraces != 64 ||
		admittedModelMetadataPolicy(expanded, ordinary) || admittedModelMetadataPolicy(standard, compaction) ||
		!admittedModelMetadataPolicy(expanded, compaction) {
		t.Fatal("compaction retention escaped its exact profile")
	}
	expanded.MaxTraces = standard.MaxTraces
	if expanded != standard {
		t.Fatal("unrelated diagnostic limits changed")
	}
}
