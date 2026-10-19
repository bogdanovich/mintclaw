package document

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func taskQuestionFixture(t *testing.T) (*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest,
	FormTaskRouteSnapshot, *FormProtectedAnswerSink, interactions.ProtectedAnswerSinkRequest,
) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	route = decideTaskRouteFixture(t, store, request, route, FormTaskAsk)
	binding, err := store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	answer := interactions.ProtectedAnswerSinkRequest{
		Binding:         binding,
		Workspace:       request.Owner.WorkspaceID,
		Route:           testProtectedAnswerRoute(request.Owner),
		InteractionID:   "task-question-1",
		IdempotencyKey:  "task-answer-idempotency-1",
		MessageIdentity: "task-answer-message-1",
		Intent:          interactions.ProtectedAnswerValue,
		Text:            "PRIVATE_FREE_TEXT_TASK_REPLY",
	}
	return store, options, request, route, sink, answer
}

func TestFormTaskQuestionExhaustionPreservesCanonicalChoiceAndExpiryPreservesRequest(t *testing.T) {
	store, _, request, route, sink, answer := taskQuestionFixture(t)
	original, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	// Expiry recovery discards an unclaimed answer, not the pending task.
	if err = sink.Discard(t.Context(), interactions.ProtectedAnswerDiscardRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: "expired-without-answer", Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	for index := range MaxFormTaskQuestionAnswers {
		answer.Binding, err = store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
		if err != nil {
			t.Fatal(err)
		}
		answer.MessageIdentity, answer.InteractionID = fmt.Sprintf("free-%d", index), fmt.Sprintf("question-%d", index)
		receipt := commitTaskReplyFixture(t, sink, answer)
		if err = store.HoldFormTaskAnswer(t.Context(), receipt.Reference, request.Owner); err != nil {
			t.Fatal(err)
		}
	}
	answer.Binding, err = store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal("exhausted free text disabled canonical buttons")
	}
	answer.MessageIdentity, answer.InteractionID = "last-choice", "last-question"
	if _, err = sink.Accept(t.Context(), answer); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatal("free-answer exhaustion was bypassed")
	}
	answer.Text = "switch_task"
	receipt := commitTaskReplyFixture(t, sink, answer)
	if _, err = store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner); err != nil {
		t.Fatal(err)
	}
	handoff, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	if err != nil || handoff.Text != original.Text || len(handoff.Media) != len(original.MediaRefs) {
		t.Fatal("expiry or free-answer exhaustion lost the bound task")
	}
}

func TestFormTaskQuestionRefreshesAtNewRevisionWithoutAcceptingOldButtons(t *testing.T) {
	store, _, request, route, sink, answer := taskQuestionFixture(t)
	stale := answer
	for index := range maxFormTaskStoredAnswers + 2 {
		current, err := store.Get(t.Context(), request.JobID, request.Owner)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := store.SetFormIntakeState(
			t.Context(),
			request.JobID,
			route.ModeID,
			current.Revision,
			request.Owner,
			FormIntakePaused,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.SetFormIntakeState(
			t.Context(),
			request.JobID,
			route.ModeID,
			updated.Revision,
			request.Owner,
			FormIntakeActive,
		); err != nil {
			t.Fatal(err)
		}
		answer.Binding, err = store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
		if err != nil {
			t.Fatal("revision-only refresh exhausted the question")
		}
		answer.MessageIdentity, answer.InteractionID = fmt.Sprintf("fresh-%d", index), fmt.Sprintf("question-%d", index)
	}
	stale.Text = "switch_task"
	if _, err := sink.Accept(t.Context(), stale); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("stale buttons selected the refreshed task")
	}
	answer.Text = "switch_task"
	receipt := commitTaskReplyFixture(t, sink, answer)
	if _, err := store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner); err != nil {
		t.Fatal(err)
	}
}

func TestFormTaskQuestionStagesCommitsAndReplaysWithoutFieldAssignmentAcrossRestart(t *testing.T) {
	store, options, request, route, sink, answer := taskQuestionFixture(t)
	again, err := store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
	if err != nil || again.Token != answer.Binding.Token {
		t.Fatal("question binding was not idempotent")
	}
	receipt, err := sink.Accept(t.Context(), answer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadFormTaskRouteAnswer(
		t.Context(),
		receipt.Reference,
		request.Owner,
	); !errors.Is(
		err,
		ErrFormJobConflict,
	) {
		t.Fatal("unclaimed task answer was readable")
	}
	if _, _, _, err := ParseFormTaskRouteAnswerReference(receipt.Reference); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFormIntakeReference(receipt.Reference); err == nil {
		t.Fatal("task selection reply was exposed as an ordinary field receipt")
	}
	if _, _, err = store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID:            request.JobID,
		ExpectedRevision: route.Revision,
		Owner:            request.Owner,
		FieldID:          request.Schema.Fields[0].ID,
		IdempotencyKey:   "competing-write",
		State:            FormValueSupplied,
		Source:           FormValueSourceUser,
		Value:            FormProtectedValue{Kind: ProtectedValueText, Text: "SHOULD_NOT_BE_WRITTEN"},
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("a field mutation raced the pending question claim")
	}
	if _, err = store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID, route.Revision,
		request.Owner, FormIntakePaused); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("pause raced the pending question claim")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	sink, err = NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := sink.Accept(t.Context(), answer)
	if err != nil || replay != receipt {
		t.Fatal("restart admitted a second task answer")
	}
	commit := interactions.ProtectedAnswerCommitRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: receipt,
	}
	if err = sink.Commit(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), commit); err != nil {
		t.Fatal("committed answer replay failed")
	}
	current, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || current.Revision != route.Revision || current.LedgerRevision != 0 || len(current.Fields) != 0 {
		t.Fatal("task-selection answer gained field or lifecycle authority")
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("receipt leaked reply text")
	}
	assertFormStoreContainsNoPlaintext(t, options, answer.Text, "PRIVATE_NEW_TASK_REQUEST")
	answer.Text += " CHANGED"
	if _, err = sink.Accept(t.Context(), answer); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("same transport answer changed its stored body")
	}
}

func TestFormTaskQuestionCanonicalChoicesAreClaimBoundAndDoNotAssignFields(t *testing.T) {
	for _, choice := range []string{"switch_task", "keep_form"} {
		t.Run(choice, func(t *testing.T) {
			store, _, request, route, sink, answer := taskQuestionFixture(t)
			answer.Text = choice
			receipt, err := sink.Accept(t.Context(), answer)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.ApplyFormTaskRouteChoice(
				t.Context(),
				receipt.Reference,
				request.Owner,
			); !errors.Is(
				err,
				ErrFormJobConflict,
			) {
				t.Fatal("unclaimed choice gained routing authority")
			}
			if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
				Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
				InteractionID: answer.InteractionID, Receipt: receipt,
			}); err != nil {
				t.Fatal(err)
			}
			private, err := store.ReadFormTaskRouteAnswer(t.Context(), receipt.Reference, request.Owner)
			if err != nil || private.Answer == nil || private.Answer.Text != choice {
				t.Fatal("committed choice was lost")
			}
			selected, err := store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner)
			if err != nil || !replay.Reused || replay.State != selected.State || replay.Revision != selected.Revision {
				t.Fatal("choice replay changed the bound task or revision")
			}
			if choice == "switch_task" {
				if selected.State != FormTaskReady {
					t.Fatal("confirmed new task was not ready")
				}
				handoff, handoffErr := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
				if handoffErr != nil || handoff.Text != "PRIVATE_NEW_TASK_REQUEST" || len(handoff.Media) != 1 {
					t.Fatal("confirmed choice did not release exactly the original new request")
				}
				if again, repeatErr := store.GrantFormTaskHandoff(
					t.Context(),
					route.Reference,
					request.Owner,
				); repeatErr != nil || !again.Reused || again.Text != "" ||
					len(again.Media) != 0 {
					t.Fatal("choice replay released the new task twice")
				}
			} else {
				if selected.State != FormTaskCurrent {
					t.Fatal("stay choice discarded the original accompanying text")
				}
				admission := FormIntakeMessageRequest{
					JobID: request.JobID, ModeID: route.ModeID, Owner: request.Owner, Schema: request.Schema,
					ExpectedRevision: selected.Revision, MessageIdentity: route.Reference,
					Text: "PRIVATE_NEW_TASK_REQUEST", RoutingReference: route.Reference,
				}
				admitted, admitErr := store.AdmitFormIntakeMessage(t.Context(), admission)
				if admitErr != nil {
					t.Fatal(admitErr)
				}
				privateInput, readErr := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
				if readErr != nil || privateInput.Text != "PRIVATE_NEW_TASK_REQUEST" {
					t.Fatal("ignoring the attachment lost its accompanying protected text")
				}
				if duplicate, duplicateErr := store.AdmitFormIntakeMessage(
					t.Context(),
					admission,
				); duplicateErr != nil || !duplicate.Reused ||
					duplicate.Receipt != admitted.Receipt {
					t.Fatal("ignoring media broke atomic field-admission replay")
				}
				if progress, findErr := store.FindFormIntake(
					t.Context(),
					request.Owner,
				); findErr != nil || progress == nil ||
					progress.State != FormIntakeActive {
					t.Fatal("stay choice detached the old form")
				}
				if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
					t.Fatal("stay choice released the discarded new task")
				}
			}
			current, err := store.Get(t.Context(), request.JobID, request.Owner)
			if err != nil || current.LedgerRevision != 0 || len(current.Fields) != 0 || current.State.terminal() {
				t.Fatal("task choice assigned a field or canceled the preserved job")
			}
		})
	}
}

func TestFormTaskQuestionWrongOwnerDiscardAndCancelPreserveOriginalJob(t *testing.T) {
	store, _, request, route, sink, answer := taskQuestionFixture(t)
	wrong := answer
	wrong.Route.TopicID = "wrong-topic"
	if _, err := sink.Accept(t.Context(), wrong); err == nil {
		t.Fatal("another topic answered the task question")
	}
	receipt, err := sink.Accept(t.Context(), answer)
	if err != nil {
		t.Fatal(err)
	}
	discard := interactions.ProtectedAnswerDiscardRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: &receipt, Force: true,
	}
	if err = sink.Discard(t.Context(), discard); err != nil {
		t.Fatal(err)
	}
	if err = sink.Discard(t.Context(), discard); err != nil {
		t.Fatal("discard was not idempotent")
	}
	if err = sink.Cancel(t.Context(), interactions.ProtectedAnswerCancelRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, IdempotencyKey: "cancel-this-question-only",
	}); err != nil {
		t.Fatal(err)
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.State != FormIntakeActive || progress.PendingTasks != 0 {
		t.Fatal("canceling a new-task question canceled or blocked the old form")
	}
	current, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || current.State.terminal() || current.SourceDigest != request.Schema.SourceSHA256 ||
		current.LedgerRevision != 0 {
		t.Fatal("task-question cancel erased the original job or source")
	}
	if _, err = sink.Accept(t.Context(), answer); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("canceled question accepted a late answer")
	}
	if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("canceling a task question granted the new task")
	}
}
