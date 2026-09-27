package protocoltypes

import (
	"reflect"
	"testing"
)

func TestProjectTurnEnvelopeVersion1IsDeterministicAndDetached(t *testing.T) {
	envelope := &TurnEnvelope{
		Version: TurnEnvelopeVersion1,
		Parts: []TurnEnvelopePart{
			{ID: "context.runtime", Content: "time: 2026-09-26 10:00"},
			{ID: "context.sender", Content: "Anton <owner>"},
		},
	}
	original := Message{Role: "user", Content: "hello", TurnEnvelope: envelope}

	first := ProjectTurnEnvelope(original)
	second := ProjectTurnEnvelope(original)
	want := "hello\n\n<mintclaw_turn_context version=\"1\">\n" +
		"time: 2026-09-26 10:00\n\n---\n\nAnton <owner>" +
		"\n</mintclaw_turn_context>"
	if first.Content != want || second.Content != want {
		t.Fatalf("projected content = %q and %q, want %q", first.Content, second.Content, want)
	}
	if first.TurnEnvelope != nil {
		t.Fatal("projected message retained canonical turn envelope")
	}
	if original.Content != "hello" || original.TurnEnvelope != envelope {
		t.Fatalf("projection mutated original: %#v", original)
	}
}

func TestProjectTurnEnvelopeEmptyCanonicalContent(t *testing.T) {
	got := ProjectTurnEnvelope(Message{
		Role:         "user",
		TurnEnvelope: &TurnEnvelope{Version: TurnEnvelopeVersion1},
	})
	want := ""
	if got.Content != want {
		t.Fatalf("projected content = %q, want %q", got.Content, want)
	}
}

func TestProjectTurnEnvelopeLeavesLegacyMessageUnchanged(t *testing.T) {
	original := Message{Role: "user", Content: "legacy"}
	if got := ProjectTurnEnvelope(original); !reflect.DeepEqual(got, original) {
		t.Fatalf("legacy projection = %#v, want %#v", got, original)
	}
}

func TestProjectTurnEnvelopeRejectsUnknownVersion(t *testing.T) {
	got := ProjectTurnEnvelope(Message{
		Role: "user", Content: "visible",
		TurnEnvelope: &TurnEnvelope{
			Version: 99,
			Parts:   []TurnEnvelopePart{{ID: "future", Content: "must not leak"}},
		},
	})
	if got.Content != "visible" || got.TurnEnvelope != nil {
		t.Fatalf("unknown-version projection = %#v, want visible content only", got)
	}
}
