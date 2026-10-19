package eval

import "testing"

func TestQuestionOrApprovalExpectationOnlyAdmitsCancelForBrowserDenial(t *testing.T) {
	item := browserCorpusCase(t, "denied").Case
	if item.Driver.Steps[0].WaitFor.Kind != "question_or_approval" || item.Validate() != nil {
		t.Fatal("denial must accept either permission shape")
	}
	for _, kind := range []string{"question", "approval", "final", "stream", "canceled", "error", ""} {
		want := kind == "question" || kind == "approval"
		if item.Driver.Steps[0].WaitFor.matches(kind, "permission") != want {
			t.Fatal("permission family admitted an unrelated outcome", kind)
		}
	}
	expected := Expectation{Kind: "question_or_approval", Contains: "permission"}
	if expected.matches("final", "permission") || expected.matches("question", "unrelated") ||
		!expected.matches("approval", "request permission") {
		t.Fatal("permission matching lost kind or literal constraints")
	}
	for _, action := range []string{"allow_once", "confirm", "message", "finish", "stop", "protected_answer"} {
		copy := item
		request := Action{Kind: action}
		if action == "message" || action == "finish" {
			request.Text = "synthetic follow-up"
		}
		if action == "protected_answer" {
			request.Fact = "value"
			copy.Driver.Facts = map[string]string{"value": "synthetic"}
		}
		copy.Driver.Steps = []Step{{WaitFor: Expectation{Kind: "question_or_approval"}, Action: request}}
		if request.validate(copy.Driver.Facts) != nil || copy.Driver.validate(copy.Limits) == nil {
			t.Fatal("permission family did not independently reject a valid non-cancel action", action)
		}
		if copy.Validate() == nil {
			t.Fatal("permission family admitted a non-cancel action", action)
		}
	}
	withoutRubric := item
	withoutRubric.Rubric = nil
	if withoutRubric.Validate() == nil {
		t.Fatal("permission family admitted no required semantic judge")
	}
	general := testCase()
	general.Driver.Steps = item.Driver.Steps
	if general.Validate() == nil {
		t.Fatal("permission-family admission escaped the denied browser scenario")
	}
}
