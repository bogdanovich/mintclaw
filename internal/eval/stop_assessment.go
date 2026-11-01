package eval

func nativeStopOwnedReady(report RunReport) bool {
	return report.ActiveTool != nil && report.ActiveTool.valid(report) &&
		report.ActiveTool.verdict(report.TargetLifecycle) == Pass &&
		observedOwnedProfile(report.TargetLifecycle, report.PrivateSurfaces) &&
		report.TargetBudget != nil && report.TargetBudget.verdict() == Pass
}

func gradeNativeStopChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "native_stop" {
			continue
		}
		if !nativeStopOwnedReady(report) || trial.NativeStop == nil || trial.NativeStop.Code != "observed" {
			checks[index].Code = "native_stop_unavailable"
			continue
		}
		code := nativeStopEvidenceCode(trial, item, workspace, report.ActiveTool.Profile)
		checks[index] = booleanCheck(check.ID, code == "", []string{"native-stop"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}
