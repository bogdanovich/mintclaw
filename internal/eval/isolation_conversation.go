package eval

import (
	"context"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type isolationConversation struct {
	*steeringConversation
	peerItem LoadedCase
	roles    conversationRoles
	options  gatewayclient.Options
	proof    *IsolationEvidence
}

func newIsolationConversation(
	item LoadedCase, target admittedTarget, session string,
	roles conversationRoles, options gatewayclient.Options,
) (*isolationConversation, error) {
	if item.Case.Steering == nil || item.Case.Steering.Peer == nil || target.preset.ActiveTool == nil ||
		target.preset.ActiveTool.MaxParallelTurns != 2 {
		return nil, errNativeEvidence
	}
	base, err := newSteeringConversation(item.Case, target, session)
	if err != nil {
		return nil, err
	}
	peerItem := item
	peerItem.Case = isolationPeerCase(item.Case)
	if peerItem.Case.Validate() != nil {
		return nil, errNativeEvidence
	}
	return &isolationConversation{
		steeringConversation: base, peerItem: peerItem, roles: roles, options: options,
		proof: &IsolationEvidence{Version: Version, Peer: IsolationScopeEvidence{Events: []Event{}}},
	}, nil
}

func (domain *isolationConversation) afterSend(
	ctx context.Context, request protocol.MintClawMessage, number int, send func(string) (Event, error),
) error {
	if number != 1 {
		return nil
	}
	if err := domain.target.activeTool.waitEntered(ctx, domain.args); err != nil {
		return err
	}
	peerSession := uuid.NewString()
	peer, err := newSteeringConversation(domain.peerItem.Case, domain.target, peerSession)
	if err != nil {
		return err
	}
	client, err := gatewayclient.Connect(ctx, domain.target.config, peerSession, domain.options)
	if err != nil {
		return err
	}
	trial := runDomainConversation(ctx, client, domain.peerItem, peerSession,
		domain.target.preset.ExpectedAgent, domain.target.preset.ExpectedWorkspace, domain.roles, peer)
	domain.proof.Peer = isolationScope(trial)
	if trial.ExecutionVerdict != Pass || !trial.TransportClosed || client.Failure() != nil {
		return errNativeEvidence
	}
	held, err := domain.target.activeTool.snapshot(domain.args)
	if err != nil || held.State != "active" || held.ReleaseSequence != 0 {
		return errNativeEvidence
	}
	return domain.steeringConversation.afterSend(ctx, request, number, send)
}

func (domain *isolationConversation) conclude(ctx context.Context, trial *Trial) {
	domain.steeringConversation.conclude(ctx, trial)
	trial.Isolation = domain.proof
}
