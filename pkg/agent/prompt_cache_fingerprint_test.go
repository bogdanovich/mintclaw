package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPromptCacheFingerprintCharacterizesGatewayAndCodingRequests(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
	}
	tools := promptCacheTestTools(false)

	gateway := NewContextBuilder(t.TempDir())
	gatewayFirst := gateway.BuildMessagesFromPrompt(PromptBuildRequest{
		History:           history,
		CurrentMessage:    "gateway first turn",
		Channel:           "telegram",
		ChatID:            "chat-1",
		SenderID:          "sender-a",
		SenderDisplayName: "Alice",
	})
	gatewaySecond := gateway.BuildMessagesFromPrompt(PromptBuildRequest{
		History: appendCompletedPromptCacheTurn(
			history,
			gatewayFirst[len(gatewayFirst)-1],
			"gateway first answer",
		),
		CurrentMessage:    "gateway second turn",
		Channel:           "telegram",
		ChatID:            "chat-1",
		SenderID:          "sender-b",
		SenderDisplayName: "Bob",
	})

	coding := NewContextBuilder(t.TempDir())
	coding.codingPrompt = true
	codingFirst := coding.BuildMessagesFromPrompt(PromptBuildRequest{
		History:        history,
		CurrentMessage: "coding first turn",
		CodingContext: CodingPromptContext{
			SessionKey: "coding:thread-1",
			Model:      "model-a",
			Provider:   "provider-a",
		},
	})
	codingSecond := coding.BuildMessagesFromPrompt(PromptBuildRequest{
		History: appendCompletedPromptCacheTurn(
			history,
			codingFirst[len(codingFirst)-1],
			"coding first answer",
		),
		CurrentMessage: "coding second turn",
		CodingContext: CodingPromptContext{
			SessionKey: "coding:thread-1",
			Model:      "model-b",
			Provider:   "provider-a",
		},
	})

	settings := traceCaptureSettings{}
	gatewayFirstFingerprint := fingerprintPromptCacheRequest(settings, gatewayFirst, tools)
	gatewaySecondFingerprint := fingerprintPromptCacheRequest(settings, gatewaySecond, tools)
	codingFirstFingerprint := fingerprintPromptCacheRequest(settings, codingFirst, tools)
	codingSecondFingerprint := fingerprintPromptCacheRequest(settings, codingSecond, tools)

	for name, fingerprint := range map[string]PromptCacheFingerprint{
		"gateway": gatewayFirstFingerprint,
		"coding":  codingFirstFingerprint,
	} {
		if fingerprint.StableSystemParts == 0 || fingerprint.DynamicSystemParts == 0 {
			t.Fatalf("%s system segmentation = %+v, want stable prefix and dynamic suffix", name, fingerprint)
		}
		if fingerprint.HistoryMessages != len(history) || fingerprint.DynamicTailMessages != 1 {
			t.Fatalf("%s transcript segmentation = %+v", name, fingerprint)
		}
		if !fingerprint.TailBoundaryFound || !fingerprint.DynamicSystemBeforeTranscript {
			t.Fatalf("%s cache boundary characterization = %+v", name, fingerprint)
		}
	}

	assertPromptCacheDynamicSystemBreak(
		t,
		"gateway",
		gatewayFirstFingerprint,
		gatewaySecondFingerprint,
		gatewayFirst,
		gatewaySecond,
		tools,
	)
	assertPromptCacheDynamicSystemBreak(
		t,
		"coding",
		codingFirstFingerprint,
		codingSecondFingerprint,
		codingFirst,
		codingSecond,
		tools,
	)
}

func TestPromptCacheRequestPrefixAcceptsAppendOnlyTranscript(t *testing.T) {
	system := providers.Message{
		Role:    "system",
		Content: "stable system",
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "stable system",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
	}
	firstUser := userPromptMessage("first", nil)
	first := []providers.Message{system, firstUser}
	next := []providers.Message{
		system,
		firstUser,
		{Role: "assistant", Content: "answer"},
		userPromptMessage("second", nil),
	}
	tools := promptCacheTestTools(false)

	if !promptCacheRequestIsPrefix(first, tools, next, tools) {
		t.Fatal("append-only request was not recognized as a reusable prefix")
	}
}

func TestPromptCacheFingerprintCanonicalizesSchemaMapsAndContainsNoPromptText(t *testing.T) {
	messages := []providers.Message{
		{
			Role:    "system",
			Content: "private-system-canary",
			SystemParts: []providers.ContentBlock{{
				Type:         "text",
				Text:         "private-system-canary",
				CacheControl: &providers.CacheControl{Type: "ephemeral"},
			}},
		},
		userPromptMessage("private-user-canary", nil),
	}
	left := fingerprintPromptCacheRequest(traceCaptureSettings{}, messages, promptCacheTestTools(false))
	right := fingerprintPromptCacheRequest(traceCaptureSettings{}, messages, promptCacheTestTools(true))

	if left.ToolSchemaHash != right.ToolSchemaHash {
		t.Fatalf(
			"equivalent schema maps produced different hashes: %q != %q",
			left.ToolSchemaHash,
			right.ToolSchemaHash,
		)
	}
	encoded, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-system-canary") ||
		strings.Contains(string(encoded), "private-user-canary") {
		t.Fatalf("fingerprint leaked prompt text: %s", encoded)
	}
}

func TestPromptCacheFingerprintStopsStablePrefixAtFirstDynamicSystemPart(t *testing.T) {
	messages := []providers.Message{{
		Role:    "system",
		Content: "stable then dynamic then nominally stable",
		SystemParts: []providers.ContentBlock{
			{Type: "text", Text: "stable-1", CacheControl: &providers.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "dynamic"},
			{Type: "text", Text: "stable-2", CacheControl: &providers.CacheControl{Type: "ephemeral"}},
		},
	}}

	fingerprint := fingerprintPromptCacheRequest(traceCaptureSettings{}, messages, nil)
	if fingerprint.StableSystemParts != 1 || fingerprint.DynamicSystemParts != 2 {
		t.Fatalf("system segmentation = %+v, want 1 stable and 2 dynamic parts", fingerprint)
	}
}

func assertPromptCacheDynamicSystemBreak(
	t *testing.T,
	name string,
	first PromptCacheFingerprint,
	second PromptCacheFingerprint,
	firstMessages []providers.Message,
	secondMessages []providers.Message,
	tools []providers.ToolDefinition,
) {
	t.Helper()
	if first.StableSystemHash != second.StableSystemHash {
		t.Fatalf("%s stable system changed: %q != %q", name, first.StableSystemHash, second.StableSystemHash)
	}
	if first.ToolSchemaHash != second.ToolSchemaHash {
		t.Fatalf("%s tool schema changed: %q != %q", name, first.ToolSchemaHash, second.ToolSchemaHash)
	}
	if first.DynamicSystemHash == second.DynamicSystemHash {
		t.Fatalf("%s dynamic system hash did not change", name)
	}
	if promptCacheRequestIsPrefix(firstMessages, tools, secondMessages, tools) {
		t.Fatalf("%s unexpectedly preserved the full request prefix", name)
	}
}

func appendCompletedPromptCacheTurn(
	history []providers.Message,
	user providers.Message,
	answer string,
) []providers.Message {
	completed := append([]providers.Message(nil), history...)
	completed = append(completed, promptCacheProviderVisibleMessage(user))
	completed = append(completed, providers.Message{Role: "assistant", Content: answer})
	return completed
}

func promptCacheTestTools(reverseMapInsertion bool) []providers.ToolDefinition {
	properties := make(map[string]any, 2)
	if reverseMapInsertion {
		properties["second"] = map[string]any{"type": "integer"}
		properties["first"] = map[string]any{"type": "string"}
	} else {
		properties["first"] = map[string]any{"type": "string"}
		properties["second"] = map[string]any{"type": "integer"}
	}
	return []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "inspect",
			Description: "Inspect a value",
			Parameters: map[string]any{
				"type":       "object",
				"properties": properties,
			},
		},
	}}
}
