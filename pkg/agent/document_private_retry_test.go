package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

type documentPrivateRetryTestProvider struct {
	documentAuditTestProvider
	firstErr error
	laterErr error
	requests [][]providers.Message
	private  bool
}

func (provider *documentPrivateRetryTestProvider) Chat(
	ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition,
	model string, options map[string]any,
) (*providers.LLMResponse, error) {
	provider.private = provider.private && protocoltypes.IsSensitiveRequest(ctx) && len(tools) == 0 &&
		options["native_search"] == false && options["temperature"] == 0 &&
		protocoltypes.PromptCachePlanOptionPresent(options)
	provider.requests = append(provider.requests, append([]providers.Message(nil), messages...))
	provider.err = provider.laterErr
	if provider.calls == 0 {
		provider.err = provider.firstErr
	}
	return provider.documentAuditTestProvider.Chat(ctx, messages, tools, model, options)
}

func TestDocumentPrivateModelCallsRetryOnlyStructuredTemporaryFailures(t *testing.T) {
	for _, operation := range []string{"intake", "audit"} {
		t.Run(operation, func(t *testing.T) {
			for _, test := range []struct {
				name  string
				first error
				later error
				calls int
				ok    bool
			}{
				{"transient", &providers.ProviderError{Kind: "transient"}, nil, 2, true},
				{"network", &providers.ProviderError{Kind: "network"}, nil, 2, true},
				{"timeout", &providers.ProviderError{Kind: "timeout"}, nil, 2, true},
				{"persistent", &providers.ProviderError{Kind: "transient"}, &providers.ProviderError{Kind: "transient"}, 2, false},
				{"rate-limit", &providers.ProviderError{Kind: "rate_limit"}, nil, 1, false},
				{"authentication", &providers.ProviderError{Kind: "authentication"}, nil, 1, false},
				{"billing", &providers.ProviderError{Kind: "billing"}, nil, 1, false},
				{"invalid-request", &providers.ProviderError{Kind: "invalid_request"}, nil, 1, false},
				{"unknown", errors.New("PRIVATE_TRANSIENT_LOOKING_ERROR"), nil, 1, false},
				{"long-retry-after", &providers.ProviderError{Kind: "transient", RetryAfter: time.Minute}, nil, 1, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					body := `{"intent":"facts","proposals":[{"field_id":"field_name",` +
						`"literals":[{"text":"Casey Mint","occurrence":1}]}]}`
					if operation == "audit" {
						body = `{"decision":"pass"}`
					}
					provider := &documentPrivateRetryTestProvider{
						documentAuditTestProvider: documentAuditTestProvider{
							response: &providers.LLMResponse{Content: body},
						},
						firstErr: test.first,
						laterErr: test.later,
						private:  true,
					}
					auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
						"configured": {provider: provider, model: "same-model"},
					}}
					var err error
					if operation == "intake" {
						_, err = auditor.InterpretFormIntake(t.Context(), "configured", privateIntakeTestView())
					} else {
						_, err = auditor.AuditForm(t.Context(), "configured", document.FormAuditView{
							PolicyRevision: strings.Repeat("b", 64), SchemaDigest: strings.Repeat("c", 64),
							Fields: []document.FormAuditField{{Schema: document.FormField{
								ID: "field_" + strings.Repeat("a", 64), Kind: document.FormFieldText,
							}}},
						})
					}
					if (err == nil) != test.ok || provider.calls != test.calls || !provider.private ||
						(provider.calls == 2 && !reflect.DeepEqual(provider.requests[0], provider.requests[1])) {
						t.Fatal("private call retry changed its bound request, privacy controls or attempt bound")
					}
					if err != nil && strings.Contains(err.Error(), "PRIVATE_") {
						t.Fatal("temporary provider error escaped the private call")
					}
				})
			}
		})
	}
}

func TestDocumentPrivateModelCallsDoNotRetryRejectedResponses(t *testing.T) {
	for _, response := range []*providers.LLMResponse{
		{Content: "PRIVATE_MALFORMED_RESPONSE"},
		{Content: `{"intent":"facts","decision":"pass"}`, ToolCalls: []providers.ToolCall{{Name: "forbidden"}}},
		nil,
	} {
		for _, operation := range []string{"intake", "audit"} {
			provider := &documentPrivateRetryTestProvider{
				documentAuditTestProvider: documentAuditTestProvider{response: response}, private: true,
			}
			auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
				"configured": {provider: provider, model: "same-model"},
			}}
			var err error
			if operation == "intake" {
				_, err = auditor.InterpretFormIntake(t.Context(), "configured", privateIntakeTestView())
			} else {
				_, err = auditor.AuditForm(t.Context(), "configured", document.FormAuditView{})
			}
			if err == nil || provider.calls != 1 || !provider.private || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("rejected output was retried or exposed")
			}
		}
	}
}

func TestDocumentPrivateModelCallCancellationStopsTransportRetry(t *testing.T) {
	for _, before := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		if before {
			cancel()
		}
		provider := &documentPrivateRetryTestProvider{
			firstErr: &providers.ProviderError{Kind: "transient", RetryAfter: time.Second}, private: true,
		}
		response, err := callDocumentPrivateModel(ctx,
			documentFormAuditModel{provider: provider, model: "same-model"}, "private system", []byte("private data"))
		cancel()
		wantCalls := 1
		if before {
			wantCalls = 0
		}
		if response != nil || err == nil || provider.calls != wantCalls ||
			(!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
			t.Fatal("cancellation continued a private request or retry delay")
		}
	}
}
