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

func TestFormProtectedNavigationReceiptIsAuthenticatedRestartSafeAndValueFree(t *testing.T) {
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
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	request := interactions.ProtectedAnswerSinkRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-clarify", IdempotencyKey: "clarify-1",
		Intent: interactions.ProtectedAnswerClarify,
	}
	clarify, err := sink.Accept(t.Context(), request)
	if err != nil || !strings.HasPrefix(clarify.Reference, formProtectedNavigationReceiptPrefix) {
		t.Fatalf("clarify receipt = %#v, %v", clarify, err)
	}
	parts, err := ParseFormProtectedNavigationReference(clarify.Reference)
	if err != nil || parts.Action != interactions.ProtectedAnswerClarify || parts.JobID != created.JobID ||
		parts.Revision != created.Revision || strings.Contains(clarify.Reference, "field.full_name") {
		t.Fatalf("clarify receipt parts = %#v, %v", parts, err)
	}
	replayedClarify, err := sink.Accept(t.Context(), request)
	if err != nil || replayedClarify != clarify {
		t.Fatalf("idempotent clarify issuance = %#v, %v", replayedClarify, err)
	}
	distinctRequest := request
	distinctRequest.InteractionID = "interaction-clarify-distinct"
	distinctRequest.IdempotencyKey = "clarify-2"
	distinctClarify, err := sink.Accept(t.Context(), distinctRequest)
	if err != nil || distinctClarify.Reference == clarify.Reference {
		t.Fatalf("distinct clarify issuance = %#v, %v", distinctClarify, err)
	}
	if _, _, err := store.ConsumeFormProtectedNavigationReference(
		t.Context(),
		distinctClarify.Reference,
		owner,
		[]string{"field.full_name"},
		"execution-uncommitted",
		"call-uncommitted",
	); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("uncommitted navigation consume error = %v", err)
	}
	if err := sink.Discard(t.Context(), interactions.ProtectedAnswerDiscardRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: distinctRequest.InteractionID, Receipt: &distinctClarify, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: request.InteractionID, Receipt: clarify,
	}); err != nil {
		t.Fatal(err)
	}
	afterClarify, err := store.Get(t.Context(), created.JobID, owner)
	if err != nil || afterClarify.Revision != created.Revision || len(afterClarify.Fields) != 0 {
		t.Fatalf("clarify mutated form state = %#v, %v", afterClarify, err)
	}

	request.Intent = interactions.ProtectedAnswerBack
	request.InteractionID = "interaction-back-unavailable"
	request.IdempotencyKey = "back-unavailable"
	if _, err := sink.Accept(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("back without prior field error = %v", err)
	}

	valueRequest := request
	valueRequest.Intent = interactions.ProtectedAnswerValue
	valueRequest.InteractionID = "interaction-value"
	valueRequest.IdempotencyKey = "value-1"
	valueRequest.Text = "MINTCLAW_NAV_PRIVATE_7c2a"
	valueReceipt, err := sink.Accept(t.Context(), valueRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: valueRequest.InteractionID, Receipt: valueReceipt,
	}); err != nil {
		t.Fatal(err)
	}
	withValue, err := store.Get(t.Context(), created.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	nextBinding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: withValue.Revision, Owner: owner, FieldID: "field.notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	backRequest := interactions.ProtectedAnswerSinkRequest{
		Binding: nextBinding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-back", IdempotencyKey: "back-1", Intent: interactions.ProtectedAnswerBack,
	}
	back, err := sink.Accept(t.Context(), backRequest)
	if err != nil {
		t.Fatal(err)
	}
	backParts, err := ParseFormProtectedNavigationReference(back.Reference)
	if err != nil || backParts.Action != interactions.ProtectedAnswerBack || backParts.Revision != withValue.Revision {
		t.Fatalf("back receipt parts = %#v, %v", backParts, err)
	}
	action, target, err := store.ResolveFormProtectedNavigationReference(
		back.Reference,
		withValue,
		[]string{"field.full_name", "field.notes"},
	)
	if err != nil || action != interactions.ProtectedAnswerBack || target != "field.full_name" {
		t.Fatalf("resolved back = (%q, %q, %v)", action, target, err)
	}

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
	if err := restartedSink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: nextBinding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: backRequest.InteractionID, Receipt: back,
	}); err != nil {
		t.Fatalf("restart navigation commit: %v", err)
	}
	action, target, err = reopened.ConsumeFormProtectedNavigationReference(
		t.Context(),
		back.Reference,
		owner,
		[]string{"field.full_name", "field.notes"},
		"execution-navigation",
		"call-navigation",
	)
	if err != nil || action != interactions.ProtectedAnswerBack || target != "field.full_name" {
		t.Fatalf("consume navigation = (%q, %q, %v)", action, target, err)
	}
	reopened.Close()
	recovered, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recovered.Close)
	action, target, err = recovered.ConsumeFormProtectedNavigationReference(
		t.Context(),
		back.Reference,
		owner,
		[]string{"field.full_name", "field.notes"},
		"execution-navigation",
		"call-navigation",
	)
	if err != nil || action != interactions.ProtectedAnswerBack || target != "field.full_name" {
		t.Fatalf("restart idempotent consume = (%q, %q, %v)", action, target, err)
	}
	if _, _, err := recovered.ConsumeFormProtectedNavigationReference(
		t.Context(),
		back.Reference,
		owner,
		[]string{"field.full_name", "field.notes"},
		"execution-navigation",
		"call-navigation-replay",
	); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("cross-call navigation replay error = %v", err)
	}
	afterRestart, err := recovered.Get(t.Context(), created.JobID, owner)
	if err != nil || afterRestart.Revision != withValue.Revision || len(afterRestart.Fields) != 1 {
		t.Fatalf("back mutated form state = %#v, %v", afterRestart, err)
	}
	tampered := back
	if strings.HasSuffix(tampered.Reference, "0") {
		tampered.Reference = strings.TrimSuffix(tampered.Reference, "0") + "1"
	} else {
		tampered.Reference = tampered.Reference[:len(tampered.Reference)-1] + "0"
	}
	recoveredSink, err := NewFormProtectedAnswerSink(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveredSink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: nextBinding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: backRequest.InteractionID, Receipt: tampered,
	}); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("tampered navigation error = %v", err)
	}
	assertFormStoreContainsNoPlaintext(t, options, "MINTCLAW_NAV_PRIVATE_7c2a")
}

func TestFormProtectedNavigationTargetUsesLedgerRevisionWhenTimestampsTie(t *testing.T) {
	record := FormJobRecord{Fields: []FormJobFieldState{
		{FieldID: "field.older", Revision: 2, UpdatedAt: 100},
		{FieldID: "field.latest", Revision: 3, UpdatedAt: 100},
	}}
	target, err := FormProtectedNavigationTarget(
		record,
		"field.current",
		"",
		interactions.ProtectedAnswerBack,
	)
	if err != nil || target != "field.latest" {
		t.Fatalf("back target = %q, %v", target, err)
	}

	record.Fields[0].Revision = record.Fields[1].Revision
	if _, err := FormProtectedNavigationTarget(
		record,
		"field.current",
		"",
		interactions.ProtectedAnswerBack,
	); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("ambiguous ledger order error = %v", err)
	}
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

func TestFormProtectedAnswerSinkAbandonsQuestionWithoutChangingJob(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner, FieldID: "field.guidance",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := NewFormProtectedAnswerSink(store)
	if err = sink.Discard(t.Context(), interactions.ProtectedAnswerDiscardRequest{
		Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "interaction-guidance", Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := store.Get(t.Context(), created.JobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != created.Revision || after.State != created.State || len(after.Fields) != len(created.Fields) {
		t.Fatalf("abandoning a question changed the form job: before=%#v after=%#v", created, after)
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
