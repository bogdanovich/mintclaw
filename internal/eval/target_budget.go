package eval

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
)

const boundedCallsCapability = "bounded_calls"

// TargetBudgetObservation is run-scoped: repetitions share one owned child and
// ceiling. It never claims per-trial billing or coverage of provider-internal work.
type TargetBudgetObservation struct {
	Version     int             `json:"version"`
	Spec        evaltarget.Spec `json:"spec"`
	Code        string          `json:"code"`
	Proof       string          `json:"proof,omitempty"`
	ProofSHA256 string          `json:"proof_sha256,omitempty"`
}

func requiresTargetBudget(item Case) bool {
	return slices.Contains(item.Requires, boundedCallsCapability)
}

func targetBudgetLimitsDiffer(before, after *TargetBudgetObservation) bool {
	if before == nil || after == nil {
		return (before == nil) != (after == nil)
	}
	return before.Spec.Limits != after.Spec.Limits || before.Spec.Version != after.Spec.Version
}

func (observation *TargetBudgetObservation) valid(report RunReport) bool {
	if observation == nil {
		return true
	}
	if _, err := observation.Spec.Encode(); err != nil || observation.Version != Version ||
		report.Runtime.Version != 1 || report.TargetLifecycle == nil || !report.TargetLifecycle.valid() ||
		report.PrivateSurfaces == nil ||
		report.Runtime.PID != report.TargetLifecycle.PID {
		return false
	}
	if observation.Code == "receipt_unavailable" || observation.Code == "private_receipt" {
		return observation.Proof == "" && observation.ProofSHA256 == ""
	}
	if observation.Code != "observed" || observation.ProofSHA256 != digest([]byte(observation.Proof)) {
		return false
	}
	_, err := evaltarget.DecodeProof([]byte(observation.Proof), observation.Spec, report.Runtime.PID)
	return err == nil
}

func (observation *TargetBudgetObservation) verdict() Verdict {
	if observation == nil {
		return Pass
	}
	if observation.Code == "private_receipt" {
		return Fail
	}
	if observation.Code != "observed" {
		return Inconclusive
	}
	// Structural validation and launch identity binding happen at admission of
	// the containing report. Re-decode here rather than retaining a second state.
	var proofPID struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal([]byte(observation.Proof), &proofPID) != nil {
		return Inconclusive
	}
	proof, err := evaltarget.DecodeProof([]byte(observation.Proof), observation.Spec, proofPID.PID)
	if err != nil {
		return Inconclusive
	}
	state := proof.Snapshot
	if state.StoppedReason != "" && state.StoppedReason != "unknown_usage" ||
		state.ReportedTokens >= state.Limits.MaxReportedTokens {
		return Fail
	}
	if !proof.GatewayClean || state.Calls == 0 || state.InFlight != 0 || !state.UsageKnown ||
		state.StoppedReason != "" || state.ReportedTokens == 0 {
		return Inconclusive
	}
	return Pass
}

func observeTargetBudget(
	child *ownedGateway, facts map[string]string, filter *strings.Replacer,
) *TargetBudgetObservation {
	if child == nil || child.budgetSpec == nil {
		return nil
	}
	observation := &TargetBudgetObservation{
		Version: Version, Spec: *child.budgetSpec, Code: "receipt_unavailable",
	}
	select {
	case <-child.done:
	default:
		return observation
	}
	root, err := os.OpenRoot(child.profile.home)
	if err != nil {
		return observation
	}
	defer func() { _ = root.Close() }()
	name, err := child.budgetSpec.ReceiptName()
	if err != nil {
		return observation
	}
	raw, _, err := readCensusFile(root, name, evaltarget.MaxJSONBytes)
	if err != nil {
		return observation
	}
	defer clear(raw)
	// Inspect raw JSON, including duplicate/unknown members, before projecting
	// the metadata-only proof. Never retain a rejected credential-bearing receipt.
	if err = inspectNativeJSON(raw, facts, filter); err != nil {
		if errors.Is(err, ErrPrivateEvidence) {
			observation.Code = "private_receipt"
		}
		return observation
	}
	if _, err = evaltarget.DecodeProof(raw, observation.Spec, child.command.Process.Pid); err != nil {
		return observation
	}
	observation.Code, observation.Proof, observation.ProofSHA256 = "observed", string(raw), digest(raw)
	return observation
}
