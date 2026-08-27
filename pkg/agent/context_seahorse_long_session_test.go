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
			return "Other project objective: keep OTHER_PROJECT_SECRET isolated.", nil
		}
		return longSessionContinuitySummary, nil
	}
	configFor := func(dbPath string) seahorse.Config {
		return seahorse.Config{
			DBPath: dbPath, SummaryPolicy: seahorse.SummaryPolicyCodingV1,
			HistoryMaxTokens: 6_000, SummaryMaxTokens: 1_000, FreshTailMaxTokens: 6_000, RecentTailTurns: 2,
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
	for turn := 0; turn < 64; turn++ {
		facts := fmt.Sprintf(
			"turn=%02d %s\nEdit parser.go was attempted; test output and pasted logs follow. ",
			turn,
			longSessionContinuitySummary,
		)
		appendMessage(manager, sessionKey, providers.Message{
			Role:    "user",
			Content: facts + strings.Repeat("bounded historical evidence ", 150),
			Media:   []string{"media://long-session/failure.png"},
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
		SessionKey: sessionKey, Reason: ContextCompressReasonRetry, Budget: 7_000,
	}); err != nil {
		t.Fatal(err)
	}
	firstEnd := receiveRuntimeEvent(t, ends).Payload.(ContextCompressLifecyclePayload)
	if firstEnd.Status != ContextCompressLifecycleCompleted || firstEnd.SummariesCreated < 3 ||
		!firstEnd.TokenCountsObserved || firstEnd.TokensAfter >= firstEnd.TokensBefore {
		t.Fatalf("first long-session compaction = %+v", firstEnd)
	}
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

	appendMessage(manager, sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "apply-once", Type: "function",
			Function: &providers.FunctionCall{Name: "apply_patch", Arguments: `{"path":"parser.go"}`},
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
	assertLongSessionContinuity(t, assembled)
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
	assertLongSessionContinuity(t, resumed)
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
		SessionKey: sessionKey, Reason: ContextCompressReasonRetry, Budget: 7_000,
	}); err != nil {
		t.Fatal(err)
	}
	rebuiltEnd := receiveRuntimeEvent(t, ends).Payload.(ContextCompressLifecyclePayload)
	rebuilt, err := manager.Assemble(ctx, &AssembleRequest{
		SessionKey: sessionKey, Budget: 9_000, MaxTokens: 1_000, ReserveTokens: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLongSessionContinuity(t, rebuilt)
	history, err := backend.ReadTurnHistory(ctx, sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := countLongSessionToolCall(history, "apply-once"); got != 1 {
		t.Fatalf("canonical side effect count after rebuild = %d, want 1", got)
	}
	t.Logf(
		"long-session baseline: canonical_messages=%d depth=%d first_tokens=%d->%d rebuilt_tokens=%d->%d first_duration=%s wall=%s",
		len(history),
		maxDepth,
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
		func(context.Context, string, seahorse.CompleteOptions) (string, error) {
			providerCalls++
			return "", context.DeadlineExceeded
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimeBus := runtimeevents.NewBus()
	t.Cleanup(func() { _ = runtimeBus.Close() })
	subscription, ends, err := runtimeBus.Channel().OfKind(runtimeevents.KindAgentContextCompressEnd).
		SubscribeChan(ctx, runtimeevents.SubscribeOptions{Name: "compactor-timeout", Buffer: 2})
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
	err = manager.Compact(ctx, &CompactRequest{
		SessionKey: sessionKey, Reason: ContextCompressReasonManual,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("compactor timeout error = %v, want context deadline exceeded", err)
	}
	terminal := receiveRuntimeEvent(t, ends).Payload.(ContextCompressLifecyclePayload)
	if terminal.Status != ContextCompressLifecycleInterrupted || providerCalls != 1 {
		t.Fatalf("compactor timeout terminal = %+v, provider calls = %d", terminal, providerCalls)
	}
}

func assertLongSessionContinuity(t *testing.T, assembled *AssembleResponse) {
	t.Helper()
	text := longSessionAssembledText(assembled)
	for _, marker := range []string{
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
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("assembled long-session context omits %q", marker)
		}
	}
}

func assertProviderSafeToolPair(t *testing.T, history []providers.Message, callID string) {
	t.Helper()
	uses, results := 0, 0
	for _, message := range history {
		for _, call := range message.ToolCalls {
			if call.ID == callID {
				uses++
			}
		}
		if message.ToolCallID == callID {
			results++
		}
	}
	if uses != 1 || results != 1 {
		t.Fatalf("assembled tool pairing for %q = uses:%d results:%d", callID, uses, results)
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

func countLongSessionToolCall(history []providers.Message, callID string) int {
	count := 0
	for _, message := range history {
		for _, call := range message.ToolCalls {
			if call.ID == callID {
				count++
			}
		}
	}
	return count
}
