package eval

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

const browserApprovalScope = "fixture_download_allow_once_v1"

type BrowserApprovalEvidence struct {
	Scope             string `json:"scope"`
	Code              string `json:"code"`
	InteractionID     string `json:"interaction_id"`
	PromptMessageID   string `json:"prompt_message_id"`
	PromptRequestID   string `json:"prompt_request_id"`
	PromptDeliveryID  string `json:"prompt_delivery_id"`
	ApprovalRequestID string `json:"approval_request_id"`
	PreparedActionID  string `json:"prepared_action_id"`
	ActionHash        string `json:"action_hash"`
	PolicyRevision    string `json:"policy_revision"`
	WaitingRevision   int64  `json:"waiting_revision"`
	ConsumedRevision  int64  `json:"consumed_revision"`
	ConsumedAt        int64  `json:"consumed_at"`
}

type browserApprovalObservation struct {
	prompt      Event
	record      interactions.Record
	action      browser.PreparedAction
	requestID   string
	requestedAt time.Time
}

func (domain *browserConversation) observeApproval(ctx context.Context, prompt Event) error {
	if domain.item.Browser == nil || domain.item.Browser.Scenario != "artifact" || prompt.Kind != "approval" ||
		domain.approval != nil {
		return errBrowserEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		observation, err := domain.readApproval(prompt)
		if err == nil {
			domain.approval = observation
			return nil
		}
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		select {
		case <-ctx.Done():
			return errBrowserEvidence
		case <-ticker.C:
		}
	}
}

func (domain *browserConversation) readApproval(prompt Event) (*browserApprovalObservation, error) {
	record, err := domain.questions.record(prompt.InteractionID)
	if err != nil {
		return nil, err
	}
	matched, err := domain.questions.matchesPromptKind(record, prompt, interactions.KindApproval, "browser_act")
	if err != nil {
		return nil, err
	}
	origin := record.Origin
	if !matched || record.Answer != nil || record.ApprovalConsumedAt != 0 || record.Outcome != "" ||
		origin.ExecutionID == "" || origin.ToolCallID == "" || origin.ExecutionContext == nil ||
		origin.ExecutionContext.ClientSessionID != domain.questions.sessionID ||
		origin.ExecutionContext.MessageID != prompt.RequestID {
		return nil, errBrowserEvidence
	}
	raw, err := domain.questions.read(
		domain.questions.workspace,
		"state/browser/browser.json",
		browser.DefaultFileStoreBytes,
	)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	snapshot, err := browser.InspectStateSnapshot(raw)
	if err != nil {
		return nil, errBrowserEvidence
	}
	name, profile := browserEvalProfile(domain.target.config)
	policy, err := domain.target.config.Tools.Browser.PolicyRevision()
	if err != nil || profile.Policy == nil {
		return nil, errBrowserEvidence
	}
	restricted, err := browserpolicy.PolicyRevision(*profile.Policy)
	if err != nil {
		return nil, errBrowserEvidence
	}
	owner := browser.Owner{
		ActorID:     browser.OpaqueActorID("mintclaw:mintclaw-user"),
		AgentID:     browser.OpaqueAgentID(routing.NormalizeAgentID(prompt.AgentID)),
		SessionKey:  browser.OpaqueSessionKey(prompt.SessionKey),
		ExecutionID: browser.OpaqueExecutionID(origin.ExecutionID),
	}
	var selected *browser.PreparedAction
	for _, action := range snapshot.PreparedActions {
		if action.Owner != owner || action.RequestID != browser.OpaqueRequestID(origin.ExecutionID, origin.ToolCallID) {
			continue
		}
		session, found := snapshot.Sessions[action.SessionID]
		if !found || session.Owner != owner || session.State != browser.SessionReady || session.DryRun ||
			session.Target != config.BrowserDefaultTarget || session.Profile != name ||
			session.ProfileRevision != profile.Revision || session.PolicyRevision != policy ||
			session.CreatedAt < domain.questions.started.UnixNano() || session.SnapshotID != action.SnapshotID ||
			session.SnapshotGeneration != action.SnapshotGeneration || session.TabID != action.TabID ||
			action.Action.Kind != browser.ActionDownload || !action.Action.Deliver || action.DryRun ||
			action.Target != session.Target || action.Profile != session.Profile ||
			action.ProfileRevision != session.ProfileRevision || action.PolicyRevision != policy ||
			action.CurrentOrigin != domain.fixture.origin || action.ElementRole != "link" ||
			action.ElementName != "Download fixture report" || action.Effect != browser.EffectUnknown ||
			action.PolicyEffect != browser.EffectUnknown || action.CapabilityMode != browserpolicy.CapabilityRestricted ||
			action.ApprovalMode != browserpolicy.ApprovalPolicy || action.RestrictedPolicyRevision != restricted ||
			action.RestrictedDecision != browserpolicy.DecisionAsk || action.LocalRestrictedDecision != browserpolicy.DecisionAsk ||
			action.WorkerRestrictedDecision != "" || action.CreatedAt/1_000_000 > record.CreatedAt ||
			action.ExpiresAt <= time.Now().UnixNano() || action.InputBytes != 0 || action.ArtifactBytes != 0 {
			return nil, errBrowserEvidence
		}
		for _, invocation := range snapshot.Invocations {
			if invocation.PreparedActionID == action.ID && invocation.AcceptedAt != 0 {
				return nil, errBrowserEvidence
			}
		}
		if selected != nil {
			return nil, errBrowserEvidence
		}
		copy := action
		selected = &copy
	}
	if selected == nil {
		return nil, errBrowserEvidence
	}
	root, err := os.OpenRoot(domain.questions.workspace)
	if err != nil {
		return nil, errBrowserEvidence
	}
	key, err := readNativeEvidenceFile(root, "state/interaction_hmac.key", 32)
	_ = root.Close()
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, errBrowserEvidence
	}
	preparation := browser.Preparation{Action: *selected, Approval: browser.ApprovalBinding{
		PreparedActionID: selected.ID, ActionHash: selected.ActionHash,
		PolicyRevision: selected.PolicyRevision, ExpiresAt: selected.ExpiresAt,
	}}
	verified, err := interactions.VerifyArgumentHash(
		key,
		tools.BrowserActionApprovalArguments(preparation),
		origin.ArgumentHash,
	)
	if err != nil || !verified {
		return nil, errBrowserEvidence
	}
	return &browserApprovalObservation{prompt: prompt, record: record, action: *selected}, nil
}

func (domain *browserConversation) approveDownload(prompt Event) (protocol.MintClawMessage, error) {
	previous := domain.approval
	if previous == nil || previous.requestID != "" || prompt.MessageID != previous.prompt.MessageID ||
		prompt.InteractionID != previous.record.ID {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	current, err := domain.readApproval(prompt)
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	if !reflect.DeepEqual(current.record, previous.record) || !reflect.DeepEqual(current.action, previous.action) {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	request := protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.questions.sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{
			protocol.PayloadKeyContent:            "Allow this reviewed fixture download once.",
			protocol.PayloadKeyInteractionChoice:  string(bus.InboundInteractionChoiceAllowOnce),
			protocol.PayloadKeyInteractionShortID: prompt.InteractionShortID,
			protocol.PayloadKeyInteractionPrompt:  prompt.MessageID,
		},
	}
	previous.requestID, previous.requestedAt = request.ID, time.Now()
	return request, nil
}

func (domain *browserConversation) consumedApproval() (*BrowserApprovalEvidence, error) {
	before := domain.approval
	if before == nil || before.requestID == "" {
		return nil, errBrowserEvidence
	}
	current, err := domain.questions.record(before.record.ID)
	if err != nil {
		return nil, err
	}
	if current.Kind != interactions.KindApproval || current.Status != interactions.StatusResolved ||
		current.ShortID != before.record.ShortID || current.ProtectedAnswer != nil || current.FailureCode != "" ||
		current.Outcome != interactions.OutcomeAllowed || current.Route != before.record.Route ||
		!reflect.DeepEqual(
			current.Origin,
			before.record.Origin,
		) || current.PromptDeliveryID != before.record.PromptDeliveryID ||
		current.CreatedAt != before.record.CreatedAt || current.ExpiresAt != before.record.ExpiresAt ||
		current.Revision <= before.record.Revision || current.ApprovalConsumedAt < before.requestedAt.UnixMilli() ||
		current.ApprovalConsumedAt >= current.ExpiresAt || current.ResolvedAt < current.ApprovalConsumedAt ||
		current.ResolvedAt > time.Now().UnixMilli() || current.Answer == nil ||
		current.Answer.MessageID != before.requestID || current.Answer.ResponseMessageID != before.prompt.MessageID ||
		current.Answer.ReceivedAt < before.requestedAt.UnixMilli() ||
		current.Answer.ReceivedAt > current.ApprovalConsumedAt || current.Answer.Superseded ||
		current.Answer.Choice != "" || current.Answer.Text != string(bus.InboundInteractionChoiceAllowOnce) {
		return nil, errBrowserEvidence
	}
	return &BrowserApprovalEvidence{
		Scope:             browserApprovalScope,
		Code:              "native_allow_once_consumed",
		InteractionID:     current.ID,
		PromptMessageID:   before.prompt.MessageID,
		PromptRequestID:   before.prompt.RequestID,
		PromptDeliveryID:  current.PromptDeliveryID,
		ApprovalRequestID: before.requestID,
		PreparedActionID:  before.action.ID,
		ActionHash:        before.action.ActionHash,
		PolicyRevision:    before.action.PolicyRevision,
		WaitingRevision:   before.record.Revision,
		ConsumedRevision:  current.Revision,
		ConsumedAt:        current.ApprovalConsumedAt,
	}, nil
}
