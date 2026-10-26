package eval

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
)

func restartDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"general.history.restart"}})
	if err != nil || len(selected) != 1 {
		t.Fatal("restart definition", err)
	}
	return selected[0]
}

func enableTestRestart(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, ownedRestartCapability}
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget = &limits
	fixture.preset.CaptureModelMetadata, fixture.preset.JudgeModel = true, "eval-model"
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = nil
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.MaxParallelTurns, fixture.cfg.Agents.List[0].MaxParallelTurns = 1, 1
	fixture.cfg.ModelList[0].Streaming.Enabled = false
	settings, err := gatewayclient.ChannelSettings(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Streaming.Enabled = false
}

func TestRestartCaseAndProfileAdmission(t *testing.T) {
	item := restartDefinition(t).Case
	fixture, _ := preparationFixture(t)
	enableTestRestart(t, fixture)
	if item.Validate() != nil || fixture.preset.Validate() != nil ||
		validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace) != nil ||
		validateRestartBinding(fixture.preset, item) != nil {
		t.Fatal("scoped restart admission")
	}
	if !errors.Is(CheckRunAvailability([]LoadedCase{restartDefinition(t)}, false), ErrPendingCases) {
		t.Fatal("restart became default runnable before qualification")
	}
	for _, kind := range []string{
		"no_budget", "no_metadata", "model_user", "tool", "skill", "stateless",
		"parallel", "streaming", "extra_file", "driver", "selector", "leaked_marker", "ordinary_case",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(item)
			var candidate Case
			if json.Unmarshal(data, &candidate) != nil {
				t.Fatal("clone")
			}
			fixture, _ := preparationFixture(t)
			enableTestRestart(t, fixture)
			cfg, preset := fixture.cfg, fixture.preset
			switch kind {
			case "no_budget":
				preset.TargetCallBudget = nil
			case "no_metadata":
				preset.CaptureModelMetadata = false
			case "model_user":
				preset.UserModel = "eval-model"
			case "tool":
				cfg.Agents.List[0].ToolPolicy.Allow = []string{"read_file"}
			case "skill":
				cfg.Agents.List[0].Skills = []string{"some-skill"}
			case "stateless":
				cfg.Agents.Defaults.ContextManager = "none"
			case "parallel":
				cfg.Agents.Defaults.MaxParallelTurns = 2
			case "streaming":
				cfg.ModelList[0].Streaming.Enabled = true
			case "extra_file":
				preset.WorkspaceFiles = []WorkspaceFile{{Path: "AGENTS.md", SHA256: digest(nil)}}
			case "driver":
				candidate.Driver.Mode = "llm_user"
			case "selector":
				candidate.Driver.Steps[0].Action.Text = "different recall"
			case "leaked_marker":
				candidate.Restart.Recall += " " + candidate.Restart.Marker
			case "ordinary_case":
				candidate.Restart = nil
			}
			if candidate.Validate() == nil && preset.Validate() == nil &&
				validateConversationProfile(
					cfg,
					preset,
					fixture.workspace,
				) == nil && validateRestartBinding(preset, candidate) == nil {
				t.Fatal("unsafe restart admitted", kind)
			}
		})
	}
}
