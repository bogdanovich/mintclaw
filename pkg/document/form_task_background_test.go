package document

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestFormTaskTextDetourKeepsTheSameDraftCapturing(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	mode, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: mode.Progress.ModeID, ExpectedRevision: request.ExpectedRevision,
		Owner: request.Owner, MessageIdentity: "casual-detour", Text: "What is two plus two?",
	})
	if err != nil {
		t.Fatal(err)
	}
	decideTaskRouteFixture(t, store, request, route, FormTaskIndependent)
	decideTaskRouteFixture(t, store, request, route, FormTaskIndependent)
	handoff, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	if err != nil || handoff.Text != route.Text || handoff.Progress.State != FormIntakeActive {
		t.Fatal("ordinary text detour suspended the draft and lost protected return routing")
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.ModeID != mode.Progress.ModeID {
		t.Fatal("ordinary return could not find the same protected draft")
	}
	record, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || record.LedgerRevision != 0 || record.SourceDigest != request.Schema.SourceSHA256 {
		t.Fatal("text detour changed form facts or source")
	}
}

func TestFormTaskIndependentReplyConcurrentGrantIsOnceOnly(t *testing.T) {
	store, _, request, _, sink, answer := taskQuestionFixture(t)
	receipt := commitTaskReplyFixture(t, sink, answer)
	decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
	decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
	results := make(chan FormTaskHandoff, 8)
	errors := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			result, err := store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner)
			results <- result
			errors <- err
		})
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for result := range results {
		if !result.Reused {
			fresh++
			if result.Text != answer.Text || len(result.Media) != 0 {
				t.Fatal("fresh reply grant was incomplete or contained the original media")
			}
		} else if result.Text != "" || len(result.Media) != 0 {
			t.Fatal("concurrent replay released text or media")
		}
	}
	if fresh != 1 {
		t.Fatal("parallel reply consumers did not have exactly one fresh grant")
	}
}

func decideIndependentReplyFixture(t *testing.T, store *FormJobStore, reference string, owner FormJobOwner) {
	t.Helper()
	reserved, err := store.ReserveFormTaskAnswerPass(t.Context(), reference, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DecideFormTaskAnswer(t.Context(), FormTaskAnswerDecision{
		Reference: reference, Owner: owner, ExpectedRevision: reserved.Revision,
		Pass: reserved.Answer.Passes, Action: FormTaskIndependent,
		Evidence: FormIntakeLiteral{Text: strings.TrimSpace(reserved.Answer.Text), Occurrence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFormTaskIndependentReplyNeverGrantsOriginalRequestAcrossRestart(t *testing.T) {
	store, options, request, route, sink, answer := taskQuestionFixture(t)
	answer.Text = "What is two plus two?"
	receipt := commitTaskReplyFixture(t, sink, answer)
	before, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
	if _, err = store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner); err == nil {
		t.Fatal("one advisory proof granted a side reply")
	}
	decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	wrong := request.Owner
	wrong.TopicID = "another-topic"
	if _, err = store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, wrong); err == nil {
		t.Fatal("another topic granted the private reply")
	}
	handoff, err := store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner)
	if err != nil || handoff.Reused || handoff.Text != answer.Text || len(handoff.Media) != 0 ||
		handoff.Progress.State != FormIntakeActive {
		t.Fatal("side reply was lost, repeated, attached original media or suspended the form")
	}
	current, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil || current.State != FormTaskQuestion || current.Text != before.Text ||
		!reflect.DeepEqual(current.MediaRefs, before.MediaRefs) || current.GrantRevision != before.GrantRevision {
		t.Fatal("side reply authorized or changed the original pending payload")
	}
	if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("unrelated conversation became consent for the original request")
	}
	replay, err := store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner)
	if err != nil || !replay.Reused || replay.Text != "" || len(replay.Media) != 0 {
		t.Fatal("side reply replay repeated a task")
	}
	record, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || record.LedgerRevision != 0 || record.SourceDigest != request.Schema.SourceSHA256 {
		t.Fatal("unrelated conversation wrote form facts or changed source")
	}
	next, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: route.ModeID, Owner: request.Owner,
		ExpectedRevision: record.Revision, MessageIdentity: "conversational-follow-up", Text: "Why?",
	})
	if err != nil || !next.PreviousUnrelated {
		t.Fatal("settled reply focus was not available to the next private classifier")
	}
	decideTaskRouteFixture(t, store, request, next, FormTaskContinue)
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), FormIntakeMessageRequest{
		JobID: request.JobID, ModeID: route.ModeID, ExpectedRevision: record.Revision, Owner: request.Owner,
		Schema: request.Schema, MessageIdentity: next.Reference, RoutingReference: next.Reference, Text: next.Text,
	})
	if err != nil {
		t.Fatal("ordinary form return failed protected admission", err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
	if err != nil || input.Text != next.Text || input.JobID != route.JobID || input.Reference == "" {
		t.Fatal("return lost the original draft identity")
	}
	assertFormStoreContainsNoPlaintext(t, options, answer.Text, before.Text)
}

func TestFormTaskIndependentRoutingRejectsAttachmentDisagreementAndStaleReply(t *testing.T) {
	t.Run("attachment", func(t *testing.T) {
		store, _ := newTestFormJobStore(t)
		request, route := startTaskRouteFixture(t, store)
		pass, err := store.ReserveFormTaskRoute(t.Context(), route.Reference, request.Owner)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.DecideFormTaskRoute(t.Context(), FormTaskRouteDecision{
			Reference: route.Reference, Owner: request.Owner, ExpectedRevision: pass.Revision,
			Pass: pass.Passes, Action: FormTaskIndependent,
			Evidence: FormIntakeLiteral{Text: route.Text, Occurrence: 1},
		})
		if !errors.Is(err, ErrFormJobAnswerConflict) {
			t.Fatal("text-only detour released media")
		}
	})
	t.Run("disagreement", func(t *testing.T) {
		store, _, request, route, sink, answer := taskQuestionFixture(t)
		receipt := commitTaskReplyFixture(t, sink, answer)
		decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
		pass, err := store.ReserveFormTaskAnswerPass(t.Context(), receipt.Reference, request.Owner)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.DecideFormTaskAnswer(t.Context(), FormTaskAnswerDecision{
			Reference: receipt.Reference, Owner: request.Owner, ExpectedRevision: pass.Revision,
			Pass: pass.Answer.Passes, Action: FormTaskAsk,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner); err == nil {
			t.Fatal("disagreeing classifiers released the reply")
		}
		if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
			t.Fatal("disagreement released original attachment")
		}
	})
	t.Run("stale", func(t *testing.T) {
		store, _, request, route, sink, answer := taskQuestionFixture(t)
		receipt := commitTaskReplyFixture(t, sink, answer)
		decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
		decideIndependentReplyFixture(t, store, receipt.Reference, request.Owner)
		if _, err := store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID,
			route.Revision, request.Owner, FormIntakePaused); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GrantFormTaskIndependentReply(t.Context(), receipt.Reference, request.Owner); err == nil {
			t.Fatal("stale side reply obtained a live grant")
		}
		view, err := store.ReadFormTaskRouteAnswer(t.Context(), receipt.Reference, request.Owner)
		if err != nil || view.Answer.Text != answer.Text {
			t.Fatal("stale refusal erased retained data")
		}
	})
}
