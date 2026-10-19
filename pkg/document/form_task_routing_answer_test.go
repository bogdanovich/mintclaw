package document

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func commitTaskReplyFixture(t *testing.T, sink *FormProtectedAnswerSink,
	answer interactions.ProtectedAnswerSinkRequest,
) interactions.ProtectedAnswerReceipt {
	t.Helper()
	receipt, err := sink.Accept(t.Context(), answer)
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestFormTaskFreeReplyRequiresVerifiedScopeAndPreservesAllProtectedFacts(t *testing.T) {
	for _, action := range []FormTaskRouteAction{FormTaskSwitch, FormTaskKeep, FormTaskContinue, FormTaskAsk} {
		t.Run(string(action), func(t *testing.T) {
			store, options, request, route, sink, answer := taskQuestionFixture(t)
			receipt := commitTaskReplyFixture(t, sink, answer)
			passes := 1
			if action == FormTaskSwitch || action == FormTaskKeep {
				passes = 2
			}
			var result FormTaskRouteSnapshot
			for count := 1; count <= passes; count++ {
				reserved, err := store.ReserveFormTaskAnswerPass(t.Context(), receipt.Reference, request.Owner)
				if err != nil {
					t.Fatal(err)
				}
				decision := FormTaskAnswerDecision{
					Reference: receipt.Reference, Owner: request.Owner, ExpectedRevision: reserved.Revision,
					Pass: reserved.Answer.Passes, Action: action,
				}
				if passes == 2 {
					decision.Evidence = FormIntakeLiteral{Text: answer.Text, Occurrence: 1}
				}
				result, err = store.DecideFormTaskAnswer(t.Context(), decision)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.DecideFormTaskAnswer(t.Context(), decision); err != nil {
					t.Fatal("same decision replay changed its result")
				}
				if passes == 2 && count == 1 {
					if result.State != FormTaskQuestion || result.Answer.State != "committed" {
						t.Fatal("one private pass acquired scope authority")
					}
					if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
						t.Fatal("unverified free text released a task")
					}
				}
			}
			public, err := json.Marshal(result)
			if err != nil || strings.Contains(string(public), "PRIVATE_") {
				t.Fatal("private reply escaped ordinary progress")
			}
			assertFormStoreContainsNoPlaintext(t, options, answer.Text, "PRIVATE_NEW_TASK_REQUEST")
			current, err := store.Get(t.Context(), request.JobID, request.Owner)
			if err != nil || current.LedgerRevision != 0 || len(current.Fields) != 0 {
				t.Fatal("task purpose interpretation assigned fields")
			}
			switch action {
			case FormTaskSwitch:
				handoff, handoffErr := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
				if handoffErr != nil || handoff.Text != "PRIVATE_NEW_TASK_REQUEST\n\n"+answer.Text ||
					len(handoff.Media) != 1 {
					t.Fatal("verified reply did not release only the original new task and its current reply")
				}
			case FormTaskKeep:
				if result.State != FormTaskCurrent || result.Answer.Choice != FormTaskContinue ||
					result.Answer.Text != answer.Text {
					t.Fatal("stay choice lost original or reply facts before protected field admission")
				}
			case FormTaskContinue:
				admitted, admitErr := store.AdmitFormIntakeMessage(t.Context(), FormIntakeMessageRequest{
					JobID:            request.JobID,
					ModeID:           route.ModeID,
					Owner:            request.Owner,
					Schema:           request.Schema,
					ExpectedRevision: result.Revision,
					MessageIdentity:  receipt.Reference,
					RoutingReference: receipt.Reference,
					Text:             answer.Text,
				})
				if admitErr != nil {
					t.Fatal(admitErr)
				}
				input, readErr := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
				if readErr != nil || input.Text != answer.Text || input.AdmissionOrder != 2 {
					t.Fatal("form facts were not transferred with their original native order")
				}
			case FormTaskAsk:
				if result.State != FormTaskQuestion || result.Answer.Text != answer.Text {
					t.Fatal("unclear reply was dropped")
				}
			}
		})
	}
}

func TestFormTaskFreeReplyRejectsChangedEvidenceAndBoundsReservations(t *testing.T) {
	store, _, request, _, sink, answer := taskQuestionFixture(t)
	receipt := commitTaskReplyFixture(t, sink, answer)
	reserved, err := store.ReserveFormTaskAnswerPass(t.Context(), receipt.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	decision := FormTaskAnswerDecision{
		Reference: receipt.Reference, Owner: request.Owner, ExpectedRevision: reserved.Revision,
		Pass: 1, Action: FormTaskSwitch, Evidence: FormIntakeLiteral{Text: answer.Text, Occurrence: 1},
	}
	if _, err = store.DecideFormTaskAnswer(t.Context(), decision); err != nil {
		t.Fatal(err)
	}
	decision.Evidence.Text = "A_CHANGED_PRIVATE_QUOTE"
	if _, err = store.DecideFormTaskAnswer(t.Context(), decision); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("replay replaced evidence while retaining scope authority")
	}
	if _, err = store.ReserveFormTaskAnswerPass(t.Context(), receipt.Reference, request.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveFormTaskAnswerPass(
		t.Context(),
		receipt.Reference,
		request.Owner,
	); !errors.Is(
		err,
		ErrFormJobCapacityExceeded,
	) {
		t.Fatal("free-answer budget was unbounded")
	}
}
