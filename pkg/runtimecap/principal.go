package runtimecap

import "github.com/bogdanovich/mintclaw/pkg/runtimecap/identity"

type Kind = identity.Kind

const (
	KindGateway = identity.KindGateway
	KindCoding  = identity.KindCoding
)

// Principal remains an alias so existing runtime consumers share the identical
// validated value and wire contract with slim protocol consumers.
type Principal = identity.Principal
