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

// The visitor projects bounded byte snapshots, never owns native state. Callers
// must discard all projections on a nonempty reason. Keep scanning after an
// invalid file so uncertainty cannot hide a later raw private-value leak.
func visitBoundNativeTraces(
	ctx context.Context, target admittedTarget, lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation,
	bindings []nativeModelBinding, facts map[string]string, filter *strings.Replacer,
	visit func(int, diagnostictrace.Trace, string),
) string {
	if lifecycle == nil || !lifecycle.valid() || lifecycle.Code != "native_exit_zero" ||
		lifecycle.PID != target.probe.PID || target.config == nil ||
		target.config.Diagnostics.TraceCapture != evalModelMetadataCapture() {
		return "not_stopped"
	}
	if surfaces == nil || !surfaces.valid() || surfaces.Code != "observed" {
		return "private_coverage_unknown"
	}
	if !nativeModelSourceKnown(target.probe) {
		return "source_unknown"
	}
	if len(bindings) == 0 {
		return "no_scopes"
	}
	if len(bindings) > evalModelMetadataCapture().MaxTraces {
		return "scope_limit"
	}
	for index, binding := range bindings {
		for _, previous := range bindings[:index] {
			if binding.TraceScope == previous.TraceScope {
				return "native_evidence_unavailable"
			}
		}
	}
	root, err := os.OpenRoot(target.preset.IsolationRoot)
	if err != nil {
		return "native_evidence_unavailable"
	}
	defer func() { _ = root.Close() }()
	for _, path := range []string{"workspace", "workspace/state", "workspace/state/diagnostics", "workspace/state/diagnostics/traces"} {
		info, statErr := root.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			path == "workspace/state/diagnostics/traces" && info.Mode().Perm() != 0o700 {
			return "native_evidence_unavailable"
		}
	}
	store, err := root.OpenRoot("workspace/state/diagnostics/traces")
	if err != nil {
		return "native_evidence_unavailable"
	}
	defer func() { _ = store.Close() }()
	directory, err := store.Open(".")
	if err != nil {
		return "native_evidence_unavailable"
	}
	limits := evalModelMetadataCapture()
	entries, err := directory.ReadDir(limits.MaxTraces + 1)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > limits.MaxTraces {
		return "native_evidence_unavailable"
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	source := diagnostictrace.Source{Commit: target.probe.GitCommit, MintClawVersion: target.probe.BuildVersion}
	invalid, seen := false, map[string]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return "native_evidence_unavailable"
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
			return "raw_private_evidence"
		}
		if scanErr != nil {
			clear(data)
			invalid = true
			continue
		}
		_, keysErr := decodeValue(json.NewDecoder(bytes.NewReader(data)), 0)
		trace, decodeErr := diagnostictrace.Decode(data)
		sha := digest(data)
		clear(data)
		if keysErr != nil || decodeErr != nil || seen[trace.TraceID] ||
			entry.Name() != trace.TraceID+".json" || trace.Source != source ||
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
			if binding.TraceScope.TurnID == trace.Metadata.RootTurnID {
				visit(index, trace, sha)
			}
		}
	}
	if invalid {
		return "native_evidence_unavailable"
	}
	return ""
}
