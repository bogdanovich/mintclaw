package document

import (
	"errors"
	"testing"
)

func TestFormTaskStaleReadyRequiresFreshConsentAcrossRestart(t *testing.T) {
	for _, choice := range []string{"automatic", "canonical", "private"} {
		t.Run(choice, func(t *testing.T) {
			store, options, request, route, sink, answer := taskQuestionFixture(t)
			if choice == "automatic" {
				store.Close()
				store, options = newTestFormJobStore(t)
				request, route = startTaskRouteFixture(t, store)
				route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
				route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
			} else {
				if choice == "canonical" {
					answer.Text = "switch_task"
				}
				receipt := commitTaskReplyFixture(t, sink, answer)
				var err error
				if choice == "canonical" {
					route, err = store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner)
				} else {
					for range 2 {
						reserved, reserveErr := store.ReserveFormTaskAnswerPass(
							t.Context(),
							receipt.Reference,
							request.Owner,
						)
						if reserveErr != nil {
							t.Fatal(reserveErr)
						}
						route, err = store.DecideFormTaskAnswer(t.Context(), FormTaskAnswerDecision{
							Reference: receipt.Reference, Owner: request.Owner, ExpectedRevision: reserved.Revision,
							Pass: reserved.Answer.Passes, Action: FormTaskSwitch,
							Evidence: FormIntakeLiteral{Text: answer.Text, Occurrence: 1},
						})
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if route.State != FormTaskReady {
				t.Fatal("fixture did not establish current-revision consent")
			}
			if err := store.QuestionFormTaskRoute(
				t.Context(),
				route.Reference,
				route.Revision,
				request.Owner,
			); err == nil {
				t.Fatal("fresh ready consent was needlessly replaced")
			}
			paused, err := store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID,
				route.Revision, request.Owner, FormIntakePaused)
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID,
				paused.Revision, request.Owner, FormIntakeActive)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			if _, err = store.GrantFormTaskHandoff(
				t.Context(),
				route.Reference,
				request.Owner,
			); !errors.Is(
				err,
				ErrFormJobConflict,
			) {
				t.Fatal("stale ready consent granted a task")
			}
			if err = store.QuestionFormTaskRoute(
				t.Context(),
				route.Reference,
				resumed.Revision,
				request.Owner,
			); err != nil {
				t.Fatal(err)
			}
			fresh, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
			if err != nil || fresh.State != FormTaskQuestion || fresh.Text != route.Text ||
				len(fresh.MediaRefs) != len(route.MediaRefs) || fresh.Passes != route.Passes {
				t.Fatal("fresh question lost retained input/media or reset automatic budgets")
			}
			answer.Binding, err = store.NewFormTaskRouteQuestion(t.Context(), route.Reference, request.Owner)
			if err != nil {
				t.Fatal(err)
			}
			sink, err = NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			answer.MessageIdentity, answer.InteractionID, answer.Text = "fresh-choice", "fresh-question", "switch_task"
			receipt := commitTaskReplyFixture(t, sink, answer)
			if _, err = store.ApplyFormTaskRouteChoice(t.Context(), receipt.Reference, request.Owner); err != nil {
				t.Fatal(err)
			}
			handoff, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
			if err != nil || handoff.Text != route.Text || len(handoff.Media) != len(route.MediaRefs) {
				t.Fatal("fresh consent did not recover exactly the retained request")
			}
			if replay, replayErr := store.GrantFormTaskHandoff(
				t.Context(),
				route.Reference,
				request.Owner,
			); replayErr != nil ||
				!replay.Reused || replay.Text != "" ||
				len(replay.Media) != 0 {
				t.Fatal("recovery allowed a second handoff")
			}
		})
	}
}
