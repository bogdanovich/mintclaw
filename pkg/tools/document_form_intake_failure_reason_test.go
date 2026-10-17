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
