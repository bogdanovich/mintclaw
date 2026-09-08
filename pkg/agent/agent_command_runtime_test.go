package agent

import (
	"context"
	"testing"
)

func TestBuildCommandsRuntimeCapturesMCPAvailability(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	t.Cleanup(cleanup)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	build := func() bool {
		runtime := al.buildCommandsRuntime(
			context.Background(),
			effectiveModelBinding{WorkspaceAgent: agent},
			&turnSpec{},
		)
		return runtime.MCPIntegrationEnabled
	}

	if build() {
		t.Fatal("MCP availability = true while integration is disabled")
	}
	cfg.Tools.MCP.Enabled = true
	if !build() {
		t.Fatal("MCP availability = false while integration is enabled")
	}
}
