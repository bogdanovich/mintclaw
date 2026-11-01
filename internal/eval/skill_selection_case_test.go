package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func skillSelectionDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"general.skills.selection"}})
	if err != nil || len(selected) != 1 {
		t.Fatal("skill selection definition", err)
	}
	return selected[0]
}

func enableTestSkillSelection(t *testing.T, fixture *admissionFixture, item Case) {
	t.Helper()
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, skillSelectionCapability}
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget = &limits
	fixture.preset.CaptureModelMetadata, fixture.preset.JudgeModel = true, "eval-model"
	fixture.preset.WorkspaceFiles = []WorkspaceFile{{Path: item.SkillSelection.path(), SHA256: item.Fixtures[0].SHA256}}
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = nil
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.MaxParallelTurns, fixture.cfg.Agents.List[0].MaxParallelTurns = 1, 1
	fixture.cfg.Gateway.LogLevel, fixture.cfg.Events.Logging = "info", skillSelectionLogging()
	fixture.cfg.ModelList[0].Streaming.Enabled = false
	settings, err := gatewayclient.ChannelSettings(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Streaming.Enabled = false
	data, err := os.ReadFile(filepath.Join("../../evals", item.Fixtures[0].Path))
	if err != nil || digest(data) != item.Fixtures[0].SHA256 {
		t.Fatal("pinned skill source", err)
	}
	path := filepath.Join(fixture.workspace, item.SkillSelection.path())
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil || os.WriteFile(path, data, 0o600) != nil {
		t.Fatal("synthetic skill input")
	}
}

func TestSkillSelectionCaseAndProfileAdmission(t *testing.T) {
	item := skillSelectionDefinition(t).Case
	fixture, _ := preparationFixture(t)
	enableTestSkillSelection(t, fixture, item)
	caseErr, presetErr := item.Validate(), fixture.preset.Validate()
	policyErr := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace)
	profileErr := validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace)
	bindingErr := validateSkillSelectionBinding(fixture.preset, item)
	if caseErr != nil || presetErr != nil || policyErr != nil || profileErr != nil || bindingErr != nil {
		t.Fatalf("scoped admission: case=%v preset=%v policy=%v profile=%v binding=%v",
			caseErr, presetErr, policyErr, profileErr, bindingErr)
	}
	for _, kind := range []string{
		"capability", "fixture", "control", "selector", "leaked_output", "driver", "rubric", "timeout",
		"no_budget", "no_metadata", "model_user", "tool", "always_selected", "payload_logging", "warn_log",
		"extra_file", "different_sha", "fallback", "streaming", "stateless", "parallel", "ordinary_case",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(item)
			var candidate Case
			if json.Unmarshal(data, &candidate) != nil {
				t.Fatal("clone case")
			}
			fixture, _ := preparationFixture(t)
			enableTestSkillSelection(t, fixture, candidate)
			cfg, preset := fixture.cfg, fixture.preset
			switch kind {
			case "capability":
				candidate.Requires = []string{"conversation", boundedCallsCapability}
			case "fixture":
				candidate.Fixtures = nil
			case "control":
				candidate.Driver.Steps[1].Action.Text = candidate.Driver.Steps[0].Action.Text
			case "selector":
				candidate.Driver.Steps[0].Action.Text = candidate.Task
			case "leaked_output":
				candidate.Task += " " + candidate.SkillSelection.Output
			case "driver":
				candidate.Driver.Mode = "llm_user"
			case "rubric":
				candidate.Rubric = nil
			case "timeout":
				candidate.Limits.CaseTimeout = "1h"
			case "no_budget":
				preset.TargetCallBudget = nil
			case "no_metadata":
				preset.CaptureModelMetadata = false
			case "model_user":
				preset.UserModel = "eval-model"
			case "tool":
				cfg.Agents.List[0].ToolPolicy.Allow = []string{"read_file"}
			case "always_selected":
				cfg.Agents.List[0].Skills = []string{candidate.SkillSelection.Name}
			case "payload_logging":
				cfg.Events.Logging.IncludePayload = true
			case "warn_log":
				cfg.Gateway.LogLevel = "warn"
			case "extra_file":
				preset.WorkspaceFiles = append(
					preset.WorkspaceFiles,
					WorkspaceFile{Path: "AGENTS.md", SHA256: digest(nil)},
				)
			case "different_sha":
				preset.WorkspaceFiles[0].SHA256 = digest([]byte("different skill"))
			case "fallback":
				cfg.Agents.Defaults.ModelFallbacks = []string{"eval-model"}
			case "streaming":
				cfg.ModelList[0].Streaming.Enabled = true
			case "stateless":
				cfg.Agents.Defaults.ContextManager = "none"
			case "parallel":
				cfg.Agents.Defaults.MaxParallelTurns = 2
			case "ordinary_case":
				candidate.SkillSelection = nil
			}
			if candidate.Validate() == nil && preset.Validate() == nil &&
				validateConversationProfile(cfg, preset, fixture.workspace) == nil &&
				validateSkillSelectionBinding(preset, candidate) == nil {
				t.Fatal("unsafe/ambiguous skill candidate admitted", kind)
			}
		})
	}
}

func TestSkillSelectionRemainsPendingAndFrozenProofRoundTrips(t *testing.T) {
	item := skillSelectionDefinition(t)
	if !errors.Is(CheckRunAvailability([]LoadedCase{item}, false), ErrPendingCases) {
		t.Fatal("unqualified skill case became default runnable")
	}
	proof := SkillSelectionEvidence{
		Version: Version, Code: "awaiting_stop", Before: ReadFixtureSnapshot{Code: "observed"},
		After: ReadFixtureSnapshot{Code: "unavailable"}, History: []string{}, Log: []string{},
		Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
	}
	data, err := json.Marshal(proof)
	if _, decodeErr := decodeDocument[SkillSelectionEvidence](data, 8*1024*1024); err != nil || decodeErr != nil {
		t.Fatal("pending proof does not round trip", err, decodeErr)
	}
	if expectedSkillContextHash(item.Case, "/synthetic/eval", false) ==
		expectedSkillContextHash(item.Case, "/synthetic/eval", true) {
		t.Fatal("selected native identity collapsed to the empty control")
	}
}

func TestSkillSelectionPublicPolicyPrecedesCredentialAdmission(t *testing.T) {
	item := skillSelectionDefinition(t).Case
	fixture, _ := preparationFixture(t)
	enableTestSkillSelection(t, fixture, item)
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	preview, _, err := inspectProfileFiles(fixture.preset, fixture.home)
	if err != nil || validateConversationPolicy(preview.config, fixture.preset, fixture.workspace) != nil {
		t.Fatal("public policy improperly required secret resolution", err)
	}
	if _, err := gatewayclient.ChannelSettings(preview.config); err == nil ||
		validateConversationProfile(preview.config, fixture.preset, fixture.workspace) == nil {
		t.Fatal("public preview bypassed authenticated runtime admission")
	}
}
