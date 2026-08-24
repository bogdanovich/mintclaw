package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestToolExecutionMarkerIsBoundedAndRedacted(t *testing.T) {
	marker := newToolExecutionMarker(
		"private-provider-call-id",
		strings.Repeat("tool", 100),
		time.Date(2026, 8, 11, 12, 0, 0, 0, time.FixedZone("test", 3600)),
	)
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"secret-value", "another-secret", "/private/repository", "private-provider-call-id",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("durable marker leaked %q: %s", secret, encoded)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("marker fields = %v, want only correlation, tool, state, and time", fields)
	}
	if len(marker.Tool) > maxToolExecutionNameBytes {
		t.Fatalf("tool name bytes = %d, want <= %d", len(marker.Tool), maxToolExecutionNameBytes)
	}
	if len(marker.CallIDHash) != 64 || marker.State != toolExecutionStartedState ||
		marker.StartedAt.Location() != time.UTC {
		t.Fatalf("unexpected marker: %#v", marker)
	}
}

func TestRepairDanglingToolLifecyclesNeverClaimsSuccessOrReplays(t *testing.T) {
	startedMarker := newToolExecutionMarker("after-start", "write_file", time.Now())
	history := []providers.Message{
		{Role: "user", Content: "perform both writes"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{ID: "before-start", Function: &providers.FunctionCall{Name: "write_file"}},
				{ID: "after-start", Function: &providers.FunctionCall{Name: "write_file"}},
				{ID: "finished", Function: &providers.FunctionCall{Name: "write_file"}},
			},
			ToolExecutions: []providers.ToolExecution{startedMarker},
		},
		{
			Role: "tool", Content: "written", ToolCallID: "finished",
			ToolResultStatus: providers.ToolResultStatusSuccess,
		},
	}

	repaired, changed := repairDanglingToolLifecycles(history, time.Now())
	if !changed {
		t.Fatal("repair reported no change")
	}
	assertRecoveredToolStatus(t, repaired, "before-start", providers.ToolResultStatusInterrupted)
	assertRecoveredToolStatus(t, repaired, "after-start", providers.ToolResultStatusUnknown)
	assertRecoveredToolStatus(t, repaired, "finished", providers.ToolResultStatusSuccess)
	if second, secondChanged := repairDanglingToolLifecycles(
		repaired,
		time.Now(),
	); secondChanged ||
		len(second) != len(repaired) {
		t.Fatalf("repair is not idempotent: changed=%v lengths=%d/%d", secondChanged, len(repaired), len(second))
	}
}

func TestRepairDanglingToolLifecyclesScopesReusedIDsToToolBlock(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "first turn"},
		codingToolIntent("call_0", "write_file", nil),
		{
			Role: "tool", Content: "first complete", ToolCallID: "call_0",
			ToolResultStatus: providers.ToolResultStatusSuccess,
		},
		{Role: "assistant", Content: "first done"},
		{Role: "user", Content: "second turn"},
		codingToolIntent("call_0", "write_file", []providers.ToolExecution{
			mustToolExecutionMarker(t, "call_0", "write_file"),
		}),
	}
	repaired, changed := repairDanglingToolLifecycles(history, time.Now())
	if !changed {
		t.Fatal("reused ID in a later dangling block was not repaired")
	}
	results := 0
	for _, message := range repaired {
		if message.Role != "tool" || message.ToolCallID != "call_0" {
			continue
		}
		results++
		if results == 2 && message.ToolResultStatus != providers.ToolResultStatusUnknown {
			t.Fatalf("later reused-ID result status = %q, want unknown", message.ToolResultStatus)
		}
	}
	if results != 2 {
		t.Fatalf("reused-ID terminal results = %d, want 2", results)
	}
}

func TestCodingStartupRepairsCrashFixturesWithoutReplayingMutation(t *testing.T) {
	project := t.TempDir()
	stateRoot := t.TempDir()
	layout, profile := newCodingToolTestProfile(t, project, stateRoot)
	store, err := initRuntimeSessionStore(layout.StatePaths().SessionsRoot)
	if err != nil {
		t.Fatal(err)
	}
	mutationPath := filepath.Join(project, "mutation.txt")
	if err := os.WriteFile(mutationPath, []byte("one mutation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedMarker := newToolExecutionMarker(
		"during-mutation",
		"write_file",
		time.Now(),
	)
	fixtures := map[string][]providers.Message{
		"before-start": {
			{Role: "user", Content: "write"},
			codingToolIntent("before-start", "write_file", nil),
		},
		"after-start": {
			{Role: "user", Content: "write"},
			codingToolIntent("after-start", "write_file", []providers.ToolExecution{
				mustToolExecutionMarker(t, "after-start", "write_file"),
			}),
		},
		"during-mutation": {
			{Role: "user", Content: "write"},
			codingToolIntent("during-mutation", "write_file", []providers.ToolExecution{startedMarker}),
		},
		"before-result-persistence": {
			{Role: "user", Content: "exec"},
			codingToolIntent("before-result-persistence", "exec", []providers.ToolExecution{
				mustToolExecutionMarker(t, "before-result-persistence", "exec"),
			}),
		},
	}
	for sessionKey, fixture := range fixtures {
		if err := store.ReplaceTurnHistory(context.Background(), sessionKey, fixture); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	loop, err := NewCodingAgentLoop(
		codingToolTestConfig(), bus.NewMessageBus(), &mockProvider{}, profile,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()
	canonical := loop.GetRegistry().GetDefaultAgent().Sessions
	assertRecoveredToolStatus(
		t, canonical.GetHistory("before-start"), "before-start", providers.ToolResultStatusInterrupted,
	)
	for _, sessionKey := range []string{"after-start", "during-mutation", "before-result-persistence"} {
		assertRecoveredToolStatus(t, canonical.GetHistory(sessionKey), sessionKey, providers.ToolResultStatusUnknown)
	}
	content, err := os.ReadFile(mutationPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "one mutation\n" {
		t.Fatalf("startup replayed or changed the mutation: %q", content)
	}
}

func TestCodingToolDoesNotRunWhenStartMarkerCannotBePersisted(t *testing.T) {
	project := t.TempDir()
	stateRoot := t.TempDir()
	_, profile := newCodingToolTestProfile(t, project, stateRoot)
	provider := llmscenario.NewScriptedProvider("coding-tool-model", llmscenario.ProviderStep{
		Name: "attempt write",
		Response: llmscenario.ToolCallResponse(
			"writing",
			llmscenario.ToolCall("write-with-failed-marker", "write_file", map[string]any{
				"path": "must-not-exist.txt", "content": "side effect",
			}),
		),
	})
	loop, err := NewCodingAgentLoop(
		codingToolTestConfig(), bus.NewMessageBus(), provider, profile,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()
	agent := loop.GetRegistry().GetDefaultAgent()
	injectedErr := errors.New("injected lifecycle journal failure")
	agent.Sessions = &replaceFailingSessionStore{SessionStore: agent.Sessions, err: injectedErr}

	_, err = loop.ProcessDirect(context.Background(), "write the file", "coding:thread-tools")
	if !errors.Is(err, injectedErr) {
		t.Fatalf("ProcessDirect() error = %v, want %v", err, injectedErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "must-not-exist.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tool ran despite failed start marker: stat error = %v", statErr)
	}
}

func TestCodingRejectsAmbiguousToolCallIDsBeforeExecution(t *testing.T) {
	tests := []struct {
		name  string
		calls []providers.ToolCall
	}{
		{
			name: "empty",
			calls: []providers.ToolCall{{
				ID: "", Name: "write_file", Arguments: map[string]any{
					"path": "must-not-exist.txt", "content": "side effect",
				},
			}},
		},
		{
			name: "duplicate",
			calls: []providers.ToolCall{
				{
					ID: "duplicate", Name: "write_file", Arguments: map[string]any{
						"path": "must-not-exist.txt", "content": "first",
					},
				},
				{
					ID: "duplicate", Name: "write_file", Arguments: map[string]any{
						"path": "must-not-exist.txt", "content": "second",
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			stateRoot := t.TempDir()
			_, profile := newCodingToolTestProfile(t, project, stateRoot)
			provider := llmscenario.NewScriptedProvider("coding-tool-model", llmscenario.ProviderStep{
				Name: "ambiguous batch",
				Response: &providers.LLMResponse{
					Content: "writing", ToolCalls: test.calls,
				},
			})
			loop, err := NewCodingAgentLoop(
				codingToolTestConfig(), bus.NewMessageBus(), provider, profile,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer loop.Close()

			_, err = loop.ProcessDirect(context.Background(), "write", "coding:thread-tools")
			if err == nil || !strings.Contains(err.Error(), "invalid coding tool-call batch") {
				t.Fatalf("ProcessDirect() error = %v, want ambiguous-ID rejection", err)
			}
			if _, statErr := os.Stat(
				filepath.Join(project, "must-not-exist.txt"),
			); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("ambiguous batch executed a tool: stat error = %v", statErr)
			}
			history := loop.GetRegistry().GetDefaultAgent().Sessions.GetHistory("coding:thread-tools")
			if len(history) != 1 || history[0].Role != "user" {
				t.Fatalf("ambiguous assistant intent became canonical: %#v", history)
			}
		})
	}
}

func TestCodingTurnPersistsAcceptedUserMessageBeforeProviderCall(t *testing.T) {
	project := t.TempDir()
	stateRoot := t.TempDir()
	_, profile := newCodingToolTestProfile(t, project, stateRoot)
	var loop *AgentLoop
	provider := llmscenario.NewScriptedProvider("coding-tool-model", llmscenario.ProviderStep{
		Name: "observe accepted turn",
		Assert: func(_ llmscenario.ProviderCall) error {
			if loop == nil {
				return errors.New("agent loop was not assigned")
			}
			history := loop.GetRegistry().GetDefaultAgent().Sessions.GetHistory("coding:thread-tools")
			if len(history) == 0 || history[len(history)-1].Role != "user" ||
				history[len(history)-1].Content != "durable request" {
				return errors.New("accepted user message was not canonical before provider execution")
			}
			return nil
		},
		Response: llmscenario.TextResponse("observed"),
	})
	var err error
	loop, err = NewCodingAgentLoop(
		codingToolTestConfig(), bus.NewMessageBus(), provider, profile,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()
	response, err := loop.ProcessDirect(context.Background(), "durable request", "coding:thread-tools")
	if err != nil {
		t.Fatal(err)
	}
	if response != "observed" {
		t.Fatalf("response = %q, want observed", response)
	}
}

func TestPersonalRuntimeDoesNotRepairCodingToolLifecycle(t *testing.T) {
	cfg := codingToolTestConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()
	const sessionKey = "agent:main:test:direct:personal"
	history := []providers.Message{
		{Role: "user", Content: "personal request"},
		codingToolIntent("personal-call", "write_file", nil),
	}
	loop.GetRegistry().GetDefaultAgent().Sessions.SetHistory(sessionKey, history)
	if err := loop.repairCodingToolLifecycles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := loop.GetRegistry().GetDefaultAgent().Sessions.GetHistory(sessionKey); len(got) != len(history) {
		t.Fatalf("personal history length = %d, want %d", len(got), len(history))
	}
}

func codingToolIntent(callID, toolName string, executions []providers.ToolExecution) providers.Message {
	return providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: callID, Function: &providers.FunctionCall{Name: toolName},
		}},
		ToolExecutions: executions,
	}
}

func mustToolExecutionMarker(t *testing.T, callID, toolName string) providers.ToolExecution {
	t.Helper()
	return newToolExecutionMarker(callID, toolName, time.Now())
}

func assertRecoveredToolStatus(
	t *testing.T,
	history []providers.Message,
	callID string,
	want providers.ToolResultStatus,
) {
	t.Helper()
	found := 0
	for _, message := range history {
		if message.Role != "tool" || message.ToolCallID != callID {
			continue
		}
		found++
		if message.ToolResultStatus != want {
			t.Fatalf("tool result %q status = %q, want %q", callID, message.ToolResultStatus, want)
		}
		if message.ToolResultStatus == providers.ToolResultStatusSuccess && want != providers.ToolResultStatusSuccess {
			t.Fatalf("recovered tool result %q falsely claims success", callID)
		}
	}
	if found != 1 {
		t.Fatalf("tool result %q count = %d, want 1", callID, found)
	}
}
