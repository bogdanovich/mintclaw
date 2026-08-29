package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const longSessionContinuitySummary = `Objective: finish the parser migration and satisfy the documented done criteria.
Changed paths: parser.go, parser_test.go, and docs/parser.md.
Validation: parser unit tests passed; Windows path test failed; integration tests were not run.
Unresolved failure: Windows path normalization still fails. Next action: inspect normalizeWindowsPath.
Constraints: keep --yolo as the default and do not add backward compatibility.
Rejected approach: a regex-only parser rewrite was rejected because it loses source locations.
Artifacts: artifact://long-session/test-output.log and media://long-session/failure.png.
Historical workspace claim: branch old-summary-branch was dirty.`

var longSessionContinuityMarkers = []string{
	"finish the parser migration",
	"parser.go, parser_test.go, and docs/parser.md",
	"parser unit tests passed",
	"Windows path test failed",
	"integration tests were not run",
	"normalizeWindowsPath",
	"do not add backward compatibility",
	"regex-only parser rewrite was rejected",
	"artifact://long-session/test-output.log",
	"media://long-session/failure.png",
}

func TestCodingLongSessionCompactionContinuity(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	canonical, err := memory.NewJSONLStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	t.Cleanup(func() { _ = backend.Close() })
	complete := func(_ context.Context, prompt string, _ seahorse.CompleteOptions) (string, error) {
		if strings.Contains(prompt, "OTHER_PROJECT_SECRET") {
			return "", errors.New("cross-thread content reached compaction fixture")
		}
		for _, marker := range longSessionContinuityMarkers {
			if !strings.Contains(prompt, marker) {
				return "", fmt.Errorf(
					"compaction prompt omitted continuity marker %q: suffix=%q",
					marker,
					prompt[max(0, len(prompt)-1_200):],
				)
			}
		}
		return longSessionContinuitySummary, nil
	}
	configFor := func(dbPath string) seahorse.Config {
		return seahorse.Config{
			DBPath: dbPath, SummaryPolicy: seahorse.SummaryPolicyCodingV1,
			HistoryMaxTokens: 6_800, SummaryMaxTokens: 600, FreshTailMaxTokens: 6_800, RecentTailTurns: 1,
		}
	}
	openManager := func(dbPath string) (*seahorseContextManager, *seahorse.Engine) {
		t.Helper()
		engine, openErr := seahorse.NewEngine(ctx, configFor(dbPath), complete)
		if openErr != nil {
			t.Fatal(openErr)
		}
		manager := newSingleRuntimeTestManager(engine, backend)
		singleTestRuntime(manager).reconciliationGeneration = seahorse.SummaryPolicyCodingV1.ReconciliationGeneration(
			seahorseReconciliationGeneration,
		)
		return manager, engine
	}
	appendMessage := func(manager *seahorseContextManager, key string, message providers.Message) {
		t.Helper()
		if appendErr := backend.AppendTurnMessage(ctx, key, message); appendErr != nil {
			t.Fatal(appendErr)
		}
		if ingestErr := manager.Ingest(ctx, &IngestRequest{SessionKey: key, Message: message}); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	const sessionKey = "coding-long-session"
	dbPath := filepath.Join(root, "derived", "context.db")
	manager, engine := openManager(dbPath)
	for turn := 0; turn < 32; turn++ {
		facts := fmt.Sprintf("turn=%02d Edit parser.go was attempted; test output and pasted logs follow. ", turn)
		var media []string
		assistantFacts := fmt.Sprintf("turn=%02d assistant recorded the observed coding state. ", turn)
		if turn < 31 {
			facts += longSessionContinuitySummary
			assistantFacts += longSessionContinuitySummary
		} else {
			media = []string{"media://long-session/failure.png"}
		}
		appendMessage(manager, sessionKey, providers.Message{
			Role:    "user",
			Content: facts + strings.Repeat("bounded historical evidence ", 150),
			Media:   media,
		})
		appendMessage(manager, sessionKey, providers.Message{
			Role:    "assistant",
			Content: assistantFacts + strings.Repeat("bounded assistant evidence ", 100),
		})
	}

	runtimeBus := runtimeevents.NewBus()
	t.Cleanup(func() { _ = runtimeBus.Close() })
	manager.al = &AgentLoop{runtimeEvents: runtimeBus}
	subscription, ends, err := runtimeBus.Channel().OfKind(runtimeevents.KindAgentContextCompressEnd).
		SubscribeChan(ctx, runtimeevents.SubscribeOptions{Name: "long-session-compaction", Buffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subscription.Close() })
	compactStarted := time.Now()
	if err = manager.Compact(ctx, &CompactRequest{
		SessionKey: sessionKey, Reason: ContextCompressReasonRetry, Budget: 7_400,
	}); err != nil {
		conversation, lookupErr := engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, sessionKey)
		if lookupErr == nil && conversation != nil {
			historyTokens, summaryTokens, countErr := engine.GetRetrieval().Store().GetContextTokenCounts(
				ctx,
				conversation.ConversationID,
			)
			t.Fatalf("%v (history=%d summary=%d count_error=%v)", err, historyTokens, summaryTokens, countErr)
		}
		t.Fatal(err)
	}
	firstEnd := receiveRuntimeEvent(t, ends).Payload.(ContextCompressLifecyclePayload)
	assertCompletedLongSessionCompaction(t, firstEnd)
	maxDepth := longSessionSummaryDepth(t, ctx, engine, sessionKey)

	appendMessage(manager, sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "apply-once", Type: "function", Name: "apply_patch", Arguments: map[string]any{"path": "parser.go"},
		}},
	})
	appendMessage(manager, sessionKey, providers.Message{
		Role:             "tool",
		ToolCallID:       "apply-once",
		ToolResultStatus: providers.ToolResultStatusSuccess,
		Content:          "File edited: parser.go",
		Media:            []string{"media://long-session/failure.png"},
	})
	appendMessage(manager, "other-project-thread", providers.Message{
		Role: "user", Content: "OTHER_PROJECT_SECRET must never enter the primary thread",
	})

	assembled, err := manager.Assemble(ctx, &AssembleRequest{
		SessionKey: sessionKey, Budget: 9_000, MaxTokens: 1_000, ReserveTokens: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLongSessionSummary(t, assembled)
	assertProviderSafeToolPair(t, assembled.History, "apply-once")
	if strings.Contains(longSessionAssembledText(assembled), "OTHER_PROJECT_SECRET") {
		t.Fatal("cross-thread content leaked into primary assembled context")
	}
	if assembled.Budget == nil || assembled.Budget.SelectedHistoryTokens > assembled.Budget.HistoryBudget ||
		assembled.Budget.SelectedSummaryTokens > assembled.Budget.SummaryBudget {
		t.Fatalf("assembled context exceeded budget: %+v", assembled.Budget)
	}

	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}
	manager, engine = openManager(dbPath)
	if _, err = manager.ensureReconciledRuntime(ctx, singleTestRuntime(manager), sessionKey); err != nil {
		t.Fatal(err)
	}
	resumed, err := manager.Assemble(ctx, &AssembleRequest{
		SessionKey: sessionKey, Budget: 9_000, MaxTokens: 1_000, ReserveTokens: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLongSessionSummary(t, resumed)
	assertProviderSafeToolPair(t, resumed.History, "apply-once")
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			t.Fatal(removeErr)
		}
	}

	manager, engine = openManager(dbPath)
	t.Cleanup(func() { _ = engine.Close() })
	manager.al = &AgentLoop{runtimeEvents: runtimeBus}
	if _, err = manager.ensureReconciledRuntime(ctx, singleTestRuntime(manager), sessionKey); err != nil {
		t.Fatal(err)
	}
	if err = manager.Compact(ctx, &CompactRequest{
		SessionKey: sessionKey, Reason: ContextCompressReasonRetry, Budget: 7_400,
	}); err != nil {
		t.Fatal(err)
	}
	rebuiltEnd := receiveRuntimeEvent(t, ends).Payload.(ContextCompressLifecyclePayload)
	assertCompletedLongSessionCompaction(t, rebuiltEnd)
	rebuiltDepth := longSessionSummaryDepth(t, ctx, engine, sessionKey)
	rebuilt, err := manager.Assemble(ctx, &AssembleRequest{
		SessionKey: sessionKey, Budget: 9_000, MaxTokens: 1_000, ReserveTokens: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLongSessionSummary(t, rebuilt)
	assertProviderSafeToolPair(t, rebuilt.History, "apply-once")
	history, err := backend.ReadTurnHistory(ctx, sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderSafeToolPair(t, history, "apply-once")
	t.Logf(
		"long-session baseline: canonical_messages=%d depth=%d first_tokens=%d->%d rebuilt_tokens=%d->%d first_duration=%s wall=%s",
		len(history),
		min(maxDepth, rebuiltDepth),
		firstEnd.TokensBefore,
		firstEnd.TokensAfter,
		rebuiltEnd.TokensBefore,
		rebuiltEnd.TokensAfter,
		firstEnd.Duration,
		time.Since(compactStarted),
	)
}

func TestCodingCompactorTimeoutProducesOneTerminalInterruption(t *testing.T) {
	ctx := t.Context()
	providerCalls := 0
	engine, err := seahorse.NewEngine(
		ctx,
		seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.db")},
		func(completeCtx context.Context, _ string, _ seahorse.CompleteOptions) (string, error) {
			providerCalls++
			select {
			case <-completeCtx.Done():
				return "", completeCtx.Err()
			case <-time.After(2 * time.Second):
				return "", errors.New("compactor did not receive caller cancellation")
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimeBus := runtimeevents.NewBus()
	t.Cleanup(func() { _ = runtimeBus.Close() })
	subscription, events, err := runtimeBus.Channel().OfKind(
		runtimeevents.KindAgentContextCompressStart,
		runtimeevents.KindAgentContextCompressEnd,
	).
		SubscribeChan(ctx, runtimeevents.SubscribeOptions{Name: "compactor-timeout", Buffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subscription.Close() })
	manager := newSingleRuntimeTestManager(engine, nil)
	manager.al = &AgentLoop{runtimeEvents: runtimeBus}
	const sessionKey = "coding-compactor-timeout"
	for index := 0; index < seahorse.FreshTailCount+seahorse.LeafMinFanout; index++ {
		if err = manager.Ingest(ctx, &IngestRequest{
			SessionKey: sessionKey,
			Message: providers.Message{
				Role: "user", Content: strings.Repeat("bounded timeout evidence ", 100),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	compactCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	err = manager.Compact(compactCtx, &CompactRequest{
		SessionKey: sessionKey, Reason: ContextCompressReasonManual,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("compactor timeout error = %v, want context deadline exceeded", err)
	}
	started := receiveRuntimeEvent(t, events).Payload.(ContextCompressLifecyclePayload)
	terminal := receiveRuntimeEvent(t, events).Payload.(ContextCompressLifecyclePayload)
	if started.Status != ContextCompressLifecycleStarted ||
		terminal.Status != ContextCompressLifecycleInterrupted ||
		started.AttemptID == "" || terminal.AttemptID != started.AttemptID ||
		providerCalls != 1 {
		t.Fatalf(
			"compactor timeout lifecycle = start:%+v end:%+v, provider calls = %d",
			started,
			terminal,
			providerCalls,
		)
	}
	select {
	case duplicate := <-events:
		t.Fatalf("compactor timeout emitted duplicate lifecycle event: %+v", duplicate)
	default:
	}
	if stats := subscription.Stats(); stats.Received != 2 || stats.Dropped != 0 {
		t.Fatalf("compactor timeout subscription stats = %+v, want received 2 and dropped 0", stats)
	}
}

func assertLongSessionSummary(t *testing.T, assembled *AssembleResponse) {
	t.Helper()
	if assembled == nil {
		t.Fatal("assembled long-session context is nil")
	}
	for _, marker := range longSessionContinuityMarkers {
		if !strings.Contains(assembled.Summary, marker) {
			t.Fatalf("assembled long-session summary omits %q: %q", marker, assembled.Summary)
		}
	}
}

func assertCompletedLongSessionCompaction(t *testing.T, terminal ContextCompressLifecyclePayload) {
	t.Helper()
	if terminal.Status != ContextCompressLifecycleCompleted || terminal.SummariesCreated < 3 ||
		!terminal.TokenCountsObserved || terminal.TokensAfter >= terminal.TokensBefore {
		t.Fatalf("long-session compaction = %+v", terminal)
	}
}

func longSessionSummaryDepth(t *testing.T, ctx context.Context, engine *seahorse.Engine, sessionKey string) int {
	t.Helper()
	conversation, err := engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, sessionKey)
	if err != nil || conversation == nil {
		t.Fatalf("long-session conversation = %#v, %v", conversation, err)
	}
	summaries, err := engine.GetRetrieval().Store().GetSummariesByConversation(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	maxDepth := 0
	for _, summary := range summaries {
		maxDepth = max(maxDepth, summary.Depth)
	}
	if maxDepth < 2 {
		t.Fatalf("summary condensation depth = %d, want at least 2", maxDepth)
	}
	return maxDepth
}

func assertProviderSafeToolPair(t *testing.T, history []providers.Message, callID string) {
	t.Helper()
	uses, results := 0, 0
	callIndex, resultIndex := -1, -1
	for index, message := range history {
		for _, call := range message.ToolCalls {
			if call.ID == callID {
				uses++
				callIndex = index
			}
		}
		if message.ToolCallID == callID {
			results++
			resultIndex = index
		}
	}
	if uses != 1 || results != 1 || resultIndex <= callIndex {
		t.Fatalf(
			"assembled tool pairing for %q = uses:%d@%d results:%d@%d",
			callID,
			uses,
			callIndex,
			results,
			resultIndex,
		)
	}
}

func longSessionAssembledText(assembled *AssembleResponse) string {
	if assembled == nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(assembled.Summary)
	for _, message := range assembled.History {
		builder.WriteString("\n")
		builder.WriteString(message.Content)
		builder.WriteString("\n")
		builder.WriteString(strings.Join(message.Media, "\n"))
	}
	return builder.String()
}
