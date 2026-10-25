package eval

import (
	"errors"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func TestFinalResponseAbsentDoesNotRejectEarlierAcknowledgements(t *testing.T) {
	checks := []Check{
		{ID: "final-only", Kind: "final_response_absent", Expected: "old-marker"},
		{ID: "all-responses", Kind: "response_absent", Expected: "old-marker"},
	}
	earlier := Event{ID: "earlier", Kind: "final", Content: "Noted old-marker."}
	for _, test := range []struct {
		name string
		last Event
		want Verdict
	}{
		{"corrected", Event{ID: "last", Kind: "final", Content: "current-marker"}, Pass},
		{"stale", Event{ID: "last", Kind: "final", Content: "old-marker"}, Fail},
		{"no-final", Event{ID: "last", Kind: "question", Content: "current-marker"}, Inconclusive},
		{"missing-identity", Event{Kind: "final", Content: "current-marker"}, Inconclusive},
		{"missing", Event{}, Inconclusive},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := gradeConversation(checks, []Event{earlier, test.last}, test.last)
			if result[0].Verdict != test.want || result[1].Verdict != Fail {
				t.Fatal("final-only absence changed transcript-wide semantics", result)
			}
		})
	}
	item := testCase()
	item.Checks = checks[:1]
	if err := item.Validate(); err != nil {
		t.Fatal("final response check not admitted", err)
	}
	item.Checks[0].Expected = ""
	if !errors.Is(item.Validate(), ErrInvalidCase) {
		t.Fatal("empty absence predicate admitted")
	}
}

func TestCoreCorrectionRunsThreeTurnsAndRejectsStaleFinal(t *testing.T) {
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"general.context.correction"}})
	if err != nil || len(items) != 1 || !errors.Is(CheckRunAvailability(items, false), ErrPendingCases) {
		t.Fatal("unqualified correction is not an explicit pending candidate", err)
	}
	if _, err = Run(t.Context(), Preset{}, items, RunOptions{}); !errors.Is(err, ErrPendingCases) {
		t.Fatal("direct Run bypassed pending candidate admission", err)
	}
	for _, test := range []struct {
		name, final string
		want        Verdict
	}{
		{"corrected", "MC_CORE_3AB40B27", Pass},
		{"stale", "MC_CORE_88284260", Fail},
		{"both", "MC_CORE_88284260 MC_CORE_3AB40B27", Fail},
		{"fabricated", "Everything succeeded.", Fail},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				content := "Noted MC_CORE_88284260."
				switch number {
				case 2:
					content = "Correction noted."
				case 3:
					content = test.final
				}
				response.Payload[protocol.PayloadKeyContent] = content
				transport.events <- response
			}
			item := items[0].Case
			item.Rubric = nil // This control tests the hard oracle, not a live semantic judge.
			trial := runFakeConversation(t, transport, item)
			if trial.Verdict != test.want || len(transport.sent) != 3 || !trial.TransportClosed ||
				transport.sent[0].ID == transport.sent[1].ID || transport.sent[1].ID == transport.sent[2].ID ||
				transport.sent[0].SessionID != transport.sent[2].SessionID {
				t.Fatal("correction oracle or three-turn scope incorrect", trial.Verdict, trial.Code)
			}
		})
	}
}
