package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func finalizedNoWriteFixture(t *testing.T) (Trial, LoadedCase, *pdfTrial, *PrivateSurfaceObservation) {
	t.Helper()
	domain, trial := postStopPDFControl(t)
	item := loadPDFTestCase(t, "pdf.cancel.no-write")
	domain.prepared.item = item
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	defer pending.close()
	prepared := domain.prepared
	inventory, err := prepared.nativeInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.close()
	surfaces := observePrivateSurfaces(t.Context(), prepared.home, nil, nil,
		map[string]bool{item.Case.Fixtures[0].SHA256: true})
	if !surfaces.valid() || surfaces.Code != "observed" {
		t.Fatal("native no-write private-file fixture was not inspectable", surfaces.Code)
	}
	pending.trials[trial.ID].conclude(t.Context(), &trial, inventory, nil, nil, cleanPDFLifecycle(), &surfaces)
	delete(pending.trials, trial.ID)
	return trial, item, prepared, &surfaces
}

func TestFinalizedPDFNoWriteJudgesOnlyAfterIndependentEvidence(t *testing.T) {
	for _, mode := range []string{
		"complete", "legacy", "scan-unknown", "invalid-scan", "leak", "forced", "truncated", "no-cleanup",
		"pending", "write", "foreign", "changed-source", "wire", "wrong-route", "terminal", "private-facts",
		"legacy-role", "unknown-role", "protected-job", "source-digest",
	} {
		t.Run(mode, func(t *testing.T) {
			trial, item, prepared, surfaces := finalizedNoWriteFixture(t)
			lifecycle := cleanPDFLifecycle()
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: adaptiveJudgePass, FinishReason: "stop",
				Usage: &providers.UsageInfo{PromptTokens: 10252, CompletionTokens: 20, TotalTokens: 10272},
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			switch mode {
			case "legacy":
				trial.PDF.PostStop = nil
			case "scan-unknown":
				surfaces.Code = "unavailable"
			case "invalid-scan":
				surfaces.Scope = ""
			case "leak":
				surfaces.Code, surfaces.LeakObserved = "leak_observed", true
			case "forced":
				lifecycle.Code, lifecycle.Forced = "forced_exit", true
			case "truncated":
				lifecycle.LogTruncated = true
			case "no-cleanup":
				trial.PDF.PostStop.FixtureRemoved = false
			case "pending":
				trial.PDF.PostStop.Census.Counts.PendingOperations++
			case "write":
				trial.PDF.PostStop.Census.Counts.WriteOperations++
			case "foreign":
				trial.PDF.PostStop.Census.Counts.ForeignChanges++
			case "changed-source":
				*trial.PDF.PostStop.Census.SourceUnchanged = false
			case "wire":
				trial.PDF.Wire.Complete = false
			case "wrong-route":
				trial.Events[1].SessionKey = "foreign-route"
			case "terminal":
				trial.Cancellation.TerminalRevision = trial.Cancellation.WaitingRevision
			case "private-facts":
				item.Case.Driver.Facts = map[string]string{"hidden": "private-canary"}
			case "legacy-role":
				trial.DriverCalls = []RoleCall{{Attempted: true}}
			case "unknown-role":
				trial.DriverCalls = []RoleCall{{Attempted: true, Boundary: &RoleBoundaryObservation{
					Scope: roleBoundaryScope, FactScope: "run_retained_facts", Input: "observed", Output: "unavailable",
				}}}
			case "protected-job":
				trial.PDF.JobID = uuid.NewString()
			case "source-digest":
				trial.PDF.SourceSHA256 = strings.Repeat("0", 64)
			}
			assessFinalizedPDF(t.Context(), judge, item.Case, &trial, prepared.workspace, lifecycle, surfaces, nil)
			if mode == "complete" {
				if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
					postStopCheck(trial, "privacy") != Pass || postStopCheck(trial, "pdf-no-write") != Pass ||
					trial.Judge == nil || trial.Judge.Call.Usage.PromptTokens != 10252 ||
					!validPDFNoWriteEvidence(trial, item.Case, prepared.workspace) {
					t.Fatal("independent finalized no-write evidence never reached the separate judge", trial.Code)
				}
			} else if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 {
				t.Fatal("incomplete or unsafe evidence reached the judge", mode, trial.Code)
			}
		})
	}
}

func TestFinalizedPDFNoWritePreservesHardFailureAndJudgeBoundaryFailure(t *testing.T) {
	for _, mode := range []string{"hard-check", "private-output", "opaque-output", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			trial, item, prepared, surfaces := finalizedNoWriteFixture(t)
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: adaptiveJudgePass, FinishReason: "stop",
			}}
			judge := &ModelRole{
				provider: provider, identity: RoleIdentity{Role: "judge"},
				filter: strings.NewReplacer("private-judge-canary", "[redacted]"),
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "hard-check":
				item.Case.Checks = append(item.Case.Checks,
					Check{ID: "missing", Kind: "response_contains", Expected: "unobserved text"})
			case "private-output":
				provider.response.Reasoning = "private-judge-canary"
			case "opaque-output":
				provider.response.Reasoning = `public\opaque`
			case "canceled":
				cancel()
			}
			assessFinalizedPDF(ctx, judge, item.Case, &trial, prepared.workspace, cleanPDFLifecycle(), surfaces, nil)
			if trial.Verdict == Pass || trial.Judge == nil || !consistentComparisonVerdict(trial) ||
				mode == "hard-check" && (trial.Verdict != Fail || trial.Judge.Verdict != Pass) ||
				mode == "private-output" && trial.Verdict != Fail ||
				mode != "hard-check" && trial.EvidenceEligible ||
				mode == "canceled" && provider.calls != 0 {
				t.Fatal(
					"final assessment hid a hard failure, privacy unknown or cancellation",
					trial.Verdict,
					trial.Code,
				)
			}
		})
	}
}

func TestPDFAllNativeCleanupPrecedesJudgingAndKeepsCaseHandlesSeparate(t *testing.T) {
	first, firstTrial := postStopPDFControl(t)
	prepared, err := preparePDFTrial(first.prepared.item, first.prepared.home, first.prepared.workspace,
		uuid.NewString(), first.prepared.agent)
	if err != nil {
		t.Fatal(err)
	}
	second, secondTrial := postStopPreparedPDFControl(t, prepared)
	pending := pdfRunFinalization{}
	defer pending.close()
	parent := filepath.Join(prepared.home, "reports")
	if err = os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := newPrivateReport(parent, prepared.home, prepared.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.root.Close() }()
	result := RunReport{}
	for _, pair := range []struct {
		domain *pdfConversation
		trial  Trial
	}{{first, firstTrial}, {second, secondTrial}} {
		pair.domain.prepared.item.Case.Driver.Facts = map[string]string{"original-handle": "private-first-fact"}
		pending.retain(pair.domain, pair.trial)
		pending.observeTrial(pair.trial)
		if err = report.write("pending-trial-"+pair.trial.ID+".json", pair.trial); err != nil {
			t.Fatal(err)
		}
		result.Trials = append(result.Trials, TrialSummary{ID: pair.trial.ID, File: "trial-" + pair.trial.ID + ".json"})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	err = pending.finish(
		ctx,
		report,
		&result,
		cleanPDFLifecycle(),
		map[string]string{
			"private-first-fact":   "private-first-fact",
			"private-earlier-fact": "private-earlier-fact",
		},
		nil,
		&PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"},
		func(item Case, trial *Trial) {
			calls++
			cancel() // Simulate a judge outliving the native cleanup context.
			if len(pending.trials) != 0 || len(item.Driver.Facts) != 1 ||
				item.Driver.Facts["original-handle"] != "private-first-fact" ||
				trial.PDF.PostStop == nil || !trial.PDF.PostStop.FixtureRemoved {
				t.Fatal("model work began before all native cleanup, or run scan facts replaced case handles")
			}
			for _, session := range []string{firstTrial.SessionID, secondTrial.SessionID} {
				if _, statErr := os.Stat(
					filepath.Join(prepared.workspace, ".eval-trials", session),
				); !errors.Is(
					statErr,
					os.ErrNotExist,
				) {
					t.Fatal("a later trial still owned its source at judge time", statErr)
				}
			}
		},
	)
	if err != nil || calls != 2 {
		t.Fatal("model work consumed the later native cleanup budget", err, calls)
	}
}

func TestGradeFinalizedPDFNoWriteWithoutSourceFixtureOrTargetWork(t *testing.T) {
	for _, sourceFields := range []bool{false, true} {
		name := "no-read-check"
		if sourceFields {
			name = "native-read-check"
		}
		t.Run(name, func(t *testing.T) { testGradeFinalizedPDFNoWrite(t, sourceFields) })
	}
}

func testGradeFinalizedPDFNoWrite(t *testing.T, sourceFields bool) {
	t.Helper()
	fixture, _, _, calls := gradeFixture(t, false)
	trial, item, prepared, surfaces := finalizedNoWriteFixture(t)
	if sourceFields {
		item.Case.PDF.SourceFields = &PDFSourceFieldsExpectation{
			MinReturnedFields: 5, RequiredFields: 3, ValuesPresent: 3, DefaultsPresent: 4,
		}
		item.Case.Checks = append(item.Case.Checks, Check{ID: "source-fields", Kind: "pdf_source_fields"})
		data, _ := json.Marshal(item.Case)
		item.Digest, trial.CaseSHA256 = digest(data), digest(data)
		binding, valid := pdfSourceBinding(trial, prepared.workspace)
		if !valid {
			t.Fatal("source turn binding unavailable")
		}
		trial.PDF.SourceFields = projectPDFSourceFields(sourceFieldsTrace(t, binding, item.Case, true),
			binding, digest([]byte("native-source-bytes")), nil)
		fixture.probe.GitCommit, fixture.probe.BuildVersion = nativeModelTestCommit, "native-test"
	}
	assessFinalizedPDF(t.Context(), nil, item.Case, &trial, prepared.workspace, cleanPDFLifecycle(), surfaces, nil)
	if !trial.EvidenceEligible || trial.Verdict != Inconclusive {
		t.Fatal("unjudged finalized source is not eligible", trial.Code)
	}
	fixture.home, fixture.workspace = prepared.home, prepared.workspace
	fixture.output = filepath.Join(prepared.home, "reports")
	fixture.preset.ConfigPath = filepath.Join(prepared.home, "config.json")
	fixture.preset.IsolationRoot, fixture.preset.ExpectedWorkspace = prepared.home, prepared.workspace
	fixture.preset.ExpectedAgent = prepared.agent
	fixture.cfg.Agents.Defaults.Workspace = prepared.workspace
	fixture.writeConfig(t)
	t.Setenv(config.EnvHome, fixture.home)
	t.Setenv(config.EnvConfig, fixture.preset.ConfigPath)
	if err := os.WriteFile(filepath.Join(fixture.home, "eval-grading-key"),
		[]byte("synthetic-grading-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.home, ".security.yml"),
		[]byte("model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.output, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := newPrivateReport(fixture.output, fixture.home, fixture.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.root.Close() }()
	name := "trial-" + trial.ID + ".json"
	trial.Repetition = 1
	writeAdmissionJSON(t, filepath.Join(report.dir, name), trial)
	source := RunReport{
		Version:         Version,
		ID:              uuid.NewString(),
		PresetID:        fixture.preset.ID,
		Mode:            "run",
		Repeat:          1,
		Complete:        true,
		Verdict:         trial.Verdict,
		TargetLifecycle: cleanPDFLifecycle(),
		PrivateSurfaces: surfaces,
		Runtime:         fixture.probe,
		Coverage:        map[Verdict]int{Inconclusive: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: item.Case.ID, CaseSHA256: item.Digest, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(report.dir, "run.json"), source)
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, report.dir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
	})
	if err != nil || !result.Complete || result.Verdict != Pass || calls.Load() != 1 ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes ||
		result.RoleUsage[0].InputTokens != 10252 || postStopCheck(comparisonTrial(t, result, 0), "privacy") != Pass {
		t.Fatal("PDF regrade lost independent checks, touched a target or required a deleted fixture", err)
	}
	if _, err = os.Stat(
		filepath.Join(prepared.workspace, ".eval-trials", trial.SessionID, "source.pdf"),
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatal("regrade recreated a removed source fixture")
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal("finalized PDF regrade could not be compared", err)
	}
	modes := []string{
		"legacy", "scope", "profile", "lifecycle", "counts", "terminal", "source", "role", "not-eligible",
	}
	if sourceFields {
		modes = append(modes, "read-missing", "read-binding", "read-build", "read-sequence", "read-overflow")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			copy := comparisonTrial(t, RunReport{ReportDir: report.dir, Trials: source.Trials}, 0)
			changed := source
			profile := *surfaces
			lifecycle := *source.TargetLifecycle
			changed.PrivateSurfaces, changed.TargetLifecycle = &profile, &lifecycle
			switch mode {
			case "legacy":
				copy.PDF.PostStop.AssessmentScope = ""
			case "scope":
				copy.PDF.PostStop.AssessmentScope = "invented"
			case "profile":
				profile.Code = "unavailable"
			case "lifecycle":
				lifecycle.LogTruncated = true
			case "counts":
				copy.PDF.PostStop.Census.Counts.MediaIntents = 1
			case "terminal":
				copy.Cancellation.TerminalRevision = copy.Cancellation.WaitingRevision
			case "source":
				copy.PDF.SourceSHA256 = strings.Repeat("0", 64)
			case "role":
				copy.DriverCalls = []RoleCall{{Attempted: true}}
			case "not-eligible":
				copy.EvidenceEligible = false
			case "read-missing":
				copy.PDF.SourceFields = nil
			case "read-binding":
				copy.PDF.SourceFields.BindingSHA256 = strings.Repeat("e", 64)
			case "read-build":
				copy.PDF.SourceFields.Source.Commit = strings.Repeat("e", 40)
			case "read-sequence":
				copy.PDF.SourceFields.Reads[0].ResultSequence = copy.PDF.SourceFields.TerminalSequence
			case "read-overflow":
				copy.PDF.SourceFields.UnknownResults = int(^uint(0) >> 1)
			}
			writeAdmissionJSON(t, filepath.Join(report.dir, name), copy)
			writeAdmissionJSON(t, filepath.Join(report.dir, "run.json"), changed)
			result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, report.dir, RunOptions{
				Home: fixture.home, OutputParent: fixture.output,
			})
			if !errors.Is(err, ErrGrade) || result.ID != "" || calls.Load() != 1 ||
				fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
				t.Fatal("unqualified PDF source reached a billable judge or target", mode, err)
			}
			writeAdmissionJSON(t, filepath.Join(report.dir, name), trial)
			writeAdmissionJSON(t, filepath.Join(report.dir, "run.json"), source)
		})
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "synthetic-grading-credential") {
		t.Fatal("grading credentials escaped to the report")
	}
}
