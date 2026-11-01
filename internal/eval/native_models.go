package eval

import (
	"encoding/hex"
	"math"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

const (
	nativeModelScope    = "routed_native_model_records_v1"
	maxNativeModelCalls = 2048
)

type NativeModelInputs struct {
	Prompt        string `json:"prompt_sha256"`
	StableSystem  string `json:"stable_system_sha256"`
	DynamicSystem string `json:"dynamic_system_sha256"`
	Tools         string `json:"tools_sha256"`
	History       string `json:"history_sha256"`
	DynamicTail   string `json:"dynamic_tail_sha256"`
}

type NativeModelUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type NativeModelCall struct {
	AttemptID        string                                   `json:"attempt_id"`
	RequestProvider  string                                   `json:"request_provider"`
	RequestModel     string                                   `json:"request_model"`
	Inputs           NativeModelInputs                        `json:"inputs"`
	SystemSources    *diagnostictrace.SystemSourceFingerprint `json:"system_sources,omitempty"`
	ResponseProvider string                                   `json:"response_provider,omitempty"`
	ResponseModel    string                                   `json:"response_model,omitempty"`
	ResponseSHA256   string                                   `json:"response_sha256,omitempty"`
	UsageState       string                                   `json:"usage_state"`
	Usage            *NativeModelUsage                        `json:"usage,omitempty"`
	RetryObserved    bool                                     `json:"retry_observed,omitempty"`
}

type NativeModelTurn struct {
	Generation           int               `json:"generation,omitempty"`
	TrialID              string            `json:"trial_id"`
	TraceScope           events.TraceScope `json:"trace_scope"`
	BindingSHA256        string            `json:"binding_sha256"`
	Code                 string            `json:"code"`
	TraceID              string            `json:"trace_id,omitempty"`
	TraceSHA256          string            `json:"trace_sha256,omitempty"`
	SessionHash          string            `json:"session_hash,omitempty"`
	RetryFallbackRecords int               `json:"retry_fallback_records"`
	Calls                []NativeModelCall `json:"calls,omitempty"`
}

// Counts cover validated routed native records, not every billable call. No
// computed provider-total, billing total or cost is claimed by this projection.
type NativeModelTotals struct {
	Coverage       string `json:"coverage"`
	Requests       int    `json:"requests"`
	Responses      int    `json:"responses"`
	UsageResponses int    `json:"usage_responses"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
}

type NativeModelObservation struct {
	Scope    string                 `json:"scope"`
	Code     string                 `json:"code"`
	Reason   string                 `json:"reason,omitempty"`
	AllCalls string                 `json:"all_call_coverage"`
	Source   diagnostictrace.Source `json:"source"`
	Turns    []NativeModelTurn      `json:"turns,omitempty"`
	Totals   NativeModelTotals      `json:"totals"`
}

func (observation *NativeModelObservation) verdict() Verdict {
	if observation != nil && observation.Code == "leak_observed" {
		return Fail
	}
	return Pass
}

func nativeModelSourceKnown(runtime health.RuntimeProbe) bool {
	_, err := hex.DecodeString(runtime.GitCommit)
	return len(runtime.GitCommit) == 40 && err == nil && strings.ToLower(runtime.GitCommit) == runtime.GitCommit &&
		boundedText(runtime.BuildVersion, 128)
}

func summarizeNativeModels(turns []NativeModelTurn) NativeModelTotals {
	result := NativeModelTotals{Coverage: "unknown"}
	partial := false
	for _, turn := range turns {
		partial = partial || turn.Code != "observed"
		for _, call := range turn.Calls {
			result.Requests++
			if call.UsageState != "not_returned" {
				result.Responses++
			}
			if call.Usage == nil {
				continue
			}
			result.UsageResponses++
			if result.Coverage == "overflow" || call.Usage.InputTokens > math.MaxInt64-result.InputTokens ||
				call.Usage.OutputTokens > math.MaxInt64-result.OutputTokens {
				result.Coverage = "overflow"
				continue
			}
			result.InputTokens += call.Usage.InputTokens
			result.OutputTokens += call.Usage.OutputTokens
		}
	}
	if result.Coverage == "overflow" {
		result.InputTokens, result.OutputTokens = 0, 0
	} else if result.UsageResponses > 0 {
		result.Coverage = "partial_native_responses"
		if !partial && result.UsageResponses == result.Requests {
			result.Coverage = "observed_native_responses"
		}
	}
	return result
}

func (observation *NativeModelObservation) valid(runtime health.RuntimeProbe) bool {
	if observation == nil {
		return true
	}
	if observation.Scope != nativeModelScope || observation.AllCalls != "unknown" ||
		len(observation.Turns) > evalModelMetadataCapture().MaxTraces ||
		observation.Totals != summarizeNativeModels(observation.Turns) ||
		observation.Totals.Requests > maxNativeModelCalls {
		return false
	}
	if observation.Code == "unavailable" || observation.Code == "leak_observed" {
		return len(observation.Turns) == 0 && observation.Source == (diagnostictrace.Source{}) &&
			slices.Contains([]string{
				"not_stopped", "private_coverage_unknown", "source_unknown", "no_scopes",
				"scope_limit", "native_evidence_unavailable", "raw_private_evidence",
			}, observation.Reason) &&
			(observation.Code != "leak_observed" || observation.Reason == "raw_private_evidence")
	}
	if !slices.Contains([]string{"observed", "partial"}, observation.Code) || observation.Reason != "" ||
		!nativeModelSourceKnown(runtime) || observation.Source.Commit != runtime.GitCommit ||
		observation.Source.MintClawVersion != runtime.BuildVersion || len(observation.Turns) == 0 {
		return false
	}
	seen, traces, partial := map[string]bool{}, map[string]bool{}, false
	for _, turn := range observation.Turns {
		if turn.Generation < 0 || turn.Generation > maxOwnedGatewayGenerations ||
			!validID(turn.TrialID) || !turn.TraceScope.Complete() || !localPath(turn.TraceScope.Workspace) ||
			!validID(turn.TraceScope.TurnID) ||
			!validDigest(turn.BindingSHA256) || seen[turn.BindingSHA256] {
			return false
		}
		seen[turn.BindingSHA256] = true
		if turn.Code != "observed" {
			if !slices.Contains([]string{"missing_trace", "invalid_trace", "ambiguous_trace"}, turn.Code) ||
				turn.TraceID != "" || turn.TraceSHA256 != "" || turn.SessionHash != "" ||
				len(turn.Calls) != 0 || turn.RetryFallbackRecords != 0 {
				return false
			}
			partial = true
			continue
		}
		if !validID(turn.TraceID) || !validDigest(turn.TraceSHA256) || !validDigest(turn.SessionHash) ||
			traces[turn.TraceID] || traces[turn.TraceSHA256] ||
			turn.RetryFallbackRecords < 0 || turn.RetryFallbackRecords > evalModelMetadataCapture().MaxRecords {
			return false
		}
		traces[turn.TraceID], traces[turn.TraceSHA256] = true, true
		attempts, retries := map[string]bool{}, 0
		for _, call := range turn.Calls {
			if !call.valid() || attempts[call.AttemptID] {
				return false
			}
			attempts[call.AttemptID] = true
			if call.RetryObserved {
				retries++
			}
		}
		if retries > turn.RetryFallbackRecords {
			return false
		}
	}
	return (observation.Code == "partial") == partial
}

func (call NativeModelCall) valid() bool {
	if !validID(call.AttemptID) ||
		call.RequestProvider != "" && !boundedText(call.RequestProvider, 128) || !boundedText(call.RequestModel, 256) ||
		call.RetryObserved && call.UsageState != "not_returned" ||
		call.SystemSources != nil && call.SystemSources.Validate() != nil {
		return false
	}
	for _, value := range []string{
		call.Inputs.Prompt, call.Inputs.StableSystem, call.Inputs.DynamicSystem,
		call.Inputs.Tools, call.Inputs.History, call.Inputs.DynamicTail,
	} {
		if !validDigest(value) {
			return false
		}
	}
	if call.UsageState == "not_returned" {
		return call.ResponseProvider == "" && call.ResponseModel == "" && call.ResponseSHA256 == "" && call.Usage == nil
	}
	if call.ResponseProvider != "" && !boundedText(call.ResponseProvider, 128) ||
		call.ResponseModel != "" && !boundedText(call.ResponseModel, 256) ||
		call.ResponseSHA256 != "" && !validDigest(call.ResponseSHA256) {
		return false
	}
	if call.UsageState == "observed" {
		return call.Usage != nil && call.Usage.InputTokens >= 0 && call.Usage.OutputTokens >= 0
	}
	return slices.Contains([]string{"legacy_unknown", "unavailable"}, call.UsageState) && call.Usage == nil
}
