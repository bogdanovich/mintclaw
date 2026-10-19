package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestDocumentIntakeFailureCategoriesNeverRetainPrivateProviderDetails(t *testing.T) {
	for _, test := range []struct {
		kind providererrors.Kind
		want error
	}{
		{"authentication", document.ErrFormIntakeProviderAuth},
		{"billing", document.ErrFormIntakeProviderBilling},
		{"invalid_request", document.ErrFormIntakeProviderRequest},
		{"rate_limit", document.ErrFormIntakeProviderLimited},
		{"transient", document.ErrFormIntakeProviderTemp},
		{"network", document.ErrFormIntakeProviderTemp},
		{"timeout", document.ErrFormIntakeProviderTemp},
		{"unknown", document.ErrFormAuditUnavailable},
	} {
		provider := &documentAuditTestProvider{err: &providers.ProviderError{
			Kind: test.kind, SafeMessage: "PRIVATE_PROVIDER_SENTINEL",
		}}
		auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
			"configured": {provider: provider, model: "configured-model"},
		}}
		_, err := auditor.InterpretFormIntake(t.Context(), "configured", privateIntakeTestView())
		if !errors.Is(err, test.want) || !errors.Is(err, document.ErrFormAuditUnavailable) ||
			!errors.Is(err, errDocumentIntakeInterpretationUnavailable) ||
			strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("private provider failure lost its closed category or disclosed its detail")
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		got := documentIntakeProviderFailure(fmt.Errorf("PRIVATE_WRAPPED_CONTEXT: %w", cause))
		if !errors.Is(got, cause) || strings.Contains(got.Error(), "PRIVATE_") {
			t.Fatal("private cancellation detail escaped classification")
		}
	}
}

func TestDocumentIntakeSeparatesModelViewAndResponseFailures(t *testing.T) {
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: "PRIVATE_INVALID_RESPONSE"}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "configured-model"},
	}}
	_, err := auditor.InterpretFormIntake(t.Context(), "missing", privateIntakeTestView())
	if !errors.Is(err, document.ErrFormIntakeModelMissing) || provider.calls != 0 {
		t.Fatal("missing alias was not distinguished without a provider call")
	}
	view := privateIntakeTestView()
	view.Pass.Input.Text = ""
	_, err = auditor.InterpretFormIntake(t.Context(), "configured", view)
	if !errors.Is(err, document.ErrFormIntakeInvalidContext) || provider.calls != 0 {
		t.Fatal("invalid private context was not distinguished before the provider")
	}
	_, err = auditor.InterpretFormIntake(t.Context(), "configured", privateIntakeTestView())
	if !errors.Is(err, document.ErrFormIntakeInvalidResponse) || provider.calls != 1 ||
		strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("invalid response was not distinguished without exposing its content")
	}
}

func TestDocumentIntakeClassifiesSecondPrivateBooleanVerificationFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  error
		response *providers.LLMResponse
		want     error
		calls    int
	}{
		{name: "authentication", failure: &providers.ProviderError{
			Kind: "authentication", SafeMessage: "PRIVATE_VERIFICATION_ERROR",
		}, want: document.ErrFormIntakeProviderAuth},
		{name: "billing", failure: &providers.ProviderError{
			Kind: "billing", SafeMessage: "PRIVATE_VERIFICATION_ERROR",
		}, want: document.ErrFormIntakeProviderBilling},
		{name: "invalid request", failure: &providers.ProviderError{
			Kind: "invalid_request", SafeMessage: "PRIVATE_VERIFICATION_ERROR",
		}, want: document.ErrFormIntakeProviderRequest},
		{name: "limited", failure: &providers.ProviderError{
			Kind: "rate_limit", SafeMessage: "PRIVATE_VERIFICATION_ERROR",
		}, want: document.ErrFormIntakeProviderLimited},
		{name: "temporary", failure: &providers.ProviderError{
			Kind: "transient", SafeMessage: "PRIVATE_VERIFICATION_ERROR",
		}, want: document.ErrFormIntakeProviderTemp, calls: 3},
		{
			name: "canceled", failure: fmt.Errorf("PRIVATE_VERIFICATION_ERROR: %w", context.Canceled),
			want: context.Canceled,
		},
		{
			name: "deadline", failure: fmt.Errorf("PRIVATE_VERIFICATION_ERROR: %w", context.DeadlineExceeded),
			want: context.DeadlineExceeded,
		},
		{name: "missing response", want: document.ErrFormIntakeInvalidResponse},
		{
			name: "malformed response", response: &providers.LLMResponse{Content: "PRIVATE_INVALID_VERIFICATION"},
			want: document.ErrFormIntakeInvalidResponse,
		},
		{name: "unexpected tool", response: &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{Name: "unexpected"}},
		}, want: document.ErrFormIntakeInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantCalls := test.calls
			if wantCalls == 0 {
				wantCalls = 2
			}
			provider := &documentBooleanVerificationTestProvider{
				private:   true,
				verifyErr: test.failure,
				responses: []*providers.LLMResponse{
					{Content: booleanAssertionTestResponse},
					test.response,
					test.response,
				},
			}
			auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
				"configured": {provider: provider, model: "configured-model"},
			}}
			result, err := auditor.InterpretFormIntake(t.Context(), "configured", booleanAssertionTestView())
			if provider.calls != wantCalls || !provider.private || len(result.Proposals) != 0 ||
				!errors.Is(err, test.want) || !errors.Is(err, document.ErrFormAuditUnavailable) ||
				!errors.Is(
					err,
					errDocumentIntakeInterpretationUnavailable,
				) || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("second private request lost its closed cause, returned proposals or disclosed private details")
			}
		})
	}
}
