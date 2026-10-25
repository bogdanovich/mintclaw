package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicmessages "github.com/bogdanovich/mintclaw/pkg/providers/anthropic_messages"
)

func TestOutputTokenLimitCapabilityMatchesWire(t *testing.T) {
	for _, test := range []struct {
		name, model, field string
		newProvider        func(string) LLMProvider
	}{
		{"openai automatic", "fixture", "max_tokens", func(url string) LLMProvider {
			return NewHTTPProvider("synthetic", url, "")
		}},
		{"openai automatic reasoning", "gpt-5-fixture", "max_completion_tokens", func(url string) LLMProvider {
			return NewHTTPProvider("synthetic", url, "")
		}},
		{"openai explicit", "fixture", "max_completion_tokens", func(url string) LLMProvider {
			return NewHTTPProviderWithMaxTokensField("synthetic", url, "", "max_completion_tokens")
		}},
		{"anthropic SDK", "claude-fixture", "max_tokens", func(url string) LLMProvider {
			return NewClaudeProviderWithBaseURL("synthetic", url)
		}},
		{"anthropic messages", "claude-fixture", "max_tokens", func(url string) LLMProvider {
			return anthropicmessages.NewProvider("synthetic", url, "")
		}},
		{"gemini", "gemini-fixture", "maxOutputTokens", func(url string) LLMProvider {
			return NewGeminiProvider("synthetic", url, "", "", 1, nil, nil)
		}},
		{"schema wrapper", "fixture", "max_tokens", func(url string) LLMProvider {
			provider, err := wrapProviderWithToolSchemaTransform(NewHTTPProvider("synthetic", url, ""), "simple")
			if err != nil {
				t.Fatal(err)
			}
			return provider
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				name := "chat"
				if streaming {
					name = "stream"
				}
				t.Run(name, func(t *testing.T) {
					captured := make(chan map[string]any, 2)
					server := httptest.NewServer(
						http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
							var body map[string]any
							if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<10)).
								Decode(&body); err != nil {
								t.Error(err)
							}
							select {
							case captured <- body:
							default:
								t.Error("unexpected adapter retry")
							}
							writer.Header().Set("Content-Type", "application/json")
							writer.WriteHeader(http.StatusBadRequest)
							_, _ = writer.Write(
								[]byte(
									`{"type":"error","error":{"type":"invalid_request_error","message":"synthetic"}}`,
								),
							)
						}),
					)
					defer server.Close()
					provider := test.newProvider(server.URL)
					if !Capabilities(provider).OutputTokenLimit {
						t.Fatal("verified adapter omitted capability")
					}
					if streaming && !Capabilities(provider).Streaming {
						return
					}
					messages := []Message{{Role: "user", Content: "synthetic"}}
					options := map[string]any{"max_tokens": 123, "native_search": false}
					var err error
					if streaming {
						_, attempted, callErr := ChatStreamEvents(
							t.Context(),
							provider,
							messages,
							nil,
							test.model,
							options,
							nil,
						)
						if !attempted {
							t.Fatal("declared streaming was not attempted")
						}
						err = callErr
					} else {
						_, err = provider.Chat(t.Context(), messages, nil, test.model, options)
					}
					if err == nil {
						t.Fatal("synthetic HTTP refusal was not returned")
					}
					select {
					case body := <-captured:
						if test.field == "maxOutputTokens" {
							body, _ = body["generationConfig"].(map[string]any)
						}
						if body[test.field] != float64(123) {
							t.Fatalf("wire output limit = %#v, want 123 in %s", body, test.field)
						}
					default:
						t.Fatal("adapter submitted no request")
					}
				})
			}
		})
	}
}

func TestOutputTokenLimitCapabilityIsConservative(t *testing.T) {
	for _, provider := range []LLMProvider{
		nil, (*GeminiProvider)(nil), &descriptorTestProvider{}, NewCodexProvider("synthetic", "account"),
		NewCodexCliProvider("."), NewClaudeCliProvider("."),
		NewHTTPProviderWithMaxTokensField("synthetic", "https://example.invalid", "", "custom_ceiling"),
		NewHTTPProviderWithMaxTokensFieldAndRequestTimeout("synthetic", "https://example.invalid", "", "", "", 1,
			map[string]any{"max_tokens": nil}, nil),
		NewGeminiProvider("synthetic", "https://example.invalid", "", "", 1,
			map[string]any{"generationConfig": map[string]any{}}, nil),
	} {
		if Capabilities(provider).OutputTokenLimit {
			t.Fatalf("unsupported adapter %T advertised an output ceiling", provider)
		}
		if provider != nil {
			wrapped, err := wrapProviderWithToolSchemaTransform(provider, "simple")
			if err != nil {
				t.Fatal(err)
			}
			if Capabilities(wrapped).OutputTokenLimit {
				t.Fatalf("schema wrapper invented output ceiling for %T", provider)
			}
		}
	}
}
