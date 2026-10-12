package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormReviewWarnsAboutRequiredDeferralsBeyondItsFieldWindow(t *testing.T) {
	review := document.FormReview{
		JobID: "owned-job", Ready: true, ReviewRevision: 9,
		OutputMode: document.FormOutputEditableAcroForm, OutputScope: document.FormOutputEditableDraft,
		Fields: make([]document.FormReviewField, documentFormCandidateLimit+3),
	}
	for index := range review.Fields {
		review.Fields[index].Required = true
		review.Fields[index].Summary = "provided"
	}
	for _, index := range []int{documentFormCandidateLimit, documentFormCandidateLimit + 2} {
		review.Fields[index].Summary = "deferred"
		review.Fields[index].Deferred = true
	}
	projection := documentFormReviewProjection(review)
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]any
	if err = json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	if public["deferred_required_field_count"] != float64(2) || !projection.FieldsTruncated ||
		projection.UnresolvedFieldCount != 0 || !projection.Ready {
		t.Fatal("draft readiness hid required deferred fields or changed review authority")
	}
	followup, err := documentFormReviewToolOnlyFollowup(safeDocumentFormResult{
		Job: &safeDocumentFormJob{JobID: review.JobID}, Review: projection,
	})
	if err != nil || followup == nil || !followup.ResponseOnly ||
		!strings.Contains(followup.Instruction, "deferred_required_field_count") ||
		!strings.Contains(followup.Instruction, "not complete") {
		t.Fatal("partial review omitted the remaining-required warning or requested an output tool")
	}
}
