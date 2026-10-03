//go:build integration && (linux || darwin)

package workerprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestNativeMintClawWorkerContextPrefixCorpus(t *testing.T) {
	fixture := newNativeWorkerFixture(t)
	path := filepath.Join(fixture.project.ProjectRoot, "corpus.txt")
	if err := os.WriteFile(path, []byte(llmscenario.PrefixMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	corpus := llmscenario.PrefixCorpus{Path: path}
	binding := fixture.binding(worker.ThreadOpenNew, "corpus-first")
	process := fixture.launch(t, binding)
	t.Cleanup(func() { _ = process.Close() })
	if err := process.StartTurn(t.Context(), "corpus-start", llmscenario.PrefixInitialPrompt, nil); err != nil {
		t.Fatal(err)
	}
	first := fixture.provider.next(t)
	arguments, err := json.Marshal(map[string]any{"path": corpus.Path})
	if err != nil {
		t.Fatal(err)
	}
	first.respond(t, openAIToolCallResponse("Inspecting the corpus.", llmscenario.PrefixCallID,
		"read_file", string(arguments)))
	second := fixture.provider.next(t)
	second.requireMessage(t, llmscenario.PrefixMarker)
	second.respond(t, openAITextResponse(llmscenario.PrefixAnswer))
	if result := waitForNativeWorkerResult(t, process); result.Outcome() != OutcomeCompleted {
		t.Fatalf("first worker outcome = %s", result.Outcome())
	}
	fixture.requireLeaseAvailable(t)
	binding.ThreadOpenMode = worker.ThreadOpenResume
	binding.WorkerGenerationID = "corpus-resumed"
	resumed := fixture.launch(t, binding)
	t.Cleanup(func() { _ = resumed.Close() })
	if err := resumed.StartTurn(t.Context(), "corpus-followup", llmscenario.PrefixFollowupPrompt, nil); err != nil {
		t.Fatal(err)
	}
	third := fixture.provider.next(t)
	visible, err := resumed.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	visibleMessages := 0
	for _, item := range visible.Snapshot.Items {
		if item.Message == nil {
			continue
		}
		visibleMessages++
		if strings.Contains(item.Message.Text, "mintclaw_turn_context") ||
			strings.Contains(item.Message.Text, "turn_envelope") {
			t.Fatal("worker presentation exposes hidden turn carrier")
		}
	}
	if visibleMessages == 0 {
		t.Fatal("worker snapshot has no visible transcript messages")
	}
	third.respond(t, openAITextResponse(llmscenario.PrefixAnswer))
	if result := waitForNativeWorkerResult(t, resumed); result.Outcome() != OutcomeCompleted {
		t.Fatalf("resumed worker outcome = %s", result.Outcome())
	}
	var requests []llmscenario.RequestSnapshot
	for _, call := range []*nativeWorkerProviderCall{first, second, third} {
		request, err := llmscenario.SnapshotJSON(call.body)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	if err := corpus.Check(requests...); err != nil {
		t.Fatal(err)
	}
}

// Interrupt temporarily narrows the authority/tool set; resume restores it.
// Verify all historical transcript data across that explicit schema boundary.
func requireNativeWorkerTranscriptExtension(before, after *nativeWorkerProviderCall) error {
	previous, err := llmscenario.SnapshotJSON(before.body)
	if err != nil {
		return err
	}
	next, err := llmscenario.SnapshotJSON(after.body)
	if err != nil {
		return err
	}
	return previous.RequireMessagesExtension(next)
}
