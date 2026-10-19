package eval

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func nativeSystemSourceTestFingerprint() *diagnostictrace.SystemSourceFingerprint {
	return &diagnostictrace.SystemSourceFingerprint{
		Scope: diagnostictrace.SystemSourceScopeV1, Code: "observed", SystemMessages: 1, SystemParts: 3,
		Workspace:    diagnostictrace.SystemSourceGroup{Parts: 1, SHA256: digest([]byte(`["workspace"]`))},
		SkillCatalog: diagnostictrace.SystemSourceGroup{Parts: 1, SHA256: digest([]byte(`["catalog"]`))},
		ActiveSkills: diagnostictrace.SystemSourceGroup{Parts: 1, SHA256: digest([]byte(`["active"]`))},
	}
}

func makeNativeSystemSourceTestTrace(binding nativeModelBinding) (diagnostictrace.Trace, error) {
	trace, err := makeNativeModelTestTrace(binding)
	if err != nil {
		return trace, err
	}
	for index := range trace.Records {
		record := &trace.Records[index]
		if record.Kind != diagnostictrace.RecordModelRequest {
			continue
		}
		var payload diagnostictrace.ModelPayload
		if err = json.Unmarshal(record.Data, &payload); err != nil {
			return trace, err
		}
		payload.SystemSources = nativeSystemSourceTestFingerprint()
		record.Data, err = json.Marshal(payload)
		if err != nil {
			return trace, err
		}
	}
	return diagnostictrace.Finalize(trace)
}

func TestNativeSystemSourceProjection(t *testing.T) {
	for _, mode := range []string{"observed", "legacy", "unavailable", "hash", "scope", "count"} {
		t.Run(mode, func(t *testing.T) {
			binding := nativeModelTestBinding("/private/workspace")
			trace, err := makeNativeSystemSourceTestTrace(binding)
			if err != nil {
				t.Fatal(err)
			}
			var payload diagnostictrace.ModelPayload
			_ = json.Unmarshal(trace.Records[1].Data, &payload)
			switch mode {
			case "legacy":
				payload.SystemSources = nil
			case "unavailable":
				payload.SystemSources = &diagnostictrace.SystemSourceFingerprint{
					Scope: diagnostictrace.SystemSourceScopeV1, Code: "unavailable", Reason: "protected_system",
				}
			case "hash":
				payload.SystemSources.Workspace.SHA256 = "raw prompt must never be retained"
			case "scope":
				payload.SystemSources.Scope = "file_snapshot"
			case "count":
				payload.SystemSources.SystemParts = 0
			}
			trace.Records[1].Data, _ = json.Marshal(payload)
			rehashNativeModelTestTrace(t, &trace)
			turn := projectNativeModelTrace(trace, binding, digest([]byte("raw")), nil)
			invalid := slices.Contains([]string{"hash", "scope", "count"}, mode)
			if invalid {
				if turn.Code != "invalid_trace" || len(turn.Calls) != 0 {
					t.Fatal("malformed source metadata reached the frozen report", mode)
				}
				return
			}
			if turn.Code != "observed" || len(turn.Calls) != 2 ||
				!reflect.DeepEqual(turn.Calls[0].SystemSources, payload.SystemSources) ||
				!reflect.DeepEqual(turn.Calls[1].SystemSources, nativeSystemSourceTestFingerprint()) {
				t.Fatal("source identity was lost, shared between requests or backfilled", mode)
			}
		})
	}
}

func nativeSystemSourceComparisonReport(t *testing.T) RunReport {
	t.Helper()
	report := nativeModelComparisonReport(t, "completed")
	for index := range report.NativeModels.Turns[0].Calls {
		report.NativeModels.Turns[0].Calls[index].SystemSources = nativeSystemSourceTestFingerprint()
	}
	return report
}

func TestNativeSystemSourceComparisonSeparatesGroupsAndCoverage(t *testing.T) {
	for _, mode := range []string{
		"same", "workspace_definition", "skill_catalog", "active_skills", "legacy", "unavailable", "partial", "empty",
	} {
		t.Run(mode, func(t *testing.T) {
			baseline, current := nativeSystemSourceComparisonReport(t), nativeSystemSourceComparisonReport(t)
			value := current.NativeModels.Turns[0].Calls[1].SystemSources
			switch mode {
			case "workspace_definition":
				value.Workspace.SHA256 = digest([]byte("changed workspace"))
			case "skill_catalog":
				value.SkillCatalog.SHA256 = digest([]byte("changed catalog"))
			case "active_skills":
				value.ActiveSkills.SHA256 = digest([]byte("changed active skills"))
			case "legacy":
				current.NativeModels.Turns[0].Calls[1].SystemSources = nil
			case "unavailable":
				*value = diagnostictrace.SystemSourceFingerprint{
					Scope: diagnostictrace.SystemSourceScopeV1, Code: "unavailable", Reason: "unstructured_system",
				}
			case "partial":
				current.NativeModels.Code = "partial"
			case "empty":
				value.ActiveSkills = diagnostictrace.SystemSourceGroup{SHA256: digest([]byte("[]"))}
			}
			result := Comparison{}
			compareNativeModels(&result, baseline, current)
			unknown := slices.Contains([]string{"legacy", "unavailable", "partial"}, mode)
			expected := []string{}
			if slices.Contains([]string{"workspace_definition", "skill_catalog", "active_skills"}, mode) {
				expected = append(expected, "native_"+mode)
			} else if mode == "empty" {
				expected = append(expected, "native_active_skills")
			}
			if !slices.Equal(result.EnvironmentChanges, expected) ||
				slices.Contains(result.EnvironmentUnknowns, "current_native_system_sources") != unknown ||
				slices.Contains(result.EnvironmentUnknowns, "baseline_native_system_sources") {
				t.Fatal("source drift or coverage was conflated with aggregate request drift", mode, result)
			}
		})
	}
}

func TestNativeSystemSourceComparisonKeepsMultiplicityAndCaseBinding(t *testing.T) {
	for _, mode := range []string{"order", "duplicate", "case", "missing", "zero_requests"} {
		t.Run(mode, func(t *testing.T) {
			baseline, current := nativeSystemSourceComparisonReport(t), nativeSystemSourceComparisonReport(t)
			switch mode {
			case "order":
				slices.Reverse(current.NativeModels.Turns[0].Calls)
			case "duplicate":
				calls := &current.NativeModels.Turns[0].Calls
				*calls = append(*calls, (*calls)[1])
			case "case":
				current.Trials[0].CaseSHA256 = digest([]byte("changed case"))
			case "missing":
				current.NativeModels = nil
			case "zero_requests":
				current.NativeModels.Turns[0].Calls = nil
				current.NativeModels.Totals.Requests = 0
			}
			result := Comparison{}
			compareNativeSystemSources(&result, baseline, current)
			changed := mode == "duplicate" || mode == "case"
			unknown := mode == "missing" || mode == "zero_requests"
			if (len(result.EnvironmentChanges) == 3) != changed ||
				slices.Contains(result.EnvironmentUnknowns, "current_native_system_sources") != unknown {
				t.Fatal("source comparison dropped multiplicity/case identity or invented absence", mode, result)
			}
		})
	}
}
