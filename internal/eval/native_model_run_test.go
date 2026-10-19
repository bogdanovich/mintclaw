package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func TestNativeModelsRunGradeAndCompareWithoutRereadingRuntime(t *testing.T) {
	for _, mode := range []string{"native_models", "native_models_retry"} {
		t.Run(mode, func(t *testing.T) { testNativeModelsRunGradeAndCompare(t, mode) })
	}
}

func testNativeModelsRunGradeAndCompare(t *testing.T, mode string) {
	t.Helper()
	fixture := newOwnedGatewayFixture(t, mode)
	enableTestModelMetadata(t, fixture)
	item := loadedTestCase(t, "general.native-models")
	item.Case.Rubric = []Criterion{{ID: "coherent", Description: "Visible replies are coherent.", Required: true}}
	data, _ := json.Marshal(item.Case)
	item.Digest = digest(data)
	source, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, Repeat: 2, SuiteTimeout: 30 * time.Second,
	})
	if source.NativeModels == nil {
		t.Fatal("run did not retain a native observation", err)
	}
	input, output, responses := int64(41408), int64(28), 8
	if mode == "native_models_retry" {
		input, output, responses = 400, 16, 4
	}
	if !errors.Is(err, ErrNotPassed) || !source.Complete ||
		source.NativeModels.Code != "observed" || !source.NativeModels.valid(source.Runtime) ||
		source.NativeModels.Totals.InputTokens != input || source.NativeModels.Totals.OutputTokens != output ||
		source.NativeModels.Totals.Responses != responses ||
		source.NativeModels.Totals.Requests != 8 || source.NativeModels.AllCalls != "unknown" ||
		source.TargetUsage.AllCalls != "unknown" {
		t.Fatal("run did not observe each routed native call independently of final usage", err,
			source.NativeModels.Code, source.NativeModels.Totals)
	}
	if _, err = Compare(source.ReportDir, source.ReportDir); err != nil {
		t.Fatal("qualified native observations were not readable offline", err)
	}
	// The historical projection must remain usable without runtime files or PID.
	if err = os.RemoveAll(filepath.Join(fixture.workspace, "state", "diagnostics", "traces")); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": adaptiveJudgePass}, "finish_reason": "stop",
		}}})
	}))
	defer server.Close()
	fixture.cfg.ModelList[0].APIBase = server.URL
	fixture.preset.JudgeModel = "eval-model"
	if _, err = config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	graded, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir,
		RunOptions{Home: fixture.home, OutputParent: fixture.output, StartTarget: true})
	if err != nil || !graded.Complete || calls.Load() != 2 ||
		!reflect.DeepEqual(source.NativeModels, graded.NativeModels) ||
		fixture.probes.Load() != 0 ||
		fixture.tasks.Load() != 0 {
		t.Fatal("offline regrade lost source metadata or performed target work", err)
	}
	comparison, err := Compare(source.ReportDir, graded.ReportDir)
	if err != nil || !comparison.CompatibleCases ||
		slices.Contains(comparison.EnvironmentChanges, "native_effective_requests") ||
		comparison.Attribution != "not_established" {
		t.Fatal("same historical model records were treated as a new model experiment", comparison, err)
	}
	graded.NativeModels.Turns[0].Calls[0].Inputs.Prompt = digest([]byte("different effective request"))
	writeAdmissionJSON(t, filepath.Join(graded.ReportDir, "run.json"), graded)
	comparison, err = Compare(source.ReportDir, graded.ReportDir)
	if err != nil || !slices.Contains(comparison.EnvironmentChanges, "native_effective_requests") ||
		!slices.Contains(comparison.EnvironmentUnknowns, "instructions_and_skills") ||
		!slices.Contains(comparison.EnvironmentUnknowns, "target_all_call_models") {
		t.Fatal("native drift was hidden or stronger identity/usage was invented", comparison, err)
	}
	variants := []string{
		"binding",
		"route",
		"request",
		"totals",
		"source",
		"all-call",
		"duplicate",
		"usage",
		"retry-response",
	}
	if mode == "native_models_retry" {
		variants = append(variants, "retry-count")
	}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			data, _ := json.Marshal(source)
			var changed RunReport
			_ = json.Unmarshal(data, &changed)
			switch variant {
			case "binding":
				changed.NativeModels.Turns[0].BindingSHA256 = strings.Repeat("b", 64)
			case "route", "request":
				path := filepath.Join(source.ReportDir, source.Trials[0].File)
				original, readErr := os.ReadFile(path)
				var trial Trial
				if readErr != nil || json.Unmarshal(original, &trial) != nil {
					t.Fatal("route fixture unavailable", readErr)
				}
				defer func() {
					if os.WriteFile(path, original, 0o600) != nil {
						t.Error("route restoration failed")
					}
				}()
				if variant == "route" {
					trial.SessionKey += "-other"
				}
				for index := range trial.Events {
					if variant == "route" && trial.Events[index].SessionKey != "" {
						trial.Events[index].SessionKey = trial.SessionKey
					}
					if variant == "request" {
						trial.Events[index].RequestID += "-other"
					}
				}
				writeAdmissionJSON(t, path, trial)
			case "totals":
				changed.NativeModels.Totals.InputTokens++
			case "source":
				changed.NativeModels.Source.Commit = strings.Repeat("a", 40)
			case "all-call":
				changed.NativeModels.AllCalls = "complete"
			case "duplicate":
				changed.NativeModels.Turns[1].TraceID = changed.NativeModels.Turns[0].TraceID
			case "usage":
				changed.NativeModels.Turns[0].Calls[1].Usage.InputTokens = -1
			case "retry-response":
				changed.NativeModels.Turns[0].Calls[1].RetryObserved = true
			case "retry-count":
				changed.NativeModels.Turns[0].RetryFallbackRecords = 0
			}
			writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), changed)
			before := calls.Load()
			if _, gradeErr := Grade(
				t.Context(),
				fixture.preset,
				[]LoadedCase{item},
				source.ReportDir,
				RunOptions{
					Home:         fixture.home,
					OutputParent: fixture.output,
				},
			); !errors.Is(gradeErr, ErrGrade) || calls.Load() != before {
				t.Fatal("contradictory native metadata reached judge", gradeErr)
			}
			if _, compareErr := Compare(source.ReportDir, source.ReportDir); !errors.Is(compareErr, ErrCompare) {
				t.Fatal("contradictory native metadata was compared", compareErr)
			}
		})
	}
}

func TestNativeModelComparisonDoesNotTreatUnknownAsEmptyModelSet(t *testing.T) {
	baseline := RunReport{}
	current := RunReport{NativeModels: &NativeModelObservation{Code: "observed", Turns: []NativeModelTurn{
		{Calls: []NativeModelCall{{RequestModel: "known", ResponseModel: "known", ResponseProvider: "openai"}}},
	}}}
	comparison := Comparison{}
	compareNativeModels(&comparison, baseline, current)
	if len(comparison.EnvironmentChanges) != 0 ||
		!slices.Contains(comparison.EnvironmentUnknowns, "baseline_native_record_coverage") {
		t.Fatal("missing observation was treated as observed empty inputs", comparison)
	}
}

func nativeModelComparisonReport(t *testing.T, status string) RunReport {
	t.Helper()
	binding := nativeModelTestBinding("/private/workspace")
	trace := nativeModelTestTrace(t, binding)
	if status == "error" || status == "aborted" {
		trace.Outcome.Status = status
		// Keep the second request but remove its response, as a failed call can.
		trace.Records = append(trace.Records[:4], trace.Records[5:]...)
		trace.Records[4].Data, _ = json.Marshal(diagnostictrace.TurnPayload{Status: status})
		rehashNativeModelTestTrace(t, &trace)
	}
	turn := projectNativeModelTrace(trace, binding, digest([]byte("raw")), nil)
	if turn.Code != "observed" {
		t.Fatal("comparison fixture did not qualify", status, turn.Code)
	}
	if status == "unknown_model" {
		turn.Calls[1].ResponseModel = ""
	}
	return RunReport{
		Trials: []TrialSummary{{ID: binding.TrialID, CaseSHA256: binding.CaseSHA256}},
		NativeModels: &NativeModelObservation{
			Code: "observed", Turns: []NativeModelTurn{turn}, Totals: summarizeNativeModels([]NativeModelTurn{turn}),
		},
	}
}

func TestNativeModelComparisonReportsRequestedDriftWithoutKnownResponses(t *testing.T) {
	for _, status := range []string{"error", "aborted", "unknown_model"} {
		for _, field := range []string{"provider", "model"} {
			t.Run(status+"/"+field, func(t *testing.T) {
				baseline := nativeModelComparisonReport(t, status)
				current := nativeModelComparisonReport(t, status)
				call := &current.NativeModels.Turns[0].Calls[1]
				if field == "provider" {
					call.RequestProvider = "anthropic"
				} else {
					call.RequestModel = "another-requested-model"
				}
				comparison := Comparison{}
				compareNativeModels(&comparison, baseline, current)
				if !slices.Contains(comparison.EnvironmentChanges, "native_requested_or_response_models") ||
					slices.Contains(comparison.EnvironmentChanges, "native_effective_requests") ||
					!slices.Contains(comparison.EnvironmentUnknowns, "baseline_native_response_models") ||
					!slices.Contains(comparison.EnvironmentUnknowns, "current_native_response_models") {
					t.Fatal("known requested drift was hidden by response unknowns", comparison)
				}
			})
		}
	}
}

func TestNativeModelComparisonSeparatesResponseCoverageFromModelDrift(t *testing.T) {
	for _, status := range []string{"error", "aborted", "unknown_model", "completed"} {
		t.Run(status, func(t *testing.T) {
			baseline := nativeModelComparisonReport(t, status)
			current := nativeModelComparisonReport(t, "completed")
			current.NativeModels.Turns[0].Calls[1].ResponseModel = "another-response-model"
			comparison := Comparison{}
			compareNativeModels(&comparison, baseline, current)
			changed := slices.Contains(comparison.EnvironmentChanges, "native_requested_or_response_models")
			unknown := slices.Contains(comparison.EnvironmentUnknowns, "baseline_native_response_models")
			if changed != (status == "completed") || unknown != (status != "completed") {
				t.Fatal("response coverage was treated as a known model identity", comparison)
			}
		})
	}
}

func TestNativeModelComparisonRetainsKnownRequestResponsePairs(t *testing.T) {
	baseline := nativeModelComparisonReport(t, "completed")
	current := nativeModelComparisonReport(t, "completed")
	for index := range 2 {
		baseline.NativeModels.Turns[0].Calls[index].RequestModel = []string{"request-one", "request-two"}[index]
		current.NativeModels.Turns[0].Calls[index].RequestModel = []string{"request-one", "request-two"}[index]
		baseline.NativeModels.Turns[0].Calls[index].ResponseModel = []string{"response-one", "response-two"}[index]
		current.NativeModels.Turns[0].Calls[index].ResponseModel = []string{"response-two", "response-one"}[index]
	}
	comparison := Comparison{}
	compareNativeModels(&comparison, baseline, current)
	if !slices.Contains(comparison.EnvironmentChanges, "native_requested_or_response_models") {
		t.Fatal("known request-response pair changes disappeared", comparison)
	}
}

func TestNativeModelComparisonPreservesUnknownProviders(t *testing.T) {
	for _, field := range []string{"request", "response"} {
		for _, change := range []string{"none", "model", "provider"} {
			t.Run(field+"/"+change, func(t *testing.T) {
				baseline := nativeModelComparisonReport(t, "completed")
				current := nativeModelComparisonReport(t, "completed")
				a := &baseline.NativeModels.Turns[0].Calls[1]
				b := &current.NativeModels.Turns[0].Calls[1]
				if field == "request" {
					a.RequestProvider = ""
					switch change {
					case "model":
						b.RequestModel = "changed-model"
					case "provider":
						b.RequestProvider = "anthropic"
					}
				} else {
					a.ResponseProvider = ""
					switch change {
					case "model":
						b.ResponseModel = "changed-model"
					case "provider":
						b.ResponseProvider = "anthropic"
					}
				}
				comparison := Comparison{}
				compareNativeModels(&comparison, baseline, current)
				changed := slices.Contains(comparison.EnvironmentChanges, "native_requested_or_response_models")
				if changed != (change == "model") ||
					!slices.Contains(comparison.EnvironmentUnknowns, "baseline_native_"+field+"_providers") ||
					slices.Contains(comparison.EnvironmentUnknowns, "baseline_native_response_models") {
					t.Fatal("provider absence was invented as identity or hid known model drift", comparison)
				}
			})
		}
	}
}
