package toolshared

import "context"

// ExternalWaitState is bounded, value-free readiness from a trusted tool.
type ExternalWaitState string

const (
	ExternalWaitPending     ExternalWaitState = "pending"
	ExternalWaitReady       ExternalWaitState = "ready"
	ExternalWaitUnavailable ExternalWaitState = "unavailable"
)

// ExternalWaitResolver owns an external condition. Implementations must check
// the original authenticated tool context; a persisted reference is neither a
// capability nor proof of approval. Resolving must not perform the paused action.
type ExternalWaitResolver interface {
	ResolveExternalWait(context.Context, string) ExternalWaitState
}
