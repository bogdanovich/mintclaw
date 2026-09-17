package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	agenttools "github.com/bogdanovich/mintclaw/pkg/tools"
)

// ReconcileRecoveredOutboundAdmission delegates domain-tagged outbox work to
// its owning durable state machine immediately before gateway publication.
func (al *AgentLoop) ReconcileRecoveredOutboundAdmission(
	admission outbox.Admission,
	now time.Time,
) (bool, error) {
	if !recoveredDocumentFill(admission.Intent) {
		return al.ReconcileRecoveredInteractionAdmission(admission, now)
	}
	documentTool, err := al.recoveredDocumentTool(admission.Intent)
	if err != nil {
		return false, err
	}
	publish, err := documentTool.ReconcileRecoveredDeliveryAdmission(context.Background(), admission.Intent)
	if err != nil || publish {
		return publish, err
	}
	coordinator := al.outboundCoordinator()
	if coordinator == nil {
		return false, errors.New("outbound coordinator is unavailable")
	}
	if _, err = coordinator.Abandon(admission.Intent.ID, outbox.Outcome{
		Error: "document delivery is already terminal",
	}); err != nil {
		return false, fmt.Errorf("abandon terminal document delivery: %w", err)
	}
	return false, nil
}

// SettleRecoveredOutboundAdmission advances the owning durable operation from
// the exact terminal receipt produced by this restart recovery admission.
func (al *AgentLoop) SettleRecoveredOutboundAdmission(
	ctx context.Context,
	admission outbox.Admission,
) error {
	if !recoveredDocumentFill(admission.Intent) {
		return al.SettleRecoveredInteractionAdmission(ctx, admission)
	}
	coordinator := al.outboundCoordinator()
	if coordinator == nil {
		return errors.New("outbound coordinator is unavailable")
	}
	intent, err := coordinator.AwaitTerminal(ctx, admission)
	if err != nil {
		return fmt.Errorf("await recovered document delivery: %w", err)
	}
	documentTool, err := al.recoveredDocumentTool(intent)
	if err != nil {
		return err
	}
	if err = documentTool.SettleRecoveredDelivery(ctx, intent); err != nil {
		return fmt.Errorf("settle recovered document delivery: %w", err)
	}
	return nil
}

func recoveredDocumentFill(intent outbox.Intent) bool {
	return intent.Media != nil && intent.Media.Recovery != nil &&
		intent.Media.Recovery.Kind == bus.OutboundRecoveryDocumentFill
}

func (al *AgentLoop) recoveredDocumentTool(intent outbox.Intent) (*agenttools.DocumentTool, error) {
	if al == nil || al.registry == nil || !recoveredDocumentFill(intent) {
		return nil, errors.New("document delivery recovery is unavailable")
	}
	wantedWorkspace := filepath.Clean(intent.OwnerWorkspace)
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil || filepath.Clean(agent.Workspace) != wantedWorkspace {
			continue
		}
		registered, ok := agent.Tools.GetRegistered("document")
		if !ok {
			continue
		}
		documentTool, ok := registered.(*agenttools.DocumentTool)
		if ok {
			return documentTool, nil
		}
	}
	return nil, fmt.Errorf("document recovery tool for workspace %q is unavailable", intent.OwnerWorkspace)
}
