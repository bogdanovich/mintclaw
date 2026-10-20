package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGradeBrowserAmbiguityUsesOnlyRetainedEvidence(t *testing.T) {
	c := completeBrowserAmbiguityControl(t)
	writer, trial := finalizedAmbiguityControl(t, c)
	f := c.fixture
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	host := ambiguityHost(c)
	assessFinalizedBrowser(t.Context(), nil, c.item.Case, &trial, f.workspace, cleanPDFLifecycle(), surfaces, host)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": ambiguityJudgePass(trial)},
			"finish_reason": "stop",
		}}})
	}))
	defer server.Close()
	f.cfg.ModelList[0].APIBase = server.URL
	f.preset.JudgeModel = "eval-model"
	f.writeConfig(t)
	if os.WriteFile(filepath.Join(f.home, "eval-grading-key"), []byte("synthetic-grading-credential"), 0o600) != nil ||
		os.WriteFile(filepath.Join(f.home, ".security.yml"),
			[]byte("model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n"), 0o600) != nil {
		t.Fatal("synthetic grading credential unavailable")
	}
	name := "trial-" + trial.ID + ".json"
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
	source := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: f.preset.ID, Mode: "run", Repeat: 1,
		Complete: true, Verdict: trial.Verdict, StartedAt: trial.StartedAt, Runtime: f.probe,
		TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: surfaces, BrowserFixtures: host,
		Coverage: map[Verdict]int{trial.Verdict: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	for _, path := range []string{"state/browser/browser.json", "state/interaction_registry.json"} {
		if os.Remove(filepath.Join(f.workspace, path)) != nil {
			t.Fatal("native state removal control unavailable")
		}
	}
	probes, tasks := f.probes.Load(), f.tasks.Load()
	graded, err := Grade(t.Context(), f.preset, []LoadedCase{c.item}, writer.dir,
		RunOptions{Home: f.home, OutputParent: f.output, SuiteTimeout: 10 * time.Second})
	if err != nil || graded.Verdict != Pass || calls.Load() != 1 || f.probes.Load() != probes ||
		f.tasks.Load() != tasks {
		t.Fatal("offline ambiguity regrade ran target work or lost evidence", err)
	}
	if _, err = Compare(writer.dir, graded.ReportDir); err != nil {
		t.Fatal("ambiguity regrade comparison lost retained identity", err)
	}
	hard := c.item
	hard.Case = cloneBrowserFormControlValue(t, c.item.Case)
	hard.Case.Checks = append(hard.Case.Checks,
		Check{ID: "contradiction", Kind: "response_contains", Expected: "not present in this conversation"})
	encoded, err := json.Marshal(hard.Case)
	if err != nil {
		t.Fatal(err)
	}
	hard.Digest = digest(encoded)
	hardTrial := cloneBrowserFormControlValue(t, trial)
	hardTrial.CaseSHA256, hardTrial.Verdict, hardTrial.Code = hard.Digest, Fail, "hard_check_failed"
	hardTrial.Checks = gradeFinalizedBrowserChecks(hard.Case, hardTrial)
	hardSource := source
	hardSource.Trials = append([]TrialSummary(nil), source.Trials...)
	hardSource.Verdict, hardSource.Coverage = Fail, map[Verdict]int{Fail: 1}
	hardSource.Trials[0].CaseSHA256 = hard.Digest
	hardSource.Trials[0].Verdict, hardSource.Trials[0].Code = hardTrial.Verdict, hardTrial.Code
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), hardTrial)
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), hardSource)
	hardGrade, err := Grade(t.Context(), f.preset, []LoadedCase{hard}, writer.dir,
		RunOptions{Home: f.home, OutputParent: f.output, SuiteTimeout: 10 * time.Second})
	if !errors.Is(err, ErrNotPassed) || hardGrade.Verdict != Fail || calls.Load() != 2 ||
		f.probes.Load() != probes || f.tasks.Load() != tasks {
		t.Fatal("passing judge overrode a hard failure or ran target work", err)
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	for _, mode := range []string{"legacy", "ineligible", "clarification", "approval", "native", "effect", "answer", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			copy := cloneBrowserFormControlValue(t, trial)
			switch mode {
			case "legacy":
				copy.Browser.Ambiguity = nil
			case "ineligible":
				copy.EvidenceEligible = false
			case "clarification":
				copy.Browser.Ambiguity.Clarification = nil
			case "approval":
				copy.Browser.Ambiguity.Approval = nil
			case "native":
				copy.Browser.Ambiguity.Native = nil
			case "effect":
				copy.Browser.Fixture.Effects = Fail
			case "answer":
				copy.Events[2].Content = "North"
			case "prompt":
				copy.Events[3].Content = "An unrelated approval"
			}
			writeAdmissionJSON(t, filepath.Join(writer.dir, name), copy)
			_, err := Grade(t.Context(), f.preset, []LoadedCase{c.item}, writer.dir,
				RunOptions{Home: f.home, OutputParent: f.output})
			if !errors.Is(err, ErrGrade) || calls.Load() != 2 || f.probes.Load() != probes || f.tasks.Load() != tasks {
				t.Fatal("incomplete/legacy report reached a judge or target", mode, err)
			}
		})
	}
}
