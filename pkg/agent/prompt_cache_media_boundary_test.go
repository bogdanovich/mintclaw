package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPromptCacheMediaBoundaryUsesAdmittedSnapshotAcrossRetries(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	exec := &turnExecution{}
	key := func() string {
		return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
			"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	}
	plain := key()
	ts.captureCanonicalRestorePoint([]providers.Message{{Role: "user", Content: "ordinary old turn"}}, "")
	if key() != plain {
		t.Fatal("ordinary history changed cache lineage")
	}
	ts.captureCanonicalRestorePoint(
		[]providers.Message{{Role: "user", Media: []string{"media://private-image-ref"}}},
		"",
	)
	retired := key()
	if retired == plain || retired == "" {
		t.Fatal("historical media did not create a compatibility boundary")
	}
	exec.messages = append(
		exec.messages,
		providers.Message{Role: "tool", Media: []string{"media://current-tool-image"}},
	)
	if key() != retired {
		t.Fatal("growing active tool transcript rotated lineage")
	}
	// A new root captures the now-historical tool reference. Later plain roots
	// retain this generation even when the image blob is missing on disk.
	ts.captureCanonicalRestorePoint([]providers.Message{
		{Role: "user", Media: []string{"media://private-image-ref"}},
		{Role: "tool", Media: []string{"media://current-tool-image"}},
	}, "")
	selectedRetired := key()
	if selectedRetired == retired {
		t.Fatal("historical tool media did not rotate lineage")
	}
	ts.canonicalRestoreHistory = append(
		ts.canonicalRestoreHistory,
		providers.Message{Role: "user", Content: "plain followup"},
	)
	if key() != selectedRetired {
		t.Fatal("plain followup rotated retired-media lineage")
	}
	exec.checkpoint = &ContextCheckpoint{Content: "checkpoint", Generation: "generation-2"}
	if key() == selectedRetired {
		t.Fatal("checkpoint boundary did not rotate media-bearing lineage")
	}
}

func TestPromptCacheMediaBoundaryTracksGenericOneShotContextWithoutRetainingIt(t *testing.T) {
	ts := &turnState{agent: &AgentInstance{ID: "agent"}, sessionKey: "session"}
	exec := &turnExecution{}
	key := func() string {
		return buildPromptCacheLineageKey(promptCacheScopeForTurn(ts, exec, promptCachePurposeTurn),
			"openai", "test", buildPromptCachePrefixSnapshot(nil, nil))
	}
	plain := key()
	exec.messages = []providers.Message{{
		Role: "tool", ToolCallID: "one-shot", Content: "PRIVATE_CONTEXT_TEXT",
		Media: []string{"media://private-context-media"},
	}}
	exec.liveToolContexts = []liveToolContextProjection{{toolCallID: "one-shot", durableContent: "receipt"}}
	live := key()
	if live == plain || live == "" || live != key() {
		t.Fatal("live projection key is missing, unstable or unscoped")
	}
	exec.consumeLiveToolContexts(ts)
	if key() != plain || len(exec.messages[0].Media) != 0 || exec.messages[0].Content != "receipt" {
		t.Fatal("one-shot context was retained or its retirement was not a compatibility boundary")
	}
	exec.liveToolContexts = []liveToolContextProjection{{toolCallID: "missing"}}
	if key() != "" {
		t.Fatal("missing live projection did not disable cache intent")
	}
}
