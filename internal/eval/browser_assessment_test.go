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

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func browserJudgePass(trial Trial) string {
	return `{"version":1,"criteria":[{"id":"accurate","verdict":"pass",` +
		`"reason":"The read-only answer matches the visible request.","evidence_ids":["` +
		trial.Events[1].ID + `"]}]}`
}

func browserReadHost(control *browserReadControl) *BrowserFixtureHostEvidence {
	return &BrowserFixtureHostEvidence{
		Scope: "owned_http_fixture_host_v1", Code: "closed",
		OriginSHA256: digest([]byte(control.page.origin)), Trials: 1,
	}
}

func TestFinalizedBrowserReadAdmitsJudgeOnlyWithAllIndependentEvidence(t *testing.T) {
	for _, mode := range []string{
		"complete", "legacy", "scope", "state", "delivery", "resources", "privacy", "leak", "forced",
		"truncated", "host", "host-origin", "host-empty", "route", "facts", "artifact", "context", "role", "no-wire-join", "execution",
	} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			trial := control.finalize(t)
			lifecycle := cleanPDFLifecycle()
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			host := browserReadHost(control)
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserJudgePass(trial), FinishReason: "stop",
				Usage: &providers.UsageInfo{PromptTokens: 10252, CompletionTokens: 20, TotalTokens: 10272},
			}}
			judge := &ModelRole{
				provider: provider, identity: RoleIdentity{Role: "judge"},
				filter: strings.NewReplacer("private-judge-canary", "[redacted]"),
			}
			switch mode {
			case "legacy":
				trial.Browser.Read = nil
			case "scope":
				trial.Browser.Read.Scope = "invented"
			case "state":
				trial.Browser.Fixture.Read = Inconclusive
			case "delivery":
				trial.Browser.Read.FinalDeliveries--
			case "resources":
				trial.Browser.Read.TemporaryEmpty = false
			case "privacy":
				surfaces.Code = "unavailable"
			case "leak":
				surfaces.Code, surfaces.LeakObserved = "leak_observed", true
			case "forced":
				lifecycle.Code, lifecycle.Forced = "forced_exit", true
			case "truncated":
				lifecycle.LogTruncated = true
			case "host":
				host.Code = "close_failed"
			case "host-origin":
				host.OriginSHA256 = digest([]byte("foreign-origin"))
			case "host-empty":
				host.Trials = 0
			case "route":
				trial.Events[1].SessionKey = "foreign"
			case "facts":
				control.item.Case.Driver.Facts = map[string]string{"private": "private-value"}
			case "artifact":
				control.item.Case.Browser.Scenario = "artifact"
			case "context":
				control.item.Case.Browser.Scenario = "followup"
			case "role":
				trial.DriverCalls = []RoleCall{{Attempted: true}}
			case "no-wire-join":
				trial.TransportClosed = false
			case "execution":
				trial.ExecutionVerdict = Inconclusive
			}
			assessFinalizedBrowser(t.Context(), judge, control.item.Case, &trial,
				control.fixture.workspace, lifecycle, surfaces, host)
			if mode == "complete" {
				if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
					!validBrowserReadEvidence(trial, control.item.Case, control.fixture.workspace) ||
					trial.Judge.Call.Usage.PromptTokens != 10252 {
					t.Fatal("bound native read evidence did not reach the separate judge", trial.Code)
				}
				for _, check := range trial.Checks {
					if check.Verdict != Pass {
						t.Fatal("native check not graded", check.ID)
					}
				}
			} else if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 {
				t.Fatal("incomplete read evidence was judged or promoted", mode, trial.Code)
			}
		})
	}
}

func TestFinalizedBrowserReadRejectsContradictoryNativeReplyBeforeJudge(t *testing.T) {
	control := newBrowserReadControl(t)
	for _, event := range control.trial.Events {
		if event.Kind != "final" {
			continue
		}
		intent, err := newBrowserFinalIntent(control.fixture.workspace, control.trial, event)
		if err != nil {
			t.Fatal(err)
		}
		intent.Message.Content = "Contradictory native reply."
		writeAdmissionJSON(t, filepath.Join(control.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	}
	trial := control.finalize(t)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: browserJudgePass(trial), FinishReason: "stop",
	}}
	judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
	assessFinalizedBrowser(t.Context(), judge, control.item.Case, &trial, control.fixture.workspace,
		cleanPDFLifecycle(), &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"},
		browserReadHost(control))
	if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 ||
		trial.Browser.Read.Code != "native_delivery_unavailable" {
		t.Fatal("contradictory native delivery evidence was judged or promoted", trial.Code, provider.calls)
	}
}

func TestFinalizedBrowserReadPreservesHardAndRolePrivacyFailures(t *testing.T) {
	for _, mode := range []string{"hard", "opaque", "private", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			trial := control.finalize(t)
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserJudgePass(trial), FinishReason: "stop",
			}}
			ctx := t.Context()
			switch mode {
			case "hard":
				control.item.Case.Checks = append(control.item.Case.Checks,
					Check{ID: "not-present", Kind: "response_contains", Expected: "unobserved-token"})
			case "opaque":
				provider.response.Reasoning = `public\opaque`
			case "private":
				provider.response.Content += "private-judge-canary"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			judge := &ModelRole{
				provider: provider, identity: RoleIdentity{Role: "judge"},
				filter: strings.NewReplacer("private-judge-canary", "[redacted]"),
			}
			if mode == "private" {
				judge.retained = &privateFactRetention{values: map[string]string{"fact": "private-judge-canary"}}
			}
			assessFinalizedBrowser(ctx, judge, control.item.Case, &trial, control.fixture.workspace,
				cleanPDFLifecycle(), &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"},
				browserReadHost(control))
			if trial.Verdict == Pass || mode == "hard" && trial.Verdict != Fail ||
				mode == "private" && trial.Verdict != Fail || mode == "opaque" && trial.EvidenceEligible {
				t.Fatal("hard failure or unavailable/private judge output was lost", mode, trial.Code)
			}
		})
	}
}

func TestGradeBrowserReadUsesRetainedEvidenceWithoutTargetOrBrowserFiles(t *testing.T) {
	control := newBrowserReadControl(t)
	trial := control.finalize(t)
	fixture := control.fixture
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	host := browserReadHost(control)
	assessFinalizedBrowser(
		t.Context(),
		nil,
		control.item.Case,
		&trial,
		fixture.workspace,
		cleanPDFLifecycle(),
		surfaces,
		host,
	)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(request.Body).Decode(&body) != nil || body["tools"] != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": browserJudgePass(trial)}, "finish_reason": "stop",
		}}})
	}))
	t.Cleanup(server.Close)
	fixture.cfg.ModelList[0].APIBase = server.URL
	fixture.preset.JudgeModel = "eval-model"
	fixture.writeConfig(t)
	if os.WriteFile(
		filepath.Join(fixture.home, "eval-grading-key"),
		[]byte("synthetic-grading-credential"),
		0o600,
	) != nil ||
		os.WriteFile(filepath.Join(fixture.home, ".security.yml"),
			[]byte("model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n"), 0o600) != nil {
		t.Fatal("synthetic grade credential reference unavailable")
	}
	report, err := newPrivateReport(fixture.output, fixture.home, fixture.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.root.Close() }()
	name := "trial-" + trial.ID + ".json"
	writeAdmissionJSON(t, filepath.Join(report.dir, name), trial)
	source := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: fixture.preset.ID, Mode: "run", Repeat: 1,
		Complete: true, Verdict: trial.Verdict, TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: surfaces,
		BrowserFixtures: host, Runtime: fixture.probe, Coverage: map[Verdict]int{trial.Verdict: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(report.dir, "run.json"), source)
	if os.Remove(filepath.Join(fixture.workspace, "state/browser/browser.json")) != nil {
		t.Fatal("cannot remove synthetic source native state")
	}
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, report.dir,
		RunOptions{Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second})
	if err != nil || result.Verdict != Pass || calls.Load() != 1 || fixture.tasks.Load() != tasks ||
		fixture.probes.Load() != probes {
		t.Fatal("browser regrade reopened a target or lost independent native evidence", err)
	}
	if _, err = os.Stat(
		filepath.Join(fixture.workspace, "state/browser/browser.json"),
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatal("grade recreated a native browser store")
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal(err)
	}
	originalTrial, marshalErr := json.Marshal(trial)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, mode := range []string{"missing-browser", "legacy", "privacy", "lifecycle", "host", "host-origin", "host-empty", "route", "scope", "cleanup", "not-eligible"} {
		t.Run(mode, func(t *testing.T) {
			copy, decodeErr := decodeDocument[Trial](originalTrial, 8*1024*1024)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			changed := source
			profile := *surfaces
			lifecycle := *source.TargetLifecycle
			hostCopy := *host
			changed.PrivateSurfaces, changed.TargetLifecycle, changed.BrowserFixtures = &profile, &lifecycle, &hostCopy
			switch mode {
			case "missing-browser":
				copy.Browser = nil
			case "legacy":
				copy.Browser.Read = nil
			case "privacy":
				profile.Code = "unavailable"
			case "lifecycle":
				lifecycle.LogTruncated = true
			case "host":
				hostCopy.Code = "close_failed"
			case "host-origin":
				hostCopy.OriginSHA256 = digest([]byte("foreign-origin"))
			case "host-empty":
				hostCopy.Trials = 0
			case "route":
				copy.Browser.Read.SessionScope = "session_foreign"
			case "scope":
				copy.Browser.Read.Scope = "invented"
			case "cleanup":
				copy.Browser.Read.LogicalSettled = false
			case "not-eligible":
				copy.EvidenceEligible = false
			}
			writeAdmissionJSON(t, filepath.Join(report.dir, name), copy)
			writeAdmissionJSON(t, filepath.Join(report.dir, "run.json"), changed)
			before := calls.Load()
			_, gradeErr := Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, report.dir,
				RunOptions{Home: fixture.home, OutputParent: fixture.output})
			if !errors.Is(gradeErr, ErrGrade) || calls.Load() != before {
				t.Fatal("invalid retained browser evidence made a billable call", mode, gradeErr)
			}
		})
	}
}

func TestBrowserReadFinalizationCapturesBeforeJudgingAndKeepsLateRawLeaks(t *testing.T) {
	for _, mode := range []string{"complete", "opaque-then-leak"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			trial := control.trial
			trial.Checks = gradeConversation(control.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
			trial.Verdict, trial.Code = Inconclusive, "required_check_unknown"
			report, err := newPrivateReport(
				control.fixture.output,
				control.fixture.home,
				control.fixture.workspace,
				uuid.NewString(),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = report.root.Close() }()
			name := "trial-" + trial.ID + ".json"
			if report.write("pending-"+name, trial) != nil {
				t.Fatal("pending control unavailable")
			}
			pending := browserRun{}
			pending.retain(control.item.Case, trial, control.page)
			facts := map[string]string{}
			if mode == "opaque-then-leak" {
				facts["private"] = "PRIVATE_BROWSER_FINALIZE_CANARY"
				if os.WriteFile(filepath.Join(control.fixture.workspace, "state/browser/browser.json"),
					[]byte("{\"unfinished\":"), 0o600) != nil ||
					os.WriteFile(
						filepath.Join(control.fixture.workspace, "state/outbox/late.json"),
						[]byte(
							`{"discarded":"\u0050RIVATE_BROWSER_FINALIZE_CANARY","discarded":"public"}`,
						),
						0o600,
					) != nil {
					t.Fatal("late raw leak control unavailable")
				}
			}
			observation := observePrivateSurfaces(t.Context(), control.fixture.home, facts, nil, nil)
			if mode == "opaque-then-leak" && !observation.LeakObserved {
				t.Fatal("whole-profile observer hid a later leak")
			}
			// The admission fixture intentionally contains synthetic inline keys;
			// this unit control scans facts, not a live credentialed profile.
			result := RunReport{
				TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: &observation,
				BrowserFixtures: browserReadHost(control), Trials: []TrialSummary{{
					ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
					Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
				}},
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserJudgePass(trial), FinishReason: "stop",
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			if err = pending.finish(t.Context(), control.target(), report, &result, facts, nil,
				func(item Case, current *Trial) {
					assessFinalizedBrowser(t.Context(), judge, item, current, control.fixture.workspace,
						result.TargetLifecycle, result.PrivateSurfaces, result.BrowserFixtures)
				}); err != nil {
				t.Fatal("finalization failed", err)
			}
			final := comparisonTrial(t, RunReport{ReportDir: report.dir, Trials: result.Trials}, 0)
			if mode == "complete" && (final.Verdict != Pass || provider.calls != 1 || !final.EvidenceEligible) ||
				mode != "complete" && (final.Verdict != Fail || provider.calls != 0 || final.EvidenceEligible) {
				t.Fatal("native finalization invented success or hid a raw leak", mode, final.Code, observation.Code)
			}
			if _, err = os.Stat(filepath.Join(report.dir, "pending-"+name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pending checkpoint was not retired")
			}
		})
	}
}
