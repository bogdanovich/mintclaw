package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestCodingToolsEditTestAndResumeWithDurableToolPairs(t *testing.T) {
	project := t.TempDir()
	stateRoot := t.TempDir()
	writeCodingToolTestFile(t, filepath.Join(project, "go.mod"), "module example.test/fixture\n\ngo 1.25\n")
	writeCodingToolTestFile(
		t,
		filepath.Join(project, "calc.go"),
		"package fixture\n\nfunc Add(a, b int) int { return a - b }\n",
	)
	writeCodingToolTestFile(t, filepath.Join(project, "calc_test.go"), `package fixture

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`)

	provider := llmscenario.NewScriptedProvider(
		"coding-tool-model",
		llmscenario.ProviderStep{
			Name: "inspect fixture",
			Response: llmscenario.ToolCallResponse(
				"I will inspect the fixture.",
				llmscenario.ToolCall("list-1", "list_dir", map[string]any{"path": "."}),
				llmscenario.ToolCall("search-1", "search_files", map[string]any{
					"pattern": "return a - b", "path": ".", "file_glob": "*.go",
				}),
				llmscenario.ToolCall("read-1", "read_file", map[string]any{"path": "calc.go"}),
			),
		},
		llmscenario.ProviderStep{
			Name: "patch defect",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := requireToolResults(call.Messages, "list-1", "search-1", "read-1"); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "return a - b")(call)
			},
			Response: llmscenario.ToolCallResponse(
				"I found the defect.",
				llmscenario.ToolCall("patch-1", "apply_patch", map[string]any{
					"input": "*** Begin Patch\n*** Update File: calc.go\n@@\n-func Add(a, b int) int { return a - b }\n+func Add(a, b int) int { return a + b }\n*** End Patch",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name:   "write report",
			Assert: llmscenario.RequireLastMessage("tool", "calc.go"),
			Response: llmscenario.ToolCallResponse(
				"I will record the change.",
				llmscenario.ToolCall("write-1", "write_file", map[string]any{
					"path": "report.txt", "content": "fixed Add\n",
				}),
				llmscenario.ToolCall("append-1", "append_file", map[string]any{
					"path": "report.txt", "content": "tests pending\n",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "test fixture",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := requireNoToolExecutionMetadata(call.Messages); err != nil {
					return err
				}
				return requireToolResults(call.Messages, "write-1", "append-1")
			},
			Response: llmscenario.ToolCallResponse(
				"I will test it.",
				llmscenario.ToolCall("exec-1", "exec", map[string]any{
					"action": "run", "command": "go test ./...",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "report success",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := requireNoToolExecutionMetadata(call.Messages); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "ok")(call)
			},
			Response: llmscenario.TextResponse("fixed and tested"),
		},
	)

	_, profile := newCodingToolTestProfile(t, project, stateRoot)
	cfg := codingToolTestConfig()
	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), provider, profile)
	if err != nil {
		t.Fatal(err)
	}
	response, err := loop.ProcessDirect(context.Background(), "fix Add and test it", "coding:thread-tools")
	if err != nil {
		loop.Close()
		t.Fatalf("ProcessDirect() error = %v", err)
	}
	if response != "fixed and tested" {
		loop.Close()
		t.Fatalf("response = %q", response)
	}
	liveHistory := loop.GetRegistry().GetDefaultAgent().Sessions.GetHistory("coding:thread-tools")
	if err := requireToolPairs(
		liveHistory,
		"list-1", "search-1", "read-1", "patch-1", "write-1", "append-1", "exec-1",
	); err != nil {
		loop.Close()
		t.Fatalf("live journal: %v", err)
	}
	for _, callID := range []string{"patch-1", "write-1", "append-1", "exec-1"} {
		if !hasToolExecutionMarker(liveHistory, callID) {
			loop.Close()
			t.Fatalf("live journal is missing durable start marker for %q", callID)
		}
	}
	for _, callID := range []string{"list-1", "search-1", "read-1"} {
		if hasToolExecutionMarker(liveHistory, callID) {
			loop.Close()
			t.Fatalf("read-only tool %q unexpectedly received a durable start marker", callID)
		}
	}
	loop.Close()
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(
		filepath.Join(project, "report.txt"),
	); err != nil ||
		string(got) != "fixed Add\ntests pending\n" {
		t.Fatalf("report = %q, %v", got, err)
	}

	resumeProvider := llmscenario.NewScriptedProvider("coding-tool-model", llmscenario.ProviderStep{
		Name:     "continue reopened thread",
		Response: llmscenario.TextResponse("resume observed"),
	})
	_, resumeProfile := newCodingToolTestProfile(t, project, stateRoot)
	resumed, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), resumeProvider, resumeProfile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	reopenedHistory := resumed.GetRegistry().GetDefaultAgent().Sessions.GetHistory("coding:thread-tools")
	if err := requireToolPairs(
		reopenedHistory,
		"list-1", "search-1", "read-1", "patch-1", "write-1", "append-1", "exec-1",
	); err != nil {
		t.Fatalf("reopened journal: %v", err)
	}
	if !hasToolResult(reopenedHistory, "exec-1", "ok") {
		t.Fatal("reopened journal is missing the successful exec output")
	}
	for _, callID := range []string{"patch-1", "write-1", "append-1", "exec-1"} {
		if !hasToolExecutionMarker(reopenedHistory, callID) {
			t.Fatalf("reopened journal is missing durable start marker for %q", callID)
		}
	}
	response, err = resumed.ProcessDirect(context.Background(), "what happened?", "coding:thread-tools")
	if err != nil {
		t.Fatalf("resumed ProcessDirect() error = %v", err)
	}
	if response != "resume observed" {
		t.Fatalf("resumed response = %q", response)
	}
}

func requireToolResults(messages []providers.Message, callIDs ...string) error {
	want := make(map[string]bool, len(callIDs))
	for _, callID := range callIDs {
		want[callID] = false
	}
	for _, message := range messages {
		if message.Role == "tool" {
			if _, ok := want[message.ToolCallID]; ok {
				want[message.ToolCallID] = true
			}
		}
	}
	for _, callID := range callIDs {
		if !want[callID] {
			return fmt.Errorf("tool result %q is missing from provider context", callID)
		}
	}
	return nil
}

func requireToolPairs(messages []providers.Message, callIDs ...string) error {
	if err := requireToolResults(messages, callIDs...); err != nil {
		return err
	}
	want := make(map[string]bool, len(callIDs))
	for _, callID := range callIDs {
		want[callID] = false
	}
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if _, ok := want[call.ID]; ok {
				want[call.ID] = true
			}
		}
	}
	for _, callID := range callIDs {
		if !want[callID] {
			return fmt.Errorf("assistant tool call %q is missing from canonical journal", callID)
		}
	}
	return nil
}

func hasToolResult(messages []providers.Message, callID, contains string) bool {
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == callID && strings.Contains(message.Content, contains) {
			return true
		}
	}
	return false
}

func hasToolExecutionMarker(messages []providers.Message, callID string) bool {
	callHash := toolLifecycleCallHash(callID)
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, marker := range message.ToolExecutions {
			if marker.CallIDHash == callHash && marker.State == toolExecutionStartedState {
				return true
			}
		}
	}
	return false
}

func requireNoToolExecutionMetadata(messages []providers.Message) error {
	for _, message := range messages {
		if len(message.ToolExecutions) > 0 {
			return fmt.Errorf("provider context leaked %d durable tool start markers", len(message.ToolExecutions))
		}
	}
	return nil
}

func newCodingToolTestProfile(t *testing.T, project, stateRoot string) (RuntimeLayout, RuntimeProfile) {
	t.Helper()
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-tools"},
		project,
		stateRoot,
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	return layout, profile
}

func codingToolTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.Provider = "test-provider"
	cfg.Agents.Defaults.ModelName = "coding-tool-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	return cfg
}

func writeCodingToolTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
