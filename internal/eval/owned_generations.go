package eval

import (
	"reflect"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// Each entry is one actual child receipt. Top-level lifecycle/budget still
// describe the last child, never a fabricated aggregate native receipt.
func validTargetGenerations(report RunReport) bool {
	if len(report.TargetGenerations) == 0 {
		return true
	}
	if len(report.TargetGenerations) < 2 || len(report.TargetGenerations) > maxOwnedGatewayGenerations {
		return false
	}
	seenPID, seenInstance, seenNonce := map[int]bool{}, map[string]bool{}, map[string]bool{}
	var remaining providers.CallBudgetLimits
	for index, generation := range report.TargetGenerations {
		runtime := generation.Runtime
		if generation.Number != index+1 || runtime.Version != 1 || runtime.PID <= 1 ||
			seenPID[runtime.PID] || !validDigest(runtime.InstanceSHA256) || seenInstance[runtime.InstanceSHA256] ||
			!validDigest(runtime.HomeSHA256) || !validDigest(runtime.ConfigPathSHA256) ||
			!validDigest(runtime.ConfigSHA256) || generation.Lifecycle.PID != runtime.PID ||
			!generation.Lifecycle.valid() || generation.Surfaces == nil || !generation.Surfaces.valid() ||
			generation.Budget == nil || !generation.Budget.valid(RunReport{
			Runtime: runtime, TargetLifecycle: &generation.Lifecycle, PrivateSurfaces: generation.Surfaces,
		}) || !generation.Models.valid(runtime) {
			return false
		}
		if index > 0 {
			previous := report.TargetGenerations[index-1].Runtime
			if runtime.HomeSHA256 != previous.HomeSHA256 || runtime.ConfigPathSHA256 != previous.ConfigPathSHA256 ||
				runtime.ConfigSHA256 != previous.ConfigSHA256 || runtime.GitCommit != previous.GitCommit ||
				runtime.BuildVersion != previous.BuildVersion || generation.Budget.Spec.Limits != remaining {
				return false
			}
		}
		if seenNonce[generation.Budget.Spec.Nonce] {
			return false
		}
		seenPID[runtime.PID], seenInstance[runtime.InstanceSHA256] = true, true
		seenNonce[generation.Budget.Spec.Nonce] = true
		if index < len(report.TargetGenerations)-1 {
			proof, err := evaltarget.DecodeProof([]byte(generation.Budget.Proof), generation.Budget.Spec, runtime.PID)
			if err != nil || generation.Budget.verdict() != Pass {
				return false
			}
			remaining = generation.Budget.Spec.Limits
			remaining.MaxCalls -= proof.Snapshot.Calls
			remaining.MaxReportedTokens -= proof.Snapshot.ReportedTokens
			if _, err := providers.NewCallBudget(remaining); err != nil {
				return false
			}
		}
	}
	last := report.TargetGenerations[len(report.TargetGenerations)-1]
	return reflect.DeepEqual(last.Runtime, report.Runtime) &&
		reflect.DeepEqual(&last.Lifecycle, report.TargetLifecycle) &&
		reflect.DeepEqual(
			last.Surfaces,
			report.PrivateSurfaces,
		) && reflect.DeepEqual(last.Budget, report.TargetBudget) &&
		reflect.DeepEqual(generationModels(report.TargetGenerations), report.NativeModels)
}

func generationModels(generations []ownedGatewayGeneration) *NativeModelObservation {
	result := &NativeModelObservation{Scope: nativeModelScope, Code: "observed", AllCalls: "unknown"}
	for _, generation := range generations {
		if generation.Models == nil || generation.Models.Code != "observed" {
			return unavailableNativeModels("native_evidence_unavailable")
		}
		result.Source = generation.Models.Source
		result.Turns = append(result.Turns, generation.Models.Turns...)
	}
	result.Totals = summarizeNativeModels(result.Turns)
	return result
}

func targetGenerationsVerdict(report RunReport) Verdict {
	for _, generation := range report.TargetGenerations {
		if generation.Surfaces != nil && generation.Surfaces.LeakObserved ||
			generation.Budget != nil && generation.Budget.Code == "private_receipt" ||
			generation.Models != nil && generation.Models.Code == "leak_observed" {
			return Fail
		}
	}
	if !validTargetGenerations(report) {
		return Inconclusive
	}
	verdict := Pass
	for _, generation := range report.TargetGenerations {
		verdict = combineVerdicts(verdict, generation.Lifecycle.verdict(), generation.Surfaces.verdict(),
			generation.Budget.verdict(), generation.Models.verdict())
		if generation.Models == nil || generation.Models.Code != "observed" ||
			!observedOwnedProfile(&generation.Lifecycle, generation.Surfaces) {
			verdict = combineVerdicts(verdict, Inconclusive)
		}
		if generation.Models == nil {
			continue
		}
		proof, err := evaltarget.DecodeProof(
			[]byte(generation.Budget.Proof),
			generation.Budget.Spec,
			generation.Runtime.PID,
		)
		if err != nil || proof.Snapshot.Calls != generation.Models.Totals.Requests ||
			generation.Models.Totals.Coverage != "observed_native_responses" ||
			proof.Snapshot.ReportedTokens != generation.Models.Totals.InputTokens+generation.Models.Totals.OutputTokens {
			return Inconclusive
		}
		for _, turn := range generation.Models.Turns {
			if turn.Generation != generation.Number {
				return Inconclusive
			}
		}
	}
	return verdict
}

func initialTargetBudget(report RunReport) *TargetBudgetObservation {
	if len(report.TargetGenerations) > 0 {
		return report.TargetGenerations[0].Budget
	}
	return report.TargetBudget
}
