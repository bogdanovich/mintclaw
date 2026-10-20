package eval

import (
	"reflect"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func browserFormMutationAllowed(action browser.PreparedAction, invocation browser.Invocation,
	inputs *BrowserFormInputsEvidence, form *BrowserFormEvidence,
) bool {
	if invocation.State != browser.InvocationSucceeded || action.CurrentOrigin == "" ||
		action.FrameID != "" || invocation.SafeFailure != "" {
		return false
	}
	if inputs == nil || len(inputs.Fills) == 0 ||
		digest([]byte(action.CurrentOrigin)) != inputs.Fills[0].Target.OriginSHA256 {
		return false
	}
	if fixtureColorAction(action) {
		return true
	}
	if fixtureSubmitAction(action) {
		return action.ID == form.SubmissionActionID && invocation.ID == form.SubmissionInvocationID &&
			form.Approval != nil && form.Approval.PreparedActionID == action.ID &&
			form.Approval.ActionHash == action.ActionHash && invocation.AcceptedAt/1_000_000 >= form.Approval.ConsumedAt
	}
	if action.Action.Kind != browser.ActionFill || inputs == nil {
		return false
	}
	for _, fill := range inputs.Fills {
		if fill.PreparedActionID == action.ID && fill.InvocationID == invocation.ID &&
			fill.ActionSHA256 == action.ActionHash && fill.Target.Ref == action.Action.Ref &&
			fill.AcceptedAt == invocation.AcceptedAt && fill.CompletedAt == invocation.CompletedAt {
			return true
		}
	}
	return false
}

func finalizeBrowserForm(trial *Trial, pending browserPendingTrial, target admittedTarget,
	observed browserNativeSnapshot, empty bool,
) error {
	if pending.conversation == nil || trial.Browser == nil {
		return nil
	}
	domain := pending.conversation
	form := &BrowserFormEvidence{Version: Version, Scope: browserFormAssessmentScope, Code: "unavailable"}
	trial.Browser.Form = form
	inputs, err := domain.readFormInputs()
	if err != nil {
		return err
	}
	trial.Browser.FormInputs = inputs
	if inputs.StateSHA256 != observed.stateSHA256 {
		return errBrowserEvidence
	}
	_, corrections, ok := browserFormInputContext(pending.item, inputs)
	if !ok {
		return errBrowserEvidence
	}
	for _, fill := range inputs.Fills {
		if fill.InteractionStatus != interactions.StatusResolved || fill.ResolvedAt == 0 {
			return errBrowserEvidence
		}
	}
	confirmation, err := domain.formConfirmation()
	if err != nil {
		return err
	}
	if confirmation.ResolvedAt == 0 || confirmation.InputSetSHA256 != browserFormInputSet(inputs) {
		return errBrowserEvidence
	}
	approval, err := domain.consumedApproval()
	if err != nil {
		return err
	}
	if domain.approval == nil || domain.approval.record.Origin.ExecutionContext == nil ||
		domain.approval.record.Origin.ExecutionContext.MessageID != confirmation.RequestID ||
		approval.ConsumedAt < confirmation.ReceivedAt {
		return errBrowserEvidence
	}
	action, found := observed.state.PreparedActions[approval.PreparedActionID]
	if !found || !reflect.DeepEqual(action, domain.approval.action) || !fixtureSubmitAction(action) ||
		action.CreatedAt/1_000_000 < confirmation.ReceivedAt ||
		action.ActionHash != approval.ActionHash || action.PolicyRevision != approval.PolicyRevision {
		return errBrowserEvidence
	}
	form.Confirmation, form.Approval = confirmation, approval
	form.SubmissionActionID = action.ID
	for _, invocation := range observed.state.Invocations {
		prepared := observed.state.PreparedActions[invocation.PreparedActionID]
		if invocation.Owner.SessionKey == browser.OpaqueSessionKey(trial.SessionKey) &&
			invocation.State == browser.InvocationSucceeded {
			if fixtureColorAction(prepared) {
				form.ColorSelections++
			}
			if fixtureSubmitAction(prepared) {
				form.Submissions++
			}
		}
		if invocation.PreparedActionID == action.ID {
			if form.SubmissionInvocationID != "" {
				return errBrowserEvidence
			}
			form.SubmissionInvocationID = invocation.ID
		}
	}
	if form.ColorSelections < 1 || form.Submissions != 1 {
		return errBrowserEvidence
	}
	form.ContextRetained, form.Corrections = true, corrections
	form.Native = inspectBrowserTask(*trial, target, pending.fixture, observed, empty, browserTaskEffects{form: form})
	if form.Native.Code != "native_post_stop_observed" || trial.Browser.Fixture == nil ||
		trial.Browser.Fixture.Effects != Pass {
		return nil
	}
	form.Code = "form_observed"
	trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
	return nil
}
