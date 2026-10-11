package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type calibrationTestProvider struct {
	roleTestProvider
	answers []Verdict
	inputs  []string
}

func (provider *calibrationTestProvider) Chat(
	ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition,
	model string, options map[string]any,
) (*providers.LLMResponse, error) {
	provider.inputs = append(provider.inputs, messages[1].Content)
	var input struct {
		Rubric []Criterion `json:"rubric"`
	}
	if json.Unmarshal([]byte(messages[1].Content), &input) != nil || len(input.Rubric) != 1 {
		return nil, errors.New("invalid calibration projection")
	}
	answer := provider.answers[len(provider.inputs)-1]
	data, _ := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{{
		ID: input.Rubric[0].ID, Verdict: answer, Reason: "Synthetic labeled provider response, not a live judgement.",
		EvidenceIDs: []string{"event-000001"},
	}}})
	provider.response = &providers.LLMResponse{Content: string(data), FinishReason: "stop"}
	return provider.roleTestProvider.Chat(ctx, messages, tools, model, options)
}

func TestPinnedCalibrationIsStrictAndLabelsAreNotModelInput(t *testing.T) {
	calibration, sha, err := LoadCalibration("../../evals/calibration/judge-v1.json")
	if err != nil || len(calibration.Examples) != 6 || !validDigest(sha) {
		t.Fatal("pinned calibration invalid", err)
	}
	for _, mismatch := range []bool{false, true} {
		provider := &calibrationTestProvider{roleTestProvider: roleTestProvider{mediated: true}}
		for _, example := range calibration.Examples {
			provider.answers = append(provider.answers, example.Expected)
		}
		if mismatch {
			provider.answers[1] = Pass
		}
		judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
		result, runErr := Calibrate(t.Context(), judge, calibration, sha)
		if !mismatch && (runErr != nil || result.Verdict != Pass) ||
			mismatch && (!errors.Is(runErr, ErrNotPassed) || result.Verdict != Fail) || len(result.Examples) != 6 {
			t.Fatal("calibration hid disagreement or lost examples", result.Verdict, runErr)
		}
		for _, input := range provider.inputs {
			if strings.Contains(input, `"expected"`) || strings.Contains(input, "mismatched-choices") ||
				strings.Contains(input, "calibration") {
				t.Fatal("judge saw hidden calibration labels")
			}
		}
	}
	calibration.Examples[0].Expected = Skipped
	if calibration.Validate() == nil {
		t.Fatal("unsupported manual label admitted")
	}
}
