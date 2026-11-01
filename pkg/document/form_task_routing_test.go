package document

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func startTaskRouteFixture(t *testing.T, store *FormJobStore) (FormIntakeStartRequest, FormTaskRouteSnapshot) {
	t.Helper()
	request := intakeFixtureRequest(t, store)
	mode, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: mode.Progress.ModeID, ExpectedRevision: request.ExpectedRevision,
		Owner: request.Owner, MessageIdentity: "switch-new-task",
		Text: "PRIVATE_NEW_TASK_REQUEST", MediaRefs: []string{"media://private-new-attachment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return request, route
}

func decideTaskRouteFixture(
	t *testing.T, store *FormJobStore, request FormIntakeStartRequest, route FormTaskRouteSnapshot,
	action FormTaskRouteAction,
) FormTaskRouteSnapshot {
	t.Helper()
	pass, err := store.ReserveFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	decision := FormTaskRouteDecision{
		Reference: route.Reference, Owner: request.Owner, ExpectedRevision: pass.Revision,
		Pass: pass.Passes, Action: action,
	}
	if action == FormTaskSwitch || action == FormTaskIndependent {
		decision.Evidence = FormIntakeLiteral{Text: strings.TrimSpace(pass.Text), Occurrence: 1}
	}
	result, err := store.DecideFormTaskRoute(t.Context(), decision)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.DecideFormTaskRoute(t.Context(), decision)
	if err != nil || !replay.Reused || replay.State != result.State || replay.Passes != result.Passes {
		t.Fatal("same private routing outcome was not immutable")
	}
	decision.Action = FormTaskAsk
	if action != FormTaskAsk {
		if _, err := store.DecideFormTaskRoute(t.Context(), decision); !errors.Is(err, ErrFormJobAnswerConflict) {
			t.Fatal("changed routing outcome replay was accepted")
		}
	}
	return result
}

func TestFormTaskRoutingPreservesDraftAndGrantsOnlyNewInputOnceAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	if route.State != FormTaskPending || route.Passes != 0 || route.Revision != request.ExpectedRevision {
		t.Fatal("routing admission changed the assignment revision or spent extraction budget")
	}
	if _, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("uninterpreted input granted a task switch")
	}
	first := decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	if first.State != FormTaskVerify {
		t.Fatal("one advisory interpretation granted a task switch")
	}
	if _, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("unverified intent suspended the form")
	}
	second := decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	if second.State != FormTaskReady || second.Passes != 2 {
		t.Fatal("independent reserved confirmation did not select the bound request")
	}
	encoded, err := json.Marshal(second)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "media://") {
		t.Fatal("routing metadata disclosed private text or attachment references")
	}
	handoff, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	if err != nil || handoff.Reused || handoff.Text != "PRIVATE_NEW_TASK_REQUEST" ||
		len(handoff.Media) != 1 || handoff.Media[0] != "media://private-new-attachment" ||
		handoff.Progress.State != FormIntakeSuspended {
		t.Fatal("switch failed to release only the newly authorized request")
	}
	if progress, err := store.FindFormIntake(t.Context(), request.Owner); err != nil || progress != nil {
		t.Fatal("suspended draft still captured the next task")
	}
	before, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || before.LedgerRevision != 0 || before.State.terminal() ||
		before.SourceDigest != request.Schema.SourceSHA256 {
		t.Fatal("switch canceled the original job, changed its source or wrote values")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	replay, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	if err != nil || !replay.Reused || replay.Text != "" || len(replay.Media) != 0 {
		t.Fatal("restart replay released the task again")
	}
	progress, err := store.FormIntakeForJob(t.Context(), request.JobID, request.Owner)
	if err != nil || progress == nil || progress.State != FormIntakeSuspended {
		t.Fatal("preserved draft could not be found by exact authenticated identity")
	}
	resumed, err := store.SetFormIntakeState(t.Context(), request.JobID, progress.ModeID,
		progress.Revision, request.Owner, FormIntakeActive)
	if err != nil || resumed.State != FormIntakeActive {
		t.Fatal("preserved draft did not resume")
	}
}

func TestFormTaskRoutingRejectsWrongOwnerChangedMediaPartialEvidenceAndStaleDecision(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	wrong := request.Owner
	wrong.TopicID = "different-topic"
	if _, err := store.ReadFormTaskRoute(t.Context(), route.Reference, wrong); err == nil {
		t.Fatal("wrong topic obtained the protected routing request")
	}
	if _, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, wrong); err == nil {
		t.Fatal("wrong topic controlled the original form")
	}
	_, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: route.ModeID, ExpectedRevision: route.Revision, Owner: request.Owner,
		MessageIdentity: "switch-new-task", Text: route.Text, MediaRefs: []string{"media://different-file"},
	})
	if !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("same incoming message substituted a different attachment")
	}
	pass, err := store.ReserveFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	decision := FormTaskRouteDecision{
		Reference: route.Reference, ExpectedRevision: pass.Revision, Pass: pass.Passes,
		Owner: request.Owner, Action: FormTaskSwitch,
		Evidence: FormIntakeLiteral{Text: "NEW_TASK", Occurrence: 1},
	}
	if _, err := store.DecideFormTaskRoute(t.Context(), decision); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("partial evidence granted release of the whole request")
	}
	if _, err := store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID,
		route.Revision, request.Owner, FormIntakePaused); err != nil {
		t.Fatal(err)
	}
	decision.Evidence.Text = route.Text
	if _, err := store.DecideFormTaskRoute(t.Context(), decision); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("stale private interpretation acquired routing authority")
	}
}

func TestFormTaskRoutingPrivateReservationIsBoundedAndCannotErasePendingInput(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	for i := 0; i < MaxFormTaskRoutingPasses; i++ {
		if _, err := store.ReserveFormTaskRoute(t.Context(), route.Reference, request.Owner); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	store, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.ReserveFormTaskRoute(
		t.Context(),
		route.Reference,
		request.Owner,
	); !errors.Is(
		err,
		ErrFormJobCapacityExceeded,
	) {
		t.Fatal("restart reset the native routing call limit")
	}
	current, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil || current.Passes != MaxFormTaskRoutingPasses || current.Text != route.Text {
		t.Fatal("capacity failure discarded or replaced the protected request")
	}
	if _, err := store.SetFormIntakeState(t.Context(), request.JobID, route.ModeID,
		current.Revision, request.Owner, FormIntakeClosed); err == nil {
		t.Fatal("closing the intake bypassed the pending routing request")
	}
}

func TestFormTaskRoutingContinuesExistingProtectedFieldAdmissionAtomically(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	mode, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	const text = "The previous telephone belongs only to the borrower; the borrower city is Sampletown."
	route, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: mode.Progress.ModeID, ExpectedRevision: request.ExpectedRevision,
		Owner: request.Owner, MessageIdentity: "same-form-clarification", Text: text,
	})
	if err != nil {
		t.Fatal(err)
	}
	selected := decideTaskRouteFixture(t, store, request, route, FormTaskContinue)
	if selected.State != FormTaskCurrent {
		t.Fatal("same-form clarification did not remain protected")
	}
	admission := FormIntakeMessageRequest{
		JobID: request.JobID, ModeID: route.ModeID, ExpectedRevision: route.Revision, Owner: request.Owner,
		Schema: request.Schema, MessageIdentity: route.Reference, Text: text, RoutingReference: route.Reference,
	}
	result, err := store.AdmitFormIntakeMessage(t.Context(), admission)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil || current.FieldReference != result.Receipt || current.Text != "" ||
		result.Progress.PendingInputs != 1 || result.Progress.PendingTasks != 0 || result.Progress.TaskBytes != 0 {
		t.Fatal("routing did not bind/erase its copy in the same field-admission transaction")
	}
	replay, err := store.AdmitFormIntakeMessage(t.Context(), admission)
	if err != nil || !replay.Reused || replay.Receipt != result.Receipt {
		t.Fatal("routing replay admitted a second field input")
	}
	if _, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("field data were released as another task")
	}
}

func TestFormTaskRoutingPreservesAcceptedAndUnresolvedFactsAndEnforcesResumeExclusivity(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, mode.Progress.ModeID, mode.Job.Revision, "existing-private-facts",
		"PRIVATE_ACCEPTED_NAME; PRIVATE_UNRESOLVED_VALUE",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, input.Receipt, input.Job.Revision, []int{0}, "one-fact")
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "PRIVATE_ACCEPTED_NAME")}
	accepted, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || accepted.Consumed || len(accepted.AcceptedFieldIDs) != 1 {
		t.Fatal("could not establish accepted plus unresolved private facts")
	}
	route, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: mode.Progress.ModeID, Owner: request.Owner,
		ExpectedRevision: accepted.Job.Revision, MessageIdentity: "new-task-preserve-facts",
		Text: "Please help with this separate document instead.", MediaRefs: []string{"media://new-private-file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	handoff, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	if err != nil || strings.Contains(handoff.Text, "PRIVATE_ACCEPTED") || handoff.Progress.PendingInputs != 1 {
		t.Fatal("handoff exposed old form values or lost retained input")
	}
	preserved, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || !reflect.DeepEqual(preserved.Fields, accepted.Job.Fields) ||
		preserved.LedgerRevision != accepted.Job.LedgerRevision {
		t.Fatal("suspension changed accepted field state or ledger")
	}
	private, err := store.ReadFormIntakeInput(t.Context(), input.Receipt, request.Owner)
	if err != nil || private.Text != "PRIVATE_ACCEPTED_NAME; PRIVATE_UNRESOLVED_VALUE" ||
		len(private.Accounted) != 1 {
		t.Fatal("suspension lost unresolved text or its accepted evidence accounting")
	}
	assertFormStoreContainsNoPlaintext(t, options, "PRIVATE_ACCEPTED_NAME", "PRIVATE_UNRESOLVED_VALUE")
	other := intakeFixtureRequest(t, store)
	otherMode, err := store.StartFormIntake(t.Context(), other)
	if err != nil {
		t.Fatal("preserved draft prevented a new form")
	}
	if _, err = store.SetFormIntakeState(t.Context(), request.JobID, mode.Progress.ModeID,
		handoff.Progress.Revision, request.Owner, FormIntakeActive); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("resume allowed two forms to capture the same route")
	}
	if _, err = store.SetFormIntakeState(t.Context(), other.JobID, otherMode.Progress.ModeID,
		otherMode.Job.Revision, other.Owner, FormIntakeClosed); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetFormIntakeState(t.Context(), request.JobID, mode.Progress.ModeID,
		handoff.Progress.Revision, request.Owner, FormIntakeActive); err != nil {
		t.Fatal("preserved draft could not resume after the other form finished")
	}
}

func TestFormTaskRoutingAtomicFailureDoesNotReleaseOrSuspend(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected atomic write failure") }
	result, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner)
	store.writeFile = write
	if err == nil || result.Text != "" || len(result.Media) != 0 || result.Progress.JobID != "" {
		t.Fatal("failed publication released the new request")
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed publication changed the preserved draft")
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.State != FormIntakeActive {
		t.Fatal("failed publication stopped capturing protected form input")
	}
	if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err != nil {
		t.Fatal("atomic retry could not publish the ungranted request")
	}
}

func TestFormTaskRoutingDisagreementAndAttachmentStayProtected(t *testing.T) {
	for _, first := range []FormTaskRouteAction{FormTaskSwitch, FormTaskContinue, FormTaskAsk} {
		t.Run(string(first), func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			request, route := startTaskRouteFixture(t, store)
			route = decideTaskRouteFixture(t, store, request, route, first)
			if first == FormTaskSwitch {
				route = decideTaskRouteFixture(t, store, request, route, FormTaskContinue)
			}
			if route.State != FormTaskQuestion || route.Text == "" || len(route.MediaRefs) != 1 {
				t.Fatal("ambiguous attachment or disagreeing interpretation lost the pending request")
			}
			if _, err := store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
				t.Fatal("ambiguous request escaped into ordinary history")
			}
		})
	}
}

func TestFormTaskRoutingReferencesAreExactNativeIdentifiers(t *testing.T) {
	valid := "form_route.form_job_" + strings.Repeat("a", 32) + ".form_route_" + strings.Repeat("b", 32)
	if _, _, err := ParseFormTaskRouteReference(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		valid + ".extra", strings.ToUpper(valid), valid + " ",
		strings.Replace(valid, strings.Repeat("a", 32), "a", 1),
		strings.Replace(valid, strings.Repeat("b", 32), strings.Repeat("z", 32), 1),
	} {
		if _, _, err := ParseFormTaskRouteReference(invalid); err == nil {
			t.Fatal("malformed route reference was accepted")
		}
	}
}

func TestFormTaskRoutingStaleFirstConfirmationFallsBackToProtectedQuestion(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request, route := startTaskRouteFixture(t, store)
	route = decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
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
	fresh := decideTaskRouteFixture(t, store, request, route, FormTaskSwitch)
	if fresh.State != FormTaskQuestion || fresh.Revision != resumed.Revision || fresh.Passes != 2 ||
		fresh.Text != route.Text || len(fresh.MediaRefs) != len(route.MediaRefs) {
		t.Fatal("a stale earlier confirmation did not preserve the request in a valid protected question")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err = store.GrantFormTaskHandoff(t.Context(), route.Reference, request.Owner); err == nil {
		t.Fatal("a stale earlier confirmation granted the new task")
	}
	if _, err = store.ReserveFormTaskRoute(
		t.Context(),
		route.Reference,
		request.Owner,
	); !errors.Is(
		err,
		ErrFormJobConflict,
	) {
		t.Fatal("question recovery continued spending the automatic confirmation budget")
	}
	current, err := store.ReadFormTaskRoute(t.Context(), route.Reference, request.Owner)
	if err != nil || current.State != FormTaskQuestion || current.Passes != 2 || current.Text != route.Text {
		t.Fatal("restart or rejected automatic retry lost the bounded protected recovery state")
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.State != FormIntakeActive {
		t.Fatal("stale confirmation suspended or erased the original draft")
	}
}
