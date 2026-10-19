package eval

import (
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

type browserQuestionReply struct {
	prompt      Event
	record      interactions.Record
	requestID   string
	requestedAt time.Time
	text        string
}

func (domain *browserConversation) browserQuestionPromptMatches(prompt Event, record interactions.Record) error {
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
		intent.Identity.ChatID != record.Route.ChatID || intent.Message == nil ||
		intent.Message.AgentID != record.Route.AgentID ||
		intent.Message.SessionKey != record.Route.SessionKey ||
		!observedBrowserQuestionPrompt(prompt, record.ID, record.ShortID, intent, domain.target) {
		return errBrowserEvidence
	}
	return nil
}

func browserQuestionAnswerMatches(before browserQuestionReply, current interactions.Record, now time.Time) bool {
	answer := current.Answer
	return before.requestID != "" &&
		current.ID == before.record.ID && current.Kind == interactions.KindQuestion && current.ProtectedAnswer == nil &&
		(current.Status == interactions.StatusResuming || current.Status == interactions.StatusResolved) &&
		current.Outcome == interactions.OutcomeAnswered && current.FailureCode == "" &&
		current.Revision > before.record.Revision && current.Route == before.record.Route &&
		reflect.DeepEqual(current.Origin, before.record.Origin) &&
		reflect.DeepEqual(current.Questions, before.record.Questions) &&
		current.ShortID == before.record.ShortID && current.CreatedAt == before.record.CreatedAt &&
		current.ExpiresAt == before.record.ExpiresAt && current.UpdatedAt <= now.UnixMilli() &&
		current.ApprovalConsumedAt == 0 && current.PromptDeliveryID == before.record.PromptDeliveryID &&
		answer != nil && answer.Protected == nil && answer.MessageID == before.requestID &&
		answer.Text == before.text && !answer.Superseded &&
		(answer.ResponseMessageID == "" || answer.ResponseMessageID == before.prompt.MessageID) &&
		browserQuestionValuesMatch(current, before.text) && len(answer.Media) == 0 && answer.Choice == "" &&
		answer.ReceivedAt >= before.requestedAt.UnixMilli() && answer.ReceivedAt < current.ExpiresAt &&
		answer.ReceivedAt <= current.UpdatedAt &&
		(current.Status != interactions.StatusResolved ||
			current.ResolvedAt >= answer.ReceivedAt && current.ResolvedAt <= now.UnixMilli())
}

func browserQuestionValuesMatch(record interactions.Record, text string) bool {
	if record.Answer == nil {
		return false
	}
	values := record.Answer.Values
	if len(values) == 0 {
		return true
	}
	if len(record.Questions) != 1 || len(values) != 1 || record.Questions[0].MultiSelect {
		return false
	}
	value, found := values[record.Questions[0].ID]
	return found && value == text
}

func browserInvocationMatchesSession(
	action browser.PreparedAction, invocation browser.Invocation, session browser.Session,
	policy, restricted string, now time.Time,
) bool {
	return action.Owner == session.Owner && invocation.Owner == session.Owner &&
		action.SessionID == session.ID && invocation.SessionID == session.ID && !action.DryRun &&
		action.Profile == session.Profile && action.ProfileRevision == session.ProfileRevision &&
		action.PolicyRevision == policy && action.ControllerGeneration == session.ControllerGeneration &&
		action.FrameID == "" && action.TabID == session.TabID && invocation.ActionHash == action.ActionHash &&
		invocation.Execution == nil && invocation.Effect == action.Effect &&
		action.CapabilityMode == browserpolicy.CapabilityRestricted &&
		action.ApprovalMode == browserpolicy.ApprovalPolicy && action.RestrictedPolicyRevision == restricted &&
		action.WorkerRestrictedDecision == "" && action.CreatedAt >= session.CreatedAt &&
		invocation.CreatedAt >= action.CreatedAt && invocation.UpdatedAt <= now.UnixNano()
}
