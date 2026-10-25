package eval

import "context"

func gradeSteeringChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "steering_boundary" {
			continue
		}
		if report.ActiveTool == nil || !report.ActiveTool.valid(report) ||
			report.ActiveTool.verdict(report.TargetLifecycle) != Pass || report.TargetBudget == nil ||
			report.TargetBudget.verdict() != Pass ||
			!observedOwnedProfile(report.TargetLifecycle, report.PrivateSurfaces) ||
			trial.Steering == nil || trial.Steering.Code != "observed" {
			checks[index].Code = "native_steering_unavailable"
			continue
		}
		if item.Steering.Peer != nil && (trial.Isolation == nil || trial.Isolation.Peer.Steering == nil ||
			trial.Isolation.Peer.Steering.Code != "observed") {
			checks[index].Code = "native_isolation_unavailable"
			continue
		}
		checks[index] = booleanCheck(check.ID,
			validSteeringEvidence(trial, item, workspace, report.ActiveTool.Profile, report.Runtime),
			[]string{"steering"})
	}
	return checks
}

func assessCoreConversation(
	ctx context.Context,
	judge *ModelRole,
	item Case,
	trial *Trial,
	report RunReport,
	workspace string,
) {
	trial.Checks = gradeCoreChecks(item, *trial, report, workspace)
	trial.Verdict, trial.Code = trial.ExecutionVerdict, trial.ExecutionCode
	for _, check := range trial.Checks {
		trial.Verdict = combineVerdicts(trial.Verdict, check.Verdict)
	}
	trial.EvidenceEligible = trial.Verdict == Pass
	if trial.Verdict != Pass {
		trial.Code = "required_steering_check_not_passed"
		if item.ReadRecovery != nil {
			trial.Code = "required_read_recovery_check_not_passed"
		}
		if item.NativeStop != nil {
			trial.Code = "required_native_stop_check_not_passed"
		}
		return
	}
	judgement := judgeConversation(ctx, judge, item, steeringJudgeEvents(*trial))
	trial.Judge = &judgement
	if !roleBoundaryEligible(judgement.Call) {
		trial.EvidenceEligible = false
	}
	if requiredRubric(item.Rubric) || !roleBoundaryEligible(judgement.Call) {
		trial.Verdict = combineVerdicts(trial.Verdict, judgement.Verdict)
		if trial.Verdict != Pass {
			trial.Code = judgement.Code
		}
	}
}

func gradeCoreChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	if item.NativeStop != nil {
		return gradeNativeStopChecks(item, trial, report, workspace)
	}
	if item.ReadRecovery != nil {
		return gradeReadRecoveryChecks(item, trial, report, workspace)
	}
	return gradeSteeringChecks(item, trial, report, workspace)
}
