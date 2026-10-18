package document

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormIntakeAdmissionOrderSurvivesEqualRevisionsReplayAndRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	mode, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	var references []string
	for _, text := range []string{"A retained value with an unspecified role.", "That value belongs to the first person."} {
		message := intakeMessage(request, mode.Progress.ModeID, request.ExpectedRevision, text, text)
		admitted, admissionErr := store.AdmitFormIntakeMessage(t.Context(), message)
		if admissionErr != nil {
			t.Fatal(admissionErr)
		}
		replay, replayErr := store.AdmitFormIntakeMessage(t.Context(), message)
		if replayErr != nil || replay.Receipt != admitted.Receipt || !replay.Reused {
			t.Fatal("same ingress identity changed admission")
		}
		references = append(references, admitted.Receipt)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for index, reference := range references {
		input, readErr := store.ReadFormIntakeInput(t.Context(), reference, request.Owner)
		if readErr != nil || input.AdmissionOrder != index+1 || input.AdmittedRevision != request.ExpectedRevision {
			t.Fatal("native receipt order was lost or inferred from equal revisions")
		}
		encoded, marshalErr := json.Marshal(input)
		if marshalErr != nil || strings.Contains(string(encoded), "admission_order") ||
			strings.Contains(string(encoded), input.Text) {
			t.Fatal("private chronology or text escaped ordinary JSON")
		}
	}
	consume := reserveIntakeApply(t, store, request, references[0], request.ExpectedRevision, []int{0}, "consume-prose")
	consume.NonData = []FormIntakeLiteral{intakeLiteral("A retained value with an unspecified role.")}
	if result, applyErr := store.ApplyFormIntakePass(t.Context(), consume); applyErr != nil || !result.Consumed {
		t.Fatal("could not establish consumed-receipt chronology")
	}
	for index, reference := range references {
		input, readErr := store.ReadFormIntakeInput(t.Context(), reference, request.Owner)
		if readErr != nil || input.AdmissionOrder != index+1 {
			t.Fatal("consuming an earlier receipt renumbered immutable native admissions")
		}
	}
}
