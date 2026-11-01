package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func TestRoleBoundaryRejectsPrivateArgumentsBeforeHashOrCall(t *testing.T) {
	for _, location := range []string{"system", "input", "model", "prompt-version"} {
		provider := &roleTestProvider{mediated: true}
		var retained privateFactRetention
		if !retained.add(map[string]string{"old": "private-role-argument-canary"}) {
			t.Fatal("fact fixture unavailable")
		}
		role := &ModelRole{
			provider: provider, retained: &retained,
			identity: RoleIdentity{Role: "user", Model: "public-model"},
		}
		system, input, version := "Public system.", "Public input.", "public-v1"
		switch location {
		case "system":
			system = "private-role-argument-canary"
		case "input":
			input = `{"value":"\u0070rivate-role-argument-canary"}`
		case "model":
			role.identity.Model = "private-role-argument-canary"
		case "prompt-version":
			version = "private-role-argument-canary"
		}
		output, call, err := role.Call(t.Context(), version, system, input)
		if !errors.Is(err, ErrPrivateEvidence) || output != "" || call.Attempted || provider.calls != 0 ||
			call.Code != "private_input" || call.PromptSHA256 != "" || call.PromptVersion != "" ||
			call.Boundary == nil || call.Boundary.Input != "leak_observed" || !call.Boundary.valid(false) {
			t.Fatal("private argument reached hashing, provider or retained metadata", location, call.Code)
		}
		data, err := json.Marshal(call)
		if err != nil || strings.Contains(string(data), "private-role-argument-canary") {
			t.Fatal("private identity was retained after argument refusal")
		}
	}
}

func TestRoleBoundaryInspectsEveryReturnedStringBeforeProjection(t *testing.T) {
	for _, field := range []string{
		"content", "reasoning-content", "reasoning", "detail-format", "detail-type", "detail-text",
		"tool-id", "tool-type", "tool-name", "tool-key", "tool-value", "tool-signature", "tool-feedback", "finish",
	} {
		t.Run(field, func(t *testing.T) {
			value := "PRIVATE_ROLE_RETURN_CANARY"
			response := &providers.LLMResponse{
				Content: `{"kind":"stop"}`, FinishReason: "stop",
				Usage: &providers.UsageInfo{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
			}
			detail := protocoltypes.ReasoningDetail{}
			tool := providers.ToolCall{Arguments: map[string]any{}}
			switch field {
			case "content":
				response.Content = value
			case "reasoning-content":
				response.ReasoningContent = value
			case "reasoning":
				response.Reasoning = value
			case "detail-format":
				detail.Format = value
			case "detail-type":
				detail.Type = value
			case "detail-text":
				detail.Text = `{"hidden":"\u0050RIVATE_ROLE_RETURN_CANARY"}`
			case "tool-id":
				tool.ID = value
			case "tool-type":
				tool.Type = value
			case "tool-name":
				tool.Name = value
			case "tool-key":
				tool.Arguments[value] = "public"
			case "tool-value":
				tool.Arguments["nested"] = []any{map[string]any{"value": []byte(value)}}
			case "tool-signature":
				tool.ThoughtSignature = value
			case "tool-feedback":
				tool.ToolFeedbackExplanation = value
			case "finish":
				response.FinishReason = value
			}
			response.ReasoningDetails = []protocoltypes.ReasoningDetail{detail}
			response.ToolCalls = []providers.ToolCall{tool} // Invalid shape must not hide a returned leak.
			provider := &roleTestProvider{
				mediated: true,
				response: response,
				err:      errors.New("opaque provider failure"),
			}
			role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			output, call, err := role.call(t.Context(), "test-v1", "Public system.", "Public input.",
				map[string]string{"fact": value})
			if !errors.Is(err, ErrPrivateEvidence) || output != "" || call.Code != "private_output" ||
				call.Usage == nil || call.Usage.TotalTokens != 12 || call.Boundary == nil ||
				call.Boundary.Output != "leak_observed" || !call.Boundary.valid(true) {
				t.Fatal("returned private field was discarded before inspection or usage was lost", field, call.Code)
			}
		})
	}
}

type canceledRoleProvider struct {
	roleTestProvider
	cancel context.CancelFunc
}

func (provider *canceledRoleProvider) Chat(
	context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any,
) (*providers.LLMResponse, error) {
	provider.cancel()
	return provider.response, context.Canceled
}

func TestRoleBoundaryInspectsLatePartialResponsesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := &canceledRoleProvider{cancel: cancel, roleTestProvider: roleTestProvider{
		mediated: true, response: &providers.LLMResponse{
			Content: "public", Reasoning: "PRIVATE_LATE_ROLE_CANARY", FinishReason: "stop",
		},
	}}
	role := &ModelRole{provider: provider}
	output, call, err := role.call(ctx, "test-v1", "Public system.", "Public input.",
		map[string]string{"fact": "PRIVATE_LATE_ROLE_CANARY"})
	if !errors.Is(err, ErrPrivateEvidence) || output != "" || call.Code != "private_output" ||
		call.Boundary.Output != "leak_observed" || ctx.Err() == nil {
		t.Fatal("cancellation hid a returned private response", call.Code)
	}
}

func TestRoleBoundaryKnownLeakOverridesEarlierOpaqueEvidence(t *testing.T) {
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: `public\opaque`, Reasoning: "PRIVATE_LATER_ROLE_CANARY", FinishReason: "stop",
	}}
	role := &ModelRole{provider: provider}
	_, call, err := role.call(t.Context(), "test-v1", "Public system.", "Public input.",
		map[string]string{"fact": "PRIVATE_LATER_ROLE_CANARY"})
	if !errors.Is(err, ErrPrivateEvidence) || call.Boundary.Output != "leak_observed" {
		t.Fatal("earlier opaque text hid a known leak in a later field", call.Code)
	}
}

func TestOptionalJudgeCannotHideUnavailableBoundaryEvidence(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		transport.events <- testResponse(request, number)
	}
	roles, _, judge := adaptiveRoles(`{"kind":"finish","text":"Finish."}`, adaptiveJudgePass)
	roles.judge.filter = strings.NewReplacer("private-credential-canary", "[redacted]")
	judge.response.ReasoningContent = `public\opaque`
	item := rolePromptCase()
	for index := range item.Rubric {
		item.Rubric[index].Required = false
	}
	trial := runAdaptiveTest(t, transport, item, roles)
	if trial.Verdict == Pass || trial.EvidenceEligible || trial.Judge == nil ||
		trial.Judge.Call.Boundary.Output != "unavailable" || !consistentComparisonVerdict(trial) {
		t.Fatal("optional assessment hid an uninspected role response", trial.Verdict, trial.Code)
	}
}

func TestGradeRejectsUnsafeRoleBoundariesBeforeAnyJudgeCall(t *testing.T) {
	for _, location := range []string{"user", "judge"} {
		for _, output := range []string{"unavailable", "leak_observed", "invalid"} {
			t.Run(location+"/"+output, func(t *testing.T) {
				fixture, item, source, count := gradeFixture(t, false)
				trial := comparisonTrial(t, source, 0)
				call := RoleCall{Attempted: true, Boundary: &RoleBoundaryObservation{
					Scope: roleBoundaryScope, FactScope: "run_retained_facts", Input: "observed", Output: output,
				}}
				if location == "user" {
					trial.DriverCalls = []RoleCall{call}
				} else {
					trial.Judge.Call = &call
				}
				writeAdmissionJSON(t, filepath.Join(source.ReportDir, source.Trials[0].File), trial)
				tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
				result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
					Home: fixture.home, OutputParent: fixture.output,
				})
				if !errors.Is(err, ErrGrade) || result.ID != "" || count.Load() != 0 ||
					fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
					t.Fatal("unsafe role receipt was regraded or started target work", err)
				}
			})
		}
	}
}

func TestComparePreservesRoleBoundaryFailuresAndUnknowns(t *testing.T) {
	for _, output := range []string{"unavailable", "leak_observed"} {
		for _, verdict := range []Verdict{Pass, Inconclusive, Fail} {
			t.Run(output+"/"+string(verdict), func(t *testing.T) {
				report := comparisonFixture(t, verdict)
				trial := comparisonTrial(t, report, 0)
				trial.DriverCalls = []RoleCall{{Attempted: true, Boundary: &RoleBoundaryObservation{
					Scope: roleBoundaryScope, FactScope: "call_facts", Input: "observed", Output: output,
				}}}
				writeAdmissionJSON(t, filepath.Join(report.ReportDir, report.Trials[0].File), trial)
				result, err := Compare(report.ReportDir, report.ReportDir)
				valid := verdict != Pass && (output != "leak_observed" || verdict == Fail)
				if valid && (err != nil || result.CurrentVerdict != verdict) ||
					!valid && !errors.Is(err, ErrCompare) {
					t.Fatal("comparison erased a role boundary failure or unknown", err)
				}
			})
		}
	}
}

type roleBoundaryMarshalerTrap struct{ Public string }

func (roleBoundaryMarshalerTrap) MarshalJSON() ([]byte, error) { panic("must not invoke custom JSON") }
func (roleBoundaryMarshalerTrap) String() string               { panic("must not invoke custom formatting") }

func TestRoleBoundaryBoundsAndUnknownsNeverBecomePassingCoverage(t *testing.T) {
	for _, kind := range []string{"cycle", "wide", "bytes", "unsupported", "opaque", "custom-method"} {
		t.Run(kind, func(t *testing.T) {
			response := &providers.LLMResponse{Content: `{"kind":"stop"}`, FinishReason: "stop"}
			args := map[string]any{}
			switch kind {
			case "cycle":
				args["cycle"] = args
			case "wide":
				args["wide"] = make([]string, 4097)
			case "bytes":
				response.ReasoningContent = strings.Repeat("x", MaxInputBytes+1)
			case "unsupported":
				args["function"] = func() {}
			case "opaque":
				response.ReasoningContent = `public\opaque`
			case "custom-method":
				args["custom"] = roleBoundaryMarshalerTrap{Public: "public"}
			}
			response.ToolCalls = []providers.ToolCall{{Arguments: args}}
			provider := &roleTestProvider{mediated: true, response: response}
			role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "user"}}
			output, call, err := role.call(t.Context(), "test-v1", "Public system.", "Public input.",
				map[string]string{"fact": "PRIVATE_ROLE_BOUND_CANARY"})
			if err == nil || output != "" || call.Boundary == nil || !call.Boundary.valid(true) {
				t.Fatal("unbounded or invalid output escaped role admission")
			}
			if kind == "custom-method" {
				if call.Code != "invalid_output" || call.Boundary.Output != "observed" {
					t.Fatal("custom marshaling was needed to inspect typed fields")
				}
			} else if call.Code != "output_evidence_unavailable" || call.Boundary.Output != "unavailable" {
				t.Fatal("unknown raw response was treated as fully observed", kind, call.Code)
			}
		})
	}
}

func TestRoleBoundaryRetainsEarlierFactsAndDoesNotHashRejectedJudgeInput(t *testing.T) {
	var retained privateFactRetention
	old := map[string]string{"old": "PRIVATE_EARLIER_ROLE_CANARY"}
	if !retained.add(old) {
		t.Fatal("fact fixture unavailable")
	}
	clear(old)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: `{"kind":"stop"}`, ReasoningContent: "PRIVATE_EARLIER_ROLE_CANARY", FinishReason: "stop",
	}}
	role := &ModelRole{provider: provider, retained: &retained, identity: RoleIdentity{Role: "judge"}}
	_, call, err := role.call(t.Context(), "test-v1", "Public system.", "Public input.",
		map[string]string{"new": "PRIVATE_CURRENT_ROLE_CANARY"})
	if !errors.Is(err, ErrPrivateEvidence) || call.Boundary.FactScope != "run_retained_facts" {
		t.Fatal("releasing the old case erased its role guard")
	}
	events := rolePromptEvents()
	events[1].Content = "PRIVATE_EARLIER_ROLE_CANARY"
	judgement := judgeConversation(t.Context(), role, rolePromptCase(), events)
	if judgement.Verdict != Fail || judgement.Call == nil || judgement.Call.Attempted || provider.calls != 1 ||
		judgement.RubricSHA256 != "" || judgement.EvidenceSHA256 != "" {
		t.Fatal("a cross-trial private judge prompt was hashed or sent")
	}
}

func TestOwnedRunGuardsUserAgainstEarlierPDFTrialFacts(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	value := "PRIVATE_OWNED_ROLE_COHORT_CANARY"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant", "content": `{"kind":"finish","text":"Finish."}`,
				"reasoning_content": value,
			}, "finish_reason": "stop",
		}}})
	}))
	defer server.Close()
	fixture.cfg.ModelList[0].APIBase = server.URL
	fixture.preset.UserModel = "eval-model"
	enableAdmissionPDF(t, fixture)
	if _, err := document.NewWriteJournal(
		filepath.Join(fixture.home, "state", "document-writes", "journal"),
	); err != nil {
		t.Fatal(err)
	}
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	first := items[0]
	for handle := range first.Case.Driver.Facts {
		first.Case.Driver.Facts[handle] = value
	}
	first.Case.Driver.Mode, first.Case.Driver.Persona, first.Case.Rubric = "scripted", "", nil
	first.Case.Driver.Steps = []Step{{
		Action:  Action{Kind: "finish", Text: "End this synthetic observation."},
		WaitFor: Expectation{Kind: "final"},
	}}
	second := LoadedCase{Case: rolePromptCase()}
	second.Case.Rubric = nil
	data, _ := json.Marshal(second.Case)
	second.Digest = digest(data)
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{first, second}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 20 * time.Second,
		IncludePending: true,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Fail || len(result.Trials) != 2 ||
		calls.Load() != 1 || result.Trials[1].Verdict != Fail {
		t.Fatal("run did not preserve earlier PDF facts at the later user boundary", err, result.Verdict)
	}
	data, err = os.ReadFile(filepath.Join(result.ReportDir, result.Trials[1].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil || len(trial.DriverCalls) != 1 ||
		trial.DriverCalls[0].Code != "private_output" ||
		trial.DriverCalls[0].Boundary.FactScope != "run_retained_facts" || strings.Contains(string(data), value) {
		t.Fatal("role cohort guard was absent or exposed the returned value", err)
	}
}
