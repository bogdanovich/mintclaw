package thread

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoreInspectReturnsContentFreeExactOwnerState(t *testing.T) {
	root := t.TempDir()
	store, metadata, lease := newCurrentPlanTestThread(t, root)
	const privatePrompt = "private inspection marker must not escape"
	if err := store.AppendUserMessage(t.Context(), lease, metadata, privatePrompt); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.Inspect(t.Context(), metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ThreadID != metadata.ThreadID || inspection.ProjectHash == "" ||
		inspection.TranscriptAvailability != "available" || inspection.HistoryCount != 1 ||
		inspection.HistoryVisibleCount != 1 || inspection.PresentationAvailability != "unavailable_by_design" ||
		inspection.Presentation != nil {
		t.Fatalf("thread inspection = %#v", inspection)
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{privatePrompt, metadata.Title, metadata.Preview, metadata.Project.ProjectRoot} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("thread inspection leaked private value %q: %s", private, encoded)
		}
	}
}
