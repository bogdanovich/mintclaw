package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentIntakePrivateWireDistinguishesLaterClarificationFromEarlierUtterance(t *testing.T) {
	for _, currentOrder := range []int{1, 2} {
		view := privateIntakeTestView()
		view.Pass.Input.JobID, view.Pass.Input.ModeID, view.Pass.Input.Reference = "job", "mode", "current"
		view.Pass.Input.AdmissionOrder = currentOrder
		view.RelatedInputs = []document.FormIntakeInput{{
			JobID: "job", ModeID: "mode", Reference: "other", Text: "This value belongs only to the borrower.",
			AdmissionOrder: 3 - currentOrder,
		}}
		encoded, err := encodeDocumentIntakeView(view)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			AdmissionOrder int `json:"admission_order"`
			RelatedContext []struct {
				AdmissionOrder int `json:"admission_order"`
			} `json:"related_input_context"`
		}
		if json.Unmarshal(encoded, &wire) != nil || wire.AdmissionOrder != currentOrder ||
			len(wire.RelatedContext) != 1 || wire.RelatedContext[0].AdmissionOrder != 3-currentOrder {
			t.Fatal("private interpreter cannot distinguish an earlier utterance from later clarification")
		}
		if !strings.Contains(documentIntakeSystemPrompt, "admission_order") ||
			!strings.Contains(documentIntakeSystemPrompt, "later clarification") {
			t.Fatal("private interpretation does not explain native chronology")
		}
	}
}

func TestDocumentIntakePrivateChronologyRejectsInconsistentAndUnboundedOrders(t *testing.T) {
	for _, orders := range [][2]int{{-1, 1}, {257, 1}, {1, -1}, {1, 257}, {1, 1}, {1, 0}, {0, 1}} {
		view := privateIntakeTestView()
		view.Pass.Input.JobID, view.Pass.Input.ModeID, view.Pass.Input.Reference = "job", "mode", "current"
		view.Pass.Input.AdmissionOrder = orders[0]
		view.RelatedInputs = []document.FormIntakeInput{{
			JobID: "job", ModeID: "mode", Reference: "other", Text: "Clarification",
			AdmissionOrder: orders[1],
		}}
		if _, err := encodeDocumentIntakeView(view); err == nil {
			t.Fatal("inconsistent native chronology was offered as ordering evidence")
		}
	}
	view := privateIntakeTestView()
	view.Pass.Input.JobID, view.Pass.Input.ModeID, view.Pass.Input.Reference = "job", "mode", "current"
	view.Pass.Input.AdmissionOrder = 1
	view.RelatedInputs = []document.FormIntakeInput{
		{JobID: "job", ModeID: "mode", Reference: "second", Text: "Second message", AdmissionOrder: 2},
		{JobID: "job", ModeID: "mode", Reference: "third", Text: "Third message", AdmissionOrder: 2},
	}
	if _, err := encodeDocumentIntakeView(view); err == nil {
		t.Fatal("distinct protected receipts claimed the same native admission order")
	}
}
