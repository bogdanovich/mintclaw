package document

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFormIntakeLiteralUsesExactOccurrenceAndUnicodeBoundaries(t *testing.T) {
	text := "Aki Mint is applicant; Робин Mint is contact."
	first, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: "Mint", Occurrence: 1})
	if err != nil || first.Start != len("Aki ") || text[first.Start:first.End] != "Mint" {
		t.Fatal("first literal occurrence was not exact")
	}
	second, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: "Mint", Occurrence: 2})
	if err != nil || second.Start != len("Aki Mint is applicant; Робин ") ||
		formIntakeSpansOverlap(first, second) {
		t.Fatal("two people acquired the same literal span")
	}
	for _, test := range []struct{ text, literal string }{
		{"Mintford", "Mint"},
		{"Робин", "Роби"},
		{"e\u0301", "e"},
		{"SECRET_VALUE", "VALUE"},
		{"12345", "123"},
		{"Casey", "casey"},
		{"Casey", "Invented"},
	} {
		if _, matchErr := resolveFormIntakeLiteral(test.text, FormIntakeLiteral{
			Text: test.literal, Occurrence: 1,
		}); !errors.Is(matchErr, ErrFormJobAnswerConflict) {
			t.Fatal("invented, transformed or partial-token literal was accepted")
		}
	}
	for _, occurrence := range []int{-1, 0, 3, MaxFormIntakeAccountedSpans + 1} {
		if _, matchErr := resolveFormIntakeLiteral(text, FormIntakeLiteral{
			Text: "Mint", Occurrence: occurrence,
		}); !errors.Is(matchErr, ErrFormJobAnswerConflict) {
			t.Fatal("missing or out-of-budget occurrence was accepted")
		}
	}
	private := FormIntakeLiteral{Text: "MINTCLAW_LITERAL_PRIVATE_36b9", Occurrence: 1}
	encoded, err := json.Marshal(private)
	if err != nil || strings.Contains(string(encoded), private.Text) ||
		strings.Contains(string(encoded), "Occurrence") {
		t.Fatal("private literal evidence escaped ordinary JSON")
	}
}

func TestFormIntakeLiteralCoverageRetainsOtherFactsAndRejectsBadRanges(t *testing.T) {
	text := "Casey Mint; Робин Mint; 202-555-0147."
	first, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: "Casey Mint", Occurrence: 1})
	if err != nil || formIntakeTextAccounted(text, []formIntakeSpan{first}) {
		t.Fatal("accounting the first view silently consumed other facts")
	}
	second, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: "Робин Mint", Occurrence: 1})
	if err != nil {
		t.Fatal(err)
	}
	third, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: "202-555-0147", Occurrence: 1})
	if err != nil || !formIntakeTextAccounted(text, []formIntakeSpan{first, second, third}) {
		t.Fatal("exact coverage did not account for the supplied facts")
	}
	for _, span := range []formIntakeSpan{
		{Start: -1, End: 1},
		{Start: 0, End: len(text) + 1},
		{Start: 1, End: 1},
		{Start: len("Casey Mint; ") + 1, End: len("Casey Mint; Робин")},
	} {
		if formIntakeTextAccounted(text, []formIntakeSpan{{Start: 0, End: len(text)}, span}) {
			t.Fatal("invalid or partial-Unicode accounting range was accepted")
		}
	}
	if formIntakeTextAccounted("✓", nil) ||
		formIntakeTextAccounted(strings.Repeat("x", MaxFormIntakeInputBytes+1), nil) ||
		formIntakeTextAccounted(text, make([]formIntakeSpan, MaxFormIntakeAccountedSpans+1)) {
		t.Fatal("unaccounted symbols or accounting bounds were ignored")
	}
}

func TestFormIntakeLiteralOccurrenceIgnoresEmbeddedEarlierSubstring(t *testing.T) {
	text := "Mintford and Mint; Робин is in Mint; again in Paperport"
	for _, test := range []struct {
		literal    string
		occurrence int
		prefix     string
	}{
		{"Mint", 1, "Mintford and "},
		{"Mint", 2, "Mintford and Mint; Робин is in "},
		{"in", 1, "Mintford and Mint; Робин is "},
		{"in", 2, "Mintford and Mint; Робин is in Mint; again "},
	} {
		span, err := resolveFormIntakeLiteral(text, FormIntakeLiteral{Text: test.literal, Occurrence: test.occurrence})
		if err != nil || span.Start != len(test.prefix) || formIntakeLiteralOccurrence(text, span) != test.occurrence {
			t.Fatalf("eligible literal occurrence mismatch: %#v, %v", span, err)
		}
	}
}
