package document

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const intakePrivateSentinel = "MINTCLAW_PDFI4_INTAKE_PRIVATE_4c91"

func intakeFixtureRequest(t *testing.T, store *FormJobStore) FormIntakeStartRequest {
	t.Helper()
	_, schema := fillNormalizationFixture()
	owner := testFormJobOwner()
	digest, err := FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	create := testFormJobCreateRequest(owner)
	create.StartIdempotencyKey = t.TempDir()
	create.SourceDigest, create.FieldSchemaDigest, create.BackendRevision = schema.SourceSHA256, digest, backend
	job, err := store.Create(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		ids = append(ids, field.ID)
	}
	return FormIntakeStartRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: ids, FocusFieldID: ids[0], RoleContext: "Applicant supplies their own facts",
			Question: "Provide the known details in your own words", Language: "en",
		},
		IdempotencyKey: "start-intake-platform-1",
	}
}

func intakeMessage(
	request FormIntakeStartRequest,
	modeID string,
	revision int64,
	identity, text string,
) FormIntakeMessageRequest {
	return FormIntakeMessageRequest{
		JobID: request.JobID, ModeID: modeID, ExpectedRevision: revision, Owner: request.Owner,
		Schema: request.Schema, MessageIdentity: identity, Text: text,
	}
}

func TestFormIntakeStandaloneMessagesRestartReplayAndPrivateProjection(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil || started.Reused || started.Job.Revision != request.ExpectedRevision {
		t.Fatalf("start = %#v, %v", started, err)
	}
	replayedStart, err := store.StartFormIntake(t.Context(), request)
	if err != nil || !replayedStart.Reused || replayedStart.Binding.Token != started.Binding.Token {
		t.Fatalf("start replay = %#v, %v", replayedStart, err)
	}
	firstMessage := intakeMessage(
		request,
		started.Progress.ModeID,
		started.Job.Revision,
		"platform-1",
		intakePrivateSentinel,
	)
	first, err := store.AdmitFormIntakeMessage(t.Context(), firstMessage)
	if err != nil || first.Reused || first.Job.Revision != request.ExpectedRevision+1 || len(first.Job.Fields) != 0 {
		t.Fatalf("raw admission incorrectly accepted field facts: %#v, %v", first, err)
	}
	revision := first.Job.Revision
	for index := 2; index <= 3; index++ {
		next, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			request, started.Progress.ModeID, revision, fmt.Sprintf("platform-%d", index),
			fmt.Sprintf("More freely worded synthetic facts, message %d", index),
		))
		if err != nil {
			t.Fatal(err)
		}
		revision = next.Job.Revision
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	progress, err := reopened.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.PendingInputs != 3 || progress.Revision != revision {
		t.Fatalf("restart discovery = %#v, %v", progress, err)
	}
	input, err := reopened.ReadFormIntakeInput(t.Context(), first.Receipt, request.Owner)
	if err != nil || input.Text != intakePrivateSentinel || input.Context.Question != request.Context.Question {
		t.Fatalf("private read failed: %v", err)
	}
	encoded, err := json.Marshal([]any{started, first, progress, input})
	if err != nil || strings.Contains(string(encoded), intakePrivateSentinel) ||
		strings.Contains(string(encoded), "Applicant supplies") {
		t.Fatal("private input or interpretation context leaked into ordinary JSON")
	}
	assertFormStoreContainsNoPlaintext(
		t,
		options,
		intakePrivateSentinel,
		"More freely worded",
		request.Context.Question,
	)
	// Replay at the original revision after two later messages is still one
	// input. A fabricated higher revision never causes reinterpretation either.
	for _, replayRevision := range []int64{firstMessage.ExpectedRevision, revision, revision + 99} {
		firstMessage.ExpectedRevision = replayRevision
		replay, err := reopened.AdmitFormIntakeMessage(t.Context(), firstMessage)
		if err != nil || !replay.Reused || replay.Receipt != first.Receipt || replay.Progress.InputCount != 3 ||
			replay.Job.Revision != revision {
			t.Fatalf("message replay = %#v, %v", replay, err)
		}
	}
	firstMessage.Text += " changed"
	if _, err := reopened.AdmitFormIntakeMessage(t.Context(), firstMessage); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("same transport identity changed body: %v", err)
	}
	for _, mutate := range []func(*FormJobOwner){
		func(o *FormJobOwner) { o.SenderID = "someone-else" },
		func(o *FormJobOwner) { o.TopicID = "another-topic" },
		func(o *FormJobOwner) { o.WorkspaceID = "another-workspace" },
		func(o *FormJobOwner) { o.AccountID = "another-account" },
		func(o *FormJobOwner) { o.RouteSessionKey += "-other" },
	} {
		owner := request.Owner
		mutate(&owner)
		if _, err := reopened.ReadFormIntakeInput(
			t.Context(),
			first.Receipt,
			owner,
		); !errors.Is(
			err,
			ErrFormJobUnauthorized,
		) {
			t.Fatalf("cross-route private read: %v", err)
		}
		found, err := reopened.FindFormIntake(t.Context(), owner)
		if err != nil || found != nil {
			t.Fatalf("cross-route discovery = %#v, %v", found, err)
		}
	}
	changedSource := intakeMessage(request, started.Progress.ModeID, revision, "new-transport", "New facts")
	changedSource.Schema.SourceSHA256 = strings.Repeat("b", 64)
	if _, err := reopened.AdmitFormIntakeMessage(t.Context(), changedSource); !errors.Is(err, ErrFormJobStale) {
		t.Fatalf("source change accepted: %v", err)
	}
}

func TestFormIntakeQuestionClaimStagingRestartCommitAndTransportReplay(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	answer := interactions.ProtectedAnswerSinkRequest{
		Binding: started.Binding, Workspace: request.Owner.WorkspaceID, Route: testProtectedAnswerRoute(request.Owner),
		InteractionID: "question-1", IdempotencyKey: "legacy-question-scoped-key",
		MessageIdentity: "stable-platform-1", Intent: interactions.ProtectedAnswerValue, Text: intakePrivateSentinel,
	}
	receipt, err := sink.Accept(t.Context(), answer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadFormIntakeInput(
		t.Context(),
		receipt.Reference,
		request.Owner,
	); !errors.Is(
		err,
		ErrFormJobConflict,
	) {
		t.Fatalf("unclaimed private input readable: %v", err)
	}
	if result, err := store.AppendComposite(t.Context(), FormCompositeRequest{
		JobID: request.JobID, ExpectedRevision: started.Job.Revision, Owner: request.Owner, Schema: request.Schema,
		Assignments: []FormCompositeAssignment{
			{
				FieldID: request.Schema.Fields[0].ID,
				Value:   FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic Person"},
			},
		}, IdempotencyKey: "competing-composite",
	}); !errors.Is(err, ErrFormJobConflict) || result.Receipt != "" {
		t.Fatal("composite advanced a pending question's revision")
	}
	if _, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: request.JobID, ExpectedRevision: started.Job.Revision, Owner: request.Owner,
		FieldID: request.Schema.Fields[0].ID, IdempotencyKey: "competing-single",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic Person"},
		State: FormValueSupplied, Source: FormValueSourceUser,
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("single-field append advanced a pending question's revision")
	}
	if _, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, answer.MessageIdentity, answer.Text,
	)); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("standalone retry committed unclaimed question: %v", err)
	}
	if again, err := sink.Accept(t.Context(), answer); err != nil || again != receipt {
		t.Fatalf("staged replay = %#v, %v", again, err)
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	sink, err = NewFormProtectedAnswerSink(reopened)
	if err != nil {
		t.Fatal(err)
	}
	commit := interactions.ProtectedAnswerCommitRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: receipt,
	}
	wrong := commit
	wrong.InteractionID = "different-question"
	if err := sink.Commit(t.Context(), wrong); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("wrong question claimed input: %v", err)
	}
	if err := sink.Commit(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(t.Context(), commit); err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	input, err := reopened.ReadFormIntakeInput(t.Context(), receipt.Reference, request.Owner)
	if err != nil || input.Text != intakePrivateSentinel || input.Revision != started.Job.Revision+1 {
		t.Fatalf("committed private input unavailable: %v", err)
	}
	nativeReplay, err := reopened.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, input.Revision, answer.MessageIdentity, answer.Text,
	))
	if err != nil || !nativeReplay.Reused || nativeReplay.Receipt != receipt.Reference ||
		nativeReplay.Progress.InputCount != 1 {
		t.Fatalf("question-to-standalone replay = %#v, %v", nativeReplay, err)
	}
	stale := answer
	stale.MessageIdentity, stale.IdempotencyKey = "new-message", "new-legacy-key"
	if _, err := sink.Accept(t.Context(), stale); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("new answer to old binding accepted: %v", err)
	}
	if err := sink.Discard(t.Context(), interactions.ProtectedAnswerDiscardRequest{
		Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
		InteractionID: answer.InteractionID, Receipt: &receipt, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReadFormIntakeInput(t.Context(), receipt.Reference, request.Owner); err != nil {
		t.Fatal("discard removed a durably claimed input")
	}
	assertFormStoreContainsNoPlaintext(t, options, intakePrivateSentinel)
}

func TestFormIntakeBudgetCapsPersistAndDoNotResetOnRetry(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", MaxFormIntakeInputBytes)
	tooLarge := intakeMessage(request, started.Progress.ModeID, started.Job.Revision, "too-large", large+"x")
	if result, err := store.AdmitFormIntakeMessage(t.Context(), tooLarge); err == nil || result.Receipt != "" {
		t.Fatal("oversized input acquired a receipt")
	}
	revision := started.Job.Revision
	var first FormIntakeAdmission
	for index := 0; index < MaxFormIntakePendingInputs; index++ {
		admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			request, started.Progress.ModeID, revision, fmt.Sprintf("large-%d", index), large,
		))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = admitted
		}
		revision = admitted.Job.Revision
	}
	if result, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, revision, "overflow", "do not evict any previous input",
	)); !errors.Is(err, ErrFormJobCapacityExceeded) || result.Receipt != "" {
		t.Fatalf("pending input cap: %#v, %v", result, err)
	}
	passRequest := FormIntakePassRequest{
		Reference: first.Receipt, ExpectedRevision: revision, Owner: request.Owner, Schema: request.Schema,
		CandidateIDs: request.Context.CandidateFieldIDs, IdempotencyKey: "pass-0",
	}
	pass, err := store.ReserveFormIntakePass(t.Context(), passRequest)
	if err != nil || pass.Input.Text != large || pass.Input.Passes != 1 || len(pass.Fields) != 8 {
		t.Fatalf("bounded protected pass unavailable: %v", err)
	}
	replayed, err := store.ReserveFormIntakePass(t.Context(), passRequest)
	if err != nil || !replayed.Reused || replayed.ID != pass.ID || replayed.Input.Passes != 1 {
		t.Fatalf("pass replay = %#v, %v", replayed, err)
	}
	changed := passRequest
	changed.CandidateIDs = slices.Clone(changed.CandidateIDs)
	slices.Reverse(changed.CandidateIDs)
	if _, err := store.ReserveFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("same pass key changed candidates: %v", err)
	}
	changed.CandidateIDs = append(changed.CandidateIDs, changed.CandidateIDs[0])
	if _, err := store.ReserveFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatalf("unbounded candidate view: %v", err)
	}
	for index := 1; index < MaxFormIntakeExtractionPasses; index++ {
		passRequest.IdempotencyKey = fmt.Sprintf("pass-%d", index)
		if _, err := store.ReserveFormIntakePass(t.Context(), passRequest); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	last, err := reopened.ReserveFormIntakePass(t.Context(), passRequest)
	if err != nil || !last.Reused || last.Input.Passes != MaxFormIntakeExtractionPasses {
		t.Fatalf("last budget reservation not recoverable: %v", err)
	}
	for _, originalKey := range []string{"pass-0", "pass-17"} {
		passRequest.IdempotencyKey = originalKey
		old, err := reopened.ReserveFormIntakePass(t.Context(), passRequest)
		if err != nil || !old.Reused || old.Input.Passes != MaxFormIntakeExtractionPasses {
			t.Fatalf("older reservation retry at the full budget: %v", err)
		}
	}
	passRequest.IdempotencyKey = "one-too-many"
	if result, err := reopened.ReserveFormIntakePass(
		t.Context(),
		passRequest,
	); !errors.Is(err, ErrFormJobCapacityExceeded) ||
		result.ID != "" {
		t.Fatalf("extraction budget reset: %#v, %v", result, err)
	}
	input, err := reopened.ReadFormIntakeInput(t.Context(), first.Receipt, request.Owner)
	if err != nil || input.Text != large || input.Passes != MaxFormIntakeExtractionPasses {
		t.Fatal("budget failure dropped unresolved input")
	}
	encoded, err := json.Marshal(last)
	if err != nil || strings.Contains(string(encoded), large) || strings.Contains(string(encoded), `"fields"`) {
		t.Fatal("reserved private candidate view escaped ordinary JSON")
	}
}

func TestFormIntakeAtomicWriteFailureConcurrentDuplicateAndNoPrefix(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message := intakeMessage(
		request,
		started.Progress.ModeID,
		started.Job.Revision,
		"same-platform-message",
		intakePrivateSentinel,
	)
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected atomic publication failure") }
	if result, err := store.AdmitFormIntakeMessage(
		t.Context(),
		message,
	); err == nil || result.Receipt != "" ||
		result.Job.JobID != "" {
		t.Fatal("failed publication returned acceptance")
	}
	store.writeFile = write
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed atomic publication modified snapshot")
	}
	secondStore, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondStore.Close)
	results := make(chan FormIntakeAdmission, 8)
	errorsFound := make(chan error, 8)
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Go(func() {
			result, err := secondStore.AdmitFormIntakeMessage(t.Context(), message)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		})
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	newCount, receipt := 0, ""
	for result := range results {
		if !result.Reused {
			newCount++
		}
		if receipt == "" {
			receipt = result.Receipt
		}
		if result.Receipt != receipt || result.Progress.InputCount != 1 ||
			result.Job.Revision != started.Job.Revision+1 {
			t.Fatalf("duplicate atomic admission = %#v", result)
		}
	}
	if newCount != 1 {
		t.Fatalf("new admissions = %d", newCount)
	}
	assertFormStoreContainsNoPlaintext(t, options, intakePrivateSentinel)
}

func TestFormIntakePauseResumeOwnerExclusivityAndCancellationErasure(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	other := intakeFixtureRequest(t, store)
	if _, err := store.StartFormIntake(t.Context(), other); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("ambiguous same-route modes accepted: %v", err)
	}
	first, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
	))
	if err != nil {
		t.Fatal(err)
	}
	paused, err := store.SetFormIntakeState(
		t.Context(),
		request.JobID,
		started.Progress.ModeID,
		first.Job.Revision,
		request.Owner,
		FormIntakePaused,
	)
	if err != nil || paused.State != FormIntakePaused || paused.PendingInputs != 1 {
		t.Fatalf("pause = %#v, %v", paused, err)
	}
	if _, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, paused.Revision, "platform-2", "More facts while paused",
	)); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("paused new input accepted: %v", err)
	}
	if _, err := store.SetFormIntakeState(
		t.Context(),
		request.JobID,
		started.Progress.ModeID,
		paused.Revision,
		request.Owner,
		FormIntakeClosed,
	); !errors.Is(
		err,
		ErrFormJobConflict,
	) {
		t.Fatalf("close discarded unresolved input: %v", err)
	}
	resumed, err := store.SetFormIntakeState(
		t.Context(),
		request.JobID,
		started.Progress.ModeID,
		paused.Revision,
		request.Owner,
		FormIntakeActive,
	)
	if err != nil || resumed.State != FormIntakeActive || resumed.PendingInputs != 1 {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	cancel := interactions.ProtectedAnswerCancelRequest{
		Binding: started.Binding, Workspace: request.Owner.WorkspaceID, Route: testProtectedAnswerRoute(request.Owner),
		InteractionID: "initial-question", IdempotencyKey: "cancel-message",
	}
	if err := sink.Cancel(t.Context(), cancel); err != nil {
		t.Fatal(err)
	}
	if err := sink.Cancel(t.Context(), cancel); err != nil {
		t.Fatalf("cancel replay: %v", err)
	}
	job, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || job.State != FormJobCanceled {
		t.Fatalf("canceled job = %#v, %v", job, err)
	}
	if _, err := store.ReadFormIntakeInput(t.Context(), first.Receipt, request.Owner); err == nil {
		t.Fatal("cancellation retained decryptable input")
	}
	found, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || found != nil {
		t.Fatalf("canceled mode discoverable = %#v, %v", found, err)
	}
	if _, err := store.StartFormIntake(t.Context(), other); err != nil {
		t.Fatalf("canceled mode blocked new job: %v", err)
	}
	assertFormStoreContainsNoPlaintext(t, options, intakePrivateSentinel)
}

func TestFormIntakeDiscardOnlyUnclaimedAndTypedControlsNeverBecomeFieldValues(t *testing.T) {
	for _, intent := range []interactions.ProtectedAnswerIntent{interactions.ProtectedAnswerClarify, interactions.ProtectedAnswerBack} {
		t.Run(string(intent), func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			request := intakeFixtureRequest(t, store)
			started, err := store.StartFormIntake(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			sink, err := NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			answer := interactions.ProtectedAnswerSinkRequest{
				Binding:         started.Binding,
				Workspace:       request.Owner.WorkspaceID,
				Route:           testProtectedAnswerRoute(request.Owner),
				InteractionID:   "question-1",
				IdempotencyKey:  "question-key",
				MessageIdentity: "control-1",
				Intent:          intent,
			}
			receipt, err := sink.Accept(t.Context(), answer)
			if err != nil {
				t.Fatal(err)
			}
			discard := interactions.ProtectedAnswerDiscardRequest{
				Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
				InteractionID: answer.InteractionID, Receipt: &receipt,
			}
			if err := sink.Discard(t.Context(), discard); err != nil {
				t.Fatal(err)
			}
			found, err := store.FindFormIntake(t.Context(), request.Owner)
			if err != nil || found.PendingInputs != 1 {
				t.Fatal("grace period did not protect in-flight claim")
			}
			discard.Force = true
			if err := sink.Discard(t.Context(), discard); err != nil {
				t.Fatal(err)
			}
			found, err = store.FindFormIntake(t.Context(), request.Owner)
			if err != nil || found.PendingInputs != 0 {
				t.Fatal("unclaimed input was not discarded")
			}
			receipt, err = sink.Accept(t.Context(), answer)
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
				Binding: answer.Binding, Workspace: answer.Workspace, Route: answer.Route,
				InteractionID: answer.InteractionID, Receipt: receipt,
			}); err != nil {
				t.Fatal(err)
			}
			input, err := store.ReadFormIntakeInput(t.Context(), receipt.Reference, request.Owner)
			if err != nil || input.Intent != intent || input.Text != "" {
				t.Fatalf("typed control read = %#v, %v", input, err)
			}
			job, err := store.Get(t.Context(), request.JobID, request.Owner)
			if err != nil || len(job.Fields) != 0 || job.LedgerRevision != 0 {
				t.Fatal("navigation became a supplied field value")
			}
		})
	}
}

func TestFormIntakeExpiryErasesPrivateSourceAndLegacySourceRemainsReadable(t *testing.T) {
	store, options := newTestFormJobStore(t)
	legacy := intakeFixtureRequest(t, store)
	legacyValues, err := store.SourceRef(t.Context(), legacy.JobID, legacy.Owner)
	if err != nil || legacyValues == "" {
		t.Fatal("legacy source is unreadable")
	}
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
	))
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(first.Job.ExpiresAt).Add(time.Second) }
	if _, err := store.ReadFormIntakeInput(t.Context(), first.Receipt, request.Owner); err == nil {
		t.Fatal("expired input is readable")
	}
	job, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || job.State != FormJobExpired {
		t.Fatalf("expiry = %#v, %v", job, err)
	}
	state, err := os.ReadFile(formJobStatePath(options))
	if err != nil || strings.Contains(string(state), `"ciphertext"`) ||
		strings.Contains(string(state), `"wrapped_key"`) {
		t.Fatal("expiry retained encrypted protected material")
	}
}

func TestFormIntakePendingInputFencesNativeReviewApprovalAndMaterialization(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged_%t", staged), func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			owner := testFormJobOwner()
			schema := *successfulTestFormFields()
			policy := testFormAuditPolicy("document-deliberative")
			job := createReviewMappedJob(t, store, owner, schema, policy, "Synthetic initial field")
			auditor := &testFormAuditor{proposals: map[string]FormAuditProposal{
				"document-deliberative": {Decision: FormAuditPass},
			}}
			reviewRequest := FormReviewRequest{
				JobID:            job.JobID,
				ExpectedRevision: job.Revision,
				Owner:            owner,
				Schema:           schema,
				Policy:           policy,
				Auditor:          auditor,
			}
			reviewed, err := store.ReviewFormJob(t.Context(), reviewRequest)
			if err != nil {
				t.Fatal(err)
			}
			policyRevision, err := policy.Revision()
			if err != nil {
				t.Fatal(err)
			}
			commitRequest := FormCommitRequest{
				JobID: job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: owner, Schema: schema,
				AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
			}
			approvalJob, approval, err := store.PrepareFormCommitApproval(t.Context(), commitRequest)
			if err != nil {
				t.Fatal(err)
			}
			startRequest := FormIntakeStartRequest{
				JobID:            job.JobID,
				ExpectedRevision: approvalJob.Revision,
				Owner:            owner,
				Schema:           schema,
				Context: FormIntakeContext{
					CandidateFieldIDs: []string{schema.Fields[0].ID},
					Question:          "Anything to add?",
				},
				IdempotencyKey: "intake-after-review",
			}
			started, err := store.StartFormIntake(t.Context(), startRequest)
			if err != nil {
				t.Fatal(err)
			}
			if staged {
				sink, err := NewFormProtectedAnswerSink(store)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
					Binding: started.Binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
					InteractionID: "question-1", IdempotencyKey: "question-key", MessageIdentity: "platform-1",
					Intent: interactions.ProtectedAnswerValue, Text: intakePrivateSentinel,
				}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				startRequest, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
			)); err != nil {
				t.Fatal(err)
			}
			current, err := store.Get(t.Context(), job.JobID, owner)
			if err != nil || current.ReviewDigest != "" || current.ApprovalDigest != "" || current.OperationID != "" {
				t.Fatal("raw input did not invalidate old finish authority")
			}
			reviewRequest.ExpectedRevision = current.Revision
			if _, err := store.ReviewFormJob(t.Context(), reviewRequest); !errors.Is(err, ErrFormJobConflict) {
				t.Fatalf("pending input reviewed: %v", err)
			}
			if len(auditor.calls) != 1 {
				t.Fatal("pending private text entered an audit model call")
			}
			commitRequest.ExpectedRevision = current.Revision
			if _, _, err := store.PrepareFormCommitApproval(
				t.Context(),
				commitRequest,
			); !errors.Is(
				err,
				ErrFormJobConflict,
			) {
				t.Fatalf("pending input acquired final approval: %v", err)
			}
			if _, err := store.BeginFormCommit(
				t.Context(),
				commitRequest,
				approval,
			); !errors.Is(
				err,
				ErrFormJobConflict,
			) {
				t.Fatalf("pending input started final commit: %v", err)
			}
			if _, fill, err := store.MaterializeFormCommit(
				t.Context(),
				commitRequest,
			); !errors.Is(err, ErrFormJobConflict) ||
				len(fill.Assignments) != 0 {
				t.Fatalf("pending input materialized: %v", err)
			}
		})
	}
}

func TestFormIntakeStagedAdmissionDuringAuditCannotPublishStaleReview(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := testFormAuditPolicy("document-deliberative")
	job := createReviewMappedJob(t, store, owner, schema, policy, "Synthetic initial field")
	started, err := store.StartFormIntake(t.Context(), FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[0].ID},
			Question:          "Anything to add?",
		},
		IdempotencyKey: "intake-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	auditor := &testFormAuditor{
		proposals: map[string]FormAuditProposal{"document-deliberative": {Decision: FormAuditPass}},
		onAudit: func(string, FormAuditView) {
			if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
				Binding: started.Binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
				InteractionID: "question-1", IdempotencyKey: "question-key", MessageIdentity: "platform-1",
				Intent: interactions.ProtectedAnswerValue, Text: intakePrivateSentinel,
			}); err != nil {
				t.Fatal(err)
			}
		},
	}
	if _, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Policy:           policy,
		Auditor:          auditor,
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("audit raced staged input: %v", err)
	}
	current, err := store.Get(t.Context(), job.JobID, owner)
	if err != nil || current.Revision != job.Revision || current.ReviewDigest != "" ||
		current.State != FormJobCollecting {
		t.Fatal("staged input race published stale review")
	}
}

func TestFormIntakeNewEncryptedShapeFailsClosedForOldReadersAndPreservesSource(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	original, err := store.SourceRef(t.Context(), request.JobID, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
	)); err != nil {
		t.Fatal(err)
	}
	after, err := store.SourceRef(t.Context(), request.JobID, request.Owner)
	if err != nil || after != original {
		t.Fatal("encrypted intake changed immutable source reference")
	}
	err = store.update(t.Context(), func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		var legacy struct {
			SourceRef string `json:"source_ref"`
		}
		if err := openFormJobEnvelope(key, *record.Source, &legacy); !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatal("old binary silently interpreted new protected intake source")
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyBinding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: request.JobID, ExpectedRevision: started.Job.Revision + 1, Owner: request.Owner,
		FieldID: request.Schema.Fields[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding:        legacyBinding,
		Workspace:      request.Owner.WorkspaceID,
		Route:          testProtectedAnswerRoute(request.Owner),
		InteractionID:  "legacy-question",
		IdempotencyKey: "legacy-answer",
		Intent:         interactions.ProtectedAnswerValue,
		Text:           "Another name",
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("legacy claim competed for intake revision: %v", err)
	}
}

func TestFormIntakeRejectsMalformedAdmissionAndCandidateContextWithoutWriting(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*FormIntakeStartRequest){
		func(r *FormIntakeStartRequest) { r.JobID = " " + r.JobID },
		func(r *FormIntakeStartRequest) { r.ExpectedRevision++ },
		func(r *FormIntakeStartRequest) { r.Context.CandidateFieldIDs[0] = fillFieldID(99) },
		func(r *FormIntakeStartRequest) { r.Context.CandidateFieldIDs[1] = r.Context.CandidateFieldIDs[0] },
		func(r *FormIntakeStartRequest) { r.Context.FocusFieldID = fillFieldID(99) },
		func(r *FormIntakeStartRequest) {
			r.Context.RoleContext = strings.Repeat("x", maxFormIntakeRoleContextRunes+1)
		},
		func(r *FormIntakeStartRequest) { r.Context.Question = "" },
		func(r *FormIntakeStartRequest) { r.Context.Language = "RU" },
		func(r *FormIntakeStartRequest) { r.Owner.TopicID = "other-topic" },
		func(r *FormIntakeStartRequest) { r.Schema.Backend.Version = "another-backend" },
	} {
		changed := request
		changed.Context = cloneFormIntakeContext(request.Context)
		mutate(&changed)
		if result, err := store.StartFormIntake(t.Context(), changed); err == nil || result.Binding.Token != "" {
			t.Fatal("invalid candidate binding accepted")
		}
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(after) != string(before) {
		t.Fatal("invalid starts modified the source snapshot")
	}
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "  ", string([]byte{0xff}), strings.Repeat("x", MaxFormIntakeInputBytes+1)} {
		message := intakeMessage(request, started.Progress.ModeID, started.Job.Revision, "platform-1", text)
		if result, err := store.AdmitFormIntakeMessage(t.Context(), message); err == nil || result.Receipt != "" {
			t.Fatal("invalid raw input accepted")
		}
	}
}

func TestFormIntakeConstructionErrorDuringUnrelatedExpiryDoesNotAliasSource(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	expiringRequest := testFormJobCreateRequest(request.Owner)
	expiringRequest.StartIdempotencyKey = "unrelated-expiry"
	expiringRequest.Retention = time.Minute
	expiring, err := store.Create(t.Context(), expiringRequest)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(expiring.ExpiresAt).Add(time.Second) }
	store.random = io.LimitReader(rand.Reader, 0)
	if result, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
	)); err == nil || result.Receipt != "" {
		t.Fatal("failed seal produced an acceptance receipt")
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress == nil || progress.InputCount != 0 || progress.Revision != started.Job.Revision {
		t.Fatalf("expiry saved an aliased pending source = %#v, %v", progress, err)
	}
	other, err := store.Get(t.Context(), expiring.JobID, request.Owner)
	if err != nil || other.State != FormJobExpired {
		t.Fatal("unrelated expiry was not published")
	}
}

func TestFormIntakeReceiptCapacityNeverEvictsPriorReplayIdentity(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message := intakeMessage(
		request,
		started.Progress.ModeID,
		started.Job.Revision,
		"original-platform-1",
		intakePrivateSentinel,
	)
	first, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	// Consumer accounting belongs to Packet 2. Seed bounded, value-free retired
	// entries here only to qualify the transport cap without claiming extraction.
	err = store.update(t.Context(), func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		for index := 1; index < MaxFormIntakeReceipts; index++ {
			input := source.Intake.Inputs[0]
			input.MessageDigest = keyedDigest(store.profileKey, "test_retired_input", fmt.Sprint(index))
			input.ID = "form_input_" + input.MessageDigest[:32]
			input.State, input.Text, input.InteractionID = formIntakeInputConsumed, "", ""
			source.Intake.Inputs = append(source.Intake.Inputs, input)
		}
		if err := store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[record.Public.JobID] = record
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, first.Job.Revision, "new-message", "More facts",
	)); !errors.Is(err, ErrFormJobCapacityExceeded) || result.Receipt != "" {
		t.Fatalf("receipt cap = %#v, %v", result, err)
	}
	replayed, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil || !replayed.Reused || replayed.Receipt != first.Receipt ||
		replayed.Progress.InputCount != MaxFormIntakeReceipts {
		t.Fatalf("capacity evicted original replay identity = %#v, %v", replayed, err)
	}
}

func TestFormIntakeInterleavedPassReplayRetainsExactBindingAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "platform-1", intakePrivateSentinel,
	))
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := FormIntakePassRequest{
		Reference:        admitted.Receipt,
		ExpectedRevision: admitted.Job.Revision,
		Owner:            request.Owner,
		Schema:           request.Schema,
		CandidateIDs:     slices.Clone(request.Context.CandidateFieldIDs[:4]),
		IdempotencyKey:   "pass-A",
	}
	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "pass-B"
	secondRequest.CandidateIDs = slices.Clone(request.Context.CandidateFieldIDs[4:])
	first, err := store.ReserveFormIntakePass(t.Context(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReserveFormIntakePass(t.Context(), secondRequest)
	if err != nil || second.ID == first.ID || second.Input.Passes != 2 {
		t.Fatal("distinct extraction views did not reserve two distinct IDs")
	}
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	for _, retry := range []struct {
		request FormIntakePassRequest
		id      string
	}{
		{firstRequest, first.ID}, {secondRequest, second.ID}, {firstRequest, first.ID},
	} {
		replayed, err := reopened.ReserveFormIntakePass(t.Context(), retry.request)
		if err != nil || !replayed.Reused || replayed.ID != retry.id || replayed.Input.Passes != 2 ||
			!slices.Equal(replayed.CandidateIDs, retry.request.CandidateIDs) {
			t.Fatalf("interleaved reservation replay = %#v, %v", replayed, err)
		}
	}
	changed := firstRequest
	changed.CandidateIDs = secondRequest.CandidateIDs
	if result, err := reopened.ReserveFormIntakePass(
		t.Context(),
		changed,
	); !errors.Is(err, ErrFormJobAnswerConflict) ||
		result.ID != "" {
		t.Fatal("original A key acquired B's candidate view")
	}
	changed.CandidateIDs = slices.Clone(firstRequest.CandidateIDs)
	slices.Reverse(changed.CandidateIDs)
	if _, err := reopened.ReserveFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("original A key changed candidate ordering")
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(after) != string(before) {
		t.Fatal("interleaved retries or conflicts charged or mutated a reservation")
	}
	newInput, err := reopened.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, admitted.Job.Revision, "platform-2", "Further supplied facts",
	))
	if err != nil {
		t.Fatal(err)
	}
	changed = firstRequest
	changed.ExpectedRevision = newInput.Job.Revision
	if _, err := reopened.ReserveFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("original A key rebound itself to a newer revision")
	}
	if _, err := reopened.ReserveFormIntakePass(t.Context(), firstRequest); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("old A view became current interpretation authority")
	}
	input, err := reopened.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
	if err != nil || input.Passes != 2 || input.Text != intakePrivateSentinel {
		t.Fatal("stale reservation failure charged budget or dropped retained facts")
	}
}
