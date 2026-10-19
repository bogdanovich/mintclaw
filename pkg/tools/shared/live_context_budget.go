package toolshared

import "context"

type liveContextAllowanceKey struct{}

// WithToolLiveContextTextAllowance is trusted per-call runtime metadata, not a
// model argument. A protected handoff must not be consumed and then truncated.
func WithToolLiveContextTextAllowance(ctx context.Context, remaining int) context.Context {
	return context.WithValue(ctx, liveContextAllowanceKey{}, max(0, remaining))
}

func ToolLiveContextTextAllowance(ctx context.Context) int {
	if remaining, ok := ctx.Value(liveContextAllowanceKey{}).(int); ok {
		return remaining
	}
	return MaxLiveContextTextBytes
}
