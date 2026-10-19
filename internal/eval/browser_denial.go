package eval

import (
	"context"
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const browserDenialAssessmentScope = "owned_post_stop_browser_denial_v1"

const browserQuestionDenialAssessmentScope = "owned_post_stop_browser_question_cancel_v1"

// The scope distinguishes a canceled prepared action from an ordinary question.
// Neither is a grant or a claim about which account control would be dispatched.
type BrowserDenialEvidence struct {
	Version                int                              `json:"version"`
	Scope                  string                           `json:"scope"`
	Code                   string                           `json:"code"`
	InteractionKind        string                           `json:"interaction_kind,omitempty"`
	PermissionPromptSHA256 string                           `json:"permission_prompt_sha256,omitempty"`
	PreparedActionID       string                           `json:"prepared_action_id,omitempty"`
	ActionHash             string                           `json:"action_hash,omitempty"`
	PolicyRevision         string                           `json:"policy_revision,omitempty"`
	CancelRequestedAt      int64                            `json:"cancel_requested_at"`
	Cancellation           *InteractionCancellationEvidence `json:"cancellation"`
	Native                 *BrowserTaskEvidence             `json:"native,omitempty"`
}

func (domain *browserConversation) matchesApprovalAction(action browser.PreparedAction) bool {
	if domain.item.Browser == nil {
		return false
	}
	switch domain.item.Browser.Scenario {
	case "artifact":
		return action.Action.Kind == browser.ActionDownload && action.Action.Deliver && action.ElementRole == "link" &&
			action.ElementName == "Download fixture report" && action.Effect == browser.EffectUnknown &&
			action.PolicyEffect == browser.EffectUnknown
	case "denied":
		return fixtureRemovalAction(action)
	case "ambiguous":
		return isBrowserAmbiguity(domain.item) && fixtureSouthRemovalAction(action)
	case "submit", "followup":
		return fixtureSubmitAction(action)
	default:
		return false
	}
}

func fixtureRemovalAction(action browser.PreparedAction) bool {
	return action.Action.Kind == browser.ActionClick && !action.Action.Deliver && action.ElementRole == "button" &&
		action.ElementName == "Remove account" && action.PolicyEffect == browser.EffectExternalCommit &&
		action.InputBytes == 0 && action.ArtifactBytes == 0
}

func (domain *browserConversation) prepareCancellation(prompt Event) error {
	if domain.item.Browser == nil || domain.item.Browser.Scenario != "denied" {
		return errBrowserEvidence
	}
	before := domain.approval
	if before == nil || before.requestID != "" || !domain.questions.cancelAt.IsZero() ||
		prompt.MessageID != before.prompt.MessageID || prompt.InteractionID != before.record.ID {
		return errBrowserEvidence
	}
	current, err := domain.readApproval(prompt)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, before) {
		return errBrowserEvidence
	}
	domain.questions.prompt, domain.questions.waiting = prompt, before.record
	domain.questions.cancelAt = time.Now()
	return nil
}

func (domain *browserConversation) observeCancellation(ctx context.Context, prompt, reply Event) error {
	if domain.item.Browser.Scenario != "denied" || prompt.Kind != "approval" {
		return domain.questions.observeCancellation(ctx, prompt, reply)
	}
	return domain.questions.observeCancellationKind(prompt, reply, interactions.KindApproval)
}

func (domain *browserConversation) denialEvidence() *BrowserDenialEvidence {
	if domain.questions.cancellation == nil {
		return nil
	}
	proof := &BrowserDenialEvidence{
		Version: Version, Scope: browserDenialAssessmentScope, Code: "native_cancel_observed",
		CancelRequestedAt: domain.questions.cancelAt.UnixMilli(), Cancellation: domain.questions.cancellation,
	}
	if domain.approval == nil {
		if domain.questions.waiting.Kind != interactions.KindQuestion {
			return nil
		}
		proof.Scope, proof.InteractionKind = browserQuestionDenialAssessmentScope, "question"
		proof.PermissionPromptSHA256 = digest([]byte(domain.questions.prompt.Content))
		return proof
	}
	if domain.approval.requestID != "" {
		return nil
	}
	proof.PreparedActionID, proof.ActionHash = domain.approval.action.ID, domain.approval.action.ActionHash
	proof.PolicyRevision = domain.approval.action.PolicyRevision
	return proof
}

func browserDenialKind(proof *BrowserDenialEvidence) (interactions.Kind, bool) {
	if proof == nil {
		return "", false
	}
	switch proof.Scope {
	case browserDenialAssessmentScope:
		return interactions.KindApproval, proof.InteractionKind == "" && proof.PermissionPromptSHA256 == "" &&
			validID(proof.PreparedActionID) &&
			validDigest(proof.ActionHash) && validDigest(proof.PolicyRevision)
	case browserQuestionDenialAssessmentScope:
		return interactions.KindQuestion, proof.InteractionKind == "question" && proof.PreparedActionID == "" &&
			proof.ActionHash == "" && proof.PolicyRevision == "" && validDigest(proof.PermissionPromptSHA256)
	default:
		return "", false
	}
}

func validBrowserCancellation(trial Trial, reply Event, workspace string) bool {
	if trial.Browser == nil {
		return false
	}
	return validBrowserCancellationProof(trial.Browser.Denial, trial, reply, workspace)
}

func validBrowserCancellationProof(proof *BrowserDenialEvidence, trial Trial, reply Event, workspace string) bool {
	kind, valid := browserDenialKind(proof)
	if kind == interactions.KindQuestion {
		_, matched := browserPermissionPrompt(trial, proof)
		valid = valid && matched
	}
	return valid && proof.Version == Version &&
		(proof.Code == "native_cancel_observed" || proof.Code == "denial_observed") &&
		proof.Cancellation != nil && proof.CancelRequestedAt >= trial.StartedAt.UnixMilli() &&
		proof.CancelRequestedAt <= proof.Cancellation.ResolvedAt &&
		validInteractionCancellation(proof.Cancellation, trial, reply, workspace, string(kind))
}

func finalizeBrowserDenial(
	trial *Trial, pending browserPendingTrial, target admittedTarget, observed browserNativeSnapshot, empty bool,
) error {
	if trial.Browser == nil || trial.Browser.Denial == nil || pending.conversation == nil {
		return nil
	}
	domain, proof := pending.conversation, trial.Browser.Denial
	if !reflect.DeepEqual(proof, domain.denialEvidence()) ||
		!validBrowserCancellation(*trial, lastOutcome(trial.Events), target.preset.ExpectedWorkspace) {
		return nil
	}
	kind, _ := browserDenialKind(proof)
	record, err := domain.questions.cancelledRecord(kind)
	if err != nil {
		return err
	}
	if record.Revision != proof.Cancellation.TerminalRevision || record.ResolvedAt != proof.Cancellation.ResolvedAt {
		return nil
	}
	if kind == interactions.KindApproval {
		action, found := observed.state.PreparedActions[proof.PreparedActionID]
		if domain.approval == nil || !found || !reflect.DeepEqual(action, domain.approval.action) {
			return nil
		}
	}
	proof.Native = inspectBrowserTask(
		*trial,
		target,
		pending.fixture,
		observed,
		empty,
		browserTaskEffects{denial: proof},
	)
	if proof.Native.Code == "native_post_stop_observed" && trial.Browser.Fixture != nil &&
		trial.Browser.Fixture.Effects == Pass {
		proof.Code = "denial_observed"
		trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
	}
	return nil
}

func validBrowserDenialEvidence(trial Trial, item Case, workspace string) bool {
	if item.Browser == nil || item.Browser.Scenario != "denied" || !validBrowserConversation(trial, item, workspace) ||
		trial.Browser.Read != nil || trial.Browser.Artifact != nil || trial.Browser.Download != nil ||
		!validBrowserCancellation(trial, lastOutcome(trial.Events), workspace) {
		return false
	}
	proof, fixture := trial.Browser.Denial, trial.Browser.Fixture
	if proof.Scope == browserQuestionDenialAssessmentScope && !requiredRubric(item.Rubric) {
		return false
	}
	return proof.Code == "denial_observed" &&
		validBrowserTaskProof(trial, proof.Native, proof.Scope) && fixture != nil &&
		fixture.Version == Version && fixture.Scope == "local_fixture_only" && validDigest(fixture.PageSHA256) &&
		validDigest(fixture.CSVSourceSHA256) && fixture.FixtureClosed && fixture.Visits > 0 &&
		fixture.Downloads == 0 && fixture.Submissions == 0 && fixture.Removals == 0 &&
		fixture.Effects == Pass && fixture.EffectsCode == "matched"
}
