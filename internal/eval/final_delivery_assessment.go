package eval

func gradeFinalDeliveryChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "final_delivery" {
			continue
		}
		if !readRecoveryOwnedReady(report) || trial.FinalDelivery == nil || trial.FinalDelivery.Code != "observed" {
			checks[index].Code = "native_final_delivery_unavailable"
			continue
		}
		code := finalDeliveryEvidenceCode(trial, item, workspace)
		checks[index] = booleanCheck(check.ID, code == "", []string{"final-delivery"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}
