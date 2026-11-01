package eval

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
)

func finalDeliveryDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"general.delivery.final-once"}})
	if err != nil || len(selected) != 1 {
		t.Fatal("final delivery definition", err)
	}
	return selected[0]
}

func enableTestFinalDelivery(t *testing.T, fixture *admissionFixture, streaming bool) {
	t.Helper()
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, finalDeliveryCapability}
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget = &limits
	fixture.preset.CaptureModelMetadata, fixture.preset.JudgeModel = true, "eval-model"
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = nil
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.ModelList[0].Streaming.Enabled = streaming
	settings, err := gatewayclient.ChannelSettings(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Streaming.Enabled = streaming
	settings.Streaming.ThrottleSeconds, settings.Streaming.MinGrowthChars = 0, 1
}

func TestFinalDeliveryCaseAndProfileAdmission(t *testing.T) {
	item := finalDeliveryDefinition(t)
	if item.Case.Validate() != nil || item.Availability().Default ||
		!errors.Is(CheckRunAvailability([]LoadedCase{item}, false), ErrPendingCases) {
		t.Fatal("streaming contract lost pending policy")
	}
	for _, mutation := range []string{"capability", "budget", "native", "followup_marker", "fixture", "rubric", "timeout"} {
		t.Run(mutation, func(t *testing.T) {
			data, _ := json.Marshal(item.Case)
			var changed Case
			if json.Unmarshal(data, &changed) != nil {
				t.Fatal("clone")
			}
			switch mutation {
			case "capability":
				changed.Requires = changed.Requires[:2]
			case "budget":
				changed.Requires[1] = "interactions"
			case "native":
				changed.FinalDelivery = nil
			case "followup_marker":
				changed.Driver.Steps[0].Action.Text = changed.FinalDelivery.Marker
			case "fixture":
				changed.Fixtures = []Fixture{{ID: "extra", Path: "extra.txt", Kind: "file", SHA256: digest(nil)}}
			case "rubric":
				changed.Rubric = nil
			case "timeout":
				changed.Limits.CaseTimeout = "1h"
			}
			if changed.Validate() == nil {
				t.Fatal("case bypass admitted")
			}
		})
	}
	for _, mutation := range []string{"none", "metadata", "judge", "extra_capability", "tool", "fallback", "stateless"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestFinalDelivery(t, fixture, true)
			switch mutation {
			case "metadata":
				fixture.preset.CaptureModelMetadata = false
			case "judge":
				fixture.preset.JudgeModel = ""
			case "extra_capability":
				fixture.preset.Capabilities = append(fixture.preset.Capabilities, "interactions")
			case "tool":
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"request_user_input"}
			case "fallback":
				fixture.cfg.Agents.Defaults.ModelFallbacks = []string{"another"}
			case "stateless":
				fixture.cfg.Agents.Defaults.ContextManager = "none"
			}
			err := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace)
			if (err == nil) != (mutation == "none") {
				t.Fatal("profile admission", mutation, err)
			}
			if mutation == "none" {
				target := admittedTarget{preset: fixture.preset, config: fixture.cfg}
				if !finalDeliveryStreamingMatches(target, item.Case) {
					t.Fatal("explicit stream settings lost")
				}
				fixture.cfg.ModelList[0].Streaming.Enabled = false
				if finalDeliveryStreamingMatches(target, item.Case) {
					t.Fatal("model/channel/case streaming mismatch admitted")
				}
			}
		})
	}
	if _, err := Run(t.Context(), Preset{}, []LoadedCase{item}, RunOptions{IncludePending: true}); err == nil {
		t.Fatal("unowned target admitted")
	}
}

func TestFinalDeliveryPendingEvidenceRoundTrip(t *testing.T) {
	for _, completed := range []bool{false, true} {
		domain := &finalDeliveryConversation{}
		trial := Trial{ExecutionVerdict: Inconclusive}
		if completed {
			trial.ExecutionVerdict = Pass
			domain.settled = []SteeringOutboxEvidence{{File: "first.json"}, {File: "second.json"}}
		}
		domain.conclude(t.Context(), &trial)
		data, err := json.Marshal(trial.FinalDelivery)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := DecodeManifest[FinalDeliveryEvidence](data)
		if err != nil || proof.Version != Version || completed && proof.Code != "awaiting_stop" ||
			!completed && proof.Code != "not_completed" {
			t.Fatal("pending proof cannot survive strict checkpoint decoding", err)
		}
	}
}
