package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormOutputCapabilityUsesVerifiedEligibilityNotBackendIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		eligibility *document.FormEligibilityFacts
		want        document.FormOutputMode
	}{
		{name: "absent"},
		{name: "ordinary", eligibility: &document.FormEligibilityFacts{
			State: document.FormEligible, Mode: document.FormEligibilityOrdinary,
		}, want: document.FormOutputEditableAcroForm},
		{name: "hybrid print", eligibility: &document.FormEligibilityFacts{
			State: document.FormEligible, Mode: document.FormEligibilityHybridPrintReady,
		}, want: document.FormOutputFlattenedPrint},
		{name: "discovery only", eligibility: &document.FormEligibilityFacts{
			State: document.FormEligible, Mode: document.FormEligibilityHybridDiscovery,
		}},
		{name: "blocked", eligibility: &document.FormEligibilityFacts{
			State: document.FormBlocked, Mode: document.FormEligibilityOrdinary,
		}},
		{name: "contradictory blockers", eligibility: &document.FormEligibilityFacts{
			State: document.FormEligible, Mode: document.FormEligibilityOrdinary,
			Blockers: []document.FormBlocker{{Code: document.FormBlockerEncryption, State: document.FactPresent}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := documentFormDiscoveryOutputMode(test.eligibility); got != test.want {
				t.Fatalf("output mode = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDocumentFormOutputScopeProjectionsAndDeliveryDistinguishDraft(t *testing.T) {
	field := document.FormReviewField{
		FieldID: "field_" + strings.Repeat("a", 64), Required: true,
		Summary: "deferred", Deferred: true,
	}
	review := document.FormReview{
		OutputScope: document.FormOutputEditableDraft,
		OutputMode:  document.FormOutputEditableAcroForm, Fields: []document.FormReviewField{field}, Ready: true,
	}
	projection := documentFormReviewProjection(review)
	if projection.DeferredFieldCount != 1 || projection.ProvidedFieldCount != 0 ||
		projection.UnresolvedFieldCount != 0 || projection.OutputScope != document.FormOutputEditableDraft ||
		len(projection.Fields) != 1 || !projection.Fields[0].Required || !projection.Fields[0].Deferred {
		t.Fatal("bounded review confused deferred with provided, unresolved or optional")
	}
	job := document.FormJobRecord{
		OutputScope: review.OutputScope, OutputMode: review.OutputMode,
		DeferredFieldIDs: []string{field.FieldID},
	}
	deliverable := documentFormWorkflowDeliverable(job, document.WriteOperationRecord{})
	if !strings.Contains(deliverable.Text, "editable PDF draft") ||
		!strings.Contains(deliverable.Text, "not a complete form") ||
		documentFormDeliveryDescription(job) != deliverable.Text {
		t.Fatal("draft delivery claimed full completion")
	}
	legacy := documentFormApprovalArguments(document.FormCommitBinding{})
	if _, present := legacy["output_scope"]; present {
		t.Fatal("empty output scope changed the persisted legacy approval shape")
	}
	draft := documentFormApprovalArguments(document.FormCommitBinding{
		OutputScope: review.OutputScope, OutputMode: review.OutputMode,
	})
	if draft["output_scope"] != review.OutputScope || draft["output_mode"] != review.OutputMode {
		t.Fatal("draft approval arguments lost output identity")
	}
}
