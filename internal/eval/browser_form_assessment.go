package eval

import (
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

func validBrowserFormEvidence(trial Trial, item Case, workspace string) bool {
	if !isBrowserForm(item) || !validBrowserConversation(trial, item, workspace) || trial.Browser.Form == nil ||
		trial.Browser.FormInputs == nil || trial.Browser.Read != nil || trial.Browser.Artifact != nil ||
		trial.Browser.Download != nil || trial.Browser.Denial != nil {
		return false
	}
	form, inputs, fixture := trial.Browser.Form, trial.Browser.FormInputs, trial.Browser.Fixture
	sessionID, corrections, ok := browserFormInputContext(item, inputs)
	if !ok || form.Version != Version || form.Scope != browserFormAssessmentScope || form.Code != "form_observed" ||
		!form.ContextRetained || form.Corrections != corrections || form.Submissions != 1 || form.ColorSelections < 1 ||
		form.ColorSelections > browser.DefaultFileStoreRecords ||
		!validBrowserTaskProof(trial, form.Native, browserFormAssessmentScope) || form.Native.Sessions != 1 ||
		form.Native.Actions != form.Native.Invocations || form.Native.Actions < len(inputs.Fills)+form.ColorSelections+2 ||
		inputs.StateSHA256 != form.Native.StateSHA256 || !validDigest(inputs.RegistrySHA256) ||
		fixture == nil || fixture.Version != Version || fixture.Scope != "local_fixture_only" || !fixture.FixtureClosed ||
		fixture.Visits < 1 || fixture.Downloads != 0 || fixture.Submissions != 1 || fixture.Removals != 0 ||
		fixture.Effects != Pass || fixture.EffectsCode != "matched" || !validDigest(fixture.PageSHA256) ||
		!validDigest(fixture.CSVSourceSHA256) {
		return false
	}
	confirmation, approval := form.Confirmation, form.Approval
	now := time.Now().UnixMilli()
	if confirmation == nil || confirmation.Scope != "native_fixture_form_confirmation_v1" ||
		!validID(
			confirmation.InteractionID,
		) || !validID(confirmation.ShortID) || !validDigest(confirmation.PromptSHA256) ||
		!validID(confirmation.PromptDeliveryID) ||
		!boundedIdentity(confirmation.PromptMessageID, 128) || !validID(confirmation.PromptRequestID) ||
		!validID(confirmation.RequestID) || confirmation.InputSetSHA256 != browserFormInputSet(inputs) ||
		!validDigest(confirmation.PageStateSHA256) || confirmation.SessionID != sessionID ||
		confirmation.TabID != inputs.Fills[0].Target.TabID ||
		confirmation.ControllerGeneration != inputs.Fills[0].Target.ControllerGeneration ||
		confirmation.WaitingRevision < 1 || confirmation.ObservedRevision <= confirmation.WaitingRevision ||
		confirmation.RequestedAt < trial.StartedAt.UnixMilli() || confirmation.ReceivedAt < confirmation.RequestedAt ||
		confirmation.ResolvedAt < confirmation.ReceivedAt || confirmation.ResolvedAt > now ||
		!browserFormConfirmationEventsMatch(trial, confirmation) {
		return false
	}
	if approval == nil || approval.Scope != browserFormApprovalScope || approval.Code != "native_allow_once_consumed" ||
		!validID(
			approval.InteractionID,
		) || !validID(approval.PromptDeliveryID) || !validID(approval.ApprovalRequestID) ||
		!boundedIdentity(approval.PromptMessageID, 128) || approval.PromptRequestID != confirmation.RequestID ||
		approval.PreparedActionID != form.SubmissionActionID || !validID(form.SubmissionActionID) ||
		!validID(form.SubmissionInvocationID) || !validDigest(approval.ActionHash) ||
		approval.PolicyRevision != form.Native.PolicyRevision || approval.WaitingRevision < 1 ||
		approval.ConsumedRevision <= approval.WaitingRevision || approval.ConsumedAt < confirmation.ReceivedAt ||
		approval.ConsumedAt > now || !browserApprovalEventsMatch(trial, approval) {
		return false
	}
	seen := map[string]bool{}
	for _, fill := range inputs.Fills {
		target := fill.Target
		if fill.InteractionStatus != interactions.StatusResolved || fill.WaitingRevision < 1 ||
			fill.ObservedRevision <= fill.WaitingRevision || fill.ReceivedAt < trial.StartedAt.UnixMilli() ||
			fill.ResolvedAt < fill.ReceivedAt || fill.ResolvedAt > now || fill.AcceptedAt/1_000_000 < fill.ReceivedAt ||
			fill.CompletedAt < fill.AcceptedAt || fill.CompletedAt/1_000_000 > confirmation.RequestedAt ||
			!validID(fill.PreparedActionID) || !validID(fill.InvocationID) || !validDigest(fill.ActionSHA256) ||
			!validID(fill.PromptDeliveryID) || !validID(fill.AnswerRequestID) || !validID(fill.InteractionID) ||
			!validID(fill.TraceID) || !validDigest(fill.TraceSHA256) || !boundedIdentity(fill.ToolCallID, 256) ||
			!boundedIdentity(
				fill.ToolCallGeneration,
				256,
			) || !fill.TraceScope.Complete() || fill.TraceScope.Workspace != workspace ||
			!nativeModelSourceKnown(
				health.RuntimeProbe{GitCommit: fill.Source.Commit, BuildVersion: fill.Source.MintClawVersion},
			) ||
			fill.Source != inputs.Fills[0].Source || target.SessionKey != browser.OpaqueSessionKey(trial.SessionKey) ||
			target.AgentID != browser.OpaqueAgentID(routing.NormalizeAgentID(trial.AgentID)) ||
			target.ActorID != browser.OpaqueActorID("mintclaw:mintclaw-user") ||
			seen[fill.InteractionID] || !browserFormFillEventsMatch(trial, fill) {
			return false
		}
		seen[fill.InteractionID] = true
	}
	return true
}

func browserFormConfirmationEventsMatch(trial Trial, confirmation *BrowserFormConfirmationEvidence) bool {
	if _, found := browserFormConfirmationPrompt(trial, confirmation); !found {
		return false
	}
	for index, event := range trial.Events {
		if event.Kind == "question" && event.InteractionID == confirmation.InteractionID {
			return event.MessageID == confirmation.PromptMessageID && event.RequestID == confirmation.PromptRequestID &&
				index+1 < len(trial.Events) && trial.Events[index+1].Kind == "input" &&
				trial.Events[index+1].RequestID == confirmation.RequestID &&
				trial.Events[index+1].Content == browserFormConfirmationText && trial.Events[index+1].Turn == event.Turn+1
		}
	}
	return false
}

func browserFormConfirmationPrompt(trial Trial, confirmation *BrowserFormConfirmationEvidence) (Event, bool) {
	var selected Event
	for _, event := range trial.Events {
		if event.Kind != "question" || event.InteractionID != confirmation.InteractionID {
			continue
		}
		if selected.ID != "" || event.MessageID != confirmation.PromptMessageID ||
			event.RequestID != confirmation.PromptRequestID ||
			event.InteractionShortID != confirmation.ShortID ||
			digest([]byte(event.Content)) != confirmation.PromptSHA256 {
			return Event{}, false
		}
		selected = event
	}
	return selected, selected.ID != ""
}

func browserFormFillEventsMatch(trial Trial, fill BrowserInputFillEvidence) bool {
	for index, event := range trial.Events {
		if event.Kind == "question" && event.InteractionID == fill.InteractionID {
			return event.MessageID == fill.PromptMessageID && event.RequestID == fill.PromptRequestID &&
				slices.Contains(event.TraceScopes, fill.TraceScope) && index+1 < len(trial.Events) &&
				trial.Events[index+1].Kind == "input" && trial.Events[index+1].RequestID == fill.AnswerRequestID &&
				trial.Events[index+1].Turn == event.Turn+1 &&
				trial.Events[index+1].Content == "[protected answer supplied using an admitted fact handle]"
		}
	}
	return false
}
