package eval

import (
	"context"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

func (domain *browserConversation) confirmForm(ctx context.Context, prompt Event) (protocol.MintClawMessage, error) {
	if !isBrowserForm(domain.item) || domain.review != nil || domain.approval != nil || prompt.Kind != "question" {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	if err := domain.questions.observe(ctx, prompt); err != nil {
		return protocol.MintClawMessage{}, err
	}
	if err := domain.formPromptMatches(prompt, domain.questions.waiting); err != nil {
		return protocol.MintClawMessage{}, err
	}
	inputs, state, err := domain.readFormInputState()
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	sessionID, _, ok := browserFormInputContext(domain.item, inputs)
	session := state.Sessions[sessionID]
	origin := domain.questions.waiting.Origin
	if !ok || !domain.formStateReady(inputs, state, "") || session.State != browser.SessionReady ||
		session.EffectiveController() != browser.ControllerAgent || !validDigest(session.PageStateHash) ||
		origin.ExecutionID == "" || origin.ExecutionContext == nil ||
		origin.ExecutionContext.ClientSessionID != domain.questions.sessionID ||
		session.Owner != (browser.Owner{
			ActorID:    browser.OpaqueActorID("mintclaw:mintclaw-user"),
			AgentID:    browser.OpaqueAgentID(routing.NormalizeAgentID(prompt.AgentID)),
			SessionKey: browser.OpaqueSessionKey(prompt.SessionKey), ExecutionID: browser.OpaqueExecutionID(origin.ExecutionID),
		}) {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	now := time.Now()
	request := protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.questions.sessionID,
		Timestamp: now.UnixMilli(), Payload: map[string]any{protocol.PayloadKeyContent: browserFormConfirmationText},
	}
	domain.review = &browserFormReview{
		prompt: prompt, record: domain.questions.waiting, session: session,
		inputSetSHA256: browserFormInputSet(inputs), requestID: request.ID, requestedAt: now,
	}
	return request, nil
}

func (domain *browserConversation) formPromptMatches(prompt Event, record interactions.Record) error {
	raw, err := domain.questions.read(
		domain.questions.workspace,
		"state/outbox/"+record.PromptDeliveryID+".json",
		outbox.MaxInspectionBytes,
	)
	if err != nil {
		return err
	}
	defer clear(raw)
	intent, err := outbox.InspectIntent(raw, record.PromptDeliveryID)
	if err != nil || intent.OwnerWorkspace != domain.questions.workspace || intent.Status != outbox.StatusDelivered ||
		intent.Identity.SessionKey != record.Route.SessionKey || intent.Identity.Channel != record.Route.Channel ||
		intent.Identity.ChatID != record.Route.ChatID || intent.Message == nil || intent.Message.AgentID != record.Route.AgentID ||
		intent.Message.SessionKey != record.Route.SessionKey ||
		!observedBrowserQuestionPrompt(prompt, record.ID, record.ShortID, intent, domain.target) {
		return errBrowserEvidence
	}
	return nil
}

// Check only passive native records. This is not a new browser engine or grant;
// the native approval/HMAC and broker independently authorize the exact action.
func (domain *browserConversation) formStateReady(
	inputs *BrowserFormInputsEvidence,
	state browser.StateSnapshot,
	submitID string,
) bool {
	sessionID, _, ok := browserFormInputContext(domain.item, inputs)
	if !ok {
		return false
	}
	session, found := state.Sessions[sessionID]
	name, profile := browserEvalProfile(domain.target.config)
	policy, err := domain.target.config.Tools.Browser.PolicyRevision()
	if profile.Policy == nil {
		return false
	}
	restricted, restrictedErr := browserpolicy.PolicyRevision(*profile.Policy)
	if !found || err != nil || session.State != browser.SessionReady || session.DryRun ||
		restrictedErr != nil ||
		session.Target != config.BrowserDefaultTarget || session.Profile != name ||
		session.ProfileRevision != profile.Revision || session.PolicyRevision != policy ||
		session.EffectiveController() != browser.ControllerAgent || session.FrameID != "" ||
		session.SnapshotOrigin != domain.fixture.origin || session.ExpiresAt <= time.Now().UnixNano() ||
		session.Owner.ExecutionID != inputs.Fills[0].Target.ExecutionID {
		return false
	}
	for id, other := range state.Sessions {
		if other.Owner.SessionKey == session.Owner.SessionKey && id != session.ID {
			return false
		}
	}
	stateNow := domain.fixture.snapshot()
	if stateNow.failed || stateNow.overflow || len(stateNow.submissions) != 0 || len(stateNow.removals) != 0 {
		return false
	}
	filled := map[string]string{}
	for _, fill := range inputs.Fills {
		filled[fill.PreparedActionID] = fill.ActionSHA256
	}
	used := map[string]bool{}
	color, navigation := false, false
	for _, invocation := range state.Invocations {
		if invocation.Owner.SessionKey != session.Owner.SessionKey {
			continue
		}
		action, exists := state.PreparedActions[invocation.PreparedActionID]
		if !exists || action.Owner != session.Owner || invocation.Owner != session.Owner ||
			action.SessionID != session.ID || invocation.SessionID != session.ID || action.DryRun ||
			action.Profile != session.Profile || action.ProfileRevision != session.ProfileRevision ||
			action.PolicyRevision != policy || action.ControllerGeneration != session.ControllerGeneration ||
			action.FrameID != "" || action.TabID != session.TabID || invocation.ActionHash != action.ActionHash ||
			invocation.Execution != nil || invocation.Effect != action.Effect ||
			action.CapabilityMode != browserpolicy.CapabilityRestricted || action.ApprovalMode != browserpolicy.ApprovalPolicy ||
			action.RestrictedPolicyRevision != restricted || action.WorkerRestrictedDecision != "" ||
			action.CreatedAt < session.CreatedAt || invocation.CreatedAt < action.CreatedAt ||
			invocation.UpdatedAt > time.Now().UnixNano() {
			return false
		}
		used[action.ID] = true
		if action.ID == submitID {
			if invocation.AcceptedAt != 0 || !fixtureSubmitAction(action) ||
				action.RestrictedDecision != browserpolicy.DecisionAsk || action.LocalRestrictedDecision != browserpolicy.DecisionAsk ||
				action.CurrentOrigin != domain.fixture.origin {
				return false
			}
			continue
		}
		if invocation.State != browser.InvocationSucceeded || invocation.SafeFailure != "" ||
			action.RestrictedDecision != browserpolicy.DecisionAllow || action.LocalRestrictedDecision != browserpolicy.DecisionAllow {
			return false
		}
		allowed := action.PolicyEffect == browser.EffectRead && action.CurrentOrigin == domain.fixture.origin
		if action.Action.Kind == browser.ActionNavigate && action.Effect == browser.EffectNavigation &&
			action.PolicyEffect == browser.EffectNavigation && action.DestinationOrigin == domain.fixture.origin &&
			(action.CurrentOrigin == "about:blank" || action.CurrentOrigin == domain.fixture.origin) &&
			action.Action.URL == domain.fixture.origin+domain.fixture.path {
			allowed, navigation = true, true
		}
		if filled[action.ID] == action.ActionHash && action.Action.Kind == browser.ActionFill &&
			action.CurrentOrigin == domain.fixture.origin {
			allowed = true
		}
		if fixtureColorAction(action) && action.CurrentOrigin == domain.fixture.origin {
			allowed, color = true, true
		}
		if !allowed {
			return false
		}
	}
	for id, action := range state.PreparedActions {
		if action.Owner.SessionKey == session.Owner.SessionKey && (!used[id] || action.SessionID != session.ID) {
			return false
		}
	}
	return color && navigation
}

func (domain *browserConversation) formConfirmation() (*BrowserFormConfirmationEvidence, error) {
	before := domain.review
	if before == nil || before.requestID == "" {
		return nil, errBrowserEvidence
	}
	current, err := domain.questions.record(before.record.ID)
	if err != nil {
		return nil, err
	}
	return projectBrowserFormConfirmation(before, current, time.Now())
}

func projectBrowserFormConfirmation(
	before *browserFormReview,
	current interactions.Record,
	now time.Time,
) (*BrowserFormConfirmationEvidence, error) {
	if before == nil || before.requestID == "" {
		return nil, errBrowserEvidence
	}
	answer := current.Answer
	if current.ID != before.record.ID || current.Kind != interactions.KindQuestion || current.ProtectedAnswer != nil ||
		(current.Status != interactions.StatusResuming && current.Status != interactions.StatusResolved) ||
		current.Outcome != interactions.OutcomeAnswered || current.FailureCode != "" || current.Revision <= before.record.Revision ||
		current.Route != before.record.Route || !reflect.DeepEqual(current.Origin, before.record.Origin) ||
		!reflect.DeepEqual(current.Questions, before.record.Questions) ||
		current.ShortID != before.record.ShortID || current.CreatedAt != before.record.CreatedAt ||
		current.ExpiresAt != before.record.ExpiresAt || current.UpdatedAt > now.UnixMilli() ||
		current.ApprovalConsumedAt != 0 ||
		current.PromptDeliveryID != before.record.PromptDeliveryID || answer == nil || answer.Protected != nil ||
		answer.MessageID != before.requestID || answer.Text != browserFormConfirmationText || answer.Superseded ||
		answer.ResponseMessageID != "" && answer.ResponseMessageID != before.prompt.MessageID ||
		len(answer.Values) != 0 || len(answer.Media) != 0 || answer.Choice != "" ||
		answer.ReceivedAt < before.requestedAt.UnixMilli() || answer.ReceivedAt >= current.ExpiresAt ||
		answer.ReceivedAt > current.UpdatedAt || current.Status == interactions.StatusResolved &&
		(current.ResolvedAt < answer.ReceivedAt || current.ResolvedAt > now.UnixMilli()) {
		return nil, errBrowserEvidence
	}
	return &BrowserFormConfirmationEvidence{
		Scope:                "native_fixture_form_confirmation_v1",
		InteractionID:        current.ID,
		ShortID:              current.ShortID,
		PromptSHA256:         digest([]byte(before.prompt.Content)),
		PromptMessageID:      before.prompt.MessageID,
		PromptRequestID:      before.prompt.RequestID,
		PromptDeliveryID:     current.PromptDeliveryID,
		RequestID:            before.requestID,
		InputSetSHA256:       before.inputSetSHA256,
		PageStateSHA256:      before.session.PageStateHash,
		SessionID:            before.session.ID,
		TabID:                before.session.TabID,
		ControllerGeneration: before.session.ControllerGeneration,
		WaitingRevision:      before.record.Revision,
		ObservedRevision:     current.Revision,
		RequestedAt:          before.requestedAt.UnixMilli(),
		ReceivedAt:           answer.ReceivedAt,
		ResolvedAt:           current.ResolvedAt,
	}, nil
}

func (domain *browserConversation) formApprovalMatches(
	record interactions.Record,
	action browser.PreparedAction,
	state browser.StateSnapshot,
) bool {
	confirmation, err := domain.formConfirmation()
	if err != nil || record.Origin.ExecutionContext == nil ||
		record.Origin.ExecutionContext.MessageID != confirmation.RequestID || record.CreatedAt < confirmation.ReceivedAt ||
		action.CreatedAt/1_000_000 < confirmation.ReceivedAt ||
		action.SessionID != confirmation.SessionID || action.TabID != confirmation.TabID {
		return false
	}
	inputs, current, err := domain.readFormInputState()
	session := state.Sessions[action.SessionID]
	return err == nil && reflect.DeepEqual(current, state) &&
		browserFormInputSet(inputs) == confirmation.InputSetSHA256 &&
		session.PageStateHash == confirmation.PageStateSHA256 &&
		session.ControllerGeneration == confirmation.ControllerGeneration &&
		domain.formStateReady(inputs, state, action.ID)
}
