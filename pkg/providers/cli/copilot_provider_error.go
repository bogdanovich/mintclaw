package cliprovider

import (
	"strings"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func normalizeCopilotError(err error, event *copilot.SessionEvent) error {
	if err == nil {
		return nil
	}
	if event == nil {
		return normalizeCLIError(err, err.Error())
	}

	status := 0
	if event.Data.StatusCode != nil {
		status = int(*event.Data.StatusCode)
	}
	requestID := ""
	if event.Data.ProviderCallID != nil {
		requestID = *event.Data.ProviderCallID
	}
	diagnostic := ""
	if event.Data.Message != nil {
		diagnostic = *event.Data.Message
	}
	kind := providererrors.KindUnknown
	if event.Data.ErrorType != nil {
		kind = copilotErrorTypeKind(*event.Data.ErrorType)
	}
	normalized := providererrors.FromStructuredError(
		kind,
		status,
		nil,
		requestID,
		cliSafeMessage(kind),
		err,
	)
	if normalized.Kind != providererrors.KindUnknown {
		return normalized
	}
	if transport, ok := providererrors.FromTransportError(err); ok {
		return transport.WithRequestID(requestID)
	}
	compatibilityKind := classifyCLICompatibilityText(diagnostic)
	return providererrors.FromStructuredError(
		compatibilityKind,
		status,
		nil,
		requestID,
		cliSafeMessage(compatibilityKind),
		err,
	)
}

func copilotErrorTypeKind(errorType string) providererrors.Kind {
	errorType = strings.ToLower(strings.TrimSpace(errorType))
	errorType = strings.NewReplacer("-", "_", " ", "_").Replace(errorType)
	switch errorType {
	case "authentication", "authorization":
		return providererrors.KindAuthentication
	case "billing", "payment_required", "quota", "subscription":
		return providererrors.KindBilling
	case "rate_limit":
		return providererrors.KindRateLimit
	case "context_length", "context_overflow", "context_window":
		return providererrors.KindContextOverflow
	case "deadline_exceeded", "timeout":
		return providererrors.KindTimeout
	case "canceled", "cancelled": //nolint:misspell // Copilot events may use either spelling.
		return providererrors.KindCanceled
	case "internal", "server", "service_unavailable", "transient":
		return providererrors.KindTransient
	case "invalid_request", "query":
		return providererrors.KindInvalidRequest
	default:
		return providererrors.KindUnknown
	}
}
