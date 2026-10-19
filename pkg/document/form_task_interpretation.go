package document

import "context"

// FormTaskRouteInterpretation is advisory. Native routing owns reservation,
// revision, literal evidence, question consent and eventual handoff authority.
type FormTaskRouteInterpretation struct {
	Action   FormTaskRouteAction `json:"-"`
	Evidence FormIntakeLiteral   `json:"-"`
}

type FormTaskRouter interface {
	InterpretFormTaskRoute(context.Context, string, FormTaskRouteSnapshot) (FormTaskRouteInterpretation, error)
}
