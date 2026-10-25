package eval

func readRecoveryOwnedReady(report RunReport) bool {
	return observedOwnedProfile(report.TargetLifecycle, report.PrivateSurfaces) &&
		report.TargetBudget != nil && report.TargetBudget.verdict() == Pass
}

func gradeReadRecoveryChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "read_recovery" {
			continue
		}
		if !readRecoveryOwnedReady(report) || trial.ReadRecovery == nil || trial.ReadRecovery.Code != "observed" {
			checks[index].Code = "native_read_recovery_unavailable"
			continue
		}
		checks[index] = booleanCheck(
			check.ID,
			validReadRecoveryEvidence(trial, item, workspace),
			[]string{"read-recovery"},
		)
	}
	return checks
}
