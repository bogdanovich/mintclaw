package eval

func approvalDeniedOwnedReady(report RunReport) bool {
	return readRecoveryOwnedReady(report) && report.PrivateSurfaces != nil &&
		report.PrivateSurfaces.AuthorizedInteractionKeys == 1
}

func gradeApprovalDeniedChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "approval_denied" {
			continue
		}
		if !approvalDeniedOwnedReady(report) || trial.ApprovalDenied == nil || trial.ApprovalDenied.Code != "observed" {
			checks[index].Code = "native_approval_denied_unavailable"
			continue
		}
		code := approvalDeniedEvidenceCode(trial, item, workspace)
		checks[index] = booleanCheck(check.ID, code == "", []string{"approval-denied"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}
