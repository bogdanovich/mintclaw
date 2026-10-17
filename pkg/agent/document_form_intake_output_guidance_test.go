package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentIntakeOutputGuidanceEligibilityStaysPrivateAndExplicit(t *testing.T) {
	view := privateIntakeTestView()
	view.Pass.Input.Accounted = []document.FormIntakeAccountedLiteral{{
		Literal:           document.FormIntakeLiteral{Text: "Finish this PDF", Occurrence: 1},
		ScopedInstruction: true, ReclassifiableOutput: true,
	}}
	encoded, err := encodeDocumentIntakeView(view)
	if err != nil || !strings.Contains(string(encoded), `"reclassifiable_output":true`) {
		t.Fatal("private native eligibility was not conveyed")
	}
	public, err := json.Marshal(view.Pass.Input.Accounted[0])
	if err != nil || string(public) != "{}" {
		t.Fatal("private classification escaped ordinary JSON")
	}
	for _, fieldEvidence := range []bool{false, true} {
		view.Pass.Input.Accounted[0].ScopedInstruction = fieldEvidence
		if fieldEvidence {
			view.Pass.Input.Accounted[0].FieldID = "field_name"
		}
		if _, err = encodeDocumentIntakeView(view); err == nil {
			t.Fatal("unscoped or positive field evidence was marked reclassifiable")
		}
	}
}
