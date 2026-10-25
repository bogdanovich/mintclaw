package eval

import "strings"

func gradeConversation(checks []Check, evidence []Event, last Event) []CheckResult {
	results := make([]CheckResult, 0, len(checks))
	for _, check := range checks {
		result := CheckResult{ID: check.ID, Verdict: Inconclusive, Code: "missing_evidence", EvidenceIDs: []string{}}
		switch check.Kind {
		case "response_contains":
			if last.ID != "" {
				matched := strings.Contains(last.Content, check.Expected)
				result = booleanCheck(check.ID, matched, []string{last.ID})
			}
		case "final_response_absent":
			if last.ID != "" && last.Kind == "final" {
				result = booleanCheck(check.ID, !strings.Contains(last.Content, check.Expected), []string{last.ID})
			}
		case "response_absent":
			for _, event := range evidence {
				if event.Kind == "input" || event.Content == "" {
					continue
				}
				if strings.Contains(event.Content, check.Expected) {
					result = booleanCheck(check.ID, false, []string{event.ID})
					break
				}
				result.EvidenceIDs = append(result.EvidenceIDs, event.ID)
			}
			if result.Verdict != Fail && len(result.EvidenceIDs) > 0 {
				result.Verdict, result.Code = Pass, "matched"
			}
		case "last_outcome":
			if last.ID != "" {
				result = booleanCheck(check.ID, last.Kind == check.Expected, []string{last.ID})
			}
		case "same_session":
			key := ""
			requests := make(map[string]bool)
			for _, event := range evidence {
				if event.Kind == "final" || event.Kind == "question" || event.Kind == "approval" ||
					event.Kind == "canceled" || event.Kind == "stopped" {
					if key == "" {
						key = event.SessionKey
					}
					if event.SessionKey == "" || event.SessionKey != key {
						result = booleanCheck(check.ID, false, []string{event.ID})
						break
					}
					result.EvidenceIDs = append(result.EvidenceIDs, event.ID)
					if event.RequestID != "" {
						requests[event.RequestID] = true
					}
				}
			}
			if result.Verdict != Fail && key != "" && len(requests) >= 2 {
				result.Verdict, result.Code = Pass, "matched"
			}
		case "interaction_observed":
			for _, event := range evidence {
				if event.Kind == "question" && boundedIdentity(event.InteractionShortID, 64) &&
					boundedIdentity(event.MessageID, 128) {
					result = booleanCheck(check.ID, true, []string{event.ID})
					break
				}
			}
		}
		results = append(results, result)
	}
	return results
}

func booleanCheck(id string, matched bool, evidence []string) CheckResult {
	result := CheckResult{ID: id, Verdict: Fail, Code: "mismatch", EvidenceIDs: evidence}
	if matched {
		result.Verdict, result.Code = Pass, "matched"
	}
	return result
}
