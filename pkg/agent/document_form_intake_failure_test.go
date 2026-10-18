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
