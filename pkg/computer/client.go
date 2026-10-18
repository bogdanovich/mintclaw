package computer

// NamedTarget connects a gateway-owned model-visible alias to metadata from an
// admitted native profile. Native scope is evidence, not an open argument.
type NamedTarget struct {
	Alias  string `json:"alias"`
	Target Target `json:"target"`
}
