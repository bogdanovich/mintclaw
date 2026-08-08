package providers

import "github.com/bogdanovich/mintclaw/pkg/providers/providererrors"

type (
	ProviderError     = providererrors.ProviderError
	ProviderErrorKind = providererrors.Kind
)

const (
	ProviderErrorUnknown         = providererrors.KindUnknown
	ProviderErrorAuthentication  = providererrors.KindAuthentication
	ProviderErrorBilling         = providererrors.KindBilling
	ProviderErrorRateLimit       = providererrors.KindRateLimit
	ProviderErrorContextOverflow = providererrors.KindContextOverflow
	ProviderErrorTimeout         = providererrors.KindTimeout
	ProviderErrorCanceled        = providererrors.KindCanceled
	ProviderErrorTransient       = providererrors.KindTransient
	ProviderErrorInvalidRequest  = providererrors.KindInvalidRequest
	ProviderErrorNetwork         = providererrors.KindNetwork
)
