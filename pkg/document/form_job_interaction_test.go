package document

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestFormProtectedAnswerSinkPersistsIdempotentlyAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.full_name",
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Namespace != FormProtectedAnswerNamespace || strings.Contains(binding.Token, protectedFormSentinel) {
		t.Fatalf("binding = %#v", binding)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	request := interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-protected", IdempotencyKey: "protected-answer-1",
		Intent: interactions.ProtectedAnswerValue, Text: protectedFormSentinel,
	}
	first, err := sink.Accept(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reference == "" || first.State != "stored" {
		t.Fatalf("receipt = %#v", first)
	}
	receiptJobID, firstEventID, err := ParseFormProtectedAnswerReference(first.Reference)
	if err != nil || receiptJobID != created.JobID || firstEventID == "" {
		t.Fatalf("receipt reference = (%q, %q, %v)", receiptJobID, firstEventID, err)
	}
	staged, err := store.Get(t.Context(), created.JobID, owner)
	if err != nil || staged.Revision != created.Revision || len(staged.Fields) != 0 {
		t.Fatalf("staged value became current = %#v, %v", staged, err)
	}
	if _, err := store.ReadValues(
		t.Context(),
		created.JobID,
		owner,
		[]string{"field.full_name"},
	); !errors.Is(
		err,
		ErrFormJobNotFound,
	) {
		t.Fatalf("staged value is readable before commit: %v", err)
	}
	replayed, err := sink.Accept(t.Context(), request)
	if err != nil || replayed != first {
		t.Fatalf("idempotent receipt = %#v, %v; want %#v", replayed, err, first)
	}
	conflict := request
	conflict.Text = "different protected answer"
	if _, err := sink.Accept(t.Context(), conflict); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-protected", Receipt: first,
	}); err != nil {
		t.Fatal(err)
	}
	assertFormStoreContainsNoPlaintext(t, options, protectedFormSentinel)

	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	restartedSink, err := NewFormProtectedAnswerSink(reopened)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restartedSink.Accept(t.Context(), request)
	if err != nil || afterRestart != first {
		t.Fatalf("restart replay = %#v, %v; want %#v", afterRestart, err, first)
	}
	if err := restartedSink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-protected", Receipt: afterRestart,
	}); err != nil {
		t.Fatalf("idempotent restart commit: %v", err)
	}
	values, err := reopened.ReadValues(t.Context(), created.JobID, owner, []string{"field.full_name"})
	if err != nil || values["field.full_name"].Value.Text != protectedFormSentinel {
		t.Fatalf("protected values = %#v, %v", values, err)
	}
	public, err := reopened.Get(t.Context(), created.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	correctionBinding, err := reopened.NewProtectedAnswerBinding(
		t.Context(),
		FormProtectedAnswerBindingRequest{
			JobID: created.JobID, ExpectedRevision: public.Revision, Owner: owner,
			FieldID: "field.full_name", SupersedesEventID: firstEventID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := restartedSink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: correctionBinding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-correction", IdempotencyKey: "protected-answer-2",
		Intent: interactions.ProtectedAnswerValue, Text: "MINTCLAW_PDF3_CORRECTED_PRIVATE_6ac2",
	})
	if err != nil || corrected.Reference == first.Reference {
		t.Fatalf("correction receipt = %#v, %v", corrected, err)
	}
	if err := restartedSink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: correctionBinding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-correction", Receipt: corrected,
	}); err != nil {
		t.Fatal(err)
	}
	values, err = reopened.ReadValues(t.Context(), created.JobID, owner, []string{"field.full_name"})
	if err != nil || values["field.full_name"].Value.Text != "MINTCLAW_PDF3_CORRECTED_PRIVATE_6ac2" ||
		values["field.full_name"].SupersedesEventID != firstEventID {
		t.Fatalf("corrected protected values = %#v, %v", values, err)
	}
	assertFormStoreContainsNoPlaintext(
		t,
		options,
		protectedFormSentinel,
		"MINTCLAW_PDF3_CORRECTED_PRIVATE_6ac2",
	)
}

func TestFormProtectedAnswerSinkHandlesBlankControlsAuthorityAndCancel(t *testing.T) {
	for _, test := range []struct {
		name       string
		intent     interactions.ProtectedAnswerIntent
		wantState  FormValueState
		wantReason FormBlankReason
	}{
		{name: "skip", intent: interactions.ProtectedAnswerSkip, wantState: FormValueBlanked, wantReason: FormBlankSkipped},
		{
			name: "not applicable", intent: interactions.ProtectedAnswerNotApplicable,
			wantState: FormValueNotApplicable, wantReason: FormBlankNotApplicable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			owner := testFormJobOwner()
			create := testFormJobCreateRequest(owner)
			create.StartIdempotencyKey = "start-" + test.name
			created, err := store.Create(t.Context(), create)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
				JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.optional",
			})
			if err != nil {
				t.Fatal(err)
			}
			sink, _ := NewFormProtectedAnswerSink(store)
			wrongRoute := testProtectedAnswerRoute(owner)
			wrongRoute.SenderID = "other-user"
			if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
				Binding: binding, Workspace: owner.WorkspaceID, Route: wrongRoute,
				InteractionID: "interaction-wrong", IdempotencyKey: "wrong", Intent: test.intent,
			}); !errors.Is(err, ErrFormJobUnauthorized) {
				t.Fatalf("wrong authority error = %v", err)
			}
			receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
				Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
				InteractionID: "interaction-blank", IdempotencyKey: "blank-1", Intent: test.intent,
			})
			if err != nil || receipt.Reference == "" {
				t.Fatalf("Accept() = %#v, %v", receipt, err)
			}
			if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
				Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
				InteractionID: "interaction-blank", Receipt: receipt,
			}); err != nil {
				t.Fatal(err)
			}
			values, err := store.ReadValues(t.Context(), created.JobID, owner, []string{"field.optional"})
			if err != nil || values["field.optional"].State != test.wantState ||
				values["field.optional"].BlankReason != test.wantReason {
				t.Fatalf("blank value = %#v, %v", values, err)
			}
		})
	}

	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := NewFormProtectedAnswerSink(store)
	if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-cancel", IdempotencyKey: "pending-before-cancel",
		Intent: interactions.ProtectedAnswerValue, Text: "MINTCLAW_PENDING_CANCEL_PRIVATE_7e19",
	}); err != nil {
		t.Fatal(err)
	}
	cancelRequest := interactions.ProtectedAnswerCancelRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-cancel", IdempotencyKey: "cancel-message-1",
	}
	if err := sink.Cancel(t.Context(), cancelRequest); err != nil {
		t.Fatal(err)
	}
	if err := sink.Cancel(t.Context(), cancelRequest); err != nil {
		t.Fatalf("idempotent Cancel() error = %v", err)
	}
	assertStoredJobHasNoCiphertext(t, options, created.JobID)
}

func TestFormProtectedAnswerBindingRejectsTamperAndStaleRevision(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := binding
	tampered.Token = tampered.Token[:len(tampered.Token)-1] + "A"
	sink, _ := NewFormProtectedAnswerSink(store)
	if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: tampered, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-tamper", IdempotencyKey: "tamper", Intent: interactions.ProtectedAnswerValue,
		Text: "private",
	}); !errors.Is(err, ErrFormJobRecordCorrupt) {
		t.Fatalf("tampered token error = %v", err)
	}
	if _, _, err := appendTestFormValue(t, store, created, owner, "newer"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-stale", IdempotencyKey: "stale", Intent: interactions.ProtectedAnswerValue,
		Text: "private",
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("stale binding error = %v", err)
	}
}

func TestFormProtectedAnswerSinkDiscardsOnlyOrphanedPendingValue(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := NewFormProtectedAnswerSink(store)
	firstRequest := interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-pending", IdempotencyKey: "pending-first",
		Intent: interactions.ProtectedAnswerValue, Text: "first private value",
	}
	if _, err := sink.Accept(t.Context(), firstRequest); err != nil {
		t.Fatal(err)
	}
	discard := interactions.ProtectedAnswerDiscardRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-pending",
	}
	if err := sink.Discard(t.Context(), discard); err != nil {
		t.Fatal(err)
	}
	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "pending-second"
	secondRequest.Text = "second private value"
	if _, err := sink.Accept(t.Context(), secondRequest); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("live pending value was discarded early: %v", err)
	}
	now = now.Add(protectedAnswerPendingGrace + time.Second)
	if err := sink.Discard(t.Context(), discard); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Accept(t.Context(), secondRequest); err != nil {
		t.Fatalf("replacement after orphan cleanup: %v", err)
	}
}

func TestFormProtectedAnswerSinkCancelsAfterAnswerCommitRace(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.race",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := NewFormProtectedAnswerSink(store)
	receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-race", IdempotencyKey: "answer-before-cancel",
		Intent: interactions.ProtectedAnswerValue, Text: "private race value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-race", Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	cancel := interactions.ProtectedAnswerCancelRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-race", IdempotencyKey: "cancel-after-answer",
	}
	if err := sink.Cancel(t.Context(), cancel); err != nil {
		t.Fatal(err)
	}
	if err := sink.Cancel(t.Context(), cancel); err != nil {
		t.Fatalf("idempotent committed cancellation: %v", err)
	}
	public, err := store.Get(t.Context(), created.JobID, owner)
	if err != nil || public.State != FormJobCanceled || public.Revision != created.Revision+2 {
		t.Fatalf("committed cancellation = %#v, %v", public, err)
	}
}

func testProtectedAnswerRoute(owner FormJobOwner) interactions.Route {
	return interactions.Route{
		AgentID: owner.AgentID, SessionKey: "session-main", RouteSessionKey: owner.RouteSessionKey,
		Channel: owner.Channel, AccountID: owner.AccountID, ChatID: owner.ChatID, ChatType: owner.ChatType,
		SenderID: owner.SenderID, TopicID: owner.TopicID, SpaceID: owner.SpaceID, SpaceType: owner.SpaceType,
	}
}
