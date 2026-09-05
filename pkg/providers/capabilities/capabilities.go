// Package capabilities defines provider feature metadata without importing a
// provider implementation or protocol type.
package capabilities

// ProviderCapabilities is the authoritative declaration of the optional
// behavior and limits exposed by one provider instance.
type ProviderCapabilities struct {
	Streaming    bool
	Thinking     bool
	NativeSearch bool
	// CallerMediatedTools declares support for a request-scoped mode in which
	// Chat can only request the supplied tools and cannot autonomously act on the
	// caller's host or external systems. Callers enforcing that boundary must use
	// providers.CallerMediatedToolsOptions. False is the conservative value for
	// agentic, configurable tool-override, and unknown transports.
	CallerMediatedTools bool
	ImageGeneration     ImageGenerationCapabilities
	ToolSchema          ToolSchemaLimits
}

// ImageGenerationCapabilities describes provider-owned image generation behavior.
type ImageGenerationCapabilities struct {
	Supported    bool
	ProviderID   string
	DefaultModel string
	MaxResults   int
}

// ToolSchemaLimits describes the effective schema contract applied before a
// request reaches the provider. Zero values mean no MintClaw-imposed limit.
type ToolSchemaLimits struct {
	Transform string
	MaxDepth  int
}

const ToolSchemaTransformSimple = "simple"

// Normalized returns a conservative descriptor with internally consistent flags.
func (c ProviderCapabilities) Normalized() ProviderCapabilities {
	if !c.ImageGeneration.Supported {
		c.ImageGeneration = ImageGenerationCapabilities{}
	}
	if c.ToolSchema.MaxDepth < 0 {
		c.ToolSchema.MaxDepth = 0
	}
	return c
}
