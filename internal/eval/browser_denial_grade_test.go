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

func TestGradeBrowserDenialUsesOnlyRetainedEvidence(t *testing.T) {
	control := newBrowserDenialControl(t)
	writer, trial := finalizedDenialReport(t, control)
	fixture := control.fixture
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	host := browserReadHost(control.browserReadControl)
	assessFinalizedBrowser(t.Context(), nil, control.item.Case, &trial, fixture.workspace,
		cleanPDFLifecycle(), surfaces, host)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": browserDenialJudgePass(trial)},
			"finish_reason": "stop",
		}}})
	}))
	defer server.Close()
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
		t.Fatal("synthetic scoped credential unavailable")
	}
	name := "trial-" + trial.ID + ".json"
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
	source := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: fixture.preset.ID, Mode: "run", Repeat: 1,
		Complete: true, Verdict: trial.Verdict, StartedAt: trial.StartedAt, Runtime: fixture.probe,
		TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: surfaces, BrowserFixtures: host,
		Coverage: map[Verdict]int{trial.Verdict: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	if os.Remove(filepath.Join(fixture.workspace, "state/browser/browser.json")) != nil ||
		os.Remove(filepath.Join(fixture.workspace, "state/interaction_registry.json")) != nil {
		t.Fatal("native state control unavailable")
	}
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, writer.dir,
		RunOptions{Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second})
	if err != nil || result.Verdict != Pass || calls.Load() != 1 ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("offline denial grade failed or ran target work", err)
	}
	if _, err = Compare(writer.dir, result.ReportDir); err != nil {
		t.Fatal("denial regrade comparison lost evidence", err)
	}
	encoded, err := json.Marshal(trial)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-browser", "missing-denial", "reply", "native", "accepted", "privacy-binding"} {
		t.Run(mode, func(t *testing.T) {
			copy, err := decodeDocument[Trial](encoded, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-browser":
				copy.Browser = nil
			case "missing-denial":
				copy.Browser.Denial = nil
			case "reply":
				copy.Browser.Denial.Cancellation.ReplyMessageID = "foreign-reply"
			case "native":
				copy.Browser.Denial.Native = nil
			case "accepted":
				copy.Browser.Denial.Native.ForbiddenActionAccepted = true
			case "privacy-binding":
				copy.Browser.Denial.Cancellation.Workspace = "/foreign"
			}
			writeAdmissionJSON(t, filepath.Join(writer.dir, name), copy)
			_, err = Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, writer.dir,
				RunOptions{Home: fixture.home, OutputParent: fixture.output})
			if !errors.Is(err, ErrGrade) || calls.Load() != 1 {
				t.Fatal("invalid denial evidence reached a judge", mode, err)
			}
		})
	}
}
