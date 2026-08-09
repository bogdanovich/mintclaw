package cliprovider

import (
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func normalizeCLIError(err error, diagnostic string) error {
	if err == nil {
		return nil
	}
	var providerErr *providererrors.ProviderError
	if errors.As(err, &providerErr) && providerErr != nil {
		return providerErr
	}
	cause := withCLIDiagnostic(err, diagnostic)
	if normalized, ok := providererrors.FromTransportError(cause); ok {
		return normalized
	}
	kind := classifyCLICompatibilityText(diagnostic)
	return providererrors.FromStructuredError(
		kind,
		0,
		nil,
		"",
		cliSafeMessage(kind),
		cause,
	)
}

func normalizeCodedCLIError(code, diagnostic string, cause error) error {
	kind := cliErrorCodeKind(code)
	if kind == providererrors.KindUnknown {
		return normalizeCLIError(cause, diagnostic)
	}
	return providererrors.FromStructuredError(
		kind,
		0,
		nil,
		"",
		cliSafeMessage(kind),
		withCLIDiagnostic(cause, diagnostic),
	)
}

func cliErrorCodeKind(code string) providererrors.Kind {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(code)
	switch code {
	case "authentication_error", "invalid_api_key", "unauthorized", "token_expired":
		return providererrors.KindAuthentication
	case "billing_error", "insufficient_quota", "payment_required", "credits_exhausted":
		return providererrors.KindBilling
	case "rate_limit_error", "rate_limit_exceeded", "too_many_requests", "overloaded_error":
		return providererrors.KindRateLimit
	case "context_length_exceeded", "context_window_exceeded", "prompt_too_long":
		return providererrors.KindContextOverflow
	case "deadline_exceeded", "request_timeout", "timeout":
		return providererrors.KindTimeout
	case "canceled", "cancelled": //nolint:misspell // External CLI identifiers use both spellings.
		return providererrors.KindCanceled
	case "internal_error", "server_error", "service_unavailable":
		return providererrors.KindTransient
	case "invalid_request", "invalid_argument":
		return providererrors.KindInvalidRequest
	default:
		return providererrors.KindUnknown
	}
}

// classifyCLICompatibilityText is intentionally limited to adapters whose
// subprocess protocol exposes no machine-readable failure category.
func classifyCLICompatibilityText(diagnostic string) providererrors.Kind {
	diagnostic = strings.ToLower(diagnostic)
	patterns := []struct {
		kind    providererrors.Kind
		markers []string
	}{
		{
			providererrors.KindAuthentication,
			[]string{
				"authentication failed",
				"invalid api key",
				"login required",
				"not logged in",
				"token expired",
				"unauthorized",
			},
		},
		{providererrors.KindBilling, []string{
			"billing error", "credit balance", "credits exhausted", "insufficient quota", "payment required",
		}},
		{providererrors.KindContextOverflow, []string{
			"context length", "context window", "input is too long", "prompt is too long", "too many input tokens",
		}},
		{providererrors.KindRateLimit, []string{
			"overloaded", "rate limit", "too many requests",
		}},
		{providererrors.KindTimeout, []string{
			"deadline exceeded", "request timed out", "request timeout", "timed out",
		}},
		{providererrors.KindCanceled, []string{
			"request canceled",
			"request cancelled", //nolint:misspell // External CLIs may use the British spelling.
			"operation canceled",
			"operation cancelled", //nolint:misspell // External CLIs may use the British spelling.
		}},
		{
			providererrors.KindTransient,
			[]string{
				"bad gateway",
				"connection reset",
				"internal server error",
				"service unavailable",
				"temporarily unavailable",
			},
		},
	}
	for _, pattern := range patterns {
		for _, marker := range pattern.markers {
			if strings.Contains(diagnostic, marker) {
				return pattern.kind
			}
		}
	}
	return providererrors.KindUnknown
}

func withCLIDiagnostic(err error, diagnostic string) error {
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	if diagnostic == "" {
		return err
	}
	const maxDiagnosticRunes = 4096
	runes := []rune(diagnostic)
	if len(runes) > maxDiagnosticRunes {
		diagnostic = string(runes[:maxDiagnosticRunes]) + "..."
	}
	return &cliDiagnosticError{diagnostic: diagnostic, cause: err}
}

type cliDiagnosticError struct {
	diagnostic string
	cause      error
}

func (e *cliDiagnosticError) Error() string { return e.diagnostic }
func (e *cliDiagnosticError) Unwrap() error { return e.cause }

func cliSafeMessage(kind providererrors.Kind) string {
	switch kind.Canonical() {
	case providererrors.KindAuthentication:
		return "CLI provider authentication failed"
	case providererrors.KindBilling:
		return "CLI provider credits are unavailable"
	case providererrors.KindRateLimit:
		return "CLI provider rate limit exceeded"
	case providererrors.KindContextOverflow:
		return "CLI provider context limit exceeded"
	case providererrors.KindTimeout:
		return "CLI provider request timed out"
	case providererrors.KindCanceled:
		return "CLI provider request canceled"
	case providererrors.KindTransient:
		return "CLI provider is temporarily unavailable"
	case providererrors.KindInvalidRequest:
		return "CLI provider rejected the request"
	default:
		return "CLI provider request failed"
	}
}
