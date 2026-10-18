package document

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestFormIntakeNewerGuidanceSettlementsAccumulateAcrossBoundedWindows(t *testing.T) {
	store, options := newTestFormJobStore(t)
	schema := intakeTextSchema()
	schema.Fields = schema.Fields[:10]
	start := intakeApplyFixture(t, store, schema)
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "old-nine-uncertainties", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision,
		[]int{0, 1, 2, 3, 4, 5, 6, 7}, "old-first-window")
	first.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "Casey Mint")}
	for index := 1; index < 8; index++ {
		first.Findings = append(
			first.Findings,
			FormFieldMappingBlocker{FieldID: schema.Fields[index].ID, Code: "field_ambiguous"},
		)
	}
	partial, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := reserveIntakeApply(t, store, start, old.Receipt, partial.Job.Revision, []int{8, 9}, "old-second-window")
	for index := 8; index < 10; index++ {
		second.Findings = append(
			second.Findings,
			FormFieldMappingBlocker{FieldID: schema.Fields[index].ID, Code: "field_ambiguous"},
		)
	}
	pending, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || pending.Progress.PendingInputs != 1 {
		t.Fatal("older nine-field uncertainty was not retained")
	}
	// Verify the entire immutable old reservation journal after later decisions.
	readOldJournal := func() []byte {
		t.Helper()
		var encoded []byte
		readErr := store.update(t.Context(), func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
			record, key, keyErr := store.authorizedRecord(snapshot, start.JobID, start.Owner)
			if keyErr != nil {
				return false, keyErr
			}
			defer clear(key)
			source, sourceErr := readFormIntakeSource(record, key)
			if sourceErr != nil {
				return false, sourceErr
			}
			_, inputID, _ := parseFormIntakeReceipt(old.Receipt)
			encoded, sourceErr = json.Marshal(
				source.Intake.Inputs[formIntakeInputIndex(source.Intake, inputID)].Reservations,
			)
			return false, sourceErr
		})
		if readErr != nil {
			t.Fatal(readErr)
		}
		return encoded
	}
	before := readOldJournal()
	quote := "Leave all remaining entries blank"
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, pending.Job.Revision, "confirm-nine-fields", quote,
	))
	if err != nil {
		t.Fatal(err)
	}
	revision := current.Job.Revision
	for window, indices := range [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {0, 9}} {
		id := "current-first-window"
		if window == 1 {
			id = "current-second-window"
		}
		request := reserveIntakeApply(t, store, start, current.Receipt, revision, indices, id)
		for _, index := range indices {
			if index == 0 {
				continue
			}
			proposal := intakeProposal(schema, index, quote)
			proposal.Disposition = FormIntakeIntentionallyBlank
			request.Proposals = append(request.Proposals, proposal)
		}
		result, applyErr := store.ApplyFormIntakePass(t.Context(), request)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		revision = result.Job.Revision
		if window == 0 {
			if result.Progress.PendingInputs != 2 {
				t.Fatal("partial settlement erased unresolved input")
			}
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
		}
		replayed, replayErr := store.ApplyFormIntakePass(t.Context(), request)
		if replayErr != nil || !replayed.Reused || replayed.Job.LedgerRevision != result.Job.LedgerRevision {
			t.Fatal("restart/replay changed bounded settlement or field events")
		}
		request.Proposals[0].Literals[0].Text = "changed replay"
		if _, replayErr = store.ApplyFormIntakePass(
			t.Context(),
			request,
		); !errors.Is(
			replayErr,
			ErrFormJobAnswerConflict,
		) {
			t.Fatal("changed settlement replay was accepted")
		}
		if window == 1 && result.Progress.PendingInputs != 0 {
			t.Fatalf(
				"verified decisions in separate eight-field windows did not accumulate: pending=%d",
				result.Progress.PendingInputs,
			)
		}
	}
	input, err := store.ReadFormIntakeInput(t.Context(), old.Receipt, start.Owner)
	if err != nil || !input.Consumed || input.Passes != 2 || input.Text != "" {
		t.Fatal("bounded-window settlement changed old extraction counts or lost raw cleanup")
	}
	if string(before) != string(readOldJournal()) {
		t.Fatal("multiwindow settlement mutated an older outcome")
	}
}
