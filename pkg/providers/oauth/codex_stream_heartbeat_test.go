package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestCodexProviderIgnoresUnnamedSSEHeartbeats(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming_%t", streaming), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/responses" ||
					r.Header.Get("Authorization") != "Bearer test-token" ||
					r.Header.Get("Chatgpt-Account-Id") != "test-account" {
					t.Error("stream request lost its endpoint or authentication")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["stream"] != true || body["store"] != false {
					t.Error("stream request lost its request options")
				}
				if _, present := body["prompt_cache_key"]; present {
					t.Error("sensitive request enabled caching")
				}
				if _, present := body["tools"]; present {
					t.Error("tools-disabled request acquired tools")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, ": keep-alive\n\n\n\ndata: \n\n")
				writeSSEEvent(t, w, "response.output_text.delta", map[string]any{
					"type": "response.output_text.delta", "sequence_number": 1, "delta": "Hello",
				})
				_, _ = fmt.Fprint(w, ": keep-alive\r\n\r\n")
				writeCompletedSSE(w, map[string]any{
					"id": "resp_heartbeat", "object": "response", "status": "completed",
					"output": []map[string]any{{
						"id": "msg_heartbeat", "type": "message", "role": "assistant", "status": "completed",
						"content": []map[string]any{{"type": "output_text", "text": "Hello"}},
					}},
					"usage": map[string]any{"input_tokens": 3, "output_tokens": 1, "total_tokens": 4},
				})
			}))
			defer server.Close()
			provider := NewCodexProvider("test-token", "test-account")
			provider.client = createOpenAITestClient(server.URL, "test-token", "test-account")
			ctx := protocoltypes.WithSensitiveRequest(t.Context())
			options := map[string]any{"native_search": false}
			protocoltypes.DisablePromptCache(options)
			var response *LLMResponse
			var err error
			var updates []StreamChunk
			messages := []Message{{Role: "user", Content: "Hello"}}
			if streaming {
				response, err = provider.ChatStreamEvents(ctx, messages, nil, "gpt-6.1-sol", options,
					func(chunk StreamChunk) { updates = append(updates, chunk) })
			} else {
				response, err = provider.Chat(ctx, messages, nil, "gpt-6.1-sol", options)
			}
			if err != nil {
				t.Fatalf("valid stream with heartbeats failed: %v", err)
			}
			if response == nil || response.Content != "Hello" || response.Usage == nil ||
				response.Usage.TotalTokens != 4 {
				t.Fatal("stream lost its completed response or usage")
			}
			if requests.Load() != 1 {
				t.Fatal("heartbeat handling repeated the request")
			}
			if streaming && (len(updates) != 1 || updates[0].Content != "Hello") {
				t.Fatal("heartbeat handling changed streaming callbacks")
			}
		})
	}
}

func TestCodexProviderHeartbeatHandlingPreservesMalformedEvents(t *testing.T) {
	for _, body := range []string{"data: {]\n\n", "event: response.created\ndata: \n\n"} {
		t.Run(fmt.Sprintf("bytes_%d", len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, ": heartbeat\n\n", body)
			}))
			defer server.Close()
			provider := NewCodexProvider("test-token", "test-account")
			provider.client = createOpenAITestClient(server.URL, "test-token", "test-account")
			response, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hello"}}, nil,
				"gpt-6.1-sol", map[string]any{"native_search": false})
			var syntax *json.SyntaxError
			if response != nil || !errors.As(err, &syntax) {
				t.Fatal("malformed event was accepted or suppressed")
			}
		})
	}
}

func TestCodexProviderHeartbeatOnlyEOFIsNotSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, ": heartbeat\n\n\n")
	}))
	defer server.Close()
	provider := NewCodexProvider("test-token", "test-account")
	provider.client = createOpenAITestClient(server.URL, "test-token", "test-account")
	response, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hello"}}, nil,
		"gpt-6.1-sol", map[string]any{"native_search": false})
	var typed *providererrors.ProviderError
	var syntax *json.SyntaxError
	if response != nil || !errors.As(err, &typed) || typed.Kind != providererrors.KindTransient ||
		errors.As(err, &syntax) {
		t.Fatal("heartbeat-only EOF did not retain the incomplete-stream fence")
	}
}

func TestCodexProviderHeartbeatWaitRemainsCancellable(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	provider := NewCodexProvider("test-token", "test-account")
	provider.client = createOpenAITestClient(server.URL, "test-token", "test-account")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := provider.Chat(ctx, []Message{{Role: "user", Content: "Hello"}}, nil,
			"gpt-6.1-sol", map[string]any{"native_search": false})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("heartbeat wait lost request cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close after cancellation")
	}
}
