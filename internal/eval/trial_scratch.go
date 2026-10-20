package eval

import "slices"

const trialScratchScope = "private_trial_staging_v1"

// This is the runner's private evidence staging, not a native PDF/browser
// lease, remote cancellation, or whole-profile isolation between trials.
type TrialScratchObservation struct {
	Scope  string                 `json:"scope"`
	Trials []TrialScratchEvidence `json:"trials"`
}

type TrialScratchEvidence struct {
	TrialID          string `json:"trial_id"`
	SessionID        string `json:"session_id"`
	CaseSHA256       string `json:"case_sha256"`
	Code             string `json:"code"`
	CreatedAt        int64  `json:"created_at,omitempty"`
	CheckpointSHA256 string `json:"checkpoint_sha256,omitempty"`
}

func (observation *TrialScratchObservation) validFor(report RunReport) bool {
	if observation == nil {
		return true
	}
	if observation.Scope != trialScratchScope || len(observation.Trials) != len(report.Trials) ||
		len(observation.Trials) > MaxCorpusCases {
		return false
	}
	summaries := make(map[string]TrialSummary, len(report.Trials))
	for _, summary := range report.Trials {
		summaries[summary.ID] = summary
	}
	seen, sessions := map[string]bool{}, map[string]bool{}
	for _, evidence := range observation.Trials {
		summary, found := summaries[evidence.TrialID]
		if !found || seen[evidence.TrialID] || sessions[evidence.SessionID] ||
			!validID(evidence.TrialID) || !validID(evidence.SessionID) || !validDigest(evidence.CaseSHA256) ||
			evidence.CaseSHA256 != summary.CaseSHA256 || evidence.CreatedAt < 0 ||
			!slices.Contains([]string{"removed", "retained", "unavailable", "not_attempted"}, evidence.Code) {
			return false
		}
		switch evidence.Code {
		case "removed", "retained":
			if evidence.CreatedAt == 0 || !validDigest(evidence.CheckpointSHA256) {
				return false
			}
		case "unavailable", "not_attempted":
			if evidence.CreatedAt != 0 || evidence.CheckpointSHA256 != "" || summary.Verdict == Pass {
				return false
			}
		}
		seen[evidence.TrialID], sessions[evidence.SessionID] = true, true
	}
	return true
}

func (observation *TrialScratchObservation) matches(trial Trial) bool {
	if observation == nil {
		return true
	}
	for _, evidence := range observation.Trials {
		if evidence.TrialID != trial.ID {
			continue
		}
		if evidence.SessionID != trial.SessionID || evidence.CaseSHA256 != trial.CaseSHA256 {
			return false
		}
		if evidence.Code == "not_attempted" || evidence.Code == "unavailable" {
			return len(trial.Events) == 0 && len(trial.DriverCalls) == 0 &&
				(trial.Judge == nil || trial.Judge.Call == nil) && trial.Verdict != Pass && trial.ExecutionVerdict != Pass
		}
		return true
	}
	return false
}

func (observation *TrialScratchObservation) verdict() Verdict {
	if observation == nil {
		return Pass
	}
	for _, evidence := range observation.Trials {
		if evidence.Code != "removed" && evidence.Code != "not_attempted" {
			return Inconclusive
		}
	}
	return Pass
}

func selectTrialScratch(observation *TrialScratchObservation, identities map[string]bool) *TrialScratchObservation {
	if observation == nil {
		return nil
	}
	selected := &TrialScratchObservation{Scope: observation.Scope, Trials: []TrialScratchEvidence{}}
	for _, evidence := range observation.Trials {
		if identities[evidence.TrialID] {
			selected.Trials = append(selected.Trials, evidence)
		}
	}
	return selected
}
