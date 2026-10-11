package tools

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeFinishRequiresSeparatePostReviewAdmission(t *testing.T) {
	projection := safeDocumentFormResult{
		FormAction: "continue", Job: &safeDocumentFormJob{JobID: "same-job"},
		Review: &safeDocumentFormReview{JobID: "same-job", Ready: true, ReviewRevision: 7},
		Intake: &safeDocumentFormIntake{RequestRevision: 7, Intent: document.FormIntakeIntentFinish},
	}
	for _, revision := range []int64{0, 6, 7, 8} {
		projection.Intake.RequestRevision = revision
		followup, err := documentFormIntakeFollowup(projection, false)
		if err != nil || followup == nil || followup.ResponseOnly != (revision != 7) {
			t.Fatalf("finish revision %d crossed the wrong boundary: %v", revision, err)
		}
		if revision != 7 {
			continue
		}
		args := map[string]any{"action": "form", "form_action": "commit", "job_id": "same-job"}
		if err := followup.ValidateArguments(args); err != nil {
			t.Fatal(err)
		}
		for key, value := range map[string]any{"job_id": "other-job", "form_action": "review", "approved": true} {
			changed := map[string]any{"action": "form", "form_action": "commit", "job_id": "same-job"}
			changed[key] = value
			if err := followup.ValidateArguments(changed); err == nil {
				t.Fatal("finish boundary allowed changed authority")
			}
		}
	}
	projection.Intake.RequestRevision = 7
	projection.Intake.Progress.PendingInputs = 1
	followup, err := documentFormIntakeFollowup(projection, false)
	if err != nil || followup == nil || !followup.ResponseOnly {
		t.Fatal("ready review ignored a different pending input")
	}
	projection.Intake.Progress.PendingInputs = 0
	projection.FormAction = "review"
	followup, err = documentFormIntakeFollowup(projection, false)
	if err != nil || followup == nil || !followup.ResponseOnly {
		t.Fatal("review publication reused an earlier finish intent")
	}
}
