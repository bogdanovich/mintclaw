package eval

import (
	"encoding/json"
	"slices"
)

func compareNativeModels(result *Comparison, baseline, current RunReport) {
	if baseline.NativeModels == nil && current.NativeModels == nil {
		return
	}
	result.EnvironmentCoverage = "recorded_runtime_roles_and_partial_native_records"
	if baseline.NativeModels != nil && current.NativeModels != nil &&
		baseline.NativeModels.Code == "observed" && current.NativeModels.Code == "observed" {
		result.EnvironmentCoverage = "recorded_runtime_roles_and_routed_native_records"
	}
	for _, source := range []struct {
		name   string
		report RunReport
	}{{"baseline", baseline}, {"current", current}} {
		observation := source.report.NativeModels
		if observation == nil || observation.Code != "observed" {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_native_record_coverage")
		} else if !nativeResponseModelsKnown(observation) {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_native_response_models")
		}
	}
	if baseline.NativeModels == nil || current.NativeModels == nil ||
		baseline.NativeModels.Code != "observed" || current.NativeModels.Code != "observed" {
		return
	}
	aModels, aInputs := nativeModelComparisonInputs(baseline)
	bModels, bInputs := nativeModelComparisonInputs(current)
	if nativeResponseModelsKnown(baseline.NativeModels) && nativeResponseModelsKnown(current.NativeModels) &&
		!slices.Equal(aModels, bModels) {
		result.EnvironmentChanges = append(result.EnvironmentChanges, "native_requested_or_response_models")
	}
	if !slices.Equal(aInputs, bInputs) {
		result.EnvironmentChanges = append(result.EnvironmentChanges, "native_effective_requests")
	}
}

func nativeResponseModelsKnown(observation *NativeModelObservation) bool {
	if observation.Totals.Responses != observation.Totals.Requests {
		return false
	}
	for _, turn := range observation.Turns {
		for _, call := range turn.Calls {
			if call.ResponseProvider == "" || call.ResponseModel == "" {
				return false
			}
		}
	}
	return true
}

// Actual request shapes can change because of paths, time, different responses
// or tool loops. This records drift, not normalized inputs or causal attribution.
func nativeModelComparisonInputs(report RunReport) ([]string, []string) {
	models, inputs := []string{}, []string{}
	if report.NativeModels == nil {
		return models, inputs
	}
	cases := map[string]string{}
	for _, summary := range report.Trials {
		cases[summary.ID] = summary.CaseSHA256
	}
	for _, turn := range report.NativeModels.Turns {
		for _, call := range turn.Calls {
			model, _ := json.Marshal([]string{
				cases[turn.TrialID], call.RequestProvider, call.RequestModel,
				call.ResponseProvider, call.ResponseModel,
			})
			input, _ := json.Marshal(struct {
				Case   string            `json:"case"`
				Inputs NativeModelInputs `json:"inputs"`
			}{cases[turn.TrialID], call.Inputs})
			models, inputs = append(models, string(model)), append(inputs, string(input))
		}
	}
	slices.Sort(models)
	slices.Sort(inputs)
	return models, inputs
}
