package eval

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

func TestReturnedTargetUsageRequiresExactCompleteNonnegativeCounts(t *testing.T) {
	for _, raw := range []any{
		nil, "10252",
		[]any{10252},
		map[string]any{},
		map[string]any{"input_tokens": 0, "output_tokens": 0},
		map[string]any{"input_tokens": -1, "output_tokens": 2, "total_tokens": 1},
		map[string]any{"input_tokens": 1.5, "output_tokens": 2, "total_tokens": 3.5},
		map[string]any{"input_tokens": "1", "output_tokens": 2, "total_tokens": 3},
		map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 4},
		map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3, "other": 0},
		map[string]any{"input_tokens": int64(1 << 53), "output_tokens": 0, "total_tokens": int64(1 << 53)},
		map[string]any{"input_tokens": int64(math.MaxInt64), "output_tokens": 1, "total_tokens": 0},
		map[string]any{"input_tokens": math.Inf(1), "output_tokens": 0, "total_tokens": math.Inf(1)},
	} {
		if usage := returnedTokenUsage(raw); usage != nil {
			t.Fatal("malformed or imprecise usage was retained", raw, usage)
		}
	}
	for _, expected := range []TokenUsage{{10252, 20, 10272}, {0, 0, 0}} {
		data, _ := json.Marshal(expected)
		var raw any
		if json.Unmarshal(data, &raw) != nil {
			t.Fatal("invalid test input")
		}
		usage := returnedTokenUsage(raw)
		if usage == nil || *usage != expected {
			t.Fatal("valid returned count or observed zero lost", usage)
		}
	}
}

func TestReturnedTargetUsageIgnoresStreamsPromptsAndContextEstimates(t *testing.T) {
	for _, kind := range []string{"final", "stream", "question", "approval", "auxiliary"} {
		t.Run(kind, func(t *testing.T) {
			response := testResponse(protocol.MintClawMessage{ID: "request", SessionID: "session"}, 1)
			response.Payload[protocol.PayloadKeyUsage] = map[string]any{
				"input_tokens": 10252, "output_tokens": 20, "total_tokens": 10272,
			}
			response.Payload["context_usage"] = map[string]any{"used_tokens": 50000}
			if kind != "final" {
				response.Payload[protocol.PayloadKeyFinal] = false
			}
			if kind == "question" || kind == "approval" {
				response.Payload[protocol.PayloadKeyControls] = "prompt"
				response.Payload[protocol.PayloadKeyInteraction] = kind
			}
			if kind == "auxiliary" {
				response.Payload[protocol.PayloadKeyPlaceholder] = true
				response.Payload[protocol.PayloadKeyFinal] = true
			}
			event, err := responseEvent(response, 1)
			if err != nil || event.Kind != kind || (event.Usage != nil) != (kind == "final") {
				t.Fatal("non-final usage entered target accounting", event, err)
			}
			delete(response.Payload, protocol.PayloadKeyUsage)
			event, err = responseEvent(response, 1)
			if err != nil || event.Usage != nil {
				t.Fatal("context estimate was treated as usage", event, err)
			}
		})
	}
}

func targetUsageTrial(t *testing.T) Trial {
	t.Helper()
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyUsage] = map[string]any{
			"input_tokens": 10252, "output_tokens": 20, "total_tokens": 10272,
		}
		transport.events <- response
	}
	return runFakeConversation(t, transport, testCase())
}

func TestReturnedTargetUsageSummarizesOnlyCorrelatedFinals(t *testing.T) {
	for _, change := range []string{"none", "missing", "all_missing", "duplicate", "overlap", "foreign", "stream"} {
		t.Run(change, func(t *testing.T) {
			trial := targetUsageTrial(t)
			if trial.Verdict != Pass || len(trial.Events) != 4 {
				t.Fatal("invalid conversation fixture")
			}
			switch change {
			case "missing":
				trial.Events[1].Usage = nil
			case "all_missing":
				trial.Events[1].Usage, trial.Events[3].Usage = nil, nil
			case "duplicate":
				trial.Events = append(trial.Events, trial.Events[3])
			case "overlap":
				trial.Events[3].TraceScopes = trial.Events[1].TraceScopes
			case "foreign", "stream":
				extra := trial.Events[3]
				extra.RequestID = "foreign-request"
				if change == "stream" {
					extra.Kind = "stream"
				}
				trial.Events = append(trial.Events, extra)
			}
			usage := newTargetUsage()
			usage.addTrial(trial)
			if !usage.valid() || usage.AllCalls != "unknown" || usage.Finals != 2 {
				t.Fatal("invented coverage or counted a duplicate/foreign final", usage)
			}
			switch change {
			case "missing":
				if usage.Coverage != "partial_returned_finals" || usage.InputTokens != 10252 ||
					usage.ReturnedUsage != 1 {
					t.Fatal("missing usage became complete or was lost", usage)
				}
			case "all_missing", "duplicate", "overlap":
				want := "unknown"
				if change == "duplicate" || change == "overlap" {
					want = "conflicting_final_events"
				}
				if usage.Coverage != want || usage.TotalTokens != 0 {
					t.Fatal("unknown or conflicting usage became an observed total", usage)
				}
			default:
				if usage.Coverage != "returned_finals_only" || usage.InputTokens != 20504 || usage.OutputTokens != 40 {
					t.Fatal("returned final totals were not preserved", usage)
				}
			}
		})
	}
}

func TestReturnedTargetUsageOverflowAndConflictStayUnavailableAcrossTrials(t *testing.T) {
	trial := targetUsageTrial(t)
	trial.Events = trial.Events[:2]
	trial.Events[1].Usage = &TokenUsage{InputTokens: math.MaxInt64, TotalTokens: math.MaxInt64}
	usage := newTargetUsage()
	usage.addTrial(trial)
	usage.addTrial(trial)
	usage.addTrial(targetUsageTrial(t))
	if !usage.valid() || usage.Coverage != "overflow" || usage.TotalTokens != 0 {
		t.Fatal("overflow was hidden by a later trial", usage)
	}
	trial = targetUsageTrial(t)
	trial.Events = append(trial.Events, trial.Events[3])
	usage = newTargetUsage()
	usage.addTrial(trial)
	usage.addTrial(targetUsageTrial(t))
	if !usage.valid() || usage.Coverage != "conflicting_final_events" || usage.TotalTokens != 0 {
		t.Fatal("conflict was hidden by a later trial", usage)
	}
}

func TestReturnedTargetUsageDoesNotCountRejectedRoutes(t *testing.T) {
	for _, change := range []string{"agent", "workspace", "session_key", "scope", "request", "session"} {
		t.Run(change, func(t *testing.T) {
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				response.Payload[protocol.PayloadKeyUsage] = map[string]any{
					"input_tokens": 10252, "output_tokens": 20, "total_tokens": 10272,
				}
				foreign := testResponse(request, number)
				foreign.Payload[protocol.PayloadKeyUsage] = map[string]any{
					"input_tokens": 90000, "output_tokens": 20, "total_tokens": 90020,
				}
				if number == 2 {
					switch change {
					case "agent":
						foreign.Payload[protocol.PayloadKeyAgentID] = "foreign"
					case "workspace":
						foreign.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
							Workspace: "/foreign", TurnID: request.ID,
						}}
					case "session_key":
						foreign.Payload[protocol.PayloadKeySessionKey] = "foreign-key"
					case "scope":
						delete(foreign.Payload, protocol.PayloadKeyTraceScopes)
					case "request":
						foreign.Payload[protocol.PayloadKeyRequestID] = "foreign-request"
					case "session":
						foreign.SessionID = "foreign-session"
					}
					transport.events <- foreign
				}
				transport.events <- response
			}
			usage := newTargetUsage()
			usage.addTrial(runFakeConversation(t, transport, testCase()))
			if !usage.valid() || usage.InputTokens > 20504 || usage.InputTokens < 10252 {
				t.Fatal("rejected or unrelated response counted as native usage", usage)
			}
		})
	}
}

func TestReturnedTargetUsageRunRetainsIntegerEvidenceAndCompactSummary(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.respond = func(_ protocol.MintClawMessage, response *protocol.MintClawMessage) {
		response.Payload[protocol.PayloadKeyUsage] = map[string]any{
			"input_tokens": 10252, "output_tokens": 20, "total_tokens": 10272,
		}
	}
	result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.target-usage"))
	if err != nil || result.TargetUsage == nil || result.TargetUsage.InputTokens != 20504 || result.Usage != "unknown" {
		t.Fatal("target usage missing or all-call coverage invented", result, err)
	}
	trial := comparisonTrial(t, result, 0)
	if trial.Events[1].Usage == nil || trial.Events[1].Usage.InputTokens != 10252 {
		t.Fatal("raw final-event integer lost")
	}
	summary, err := os.ReadFile(filepath.Join(result.ReportDir, "summary.md"))
	if err != nil || !strings.Contains(string(summary), "20.5k in, 40 out") ||
		!strings.Contains(string(summary), "Target all-call usage: unknown") {
		t.Fatal("human summary rounded counts or claimed full coverage", err)
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal("actual run usage cannot be compared", err)
	}
	result.TargetUsage.InputTokens++
	result.TargetUsage.TotalTokens++
	writeAdmissionJSON(t, filepath.Join(result.ReportDir, "run.json"), result)
	if _, err = Compare(result.ReportDir, result.ReportDir); !errors.Is(err, ErrCompare) {
		t.Fatal("contradictory target aggregate accepted", err)
	}
}

func TestReturnedTargetUsageGradePreservesHistoryWithoutTargetWork(t *testing.T) {
	fixture, item, source, calls := gradeFixture(t, false)
	trial := comparisonTrial(t, source, 0)
	trial.Events[1].Usage = &TokenUsage{InputTokens: 10252, OutputTokens: 20, TotalTokens: 10272}
	source.TargetUsage = newTargetUsage()
	source.TargetUsage.addTrial(trial)
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, source.Trials[0].File), trial)
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
	})
	if err != nil || result.TargetUsage == nil || *result.TargetUsage != *source.TargetUsage ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes || calls.Load() != 1 ||
		result.Mode != "grade" || result.SourceRun != source.ID {
		t.Fatal("regrade lost historical usage or performed new target work", result, err)
	}
	if _, err = Compare(source.ReportDir, result.ReportDir); err != nil {
		t.Fatal("regrade changed usage identity", err)
	}
	trial.Events[1].Usage.TotalTokens++
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, source.Trials[0].File), trial)
	_, err = Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
	})
	if !errors.Is(err, ErrGrade) || calls.Load() != 1 {
		t.Fatal("invalid retained usage reached a paid judge", err)
	}
}
