package eval

import (
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

const browserAmbiguityAssessmentScope = "owned_post_stop_browser_ambiguity_v1"

func validBrowserAmbiguityScript(item Case) bool {
	steps := item.Driver.Steps
	return isBrowserAmbiguity(item) && item.Driver.Mode == "scripted" && len(item.Driver.Facts) == 0 &&
		len(steps) == 2 && steps[0].WaitFor.Kind == "question" && steps[1].WaitFor.Kind == "approval" &&
		steps[0].Action == (Action{Kind: "message", Text: browserAmbiguityReply}) &&
		steps[1].Action == (Action{Kind: "allow_once"}) && requiredRubric(item.Rubric)
}

type BrowserClarificationEvidence struct {
	InteractionID        string `json:"interaction_id"`
	ShortID              string `json:"short_id"`
	PromptSHA256         string `json:"prompt_sha256"`
	PromptMessageID      string `json:"prompt_message_id"`
	PromptRequestID      string `json:"prompt_request_id"`
	PromptDeliveryID     string `json:"prompt_delivery_id"`
	RequestID            string `json:"request_id"`
	RegistrySHA256       string `json:"registry_sha256"`
	PageStateSHA256      string `json:"page_state_sha256"`
	FixturePageSHA256    string `json:"fixture_page_sha256"`
	SessionID            string `json:"session_id"`
	TabID                string `json:"tab_id"`
	ExecutionID          string `json:"execution_id"`
	ControllerGeneration uint64 `json:"controller_generation"`
	WaitingRevision      int64  `json:"waiting_revision"`
	ObservedRevision     int64  `json:"observed_revision"`
	RequestedAt          int64  `json:"requested_at"`
	ReceivedAt           int64  `json:"received_at"`
	ResolvedAt           int64  `json:"resolved_at"`
}

type BrowserAmbiguityEvidence struct {
	Version              int                           `json:"version"`
	Scope                string                        `json:"scope"`
	Code                 string                        `json:"code"`
	Clarification        *BrowserClarificationEvidence `json:"clarification,omitempty"`
	Approval             *BrowserApprovalEvidence      `json:"approval,omitempty"`
	ApprovalShortID      string                        `json:"approval_short_id"`
	ApprovalPromptSHA256 string                        `json:"approval_prompt_sha256"`
	RemovalActionID      string                        `json:"removal_action_id"`
	RemovalInvocationID  string                        `json:"removal_invocation_id"`
	Native               *BrowserTaskEvidence          `json:"native,omitempty"`
}

func finalizeBrowserAmbiguity(
	trial *Trial, pending browserPendingTrial, target admittedTarget, observed browserNativeSnapshot, empty bool,
) error {
	if pending.conversation == nil || trial.Browser == nil || !isBrowserAmbiguity(pending.item) {
		return nil
	}
	domain := pending.conversation
	proof := &BrowserAmbiguityEvidence{Version: Version, Scope: browserAmbiguityAssessmentScope, Code: "unavailable"}
	trial.Browser.Ambiguity = proof
	current, err := domain.ambiguityAnswer()
	if err != nil {
		return err
	}
	if current.Status != interactions.StatusResolved || domain.approval == nil {
		return errBrowserEvidence
	}
	approval, err := domain.consumedApproval()
	if err != nil {
		return err
	}
	records, registryHash, err := domain.questions.records()
	if err != nil {
		return err
	}
	questions, approvals := 0, 0
	for _, record := range records {
		if record.Route.SessionKey != trial.SessionKey {
			continue
		}
		switch record.ID {
		case current.ID:
			if !reflect.DeepEqual(record, current) {
				return errBrowserEvidence
			}
			questions++
		case approval.InteractionID:
			if record.Kind != interactions.KindApproval || record.Status != interactions.StatusResolved ||
				record.Revision != approval.ConsumedRevision || record.ApprovalConsumedAt != approval.ConsumedAt {
				return errBrowserEvidence
			}
			approvals++
		default:
			return errBrowserEvidence
		}
	}
	before, answer := domain.clarification, current.Answer
	if questions != 1 || approvals != 1 || before == nil || answer == nil ||
		domain.fixture != pending.fixture || digest(pending.fixture.page) != before.pageSHA256 ||
		approval.Scope != browserAmbiguityApprovalScope || approval.PromptRequestID != before.reply.requestID ||
		approval.ConsumedAt < answer.ReceivedAt {
		return errBrowserEvidence
	}
	action, found := observed.state.PreparedActions[approval.PreparedActionID]
	if !found || !reflect.DeepEqual(action, domain.approval.action) || !fixtureSouthRemovalAction(action) ||
		action.SessionID != before.session.ID || action.TabID != before.session.TabID ||
		action.Owner != before.session.Owner || action.ControllerGeneration != before.session.ControllerGeneration ||
		action.CreatedAt/1_000_000 < answer.ReceivedAt || action.ActionHash != approval.ActionHash ||
		action.PolicyRevision != approval.PolicyRevision {
		return errBrowserEvidence
	}
	proof.Clarification = &BrowserClarificationEvidence{
		InteractionID:        current.ID,
		ShortID:              current.ShortID,
		PromptSHA256:         digest([]byte(before.reply.prompt.Content)),
		PromptMessageID:      before.reply.prompt.MessageID,
		PromptRequestID:      before.reply.prompt.RequestID,
		PromptDeliveryID:     current.PromptDeliveryID,
		RequestID:            before.reply.requestID,
		RegistrySHA256:       registryHash,
		PageStateSHA256:      before.session.PageStateHash,
		FixturePageSHA256:    before.pageSHA256,
		SessionID:            before.session.ID,
		TabID:                before.session.TabID,
		ExecutionID:          before.session.Owner.ExecutionID,
		ControllerGeneration: before.session.ControllerGeneration,
		WaitingRevision:      before.reply.record.Revision,
		ObservedRevision:     current.Revision,
		RequestedAt:          before.reply.requestedAt.UnixMilli(),
		ReceivedAt:           answer.ReceivedAt,
		ResolvedAt:           current.ResolvedAt,
	}
	proof.Approval, proof.RemovalActionID = approval, action.ID
	proof.ApprovalShortID = domain.approval.record.ShortID
	proof.ApprovalPromptSHA256 = digest([]byte(domain.approval.prompt.Content))
	for _, invocation := range observed.state.Invocations {
		if invocation.PreparedActionID != action.ID {
			continue
		}
		if proof.RemovalInvocationID != "" {
			return errBrowserEvidence
		}
		proof.RemovalInvocationID = invocation.ID
		if !browserAmbiguityMutationAllowed(action, invocation, proof) {
			return errBrowserEvidence
		}
	}
	if proof.RemovalInvocationID == "" {
		return errBrowserEvidence
	}
	proof.Native = inspectBrowserTask(*trial, target, pending.fixture, observed, empty,
		browserTaskEffects{ambiguity: proof})
	if proof.Native.Code == "native_post_stop_observed" && trial.Browser.Fixture != nil &&
		trial.Browser.Fixture.Effects == Pass {
		proof.Code = "ambiguity_observed"
		trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
	}
	return nil
}

func browserAmbiguityMutationAllowed(
	action browser.PreparedAction, invocation browser.Invocation, proof *BrowserAmbiguityEvidence,
) bool {
	return proof != nil && proof.Approval != nil && proof.Clarification != nil &&
		fixtureSouthRemovalAction(action) && action.ID == proof.RemovalActionID &&
		invocation.ID == proof.RemovalInvocationID && invocation.State == browser.InvocationSucceeded &&
		invocation.SafeFailure == "" && invocation.AcceptedAt > 0 &&
		invocation.AcceptedAt/1_000_000 >= proof.Approval.ConsumedAt &&
		invocation.CompletedAt >= invocation.AcceptedAt &&
		action.ActionHash == proof.Approval.ActionHash && action.ID == proof.Approval.PreparedActionID &&
		action.SessionID == proof.Clarification.SessionID && action.TabID == proof.Clarification.TabID &&
		action.ControllerGeneration == proof.Clarification.ControllerGeneration
}

func browserAmbiguityPrompt(trial Trial, proof *BrowserAmbiguityEvidence, kind string) (Event, bool) {
	if proof == nil || proof.Clarification == nil || proof.Approval == nil {
		return Event{}, false
	}
	id, short, message, request, sha := proof.Clarification.InteractionID, proof.Clarification.ShortID,
		proof.Clarification.PromptMessageID, proof.Clarification.PromptRequestID, proof.Clarification.PromptSHA256
	if kind == "approval" {
		id, short, message, request, sha = proof.Approval.InteractionID, proof.ApprovalShortID,
			proof.Approval.PromptMessageID, proof.Approval.PromptRequestID, proof.ApprovalPromptSHA256
	} else if kind != "question" {
		return Event{}, false
	}
	var selected Event
	for _, event := range trial.Events {
		if event.Kind != kind {
			continue
		}
		if selected.ID != "" || event.InteractionID != id || event.InteractionShortID != short ||
			event.MessageID != message || event.RequestID != request || digest([]byte(event.Content)) != sha ||
			event.AgentID != trial.AgentID || event.SessionKey != trial.SessionKey ||
			event.Controls != "prompt" || event.InteractionKind != kind {
			return Event{}, false
		}
		selected = event
	}
	return selected, selected.ID != ""
}

func observedBrowserAmbiguityPrompt(
	trial Trial, proof *BrowserAmbiguityEvidence, kind string, intent outbox.Intent, target admittedTarget,
) bool {
	prompt, found := browserAmbiguityPrompt(trial, proof, kind)
	return found && observedBrowserInteractionPrompt(prompt, prompt.InteractionID,
		prompt.InteractionShortID, kind, intent, target)
}

func browserClarificationEventsMatch(trial Trial, proof *BrowserAmbiguityEvidence) bool {
	prompt, found := browserAmbiguityPrompt(trial, proof, "question")
	if !found {
		return false
	}
	clarification := proof.Clarification
	answers := 0
	for index, event := range trial.Events {
		if event.Kind == "input" && event.RequestID == clarification.RequestID {
			answers++
		}
		if event.ID != prompt.ID {
			continue
		}
		if index+1 >= len(trial.Events) {
			return false
		}
		answer := trial.Events[index+1]
		if answer.Kind != "input" || answer.RequestID != clarification.RequestID || answer.Turn != event.Turn+1 ||
			answer.Content != "/answer "+clarification.ShortID+" "+browserAmbiguityReply {
			return false
		}
	}
	return answers == 1
}

func validBrowserClarification(proof *BrowserClarificationEvidence, trial Trial) bool {
	return proof != nil && validID(proof.InteractionID) && validID(proof.ShortID) &&
		validDigest(proof.PromptSHA256) && validDigest(proof.RegistrySHA256) && validDigest(proof.PageStateSHA256) &&
		validDigest(proof.FixturePageSHA256) && validID(proof.PromptDeliveryID) && validID(proof.PromptRequestID) &&
		boundedIdentity(proof.PromptMessageID, 128) && validID(proof.RequestID) &&
		boundedIdentity(proof.SessionID, browser.MaxIdentifierBytes) &&
		boundedIdentity(proof.TabID, browser.MaxIdentifierBytes) &&
		boundedIdentity(proof.ExecutionID, browser.MaxIdentifierBytes) && proof.ControllerGeneration > 0 &&
		proof.WaitingRevision > 0 && proof.ObservedRevision > proof.WaitingRevision &&
		proof.RequestedAt >= trial.StartedAt.UnixMilli() && proof.ReceivedAt >= proof.RequestedAt &&
		proof.ResolvedAt >= proof.ReceivedAt && proof.ResolvedAt <= time.Now().UnixMilli()
}
