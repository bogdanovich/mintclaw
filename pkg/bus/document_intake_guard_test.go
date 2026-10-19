package bus

import (
	"strings"
	"testing"
)

func TestDocumentIntakeGuardTaskReferencesRemainExactJobBound(t *testing.T) {
	job := "form_job_" + strings.Repeat("a", 32)
	mode := "form_intake_" + strings.Repeat("b", 32)
	route := "form_route_" + strings.Repeat("c", 32)
	answer := "form_task_answer_" + strings.Repeat("d", 32)
	for _, reference := range []string{
		"form_route." + job + "." + route,
		"form_route_answer." + job + "." + route + "." + answer,
	} {
		guard := InboundDocumentIntakeGuard{JobID: job, ModeID: mode, Reference: reference}
		if !guard.Valid() {
			t.Fatal("native task receipt was rejected")
		}
		guard.JobID = "form_job_" + strings.Repeat("e", 32)
		if guard.Valid() {
			t.Fatal("task receipt changed its job")
		}
		guard.JobID, guard.Reference = job, reference+".extra"
		if guard.Valid() {
			t.Fatal("noncanonical task receipt was accepted")
		}
	}
}
