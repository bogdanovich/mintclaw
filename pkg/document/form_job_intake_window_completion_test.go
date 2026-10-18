package document

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Deliberately not named *_windows_test.go: that suffix is Windows-only.
func TestFormIntakeCompletedWindowsAreProtectedDerivedOutcomesAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "unfinished-text", "PRIVATE_UNACCOUNTED_TEXT",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0, 1}, "completed")
	if _, err = store.ApplyFormIntakePass(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{2}, "unapplied")
	want := [][]string{{start.Schema.Fields[0].ID, start.Schema.Fields[1].ID}}
	for range 2 {
		input, readErr := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
		if readErr != nil || !reflect.DeepEqual(input.CompletedWindows, want) || input.Passes != 2 || input.Consumed {
			t.Fatal("completed metadata included an unapplied reservation or lost pending input")
		}
		encoded, marshalErr := json.Marshal(input)
		if marshalErr != nil || strings.Contains(string(encoded), "PRIVATE_") ||
			strings.Contains(string(encoded), start.Schema.Fields[0].ID) ||
			strings.Contains(string(encoded), "CompletedWindows") {
			t.Fatal("protected completion metadata escaped ordinary serialization")
		}
		input.CompletedWindows[0][0] = "changed_clone"
		other := start.Owner
		other.SenderID = "unrelated-owner"
		if _, readErr = store.ReadFormIntakeInput(t.Context(), admitted.Receipt, other); readErr == nil {
			t.Fatal("completion metadata bypassed owner authorization")
		}
		store.Close()
		store, err = OpenFormJobStore(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
	}
}
