package llmscenario

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPrefixOracleRejectsHistoricalAndCompatibilityMutations(t *testing.T) {
	base := ProviderCall{
		Model: "test", Options: map[string]any{"prompt_cache_key": "lineage"},
		Tools: []providers.ToolDefinition{{Type: "function", Function: providers.ToolFunctionDefinition{
			Name: "read_file", Parameters: map[string]any{"type": "object"},
		}}},
		Messages: []providers.Message{
			{Role: "system", Content: "instructions"},
			{Role: "user", Content: "question", Media: []string{"image-ref"}},
			{
				Role:      "assistant",
				ToolCalls: []providers.ToolCall{ToolCall("read", "read_file", map[string]any{"path": "one"})},
			},
			{Role: "tool", ToolCallID: "read", Content: "file contents"},
		},
	}
	before, err := SnapshotCall(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*ProviderCall){
		"valid append":    func(*ProviderCall) {},
		"system":          func(c *ProviderCall) { c.Messages[0].Content = "rewritten" },
		"user":            func(c *ProviderCall) { c.Messages[1].Content = "rewritten" },
		"media":           func(c *ProviderCall) { c.Messages[1].Media[0] = "other-ref" },
		"arguments":       func(c *ProviderCall) { c.Messages[2].ToolCalls[0].Arguments["path"] = "two" },
		"result":          func(c *ProviderCall) { c.Messages[3].Content = "rewritten" },
		"model":           func(c *ProviderCall) { c.Model = "other" },
		"lineage":         func(c *ProviderCall) { c.Options["prompt_cache_key"] = "other" },
		"missing lineage": func(c *ProviderCall) { delete(c.Options, "prompt_cache_key") },
		"schema":          func(c *ProviderCall) { c.Tools[0].Function.Parameters["type"] = "string" },
		"order":           func(c *ProviderCall) { c.Messages[0], c.Messages[1] = c.Messages[1], c.Messages[0] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var next ProviderCall
			encoded, marshalErr := json.Marshal(base)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := json.Unmarshal(encoded, &next); err != nil {
				t.Fatal(err)
			}
			mutate(&next)
			next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "followup"})
			after, err := SnapshotCall(next)
			if name == "missing lineage" {
				if err == nil {
					t.Fatal("accepted missing neutral lineage")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = before.RequireExtension(after)
			if (err == nil) != (name == "valid append") {
				t.Fatalf("oracle error = %v", err)
			}
		})
	}
}

func TestScriptedProviderCapturesAreDetachedFromRuntimeAndReaders(t *testing.T) {
	provider := NewScriptedProvider("test", ProviderStep{Response: TextResponse("done")})
	call := ProviderCall{
		Model: "test", Options: map[string]any{"nested": map[string]any{"key": "original"}},
		Messages: []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{
			ToolCall("one", "read", map[string]any{"nested": map[string]any{"key": "original"}}),
		}}},
		Tools: []providers.ToolDefinition{{Function: providers.ToolFunctionDefinition{
			Parameters: map[string]any{"properties": map[string]any{"key": "original"}},
		}}},
	}
	if _, err := provider.Chat(t.Context(), call.Messages, call.Tools, call.Model, call.Options); err != nil {
		t.Fatal(err)
	}
	mutate := func(call ProviderCall) {
		call.Messages[0].ToolCalls[0].Arguments["nested"].(map[string]any)["key"] = "changed"
		call.Tools[0].Function.Parameters["properties"].(map[string]any)["key"] = "changed"
		call.Options["nested"].(map[string]any)["key"] = "changed"
	}
	mutate(call)
	mutate(provider.Calls()[0])
	captured, err := json.Marshal(provider.Calls()[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(captured), "changed") {
		t.Fatal("capture shares mutable request data")
	}
}

func TestPrefixOracleRejectsBrokenToolEvidenceAndSidecars(t *testing.T) {
	for name, messages := range map[string]string{
		"missing":   `[{"role":"assistant","tool_calls":[{"id":"a"}]}]`,
		"orphaned":  `[{"role":"tool","tool_call_id":"a"}]`,
		"duplicate": `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"a"}]}]`,
		"reordered": `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},` +
			`{"role":"tool","tool_call_id":"b"},{"role":"tool","tool_call_id":"a"}]`,
		"interrupted": `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"user","content":"new"}]`,
		"sidecar":     `[{"role":"user","content":"hello","turn_envelope":{"version":1}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SnapshotJSON([]byte(`{"model":"test","messages":` + messages + `}`))
			if err == nil {
				t.Fatal("accepted corrupt request")
			}
		})
	}
}

func TestPrefixOracleCanonicalizesOnlyObjectKeysAndDetachesCapture(t *testing.T) {
	input := []byte(`{"model":"test","prompt_cache_key":"one","tools":[{"b":2,"a":1}],` +
		`"messages":[{"role":"user","content":"question"}]}`)
	before, err := SnapshotJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 'x'
	after, err := SnapshotJSON([]byte(`{"model":"test","prompt_cache_key":"one","tools":[{"a":1,"b":2}],` +
		`"messages":[{"content":"question","role":"user"},{"role":"user","content":"next"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := before.RequireExtension(after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before.messages[0]), "next") {
		t.Fatal("snapshot mutated")
	}
}

func TestPrefixCorpusRequiresReadFileToolSchema(t *testing.T) {
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "read_file", Parameters: map[string]any{"type": "object"},
		},
	}}
	first := []providers.Message{{Role: "user", Content: PrefixInitialPrompt}}
	second := append(append([]providers.Message(nil), first...),
		providers.Message{
			Role: "assistant", ToolCalls: []providers.ToolCall{
				ToolCall(PrefixCallID, "read_file", map[string]any{"path": "corpus.txt"}),
			},
		},
		providers.Message{Role: "tool", ToolCallID: PrefixCallID, Content: PrefixMarker},
	)
	third := append(append([]providers.Message(nil), second...),
		providers.Message{Role: "assistant", Content: PrefixAnswer},
		providers.Message{Role: "user", Content: PrefixFollowupPrompt},
	)

	snapshots := func(t *testing.T, schemas []providers.ToolDefinition) []RequestSnapshot {
		t.Helper()
		requests := make([]RequestSnapshot, 0, 3)
		for _, messages := range [][]providers.Message{first, second, third} {
			snapshot, err := SnapshotCall(ProviderCall{
				Model: "test", Options: map[string]any{"prompt_cache_key": "lineage"},
				Tools: schemas, Messages: messages,
			})
			if err != nil {
				t.Fatal(err)
			}
			requests = append(requests, snapshot)
		}
		return requests
	}

	corpus := PrefixCorpus{}
	if err := corpus.Check(snapshots(t, tools)...); err != nil {
		t.Fatalf("valid corpus error = %v", err)
	}
	if err := corpus.Check(snapshots(t, nil)...); err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("tool-free corpus error = %v, want missing read_file schema", err)
	}
}
