package tools

import (
	"context"
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type externalResolverTestTool struct {
	mockRegistryTool
	state toolshared.ExternalWaitState
}

func (tool *externalResolverTestTool) ResolveExternalWait(context.Context, string) toolshared.ExternalWaitState {
	return tool.state
}

func TestExternalWaitResolverUsesRegisteredToolAndBoundsState(t *testing.T) {
	registry := NewToolRegistry()
	tool := &externalResolverTestTool{
		mockRegistryTool: mockRegistryTool{name: "external_hidden"},
		state:            toolshared.ExternalWaitReady,
	}
	registry.RegisterHidden(tool)
	if _, visible := registry.Get(tool.Name()); visible {
		t.Fatal("hidden tool visible")
	}
	if !registry.SupportsExternalWait(tool.Name()) ||
		registry.ResolveExternalWait(t.Context(), tool.Name(), "reference") != toolshared.ExternalWaitReady {
		t.Fatal("hidden lifecycle resolver unavailable")
	}
	tool.state = "invented-value"
	if registry.ResolveExternalWait(t.Context(), tool.Name(), "reference") != toolshared.ExternalWaitUnavailable {
		t.Fatal("unbounded state accepted")
	}
	if registry.ResolveExternalWait(t.Context(), "missing", "reference") != toolshared.ExternalWaitUnavailable {
		t.Fatal("missing resolver ready")
	}
}
