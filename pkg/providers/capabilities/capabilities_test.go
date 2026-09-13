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

func TestProviderCapabilitiesNormalizedClearsEditLimitWithoutEditing(t *testing.T) {
	capabilities := ProviderCapabilities{
		ImageGeneration: ImageGenerationCapabilities{
			Supported:      true,
			MaxInputImages: 4,
			MaxInputBytes:  1024,
		},
	}.Normalized()

	if capabilities.ImageGeneration.MaxInputImages != 0 {
		t.Fatalf("image edit input limit = %d, want 0", capabilities.ImageGeneration.MaxInputImages)
	}
	if capabilities.ImageGeneration.MaxInputBytes != 0 {
		t.Fatalf("image edit byte limit = %d, want 0", capabilities.ImageGeneration.MaxInputBytes)
	}
}
