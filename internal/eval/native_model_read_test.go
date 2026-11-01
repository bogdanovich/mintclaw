package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func nativeModelReaderFixture(t *testing.T) (admittedTarget, nativeModelBinding, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, "workspace")
	store := filepath.Join(workspace, "state", "diagnostics", "traces")
	if err = os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
	return admittedTarget{
			config: cfg,
			preset: Preset{IsolationRoot: home, ExpectedWorkspace: workspace},
			probe: health.RuntimeProbe{
				Version:      1,
				PID:          1234,
				GitCommit:    nativeModelTestCommit,
				BuildVersion: "native-test",
			},
		},
		nativeModelTestBinding(workspace), store
}

func nativeModelTestLifecycle() *TargetLifecycle {
	code := 0
	return &TargetLifecycle{
		Mode: "owned_gateway", PID: 1234, Admitted: true, StopRequested: true,
		ExitObserved: true, ExitCode: &code, Code: "native_exit_zero",
	}
}

func nativeModelTestSurfaces() *PrivateSurfaceObservation {
	return &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
}

func writeNativeModelTestTrace(t *testing.T, store string, trace diagnostictrace.Trace) string {
	t.Helper()
	data, err := json.MarshalIndent(trace, "", "  ")
	name := filepath.Join(store, trace.TraceID+".json")
	if err != nil || os.WriteFile(name, data, 0o600) != nil {
		t.Fatal("trace fixture unavailable", err)
	}
	return name
}

func TestNativeModelReaderRequiresOwnedStoppedBoundedMetadata(t *testing.T) {
	for _, mode := range []string{
		"complete", "missing", "source", "short-source", "content", "limits", "mode",
		"alias", "parent-alias", "foreign-pid", "forced", "privacy", "ambiguous", "record-limit", "context",
	} {
		t.Run(mode, func(t *testing.T) {
			target, binding, store := nativeModelReaderFixture(t)
			trace := nativeModelTestTrace(t, binding)
			lifecycle, surfaces, ctx := nativeModelTestLifecycle(), nativeModelTestSurfaces(), t.Context()
			switch mode {
			case "source":
				trace.Source.Commit = strings.Repeat("a", 40)
			case "short-source":
				target.probe.GitCommit = target.probe.GitCommit[:8]
			case "content":
				trace.Policy.ContentMode, trace.Policy.Redactor = diagnostictrace.ContentRedacted, "test"
			case "limits":
				trace.Limits.MaxRecords++
			case "foreign-pid":
				lifecycle.PID++
			case "forced":
				lifecycle.Code, lifecycle.Forced = "forced_exit", true
			case "privacy":
				surfaces.Code = "unavailable"
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			name := writeNativeModelTestTrace(t, store, trace)
			switch mode {
			case "missing":
				_ = os.Remove(name)
			case "mode":
				if err := os.Chmod(name, 0o644); err != nil {
					t.Fatal(err)
				}
			case "alias":
				other := filepath.Join(t.TempDir(), "foreign.json")
				data, _ := os.ReadFile(name)
				if os.WriteFile(other, data, 0o600) != nil || os.Remove(name) != nil || os.Symlink(other, name) != nil {
					t.Fatal("alias fixture unavailable")
				}
			case "parent-alias":
				if os.Rename(store, store+"-original") != nil || os.Symlink(store+"-original", store) != nil {
					t.Fatal("parent alias fixture unavailable")
				}
			case "ambiguous":
				trace.TraceID = "trace-second"
				writeNativeModelTestTrace(t, store, trace)
			case "record-limit":
				for range evalModelMetadataCapture().MaxTraces {
					trace.TraceID += "x"
					writeNativeModelTestTrace(t, store, trace)
				}
			}
			observation := observeNativeModels(
				ctx,
				target,
				lifecycle,
				surfaces,
				[]nativeModelBinding{binding},
				nil,
				nil,
			)
			if !observation.valid(target.probe) || observation.AllCalls != "unknown" {
				t.Fatal("invalid observation", mode, observation)
			}
			if mode == "complete" {
				if observation.Code != "observed" || observation.Totals.InputTokens != 10352 ||
					observation.Turns[0].BindingSHA256 != binding.hash() {
					t.Fatal("native records were not bound", observation)
				}
			} else if observation.Code == "observed" || observation.Totals.Requests != 0 {
				t.Fatal("unqualified native records were promoted", mode, observation)
			}
		})
	}
}

func TestNativeModelReaderInspectsRawBeforeDecodeAndAfterEarlierMalformedFile(t *testing.T) {
	target, binding, store := nativeModelReaderFixture(t)
	if os.WriteFile(filepath.Join(store, "trace-a.json"), []byte(`{"incomplete":`), 0o600) != nil ||
		os.WriteFile(filepath.Join(store, "trace-z.json"),
			[]byte(`{"discarded":"\u004eATIVE_MODEL_CANARY","discarded":"public"}`), 0o600) != nil {
		t.Fatal("raw control unavailable")
	}
	observation := observeNativeModels(t.Context(), target, nativeModelTestLifecycle(), nativeModelTestSurfaces(),
		[]nativeModelBinding{binding}, map[string]string{"fact": "NATIVE_MODEL_CANARY"}, nil)
	data, _ := json.Marshal(observation)
	if !observation.valid(target.probe) || observation.Code != "leak_observed" || observation.verdict() != Fail ||
		strings.Contains(string(data), "NATIVE_MODEL_CANARY") {
		t.Fatal("raw leak was hidden by projection or earlier uncertainty", observation)
	}
}

func TestNativeModelReaderUsesNativeFilteredSessionHash(t *testing.T) {
	target, binding, store := nativeModelReaderFixture(t)
	filter := strings.NewReplacer("synthetic-secret", "[REDACTED]")
	trace := nativeModelTestTrace(t, binding)
	trace.Metadata.SessionHash = diagnostictrace.FilteredHash(binding.SessionKey, filter.Replace)
	for index := range trace.Records {
		trace.Records[index].Scope.SessionHash = trace.Metadata.SessionHash
	}
	rehashNativeModelTestTrace(t, &trace)
	writeNativeModelTestTrace(t, store, trace)
	observation := observeNativeModels(t.Context(), target, nativeModelTestLifecycle(), nativeModelTestSurfaces(),
		[]nativeModelBinding{binding}, nil, filter)
	if observation.Code != "observed" || observation.Turns[0].SessionHash == digest([]byte(binding.SessionKey)) {
		t.Fatal("native redacted identity was guessed from bare session text", observation)
	}
}
