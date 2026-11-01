package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestDocumentConversationAppendIsIdentityBoundAndOwnerScoped(t *testing.T) {
	store := session.NewMemoryStore()
	input := providers.Message{Role: "user", Content: "Заёмщик Casey Mint; also, what is 7 squared?"}
	// A canonical-only identity, not content equality, defines delivery replay.
	input.TurnEnvelope = &providers.TurnEnvelope{Version: 1, DocumentConversationID: "docmsg_synthetic-one"}
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			copy := input
			if err := persistFullSessionMessage(t.Context(), store, "owner-one", &copy); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	history, err := store.ReadTurnHistory(t.Context(), "owner-one")
	if err != nil || len(history) != 1 || history[0].Content != input.Content {
		t.Fatal("duplicate transport admitted multiple or altered ordinary messages")
	}
	input.TurnEnvelope = &providers.TurnEnvelope{Version: 1, DocumentConversationID: "docmsg_synthetic-two"}
	if err := persistFullSessionMessage(t.Context(), store, "owner-one", &input); err != nil {
		t.Fatal(err)
	}
	history, _ = store.ReadTurnHistory(t.Context(), "owner-one")
	if len(history) != 2 {
		t.Fatal("a legitimate repeated message was discarded by content equality")
	}
	changed := input
	changed.Content = "Different authored message under the same identity"
	if err := persistFullSessionMessage(t.Context(), store, "owner-one", &changed); err == nil {
		t.Fatal("same-ID payload conflict was silently accepted")
	}
	if err := persistFullSessionMessage(t.Context(), store, "owner-two", &input); err != nil {
		t.Fatal(err)
	}
	other, _ := store.ReadTurnHistory(t.Context(), "owner-two")
	if len(other) != 1 {
		t.Fatal("conversation identity escaped its owner session")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	input.TurnEnvelope = &providers.TurnEnvelope{Version: 1, DocumentConversationID: "docmsg_canceled"}
	if err := persistFullSessionMessage(canceled, store, "owner-one", &input); err == nil {
		t.Fatal("canceled append succeeded")
	}
	history, _ = store.ReadTurnHistory(t.Context(), "owner-one")
	if len(history) != 2 {
		t.Fatal("conflict/cancellation changed committed conversation")
	}
}

func TestDocumentConversationAppendSurvivesJSONLRestart(t *testing.T) {
	root := t.TempDir()
	journal, err := memory.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := providers.Message{
		Role: "user", Content: "Borrower Casey Mint; city Mintford. What is 7 squared?",
		Media: []string{"media://owned-document"}, RootTurnStart: true,
		TurnEnvelope: &providers.TurnEnvelope{Version: 1, DocumentConversationID: "docmsg_restart"},
	}
	if err = persistFullSessionMessage(t.Context(), session.NewJSONLBackend(journal), "owner", &input); err != nil {
		t.Fatal(err)
	}
	createdAt := *input.CreatedAt
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = memory.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	store := session.NewJSONLBackend(journal)
	input.CreatedAt = nil
	// Empty optional slices disappear in JSON; this is the same payload.
	input.Attachments = []providers.Attachment{}
	if err = persistFullSessionMessage(t.Context(), store, "owner", &input); err != nil {
		t.Fatal(err)
	}
	history, err := store.ReadTurnHistory(t.Context(), "owner")
	if err != nil || len(history) != 1 || history[0].Content != input.Content ||
		documentConversationID(history[0]) != "docmsg_restart" || !input.CreatedAt.Equal(createdAt) {
		t.Fatal("restarted replay lost identity, duplicated input or replaced its original timestamp")
	}
	conflict := input
	conflict.Media = []string{"media://different-document"}
	if err = persistFullSessionMessage(t.Context(), store, "owner", &conflict); err == nil {
		t.Fatal("same-ID replay silently changed its attachment")
	}
	conflict.Role = "assistant"
	if err = persistFullSessionMessage(t.Context(), store, "owner", &conflict); err == nil {
		t.Fatal("assistant reused ordinary user input identity")
	}
}

func TestDocumentConversationCanonicalIdentityDoesNotReachProviderOrDiagnostics(t *testing.T) {
	input := providers.Message{Role: "user", Content: "BORROWER_PRIVATE_SENTINEL"}
	input.TurnEnvelope = &providers.TurnEnvelope{Version: 1, DocumentConversationID: "docmsg_metadata-only"}
	messages := []providers.Message{input, {Role: "assistant", Content: "BORROWER_PRIVATE_SENTINEL accepted"}}
	state := newLLMIterationState(1)
	if err := state.setCanonicalAttemptMessages(messages, promptCacheTailBoundary{}); err != nil {
		t.Fatal(err)
	}
	if state.callMessages[0].Content != input.Content || state.callMessages[0].TurnEnvelope != nil {
		t.Fatal("canonical provenance reached provider or authored conversation disappeared")
	}
	for _, message := range state.diagnosticMessagesForAttempt() {
		if strings.Contains(message.Content, "PRIVATE_SENTINEL") {
			t.Fatal("ordinary-history permission was mistaken for diagnostic disclosure permission")
		}
	}
	if !state.protectedDiagnosticContext {
		t.Fatal("a response able to quote normal form history lost diagnostic protection")
	}
	if messages[0].Content != input.Content || documentConversationID(messages[0]) == "" {
		t.Fatal("provider/diagnostic projection mutated canonical conversation")
	}
}

func TestDocumentConversationProvenanceSurvivesHookAndHistoryProjection(t *testing.T) {
	input := providers.Message{
		Role: "user", Content: "PRIVATE_FORM_CONVERSATION",
		TurnEnvelope: &providers.TurnEnvelope{
			Version: 1, DocumentConversationID: "docmsg_frozen",
			Parts: []providers.TurnEnvelopePart{{ID: "context.runtime", Content: "stable context"}},
		},
	}
	before := prepareHistoryForProvider([]providers.Message{input}, true)
	if !hasDocumentConversation(before) {
		t.Fatal("canonical history reconstruction dropped diagnostic provenance")
	}
	after := stripCanonicalMessageStateFromAll(before)
	if hasDocumentConversation(after) {
		t.Fatal("hook copy exposed canonical conversation identity")
	}
	if err := restoreFrozenTurnEnvelopes(before, after); err != nil {
		t.Fatal(err)
	}
	state := newLLMIterationState(1)
	if err := state.setCanonicalAttemptMessages(after, promptCacheTailBoundary{}); err != nil {
		t.Fatal(err)
	}
	if !state.protectedDiagnosticContext ||
		strings.Contains(state.diagnosticMessagesForAttempt()[0].Content, input.Content) {
		t.Fatal("hook round trip removed diagnostic protection")
	}
	want := input.Content + "\n\n<mintclaw_turn_context version=\"1\">\n" +
		"stable context\n</mintclaw_turn_context>"
	if state.callMessages[0].Content != want {
		t.Fatal("canonical-only identity changed the normal frozen provider prompt")
	}
}
