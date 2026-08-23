package capabilities

import "testing"

func TestProviderCapabilitiesNormalizedClearsDependentFields(t *testing.T) {
	capabilities := ProviderCapabilities{
		ImageGeneration: ImageGenerationCapabilities{
			ProviderID:   "hidden",
			DefaultModel: "hidden-model",
			MaxResults:   4,
		},
		ToolSchema: ToolSchemaLimits{MaxDepth: -1},
	}.Normalized()

	if capabilities.ImageGeneration != (ImageGenerationCapabilities{}) {
		t.Fatalf("image generation metadata remained without support: %+v", capabilities.ImageGeneration)
	}
	if capabilities.ToolSchema.MaxDepth != 0 {
		t.Fatalf("tool schema max depth = %d, want 0", capabilities.ToolSchema.MaxDepth)
	}
}
