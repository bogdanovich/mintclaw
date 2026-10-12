package toolshared

import "context"

type documentIntakeProcessingKey struct{}

type documentIntakeProcessingIdentity struct {
	reference string
	identity  string
}

// WithToolDocumentIntakeProcessingIdentity binds a trusted ingress attempt to
// its receipt without changing execution, lifecycle or approval identity.
func WithToolDocumentIntakeProcessingIdentity(ctx context.Context, reference, identity string) context.Context {
	return context.WithValue(ctx, documentIntakeProcessingKey{}, documentIntakeProcessingIdentity{
		reference: reference, identity: identity,
	})
}

func ToolDocumentIntakeProcessingIdentity(ctx context.Context, reference string) string {
	bound, _ := ctx.Value(documentIntakeProcessingKey{}).(documentIntakeProcessingIdentity)
	if reference == "" || bound.reference != reference {
		return ""
	}
	return bound.identity
}
