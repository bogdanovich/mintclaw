package llmscenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// PrefixCorpus is a read-only conversation shared by real runtime entry points.
// The caller supplies a file in its admitted execution root; no tool is stubbed.
type PrefixCorpus struct {
	Path string
}

const (
	PrefixInitialPrompt  = "Read the corpus file and report its marker. Do not change files."
	PrefixFollowupPrompt = "Repeat the marker from the previous turn without reading the file again."
	PrefixMarker         = "MINTCLAW_CONTEXT_CORPUS"
	PrefixAnswer         = "The marker is MINTCLAW_CONTEXT_CORPUS."
	PrefixCallID         = "corpus-read"
)

func (c PrefixCorpus) Steps() []ProviderStep {
	return []ProviderStep{
		{Response: ToolCallResponse("Inspecting the corpus.",
			ToolCall(PrefixCallID, "read_file", map[string]any{"path": c.Path}))},
		{Assert: RequireLastMessage("tool", PrefixMarker), Response: TextResponse(PrefixAnswer)},
		{Response: TextResponse(PrefixAnswer)},
	}
}

// RequestSnapshot compares actual request data, independently of the production
// cache fingerprint. JSON object keys are canonicalized; array order and every
// message field are retained. It accepts neutral Chat captures and HTTP captures
// without pretending those two serialization formats are interchangeable.
type RequestSnapshot struct {
	model    string
	lineage  string
	tools    json.RawMessage
	messages []json.RawMessage
}

func SnapshotCall(call ProviderCall) (RequestSnapshot, error) {
	if key, _ := call.Options["prompt_cache_key"].(string); key == "" {
		return RequestSnapshot{}, fmt.Errorf("neutral request lacks runtime cache lineage")
	}
	// ModelName and CreatedAt are canonical provenance, not Chat wire fields.
	// ToolResultStatus is likewise internal; role/content carry the wire result.
	// Do not strip any sidecars here: SnapshotJSON must reject leaked state.
	messages := make([]map[string]any, 0, len(call.Messages))
	for _, message := range call.Messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return RequestSnapshot{}, err
		}
		var fields map[string]any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err = decoder.Decode(&fields); err != nil {
			return RequestSnapshot{}, err
		}
		delete(fields, "model_name")
		delete(fields, "created_at")
		delete(fields, "tool_result_status")
		// Presentation feedback is consumed by the runtime, not sent to a model.
		if calls, ok := fields["tool_calls"].([]any); ok {
			for _, call := range calls {
				delete(call.(map[string]any), "tool_feedback_explanation")
			}
		}
		messages = append(messages, fields)
	}
	payload := struct {
		Model    string `json:"model"`
		Messages any    `json:"messages"`
		Tools    any    `json:"tools"`
		Lineage  any    `json:"prompt_cache_key,omitempty"`
	}{call.Model, messages, call.Tools, call.Options["prompt_cache_key"]}
	data, err := json.Marshal(payload)
	if err != nil {
		return RequestSnapshot{}, err
	}
	return SnapshotJSON(data)
}

func SnapshotJSON(data []byte) (RequestSnapshot, error) {
	var request struct {
		Model    string            `json:"model"`
		Lineage  string            `json:"prompt_cache_key"`
		Tools    json.RawMessage   `json:"tools"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return RequestSnapshot{}, err
	}
	if request.Model == "" || len(request.Messages) == 0 {
		return RequestSnapshot{}, fmt.Errorf("request lacks model or messages")
	}
	snapshot := RequestSnapshot{model: request.Model, lineage: request.Lineage}
	var err error
	snapshot.tools, err = canonicalJSON(request.Tools)
	if err != nil {
		return RequestSnapshot{}, err
	}
	for i, message := range request.Messages {
		value, decodeErr := canonicalJSON(message)
		if decodeErr != nil {
			return RequestSnapshot{}, decodeErr
		}
		var fields map[string]json.RawMessage
		if decodeErr = json.Unmarshal(value, &fields); decodeErr != nil {
			return RequestSnapshot{}, decodeErr
		}
		for _, field := range []string{"turn_envelope", "root_turn_start", "tool_executions", "deliverable", "outbound_delivery_id"} {
			if _, exists := fields[field]; exists {
				return RequestSnapshot{}, fmt.Errorf("message %d exposes canonical sidecar %s", i, field)
			}
		}
		snapshot.messages = append(snapshot.messages, value)
	}
	return snapshot, snapshot.ValidateToolPairs()
}

func canonicalJSON(data []byte) (json.RawMessage, error) {
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// RequireExtension checks unchanged compatibility dimensions before comparing
// every historical message. It never repairs, strips, or sorts the transcript.
func (previous RequestSnapshot) RequireExtension(next RequestSnapshot) error {
	if previous.model != next.model {
		return fmt.Errorf("model changed")
	}
	// A localhost/third-party OpenAI-compatible endpoint must not receive a
	// MintClaw routing key. Wire captures compare it only when it is emitted;
	// neutral captures above always require the internal runtime lineage.
	if previous.lineage != next.lineage {
		return fmt.Errorf("cache lineage changed")
	}
	if !bytes.Equal(previous.tools, next.tools) {
		return fmt.Errorf("tool schema changed")
	}
	if len(next.messages) <= len(previous.messages) {
		return fmt.Errorf("request did not append a tail")
	}
	for i, message := range previous.messages {
		if !bytes.Equal(message, next.messages[i]) {
			return fmt.Errorf("historical message %d changed", i)
		}
	}
	return next.ValidateToolPairs()
}

// ValidateToolPairs rejects missing, orphaned, duplicate and reordered results.
// Both neutral ToolCall and OpenAI wire ToolCall encode the pairing in id.
func (s RequestSnapshot) ValidateToolPairs() error {
	var pending []string
	seen := make(map[string]bool)
	for i, raw := range s.messages {
		var message struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return err
		}
		if message.Role == "tool" {
			if len(pending) == 0 || pending[0] != message.ToolCallID {
				return fmt.Errorf("message %d has orphaned or reordered tool result", i)
			}
			pending = pending[1:]
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("message %d interrupts unresolved tool calls", i)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				return fmt.Errorf("message %d has empty or duplicate tool call id", i)
			}
			seen[call.ID] = true
			pending = append(pending, call.ID)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("request has unresolved tool calls")
	}
	return nil
}

func (s RequestSnapshot) requireFunctionTool(name string) error {
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(s.tools, &tools); err != nil {
		return fmt.Errorf("decode tool schemas: %w", err)
	}
	for _, tool := range tools {
		if tool.Type == "function" && tool.Function.Name == name {
			return nil
		}
	}
	return fmt.Errorf("required function tool %q is missing", name)
}

// Check runs the same three-request corpus contract for every composition root.
func (c PrefixCorpus) Check(requests ...RequestSnapshot) error {
	if len(requests) != 3 {
		return fmt.Errorf("corpus has %d requests, want 3", len(requests))
	}
	for i, request := range requests {
		if err := request.requireFunctionTool("read_file"); err != nil {
			return fmt.Errorf("corpus request %d: %w", i, err)
		}
	}
	for i := 1; i < len(requests); i++ {
		if err := requests[i-1].RequireExtension(requests[i]); err != nil {
			return fmt.Errorf("corpus continuation %d: %w", i, err)
		}
	}
	for i, marker := range []string{PrefixInitialPrompt, PrefixMarker, PrefixFollowupPrompt} {
		found := false
		for _, message := range requests[i].messages {
			found = found || strings.Contains(string(message), marker)
		}
		if !found {
			return fmt.Errorf("corpus request %d lacks required evidence", i)
		}
	}
	return nil
}
