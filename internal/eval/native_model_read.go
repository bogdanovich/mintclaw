package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
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
	if lifecycle == nil || !lifecycle.valid() || lifecycle.Code != "native_exit_zero" ||
		lifecycle.PID != target.probe.PID || target.config == nil ||
		target.config.Diagnostics.TraceCapture != evalModelMetadataCapture() {
		return unavailableNativeModels("not_stopped")
	}
	if surfaces == nil || !surfaces.valid() || surfaces.Code != "observed" {
		return unavailableNativeModels("private_coverage_unknown")
	}
	if !nativeModelSourceKnown(target.probe) {
		return unavailableNativeModels("source_unknown")
	}
	if len(bindings) == 0 {
		return unavailableNativeModels("no_scopes")
	}
	if len(bindings) > evalModelMetadataCapture().MaxTraces {
		return unavailableNativeModels("scope_limit")
	}
	// A routed turn cannot belong to two trial sessions, even when a record
	// happens to carry the same model/usage. Never correlate by content/time.
	for i, binding := range bindings {
		for _, previous := range bindings[:i] {
			if binding.TraceScope == previous.TraceScope {
				return unavailableNativeModels("native_evidence_unavailable")
			}
		}
	}
	root, err := os.OpenRoot(target.preset.IsolationRoot)
	if err != nil {
		return unavailableNativeModels("native_evidence_unavailable")
	}
	defer func() { _ = root.Close() }()
	for _, path := range []string{"workspace", "workspace/state", "workspace/state/diagnostics", "workspace/state/diagnostics/traces"} {
		info, statErr := root.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			path == "workspace/state/diagnostics/traces" && info.Mode().Perm() != 0o700 {
			return unavailableNativeModels("native_evidence_unavailable")
		}
	}
	store, err := root.OpenRoot("workspace/state/diagnostics/traces")
	if err != nil {
		return unavailableNativeModels("native_evidence_unavailable")
	}
	defer func() { _ = store.Close() }()
	directory, err := store.Open(".")
	if err != nil {
		return unavailableNativeModels("native_evidence_unavailable")
	}
	entries, err := directory.ReadDir(evalModelMetadataCapture().MaxTraces + 1)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > evalModelMetadataCapture().MaxTraces {
		return unavailableNativeModels("native_evidence_unavailable")
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
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
	invalid, seen := false, map[string]bool{}
	limits := evalModelMetadataCapture()
	var replace func(string) string
	if filter != nil {
		replace = filter.Replace
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return unavailableNativeModels("native_evidence_unavailable")
		}
		before, statErr := store.Lstat(entry.Name())
		data, readErr := readNativeEvidenceFile(store, entry.Name(), 2*1024*1024)
		after, afterErr := store.Lstat(entry.Name())
		if statErr != nil || readErr != nil || afterErr != nil || !sameCensusFile(before, after) ||
			int64(len(data)) != after.Size() {
			clear(data)
			invalid = true
			continue
		}
		scanErr := inspectNativeJSON(data, facts, filter)
		if errors.Is(scanErr, ErrPrivateEvidence) {
			clear(data)
			failed := unavailableNativeModels("raw_private_evidence")
			failed.Code = "leak_observed"
			return failed
		}
		if scanErr != nil {
			clear(data)
			invalid = true
			continue
		}
		// The native codec owns RawMessage payloads. Reject duplicate keys
		// before it can project a JSON object into structs/maps.
		_, keysErr := decodeValue(json.NewDecoder(bytes.NewReader(data)), 0)
		trace, decodeErr := diagnostictrace.Decode(data)
		sha := digest(data)
		clear(data)
		if keysErr != nil || decodeErr != nil || seen[trace.TraceID] ||
			entry.Name() != trace.TraceID+".json" || trace.Source != result.Source ||
			trace.Policy.ContentMode != diagnostictrace.ContentMetadataOnly ||
			trace.Limits != (diagnostictrace.AppliedLimits{
				MaxTraceBytes: limits.MaxTraceBytes,
				MaxRecords:    limits.MaxRecords, MaxRecordBytes: limits.MaxRecordBytes,
			}) {
			invalid = true
			continue
		}
		seen[trace.TraceID] = true
		for index, binding := range bindings {
			if binding.TraceScope.TurnID != trace.Metadata.RootTurnID {
				continue
			}
			if result.Turns[index].Code != "missing_trace" {
				result.Turns[index] = NativeModelTurn{
					TrialID: binding.TrialID, TraceScope: binding.TraceScope,
					BindingSHA256: binding.hash(), Code: "ambiguous_trace",
				}
				continue
			}
			result.Turns[index] = projectNativeModelTrace(trace, binding, sha, replace)
		}
	}
	if invalid {
		return unavailableNativeModels("native_evidence_unavailable")
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
