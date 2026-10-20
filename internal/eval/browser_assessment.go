package eval

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func validBrowserReadEvidence(trial Trial, item Case, workspace string) bool {
	if item.Browser == nil || item.Browser.Scenario != "read" || !validBrowserConversation(trial, item, workspace) ||
		trial.Browser.Artifact != nil || trial.Browser.Download != nil || trial.Browser.Denial != nil {
		return false
	}
	fixture := trial.Browser.Fixture
	return validBrowserTaskProof(trial, trial.Browser.Read, browserReadAssessmentScope) &&
		fixture != nil && fixture.Version == Version && fixture.Scope == "local_fixture_only" &&
		validDigest(fixture.PageSHA256) && validDigest(fixture.CSVSourceSHA256) &&
		fixture.FixtureClosed && fixture.Visits > 0 && fixture.Downloads == 0 &&
		fixture.Submissions == 0 && fixture.Removals == 0 && fixture.Effects == Pass &&
		fixture.EffectsCode == "matched" && fixture.Read == Pass && fixture.ReadCode == "matched"
}

func validBrowserConversation(trial Trial, item Case, workspace string) bool {
	if item.Browser == nil || trial.Browser == nil || !isBrowserForm(item) &&
		(len(item.Driver.Facts) != 0 || trial.Browser.FormInputs != nil || trial.Browser.Form != nil) ||
		trial.PDF != nil || trial.Cancellation != nil ||
		!isBrowserAmbiguity(item) && trial.Browser.Ambiguity != nil ||
		trial.Browser.Version != Version || trial.Browser.Scope != "owned_fixture_execution_v1" ||
		trial.Browser.NativeAuthority != "observed" || trial.Browser.NativeCleanup != "observed" ||
		!validID(trial.ID) || !validID(trial.SessionID) || !boundedIdentity(trial.SessionKey, 512) ||
		trial.ExecutionVerdict != Pass || !trial.TransportClosed || !observedRoleBoundaries(trial) ||
		!validTrialConversation(trial, trial.AgentID, workspace) {
		return false
	}
	return lastOutcome(trial.Events).Kind == "final" && item.Browser.Scenario != "denied" ||
		lastOutcome(trial.Events).Kind == "canceled" && item.Browser.Scenario == "denied"
}

func validBrowserTaskProof(trial Trial, proof *BrowserTaskEvidence, scope string) bool {
	if proof == nil || proof.Version != Version || proof.Scope != scope ||
		proof.Code != "native_post_stop_observed" || proof.ForbiddenActionAccepted ||
		!validDigest(proof.StateSHA256) || !validDigest(proof.OutboxSHA256) || !validDigest(proof.OriginSHA256) ||
		proof.SessionScope != browser.OpaqueSessionKey(trial.SessionKey) ||
		!boundedIdentity(proof.ProfileRevision, 128) || !validDigest(proof.PolicyRevision) ||
		proof.Sessions < 1 || proof.Sessions > browser.DefaultFileStoreRecords ||
		proof.Actions < 1 || proof.Invocations < proof.Actions || proof.Invocations > browser.DefaultFileStoreRecords ||
		proof.Navigations < 1 || proof.Navigations > proof.Invocations || !proof.LogicalSettled || !proof.TemporaryEmpty {
		return false
	}
	permissionPrompt := scope == browserQuestionDenialAssessmentScope || scope == browserFormAssessmentScope ||
		scope == browserAmbiguityAssessmentScope
	if permissionPrompt && proof.PermissionPromptDeliveries != 1 ||
		!permissionPrompt && proof.PermissionPromptDeliveries != 0 {
		return false
	}
	if scope == browserAmbiguityAssessmentScope && proof.ApprovalPromptDeliveries != 1 ||
		scope != browserAmbiguityAssessmentScope && proof.ApprovalPromptDeliveries != 0 {
		return false
	}
	finals := 0
	for _, event := range trial.Events {
		if event.Kind == "final" || event.Kind == "canceled" &&
			(scope == browserDenialAssessmentScope || scope == browserQuestionDenialAssessmentScope) {
			finals++
		}
	}
	return finals > 0 && proof.FinalDeliveries == finals
}

func observedBrowserHost(evidence *BrowserTrialEvidence, host *BrowserFixtureHostEvidence) bool {
	if evidence == nil || host == nil || !host.valid() || host.Code != "closed" || host.Trials < 1 {
		return false
	}
	proof := evidence.Read
	if evidence.Artifact != nil {
		proof = evidence.Artifact.Native
	}
	if evidence.Denial != nil {
		proof = evidence.Denial.Native
	}
	if evidence.Form != nil {
		proof = evidence.Form.Native
	}
	if evidence.Ambiguity != nil {
		proof = evidence.Ambiguity.Native
	}
	return proof != nil && host.OriginSHA256 == proof.OriginSHA256
}

func validBrowserEvidence(trial Trial, item Case, workspace string) bool {
	return validBrowserReadEvidence(trial, item, workspace) || validBrowserArtifactEvidence(trial, item, workspace) ||
		validBrowserDenialEvidence(trial, item, workspace) || validBrowserFormEvidence(trial, item, workspace) ||
		validBrowserAmbiguityEvidence(trial, item, workspace)
}

func gradeFinalizedBrowserChecks(item Case, trial Trial) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	source := "browser.read"
	if item.Browser != nil && item.Browser.Scenario == "artifact" {
		source = "browser.artifact"
	}
	if item.Browser != nil && item.Browser.Scenario == "denied" {
		source = "browser.denial"
	}
	if isBrowserForm(item) {
		source = "browser.form"
	}
	if isBrowserAmbiguity(item) {
		source = "browser.ambiguity"
	}
	for index, check := range item.Checks {
		switch check.Kind {
		case "browser_state",
			"browser_no_write",
			"browser_authority",
			"browser_cleanup",
			"browser_artifact",
			"browser_context":
			checks[index] = booleanCheck(check.ID, true, []string{"browser.fixture", source})
		case "private_surfaces":
			checks[index] = booleanCheck(check.ID, true, []string{"run.private_surfaces", source})
		}
	}
	return checks
}

func assessFinalizedBrowser(
	ctx context.Context, judge *ModelRole, item Case, trial *Trial, workspace string,
	lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation, host *BrowserFixtureHostEvidence,
) {
	for _, check := range trial.Checks {
		if check.Verdict == Fail {
			return
		}
	}
	if !observedOwnedProfile(lifecycle, surfaces) || !observedBrowserHost(trial.Browser, host) ||
		!validBrowserEvidence(*trial, item, workspace) {
		return
	}
	started := time.Now()
	defer func() { trial.DurationMS += time.Since(started).Milliseconds() }()
	trial.Checks = gradeFinalizedBrowserChecks(item, *trial)
	trial.EvidenceEligible = true
	trial.Verdict, trial.Code = trial.ExecutionVerdict, trial.ExecutionCode
	for _, check := range trial.Checks {
		trial.Verdict = combineVerdicts(trial.Verdict, check.Verdict)
	}
	if len(item.Rubric) == 0 {
		return
	}
	judgement := judgeConversation(ctx, judge, item, trial.Events)
	trial.Judge = &judgement
	retainBrowserJudgePrivacy(item, trial)
	if !roleBoundaryEligible(judgement.Call) {
		trial.EvidenceEligible = false
	}
	if requiredRubric(item.Rubric) || judgement.Code == "private_judge_output" ||
		!roleBoundaryEligible(judgement.Call) {
		if combined := combineVerdicts(trial.Verdict, judgement.Verdict); combined != trial.Verdict {
			trial.Verdict, trial.Code = combined, judgement.Code
		}
	}
}

func retainBrowserJudgePrivacy(item Case, trial *Trial) {
	if item.Browser == nil || trial.Browser == nil || trial.Judge == nil ||
		roleBoundaryEligible(trial.Judge.Call) && trial.Judge.Code != "private_judge_output" {
		return
	}
	trial.EvidenceEligible = false
	for index, check := range item.Checks {
		if check.Kind != "private_surfaces" || index >= len(trial.Checks) {
			continue
		}
		trial.Checks[index] = CheckResult{
			ID: check.ID, Verdict: Inconclusive, Code: "role_privacy_unavailable",
			EvidenceIDs: []string{"judge.call.boundary"},
		}
		if trial.Judge.Code == "private_judge_output" {
			trial.Checks[index].Verdict, trial.Checks[index].Code = Fail, "private_judge_output"
		}
	}
}
