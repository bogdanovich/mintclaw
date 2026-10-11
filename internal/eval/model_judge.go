package eval

import (
	"context"
	"encoding/json"
	"errors"
)

type Judgement struct {
	Version        int           `json:"version"`
	Verdict        Verdict       `json:"verdict"`
	Code           string        `json:"code"`
	RubricSHA256   string        `json:"rubric_sha256,omitempty"`
	EvidenceSHA256 string        `json:"evidence_sha256,omitempty"`
	Findings       JudgeFindings `json:"findings"`
	Call           *RoleCall     `json:"call,omitempty"`
}

// This is an assessment, not a trial verdict. The runner must combine it with
// transport completeness and mandatory checks; it never replaces those facts.
func judgeConversation(ctx context.Context, judge *ModelRole, item Case, events []Event) Judgement {
	result := Judgement{
		Version: Version, Verdict: Inconclusive, Code: "judge_unavailable",
		Findings: JudgeFindings{Version: Version, Criteria: []JudgeFinding{}},
	}
	if judge == nil || judge.identity.Role != "judge" {
		return result
	}
	input, ids, err := judgeInput(item, events)
	if err != nil {
		result.Code = "ineligible_judge_evidence"
		return result
	}
	rubric, _ := json.Marshal(item.Rubric)
	result.RubricSHA256, result.EvidenceSHA256 = digest(rubric), digest([]byte(input))
	output, call, err := judge.Call(ctx, JudgePromptVersion, judgeSystemPrompt, input)
	result.Call = &call
	if err != nil {
		result.Verdict, result.Code = Error, "judge_call_failed"
		if errors.Is(err, ErrPrivateEvidence) {
			result.Verdict, result.Code = Fail, "private_judge_output"
		}
		return result
	}
	if containsPrivate(output, item.Driver.Facts) {
		result.Verdict, result.Code = Fail, "private_judge_output"
		return result
	}
	findings, err := ParseJudgeFindings([]byte(output), item.Rubric, ids)
	if err != nil {
		result.Verdict, result.Code = Error, "invalid_judge_findings"
		return result
	}
	if privateJSONValue(findings, item.Driver.Facts) {
		result.Verdict, result.Code = Fail, "private_judge_output"
		return result
	}
	result.Findings = findings
	result.Verdict, result.Code = RubricVerdict(item.Rubric, findings), "judge_completed"
	return result
}
