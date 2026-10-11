package protocoltypes

import (
	"context"
	"errors"
)

var ErrSensitiveRequestUnsupported = errors.New("provider does not support isolated sensitive requests")

type sensitiveRequestKey struct{}

// WithSensitiveRequest scopes provider diagnostics to trusted metadata only.
// It is internal context, never an API option, and grants no execution or
// retention authority. Callers must still keep errors and responses private.
func WithSensitiveRequest(ctx context.Context) context.Context {
	if IsSensitiveRequest(ctx) {
		return ctx
	}
	return context.WithValue(ctx, sensitiveRequestKey{}, true)
}

func IsSensitiveRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	sensitive, _ := ctx.Value(sensitiveRequestKey{}).(bool)
	return sensitive
}
