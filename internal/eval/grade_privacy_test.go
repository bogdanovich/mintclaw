package eval

import (
	"bytes"
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

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestGradeRawGeneratedCanaryGuardPrecedesProjection(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, raw := range []string{
		`{"content":"MC_EVAL_0123456789abcdef"}`,
		`{"content":"MC_EVAL_0123456789abcdef","content":"safe"}`,
		`{"discarded":{"model":"MC_EVAL_0123456789abcdef"}}`,
		`{"content":"\u004d\u0043\u005fEVAL_0123456789abcdef"}`,
		`{"content":"{\"hidden\":\"\\u004dC_EVAL_0123456789abcdef\"}"}`,
		`{"MC_EVAL_0123456789abcdef":"safe"}`,
		`{"content":"safe"} MC_EVAL_0123456789abcdef`,
	} {
		if err = root.WriteFile("trial.json", []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		data, readErr := readGradeEvidence(t.Context(), root, "trial.json", nil)
		if !errors.Is(readErr, ErrGrade) || len(data) != 0 {
			t.Fatal("raw canary family was projected or retained")
		}
	}
	for _, raw := range []string{
		`{"content":"safe\u002dtext"}`, `{"content":"MC_EVAL_CONTEXT_7C9"}`, `{"prefix":"MC_EVAL_"}`,
	} {
		if err = root.WriteFile("trial.json", []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = readGradeEvidence(t.Context(), root, "trial.json", nil); err != nil {
			t.Fatal("safe public report refused", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if data, err := readGradeEvidence(
		ctx,
		root,
		"trial.json",
		nil,
	); !errors.Is(err, ErrGrade) ||
		data != nil {
		t.Fatal("exhausted scan returned evidence")
	}
}

func TestGradePrivateFactsRemainDomainAndFormatGated(t *testing.T) {
	for _, scenario := range []string{"submit", "followup"} {
		item := browserCorpusCase(t, scenario).Case
		if !offlineGradeFactsEligible(item) {
			t.Fatal("generated form facts refused")
		}
		for handle := range item.Driver.Facts {
			item.Driver.Facts[handle] = generatePrivateFact
		}
		if !offlineGradeFactsEligible(item) {
			t.Fatal("explicit v1 generation marker refused")
		}
		for handle := range item.Driver.Facts {
			item.Driver.Facts[handle] = "other-private-format"
			break
		}
		if offlineGradeFactsEligible(item) {
			t.Fatal("one literal private fact bypassed the family guard")
		}
	}
	item := rolePromptCase()
	item.Driver.Facts = map[string]string{"name": generatePrivateFact}
	if offlineGradeFactsEligible(item) {
		t.Fatal("private general/PDF admission was widened")
	}
	item.PDF = &PDFCase{}
	if offlineGradeFactsEligible(item) {
		t.Fatal("private PDF admission was widened")
	}
}

func TestGradeGeneratedFactGuardCoversTypedRoleBoundaries(t *testing.T) {
	for _, surface := range []string{"input", "reasoning", "arguments", "content"} {
		t.Run(surface, func(t *testing.T) {
			guard := newGradeFactGuard()
			defer guard.clear()
			leak := "MC_EVAL_fedcba9876543210"
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: `{"version":1,"criteria":[]}`, FinishReason: "stop",
			}}
			input := "Visible conversation."
			switch surface {
			case "input":
				input = leak
			case "reasoning":
				provider.response.Reasoning = leak
			case "arguments":
				provider.response.ToolCalls = []providers.ToolCall{{
					ID: "discarded", Name: "unused", Arguments: map[string]any{"hidden": leak},
				}}
			case "content":
				provider.response.Content = leak
			}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}, retained: &guard}
			output, call, err := judge.Call(t.Context(), JudgePromptVersion, judgeSystemPrompt, input)
			calls := 1
			if surface == "input" {
				calls = 0
			}
			if !errors.Is(err, ErrPrivateEvidence) || output != "" || provider.calls != calls ||
				call.Boundary.FactScope != generatedFactGuardScope || roleBoundaryEligible(&call) {
				t.Fatal("generated family escaped typed role guard", surface, err)
			}
		})
	}
}

func TestGradeBrowserGeneratedFormsWithoutSecretsOrTargetWork(t *testing.T) {
	for _, scenario := range []string{"submit", "followup"} {
		t.Run(scenario, func(t *testing.T) {
			c := newBrowserFormControl(t, scenario)
			record, prompt := c.prepareReview(t)
			c.confirm(t, record, prompt)
			c.submit(t)
			trial, _ := c.finalize(t)
			fixture := c.fixture
			host := &BrowserFixtureHostEvidence{
				Scope: "owned_http_fixture_host_v1", Code: "closed", Trials: 1,
				OriginSHA256: digest([]byte(c.domain.fixture.origin)),
			}
			pending := browserRun{trials: map[string]browserPendingTrial{trial.ID: {conversation: c.domain}}}
			surfaces := observePrivateSurfaces(t.Context(), fixture.home, c.item.Case.Driver.Facts, nil, nil,
				pending.approvalKeyWitness(fixture.home))
			assessFinalizedBrowser(t.Context(), nil, c.item.Case, &trial, fixture.workspace,
				cleanPDFLifecycle(), &surfaces, host)
			if !trial.EvidenceEligible {
				t.Fatal("positive native control was not eligible", trial.Code)
			}
			writer, err := newPrivateReport(fixture.output, fixture.home, fixture.workspace, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.root.Close() })
			name := "trial-" + trial.ID + ".json"
			writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
			source := RunReport{
				Version: Version, ID: uuid.NewString(), PresetID: fixture.preset.ID,
				Mode: "run", Repeat: 1, Complete: true, Verdict: trial.Verdict, StartedAt: trial.StartedAt,
				TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: &surfaces, BrowserFixtures: host,
				Runtime: c.domain.target.probe, Coverage: map[Verdict]int{trial.Verdict: 1},
				Trials: []TrialSummary{{
					ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
					Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
				}},
			}
			writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
			var calls atomic.Int32
			var response atomic.Value
			response.Store(`{"version":1,"criteria":[{"id":"coherent","verdict":"pass",` +
				`"reason":"The visible review preceded submission.","evidence_ids":["` + lastOutcome(trial.Events).ID + `"]}]}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil ||
					generatedFactJSONValue(body) {
					t.Error("judge received tools or a generated value")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": response.Load().(string)},
					"finish_reason": "stop",
				}}})
			}))
			t.Cleanup(server.Close)
			fixture.cfg.ModelList[0].APIBase = server.URL
			fixture.preset.JudgeModel = "eval-model"
			fixture.writeConfig(t)
			if os.WriteFile(filepath.Join(fixture.home, "eval-grading-key"),
				[]byte("synthetic-grading-credential"), 0o600) != nil ||
				os.WriteFile(
					filepath.Join(fixture.home, ".security.yml"),
					[]byte(
						"model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n",
					),
					0o600,
				) != nil {
				t.Fatal("synthetic judge credential unavailable")
			}
			if os.Remove(filepath.Join(fixture.workspace, "state/browser/browser.json")) != nil {
				t.Fatal("native state control unavailable")
			}
			item := browserCorpusCase(t, scenario)
			before, err := os.ReadFile(filepath.Join(writer.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
			options := RunOptions{Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: time.Minute}
			graded, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, writer.dir, options)
			if err != nil || graded.Verdict != Pass || calls.Load() != 1 ||
				fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
				t.Fatal("generated form regrade failed or performed target work", err)
			}
			got := comparisonTrial(t, graded, 0)
			if got.Judge == nil || got.Judge.Call == nil ||
				got.Judge.Call.Boundary.FactScope != generatedFactGuardScope {
				t.Fatal("regrade claimed an original-value guard")
			}
			if _, err = Compare(writer.dir, graded.ReportDir); err != nil {
				t.Fatal("private regrade could not be compared", err)
			}
			for _, mode := range []string{"stream", "metadata", "native", "execution", "literal", "run"} {
				t.Run(mode, func(t *testing.T) {
					changed := cloneBrowserFormControlValue(t, trial)
					selected := cloneBrowserFormControlValue(t, item)
					leak := "MC_EVAL_0011223344556677"
					switch mode {
					case "stream":
						changed.Events = append(
							changed.Events,
							Event{ID: uuid.NewString(), Turn: 1, Kind: "stream", Content: leak},
						)
					case "metadata":
						changed.Events[0].Model = leak
					case "native":
						changed.Browser.Form.ContextRetained = false
					case "execution":
						changed.ExecutionVerdict = Inconclusive
					case "literal":
						for handle := range selected.Case.Driver.Facts {
							selected.Case.Driver.Facts[handle] = "fixed-private-value"
						}
					case "run":
						changedSource := source
						changedSource.Runtime.BuildVersion = leak
						writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), changedSource)
						defer writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
					}
					writeAdmissionJSON(t, filepath.Join(writer.dir, name), changed)
					count := calls.Load()
					_, err := Grade(t.Context(), fixture.preset, []LoadedCase{selected}, writer.dir, options)
					if !errors.Is(err, ErrGrade) || calls.Load() != count {
						t.Fatal("unsafe private form reached a judge", mode, err)
					}
				})
			}
			if err = os.WriteFile(filepath.Join(writer.dir, name), before, 0o600); err != nil {
				t.Fatal(err)
			}
			response.Store("MC_EVAL_8899aabbccddeeff")
			leaked, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, writer.dir, options)
			if !errors.Is(err, ErrNotPassed) || leaked.Verdict != Fail {
				t.Fatal("generated judge leak was not retained as failure", err)
			}
			failed := comparisonTrial(t, leaked, 0)
			raw, err := os.ReadFile(filepath.Join(leaked.ReportDir, leaked.Trials[0].File))
			if err != nil || failed.EvidenceEligible || failed.Judge.Code != "private_judge_output" ||
				strings.Contains(string(raw), generatedPrivateFactPrefix) {
				t.Fatal("leaked judge content reached a report", err)
			}
			after, err := os.ReadFile(filepath.Join(writer.dir, name))
			if err != nil || !bytes.Equal(before, after) || fixture.tasks.Load() != tasks ||
				fixture.probes.Load() != probes {
				t.Fatal("regrade rewrote original evidence or performed target work", err)
			}
		})
	}
}
