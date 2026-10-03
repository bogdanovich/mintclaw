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

func TestPrefixOracleInstructionBoundaryStillRejectsHistoricalRewrite(t *testing.T) {
	before, err := SnapshotJSON([]byte(`{"model":"test","prompt_cache_key":"old","messages":[` +
		`{"role":"system","content":"old instructions"},{"role":"user","content":"old question"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, rewrite := range []bool{false, true} {
		history := "old question"
		if rewrite {
			history = "rewritten question"
		}
		after, err := SnapshotJSON([]byte(`{"model":"test","prompt_cache_key":"new","messages":[` +
			`{"role":"system","content":"new instructions"},{"role":"user","content":"` + history + `"},` +
			`{"role":"user","content":"followup"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := before.RequireLineageRotation(after); err != nil {
			t.Fatal(err)
		}
		if err := before.RequireMessagesExtension(after); err == nil {
			t.Fatal("full-message boundary oracle ignored rewritten system instructions")
		}
		if err := before.RequireTranscriptExtension(after); (err != nil) != rewrite {
			t.Fatalf("boundary error = %v", err)
		}
	}
	if err := before.RequireLineageRotation(before); err == nil {
		t.Fatal("accepted unchanged lineage at boundary")
	}
}

func TestPrefixOracleAllowsCompletedCallIDReuseAcrossTurns(t *testing.T) {
	conversation := `[{"role":"user","content":"first"},` +
		`{"role":"assistant","tool_calls":[{"id":"call_0"}]},` +
		`{"role":"tool","tool_call_id":"call_0","content":"first result"},` +
		`{"role":"assistant","content":"first answer"},{"role":"user","content":"second"},` +
		`{"role":"assistant","tool_calls":[{"id":"call_0"}]},` +
		`{"role":"tool","tool_call_id":"call_0","content":"second result"}]`
	before, err := SnapshotJSON([]byte(`{"model":"test","messages":` + conversation + `}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := SnapshotJSON([]byte(`{"model":"test","messages":` + strings.TrimSuffix(conversation, "]") +
		`,{"role":"assistant","content":"second answer"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := before.RequireExtension(after); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotJSON([]byte(`{"model":"test","messages":` + strings.TrimSuffix(conversation, "]") +
		`,{"role":"tool","tool_call_id":"call_0","content":"duplicate result"}]}`)); err == nil {
		t.Fatal("call ID reuse hid an orphaned duplicate result")
	}
}
