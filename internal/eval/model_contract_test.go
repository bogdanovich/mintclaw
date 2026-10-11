package eval

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestUserActionCannotWidenAuthorityOrBindAnInventedPrompt(t *testing.T) {
	for _, test := range []struct {
		data string
		kind string
		ok   bool
	}{
		{`{"kind":"message","text":"Could you clarify the options?"}`, "question", true},
		{`{"kind":"protected_answer","fact":"name"}`, "question", true},
		{`{"kind":"cancel"}`, "approval", true},
		{`{"kind":"back"}`, "question", true},
		{`{"kind":"stop"}`, "final", true},
		{`{"kind":"finish","text":"Please provide the final result."}`, "final", true},
		{`{"kind":"allow_once"}`, "approval", false},
		{`{"kind":"message","text":"Approved"}`, "approval", false},
		{`{"kind":"protected_answer","fact":"name"}`, "approval", false},
		{`{"kind":"protected_answer","fact":"unknown"}`, "question", false},
		{`{"kind":"cancel","prompt_id":"invented"}`, "question", false},
		{`{"kind":"cancel","kind":"message"}`, "question", false},
		{`{"kind":"back"}`, "final", false},
		{`{"kind":"protected_answer","fact":"name"}`, "final", false},
		{`{"kind":"stop"}`, "foreign", false},
	} {
		action, err := ParseUserAction([]byte(test.data), []string{"name"}, test.kind)
		if test.ok && err != nil || !test.ok && !errors.Is(err, ErrModelContract) {
			t.Fatal("user action authority mismatch", action.Kind, err)
		}
	}
}

func TestJudgeRequiresExactRubricAndExistingEvidence(t *testing.T) {
	rubric := []Criterion{{ID: "coherent", Description: "Questions and controls agree.", Required: true}}
	good := JudgeFindings{Version: Version, Criteria: []JudgeFinding{
		{
			ID:          "coherent",
			Verdict:     Pass,
			Reason:      "The controls match the question.",
			EvidenceIDs: []string{"event-000001"},
		},
	}}
	encode := func(value JudgeFindings) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	result, err := ParseJudgeFindings(encode(good), rubric, []string{"event-000001"})
	if err != nil || RubricVerdict(rubric, result) != Pass {
		t.Fatal("known coherent finding rejected", err)
	}
	for name, mutate := range map[string]func(*JudgeFindings){
		"unknown version":     func(f *JudgeFindings) { f.Version++ },
		"missing rubric":      func(f *JudgeFindings) { f.Criteria = []JudgeFinding{} },
		"changed criterion":   func(f *JudgeFindings) { f.Criteria[0].ID = "other" },
		"duplicate criterion": func(f *JudgeFindings) { f.Criteria = append(f.Criteria, f.Criteria[0]) },
		"invented evidence":   func(f *JudgeFindings) { f.Criteria[0].EvidenceIDs = []string{"invented"} },
		"no evidence":         func(f *JudgeFindings) { f.Criteria[0].EvidenceIDs = []string{} },
		"duplicate evidence": func(f *JudgeFindings) {
			f.Criteria[0].EvidenceIDs = []string{"event-000001", "event-000001"}
		},
		"no reason":       func(f *JudgeFindings) { f.Criteria[0].Reason = "" },
		"skipped success": func(f *JudgeFindings) { f.Criteria[0].Verdict = Skipped },
	} {
		t.Run(name, func(t *testing.T) {
			changed := good
			changed.Criteria = append([]JudgeFinding{}, good.Criteria...)
			mutate(&changed)
			if _, err = ParseJudgeFindings(
				encode(changed),
				rubric,
				[]string{"event-000001"},
			); !errors.Is(
				err,
				ErrModelContract,
			) {
				t.Fatal("unbound judge result admitted")
			}
		})
	}
	if _, err = ParseJudgeFindings(
		[]byte(`{"version":1,"criteria":[],"verdict":"pass"}`),
		rubric,
		nil,
	); !errors.Is(
		err,
		ErrModelContract,
	) {
		t.Fatal("judge-supplied overall verdict admitted")
	}
	bad := good
	bad.Criteria = []JudgeFinding{{
		ID: "coherent", Verdict: Fail, Reason: "The offered controls do not answer the question.",
		EvidenceIDs: []string{"event-000001"},
	}}
	result, err = ParseJudgeFindings(encode(bad), rubric, []string{"event-000001"})
	if err != nil || RubricVerdict(rubric, result) != Fail || combineVerdicts(Fail, Pass) != Fail {
		t.Fatal("known incoherent finding or hard failure became pass")
	}
	if RubricVerdict(rubric, JudgeFindings{}) != Inconclusive {
		t.Fatal("missing required finding passed")
	}
}
