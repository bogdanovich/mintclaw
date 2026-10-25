package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func exclusionTestView() document.FormIntakeInterpretationView {
	view := privateIntakeTestView()
	view.Pass.Input.JobID, view.Pass.Input.ModeID, view.Pass.Input.Reference = "job", "mode", "earlier"
	view.Pass.Input.AdmissionOrder = 1
	view.Pass.Input.Text = "A reference MINT; its owner is unspecified."
	view.Pass.CandidateIDs = []string{"field_owner", "field_other"}
	view.Pass.Fields = []document.FormField{
		{ID: "field_owner", Name: "Primary reference", Kind: document.FormFieldText},
		{ID: "field_other", Name: "Secondary reference", Kind: document.FormFieldText},
	}
	view.Pass.Input.Findings = []document.FormFieldMappingBlocker{{FieldID: "field_other", Code: "field_ambiguous"}}
	view.RelatedInputs = []document.FormIntakeInput{
		{
			JobID:          "job",
			ModeID:         "mode",
			Reference:      "later",
			AdmissionOrder: 2,
			Text:           "That reference is only for the first person, not the second.",
		},
	}
	return view
}

func TestDocumentIntakeExclusionNeedsIndependentPrivateVerification(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		view := exclusionTestView()
		body := `{"intent":"facts","exclusions":[{"field_id":"field_other","evidence_reference":"later","evidence":{"text":"` + view.RelatedInputs[0].Text + `","occurrence":1}}]}`
		verify := `{"verified":["field_other"],"findings":[]}`
		if rejected {
			verify = `{"verified":[],"findings":[{"field_id":"field_other","code":"field_ambiguous"}]}`
		}
		provider := &documentBooleanVerificationTestProvider{
			private:   true,
			responses: []*providers.LLMResponse{{Content: body}, {Content: verify}},
		}
		auditor := &documentFormAuditor{
			models: map[string]documentFormAuditModel{"allowed": {provider: provider, model: "configured-audit"}},
		}
		result, err := auditor.InterpretFormIntake(t.Context(), "allowed", view)
		if err != nil || provider.calls != 2 || !provider.private {
			t.Fatalf("exclusion skipped private verification: %v", err)
		}
		if rejected {
			if len(result.Exclusions) != 0 || len(result.Findings) != 1 {
				t.Fatal("unverified exclusion erased ambiguity")
			}
		} else if len(result.Exclusions) != 1 || !result.Exclusions[0].Verified {
			t.Fatal("verified exclusion missing")
		}
		public, _ := json.Marshal(result)
		if strings.Contains(string(public), "reference") || strings.Contains(string(public), "MINT") ||
			strings.Contains(string(public), "later") {
			t.Fatal("private exclusion leaked into public JSON")
		}
	}
}

func TestDocumentIntakeExclusionRejectsInjectedAuthorityAndUnknownSource(t *testing.T) {
	for _, body := range []string{
		`{"intent":"facts","exclusions":[{"field_id":"field_other","evidence_reference":"unoffered","evidence":{"text":"no","occurrence":1}}]}`,
		`{"intent":"facts","exclusions":[{"field_id":"field_other","evidence_reference":"later","verified":true,"evidence":{"text":"no","occurrence":1}}]}`,
		`{"intent":"facts","exclusions":[{"field_id":"unknown","evidence_reference":"later","evidence":{"text":"no","occurrence":1}}]}`,
		`{"intent":"facts","findings":[{"field_id":"field_other","code":"field_ambiguous"}],"exclusions":[{"field_id":"field_other","evidence_reference":"later","evidence":{"text":"no","occurrence":1}}]}`,
	} {
		view := exclusionTestView()
		if _, err := decodeDocumentIntakeInterpretation(
			body,
			view.Pass.CandidateIDs,
			view.RelatedInputs...); err == nil {
			t.Fatal("unoffered exclusion context or authority accepted")
		}
	}
}

func TestDocumentIntakeExclusionRejectsEarlierOrNonAdvisoryEvidence(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		view := exclusionTestView()
		if earlier {
			view.Pass.Input.AdmissionOrder, view.RelatedInputs[0].AdmissionOrder = 2, 1
		} else {
			view.Pass.Input.Findings[0].Code = "field_conflicting"
		}
		body := `{"intent":"facts","exclusions":[{"field_id":"field_other","evidence_reference":"later","evidence":{"text":"` + view.RelatedInputs[0].Text + `","occurrence":1}}]}`
		provider := &documentBooleanVerificationTestProvider{
			private:   true,
			responses: []*providers.LLMResponse{{Content: body}},
		}
		auditor := &documentFormAuditor{
			models: map[string]documentFormAuditModel{"allowed": {provider: provider, model: "configured-audit"}},
		}
		if _, err := auditor.InterpretFormIntake(t.Context(), "allowed", view); err == nil || provider.calls != 1 {
			t.Fatal("exclusion used earlier evidence or suppressed a native conflict")
		}
	}
}
