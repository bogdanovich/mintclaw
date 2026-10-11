package eval

import (
	"errors"
	"slices"
	"strings"
)

var ErrModelContract = errors.New("eval: invalid model action or rubric findings")

// The model selects only an intent. Prompt identities and actual fact values
// stay with the runner; they are never accepted from model-generated JSON.
func ParseUserAction(data []byte, factHandles []string, pendingKind string) (Action, error) {
	if len(data) > MaxRoleOutputBytes || !validIDs(factHandles, 64) ||
		!slices.Contains([]string{"final", "question", "approval"}, pendingKind) {
		return Action{}, ErrModelContract
	}
	action, err := DecodeManifest[Action](data)
	facts := make(map[string]string, len(factHandles))
	for _, handle := range factHandles {
		facts[handle] = "available"
	}
	if err != nil || action.validate(facts) != nil ||
		pendingKind == "approval" && action.Kind != "cancel" && action.Kind != "stop" ||
		action.Kind == "protected_answer" && pendingKind != "question" ||
		pendingKind == "final" &&
			slices.Contains([]string{"clarify", "back", "skip", "not_applicable", "cancel"}, action.Kind) {
		return Action{}, ErrModelContract
	}
	for _, line := range strings.Split(action.Text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "/") {
			return Action{}, ErrModelContract
		}
	}
	return action, nil
}

type JudgeFinding struct {
	ID          string   `json:"id"`
	Verdict     Verdict  `json:"verdict"`
	Reason      string   `json:"reason"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type JudgeFindings struct {
	Version  int            `json:"version"`
	Criteria []JudgeFinding `json:"criteria"`
}

// The judge cannot change the rubric, invent evidence, omit a required
// criterion, or supply an overall verdict that overrides deterministic checks.
func ParseJudgeFindings(data []byte, rubric []Criterion, evidenceIDs []string) (JudgeFindings, error) {
	var empty JudgeFindings
	if len(data) > MaxRoleOutputBytes || len(rubric) == 0 || len(rubric) > 16 ||
		!validIDs(evidenceIDs, 256) {
		return empty, ErrModelContract
	}
	expected := make(map[string]bool, len(rubric))
	for _, criterion := range rubric {
		if !validID(criterion.ID) || expected[criterion.ID] || !boundedText(criterion.Description, 4096) {
			return empty, ErrModelContract
		}
		expected[criterion.ID] = true
	}
	result, err := DecodeManifest[JudgeFindings](data)
	if err != nil || result.Version != Version || len(result.Criteria) != len(rubric) {
		return empty, ErrModelContract
	}
	for _, finding := range result.Criteria {
		if !expected[finding.ID] || !slices.Contains([]Verdict{Pass, Fail, Inconclusive}, finding.Verdict) ||
			!boundedText(finding.Reason, 2048) || !validIDs(finding.EvidenceIDs, 32) ||
			finding.Verdict != Inconclusive && len(finding.EvidenceIDs) == 0 ||
			!containsAll(evidenceIDs, finding.EvidenceIDs) {
			return empty, ErrModelContract
		}
		delete(expected, finding.ID)
	}
	return result, nil
}

func RubricVerdict(rubric []Criterion, findings JudgeFindings) Verdict {
	verdict := Pass
	for _, criterion := range rubric {
		if !criterion.Required {
			continue
		}
		observed := Inconclusive
		for _, finding := range findings.Criteria {
			if finding.ID == criterion.ID {
				observed = finding.Verdict
				break
			}
		}
		verdict = combineVerdicts(verdict, observed)
	}
	return verdict
}
