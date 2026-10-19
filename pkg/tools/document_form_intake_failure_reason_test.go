package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormIntakeFailureReasonsDoNotExposeWrappedPrivateErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"capacity", document.ErrFormJobCapacityExceeded, "capacity_exceeded"},
		{"answer", document.ErrFormJobAnswerConflict, "answer_rejected"},
		{"revision", document.ErrFormJobConflict, "state_changed"},
		{"audit", document.ErrFormAuditUnavailable, "interpretation_unavailable"},
		{"invalid-view", document.ErrFormIntakeInvalidContext, "interpretation_context_invalid"},
		{"invalid-response", document.ErrFormIntakeInvalidResponse, "interpretation_response_invalid"},
		{"missing-model", document.ErrFormIntakeModelMissing, "interpretation_model_unavailable"},
		{"provider-auth", document.ErrFormIntakeProviderAuth, "provider_authentication"},
		{"provider-billing", document.ErrFormIntakeProviderBilling, "provider_billing"},
		{"provider-request", document.ErrFormIntakeProviderRequest, "provider_request_invalid"},
		{"provider-rate", document.ErrFormIntakeProviderLimited, "provider_rate_limited"},
		{"provider-temp", document.ErrFormIntakeProviderTemp, "provider_temporary"},
		{"cancel", context.Canceled, "interrupted"},
		{"deadline", context.DeadlineExceeded, "interrupted"},
		{"corrupt", document.ErrFormJobRecordCorrupt, "unavailable"},
		{"unknown", errors.New("private_unknown"), "unavailable"},
		{"looks_like_code", errors.New("private_value_123456"), "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := fmt.Errorf("PRIVATE_FAILURE_SENTINEL: %w", test.err)
			reason := documentFormIntakeFailureReason(err)
			if reason != test.want {
				t.Fatalf("reason = %q, want %q", reason, test.want)
			}
			encoded, marshalErr := json.Marshal(safeDocumentFormFailure{
				Code: "intake_pending", Stage: "apply", Reason: reason,
			})
			if marshalErr != nil || strings.Contains(string(encoded), "PRIVATE_") ||
				strings.Contains(string(encoded), "private_") {
				t.Fatal("reason leaked private error content")
			}
		})
	}
}

func TestDocumentFormIntakePreservesClosedFailureThroughTheInterpreterBoundary(t *testing.T) {
	interpreter := &intakeWorkflowTestInterpreter{interpret: func(
		document.FormIntakeInterpretationView,
	) (document.FormIntakeInterpretation, error) {
		return document.FormIntakeInterpretation{}, fmt.Errorf("PRIVATE_INTERPRETER_ERROR: %w", errors.Join(
			document.ErrFormAuditUnavailable, document.ErrFormIntakeInvalidResponse,
		))
	}}
	tool, ctx, _, _, _ := intakeWorkflowFixture(t, interpreter)
	_, err := tool.interpretFormIntake(ctx, document.FormIntakeInterpretationView{})
	if !errors.Is(err, document.ErrFormAuditUnavailable) ||
		documentFormIntakeFailureReason(err) != "interpretation_response_invalid" ||
		strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("interpretation boundary erased the closed private failure category")
	}
	followup, err := documentFormIntakeFollowup(safeDocumentFormResult{
		Job: &safeDocumentFormJob{JobID: "form_job_test"},
		Intake: &safeDocumentFormIntake{Failure: &safeDocumentFormFailure{
			Code: "intake_pending", Stage: "interpretation", Reason: "interpretation_unavailable",
		}},
	}, false)
	if err != nil || followup == nil || !followup.ResponseOnly ||
		!strings.Contains(followup.Instruction, "stage alone does not identify a cause") {
		t.Fatal("unknown interpretation failure can be presented as a proven temporary provider outage")
	}
}
