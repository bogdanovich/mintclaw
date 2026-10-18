package gateway

import (
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type cleanupComputerSource struct{ *nodeComputerClient }

func (*cleanupComputerSource) Available() bool { return true }

func TestComputerCleanupFailureFencesPublicCompletionUntilStatusConfirmsCleanup(t *testing.T) {
	client, handler := computerClientFixture(t, 0)
	principal := computerClientPrincipal()
	opened := openComputerClientFixture(t, client)
	id := opened.Binding.SessionID
	tool := tools.NewComputerSessionTool(tools.NewComputerToolOptions(client.policy), &cleanupComputerSource{client})
	ctx := toolshared.WithRuntimeCapabilities(t.Context(),
		runtimecap.NewContext(runtimecap.Inputs{Kind: principal.Runtime}).BindPrincipal(principal))
	handler.cleanupError = errors.New("PRIVATE_PERSISTENT_NATIVE_CLOSE")
	result := tool.Execute(toolshared.WithToolCallID(ctx, "release"), map[string]any{
		"operation": "release", "computer_session_id": id,
	})
	if result == nil || !result.IsError || strings.Contains(result.ForLLM, "PRIVATE") || result.Deliverable != nil {
		t.Fatal("public release confirmed or exposed an unresolved native cleanup")
	}
	if handler.session.Control != computer.ControlReleased {
		t.Fatal("fixture did not revoke native control before its cleanup failed")
	}
	entry := client.sessions[id]
	for _, request := range []string{"status-one", "status-two", "status-three"} {
		if _, err := client.Status(ctx, principal, id, request); !errors.Is(err, computer.ErrUnavailable) {
			t.Fatal("failed native cleanup became a successful gateway status", err)
		}
		if entry.terminal.Load() || !entry.uncertain || entry.remote.Control != computer.ControlAgent {
			t.Fatal("gateway cached unconfirmed terminal disposition")
		}
	}
	if _, required, err := tool.TurnFinalizationRequirement(ctx); err != nil || !required {
		t.Fatal("finalization discarded an unconfirmed cleanup obligation", err)
	}
	if cleanup, err := tool.CleanupTurnWithResult(ctx); err == nil || len(cleanup.Receipts) != 0 {
		t.Fatal("turn cleanup issued a successful receipt for an unresolved native resource", err)
	}
	if err := client.Shutdown(ctx); err == nil {
		t.Fatal("gateway shutdown discarded unresolved native cleanup")
	}
	handler.cleanupError = nil
	confirmed, err := client.Status(ctx, principal, id, "confirmed-cleanup")
	if err != nil || confirmed.Control != computer.ControlReleased || !entry.terminal.Load() || entry.uncertain {
		t.Fatal("fresh successful status did not reconcile the original release", err)
	}
	if _, required, err := tool.TurnFinalizationRequirement(ctx); err != nil || required {
		t.Fatal("confirmed cleanup still blocked finalization", err)
	}
	if _, err := tool.CleanupTurnWithResult(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	opens, releases := 0, 0
	for _, command := range handler.commands {
		switch command {
		case nodes.ComputerCommandOpen:
			opens++
		case nodes.ComputerCommandRelease:
			releases++
		case nodes.ComputerCommandStatus:
		default:
			t.Fatal("cleanup dispatched an unrelated native action", command)
		}
	}
	if opens != 1 || releases != 1 || handler.observations != 0 {
		t.Fatal("cleanup reconciliation repeated ownership changes or captured pixels")
	}
}
