package eval

func gradeSkillSelectionChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "skill_selection" {
			continue
		}
		if !readRecoveryOwnedReady(report) || trial.SkillSelection == nil || trial.SkillSelection.Code != "observed" {
			checks[index].Code = "native_skill_selection_unavailable"
			continue
		}
		code := skillSelectionEvidenceCode(trial, item, workspace)
		checks[index] = booleanCheck(check.ID, code == "", []string{"skill-selection"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}
