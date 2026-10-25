package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentResumeSeparatesPendingTaskScopeFromUnprocessedFormText(t *testing.T) {
	projection := safeDocumentFormResult{
		Job: &safeDocumentFormJob{JobID: "form_job_test"}, FormAction: "resume",
		Intake: &safeDocumentFormIntake{Progress: document.FormIntakeProgress{
			PendingTasks: 1, PendingInputs: 0,
		}},
	}
	followup, err := documentFormIntakeFollowup(projection, false)
	if err != nil || followup == nil || !followup.ResponseOnly {
		t.Fatal("resume did not reach its response-only renderer")
	}
	if !strings.Contains(followup.Instruction, "pending_inputs=0") ||
		!strings.Contains(followup.Instruction, "pending_tasks") ||
		strings.Contains(followup.Instruction, "otherwise offer /retry_form") {
		t.Fatal("renderer confused pending scope with unprocessed form input")
	}
}
