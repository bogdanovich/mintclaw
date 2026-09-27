package seahorse

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestSummaryKindValues(t *testing.T) {
	if SummaryKindLeaf != "leaf" {
		t.Errorf("expected SummaryKindLeaf = 'leaf', got %q", SummaryKindLeaf)
	}
	if SummaryKindCondensed != "condensed" {
		t.Errorf("expected SummaryKindCondensed = 'condensed', got %q", SummaryKindCondensed)
	}
}

func TestEstimateMessageTokensIncludesTurnEnvelopeProjection(t *testing.T) {
	plain := Message{Role: "user", Content: "hello"}
	withEnvelope := plain
	withEnvelope.TurnEnvelope = &providers.TurnEnvelope{
		Version: providers.TurnEnvelopeVersion1,
		Parts: []providers.TurnEnvelopePart{{
			ID: "context.runtime", Content: strings.Repeat("durable replay context ", 80),
		}},
	}
	if EstimateMessageTokens(withEnvelope) <= EstimateMessageTokens(plain) {
		t.Fatalf(
			"envelope tokens = %d, plain tokens = %d",
			EstimateMessageTokens(withEnvelope),
			EstimateMessageTokens(plain),
		)
	}
}

func TestConstants(t *testing.T) {
	// Ordinal gap step
	if OrdinalStep != 100 {
		t.Errorf("expected OrdinalStep = 100, got %d", OrdinalStep)
	}

	// Compaction triggers
	if ContextThreshold != 0.75 {
		t.Errorf("expected ContextThreshold = 0.75, got %f", ContextThreshold)
	}
	if FreshTailCount != 32 {
		t.Errorf("expected FreshTailCount = 32, got %d", FreshTailCount)
	}

	// Fanout
	if LeafMinFanout != 8 {
		t.Errorf("expected LeafMinFanout = 8, got %d", LeafMinFanout)
	}
	if CondensedMinFanout != 4 {
		t.Errorf("expected CondensedMinFanout = 4, got %d", CondensedMinFanout)
	}
	if CondensedMinFanoutHard != 2 {
		t.Errorf("expected CondensedMinFanoutHard = 2, got %d", CondensedMinFanoutHard)
	}

	// Token targets
	if LeafChunkTokens != 20000 {
		t.Errorf("expected LeafChunkTokens = 20000, got %d", LeafChunkTokens)
	}
	if LeafTargetTokens != 1200 {
		t.Errorf("expected LeafTargetTokens = 1200, got %d", LeafTargetTokens)
	}
	if CondensedTargetTokens != 2000 {
		t.Errorf("expected CondensedTargetTokens = 2000, got %d", CondensedTargetTokens)
	}
	if MaxExpandTokens != 4000 {
		t.Errorf("expected MaxExpandTokens = 4000, got %d", MaxExpandTokens)
	}
}
