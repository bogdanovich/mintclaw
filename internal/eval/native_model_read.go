package eval

import (
	"context"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func unavailableNativeModels(reason string) *NativeModelObservation {
	return &NativeModelObservation{
		Scope: nativeModelScope, Code: "unavailable", Reason: reason,
		AllCalls: "unknown", Totals: NativeModelTotals{Coverage: "unknown"},
	}
}

func observeNativeModels(
	ctx context.Context, target admittedTarget, lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation,
	bindings []nativeModelBinding, facts map[string]string, filter *strings.Replacer,
) *NativeModelObservation {
	if len(bindings) > evalModelMetadataCapture().MaxTraces {
		return unavailableNativeModels("scope_limit")
	}
	result := &NativeModelObservation{
		Scope: nativeModelScope, Code: "observed", AllCalls: "unknown",
		Source: diagnostictrace.Source{Commit: target.probe.GitCommit, MintClawVersion: target.probe.BuildVersion},
	}
	for _, binding := range bindings {
		result.Turns = append(result.Turns, NativeModelTurn{
			TrialID: binding.TrialID, TraceScope: binding.TraceScope,
			BindingSHA256: binding.hash(), Code: "missing_trace",
		})
	}
	var replace func(string) string
	if filter != nil {
		replace = filter.Replace
	}
	reason := visitBoundNativeTraces(ctx, target, lifecycle, surfaces, bindings, facts, filter,
		func(index int, trace diagnostictrace.Trace, sha string) {
			binding := bindings[index]
			if result.Turns[index].Code != "missing_trace" {
				result.Turns[index] = NativeModelTurn{
					TrialID: binding.TrialID, TraceScope: binding.TraceScope,
					BindingSHA256: binding.hash(), Code: "ambiguous_trace",
				}
				return
			}
			result.Turns[index] = projectNativeModelTrace(trace, binding, sha, replace)
		})
	if reason != "" {
		failed := unavailableNativeModels(reason)
		if reason == "raw_private_evidence" {
			failed.Code = "leak_observed"
		}
		return failed
	}
	if slices.ContainsFunc(result.Turns, func(turn NativeModelTurn) bool { return turn.Code != "observed" }) {
		result.Code = "partial"
	}
	result.Totals = summarizeNativeModels(result.Turns)
	if !result.valid(target.probe) {
		return unavailableNativeModels("native_evidence_unavailable")
	}
	return result
}

func readRunModelBindings(writer *privateReport, report RunReport, workspace string) ([]nativeModelBinding, error) {
	bindings := []nativeModelBinding{}
	for _, summary := range report.Trials {
		data, err := readPrivateReportFile(writer.root, summary.File)
		if err != nil {
			return nil, ErrReport
		}
		trial, err := decodeDocument[Trial](data, 8*1024*1024)
		clear(data)
		if err != nil || trial.ID != summary.ID || trial.CaseSHA256 != summary.CaseSHA256 {
			return nil, ErrReport
		}
		current, valid := nativeModelBindings(trial, workspace)
		if !valid {
			return nil, errNativeEvidence
		}
		bindings = append(bindings, current...)
		if len(bindings) > evalModelMetadataCapture().MaxTraces {
			return bindings, nil
		}
	}
	return bindings, nil
}
