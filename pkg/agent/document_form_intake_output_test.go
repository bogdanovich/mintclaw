package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentIntakeOutputScopeUsesBoundedPrivateMetadataAndStrictInstructions(t *testing.T) {
	view := privateIntakeTestView()
	view.OutputMode, view.OutputScope = document.FormOutputEditableAcroForm, document.FormOutputEditableDraft
	view.DeferredFieldIDs = []string{"field_birth"}
	payload, err := encodeDocumentIntakeView(view)
	if err != nil || !strings.Contains(string(payload), `"output_scope":"editable_draft"`) ||
		!strings.Contains(string(payload), `"deferred":true`) {
		t.Fatal("private interpreter lost current output scope or per-view deferral")
	}
	clear(payload)
	const instruction = "Leave the remaining fields for an editable draft"
	body := `{"intent":"draft","proposals":[{"field_id":"field_birth","disposition":"deferred",` +
		`"literals":[{"text":"` + instruction + `","occurrence":1}]}],` +
		`"output":{"mode":"editable_draft","literal":{"text":"` + instruction + `","occurrence":1}}}`
	result, err := decodeDocumentIntakeInterpretation(body, view.Pass.CandidateIDs)
	if err != nil || result.Output == nil || result.Output.Mode != document.FormIntakeOutputDraft ||
		result.Output.Literal.Text != instruction || len(result.Proposals) != 1 ||
		result.Proposals[0].Disposition != document.FormIntakeDeferred {
		t.Fatal("strict private output instruction lost exact native-validation evidence")
	}
	for _, private := range []any{view, result} {
		encoded, encodeErr := json.Marshal(private)
		if encodeErr != nil || string(encoded) != "{}" {
			t.Fatal("private scope interpretation escaped ordinary JSON")
		}
	}
	for _, malformed := range []string{
		strings.Replace(body, `"mode":"editable_draft"`, `"mode":"auto_approve"`, 1),
		`{"intent":"draft","output":{"mode":"editable_draft"}}`,
		`{"intent":"draft","output":true}`,
		strings.Replace(body, `"occurrence":1`, `"occurrence":0`, 1),
		strings.Replace(body, `"mode":"editable_draft"`, `"mode":"editable_draft","approved":true`, 1),
	} {
		if _, err = decodeDocumentIntakeInterpretation(malformed, view.Pass.CandidateIDs); err == nil {
			t.Fatal("malformed output metadata crossed the private decoder")
		}
	}
	for _, change := range []func(*document.FormIntakeInterpretationView){
		func(v *document.FormIntakeInterpretationView) { v.DeferredFieldIDs = []string{"outside_window"} },
		func(v *document.FormIntakeInterpretationView) {
			v.DeferredFieldIDs = []string{"field_birth", "field_birth"}
		},
		func(v *document.FormIntakeInterpretationView) { v.OutputMode = document.FormOutputFlattenedPrint },
		func(v *document.FormIntakeInterpretationView) { v.OutputScope = "automatic_finish" },
	} {
		copy := view
		change(&copy)
		if _, err = encodeDocumentIntakeView(copy); err == nil {
			t.Fatal("unbounded, contradictory or unknown output metadata reached the private provider")
		}
	}
}
