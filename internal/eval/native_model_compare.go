package eval

import (
	"encoding/json"
	"slices"
)

func compareNativeModels(result *Comparison, baseline, current RunReport) {
	if baseline.NativeModels == nil && current.NativeModels == nil {
		return
	}
	compareNativeSystemSources(result, baseline, current)
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
			continue
		}
		if !nativeResponseModelsKnown(observation) {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_native_response_models")
		}
		if !nativeModelProvidersKnown(observation, false) {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_native_request_providers")
		}
		if !nativeModelProvidersKnown(observation, true) {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_native_response_providers")
		}
	}
	if baseline.NativeModels == nil || current.NativeModels == nil ||
		baseline.NativeModels.Code != "observed" || current.NativeModels.Code != "observed" {
		return
	}
	requestProviders := nativeModelProvidersKnown(baseline.NativeModels, false) &&
		nativeModelProvidersKnown(current.NativeModels, false)
	responseProviders := nativeModelProvidersKnown(baseline.NativeModels, true) &&
		nativeModelProvidersKnown(current.NativeModels, true)
	aRequested, aResponses, aInputs := nativeModelComparisonInputs(baseline, requestProviders, responseProviders)
	bRequested, bResponses, bInputs := nativeModelComparisonInputs(current, requestProviders, responseProviders)
	if !slices.Equal(aRequested, bRequested) ||
		nativeResponseModelsKnown(baseline.NativeModels) && nativeResponseModelsKnown(current.NativeModels) &&
			!slices.Equal(aResponses, bResponses) {
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
			if call.ResponseModel == "" {
				return false
			}
		}
	}
	return true
}

func nativeModelProvidersKnown(observation *NativeModelObservation, response bool) bool {
	for _, turn := range observation.Turns {
		for _, call := range turn.Calls {
			if response && call.ResponseProvider == "" || !response && call.RequestProvider == "" {
				return false
			}
		}
	}
	return true
}

// Actual request shapes can change because of paths, time, different responses
// or tool loops. This records drift, not normalized inputs or causal attribution.
func nativeModelComparisonInputs(
	report RunReport,
	requestProviders, responseProviders bool,
) ([]string, []string, []string) {
	requested, responses, inputs := []string{}, []string{}, []string{}
	if report.NativeModels == nil {
		return requested, responses, inputs
	}
	cases := map[string]string{}
	for _, summary := range report.Trials {
		cases[summary.ID] = summary.CaseSHA256
	}
	for _, turn := range report.NativeModels.Turns {
		for _, call := range turn.Calls {
			requestProvider, responseProvider := "", ""
			if requestProviders {
				requestProvider = call.RequestProvider
			}
			if responseProviders {
				responseProvider = call.ResponseProvider
			}
			request, _ := json.Marshal([]string{
				cases[turn.TrialID], requestProvider, call.RequestModel,
			})
			response, _ := json.Marshal([]string{
				cases[turn.TrialID], requestProvider, call.RequestModel,
				responseProvider, call.ResponseModel,
			})
			input, _ := json.Marshal(struct {
				Case   string            `json:"case"`
				Inputs NativeModelInputs `json:"inputs"`
			}{cases[turn.TrialID], call.Inputs})
			requested = append(requested, string(request))
			responses = append(responses, string(response))
			inputs = append(inputs, string(input))
		}
	}
	slices.Sort(requested)
	slices.Sort(responses)
	slices.Sort(inputs)
	return requested, responses, inputs
}
