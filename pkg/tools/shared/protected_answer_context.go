package toolshared

import "context"

type protectedAnswerContinuationKey struct{}

// WithToolProtectedAnswerContinuation is set by the runtime only for the exact
// trusted tool call derived from an accepted native protected-answer receipt.
// Model-authored arguments do not establish this context.
func WithToolProtectedAnswerContinuation(ctx context.Context, reference string) context.Context {
	return context.WithValue(ctx, protectedAnswerContinuationKey{}, reference)
}

func ToolProtectedAnswerContinuation(ctx context.Context) string {
	reference, _ := ctx.Value(protectedAnswerContinuationKey{}).(string)
	return reference
}
