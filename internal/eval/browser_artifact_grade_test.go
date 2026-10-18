package eval

import (
	"bytes"
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

func TestGradeBrowserArtifactRequiresActualRetainedFileWithoutTargetWork(t *testing.T) {
	control := newBrowserArtifactControl(t)
	writer, trial := control.finalized(t)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": browserArtifactJudgePass(trial),
				},
				"finish_reason": "stop",
			},
		}})
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
		Version:         Version,
		ID:              uuid.NewString(),
		PresetID:        fixture.preset.ID,
		Mode:            "run",
		Repeat:          1,
		Complete:        true,
		Verdict:         trial.Verdict,
		StartedAt:       trial.StartedAt,
		TargetLifecycle: cleanPDFLifecycle(),
		PrivateSurfaces: surfaces,
		BrowserFixtures: host,
		Runtime:         fixture.probe,
		Coverage:        map[Verdict]int{trial.Verdict: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	if os.Remove(filepath.Join(fixture.workspace, "state/browser/browser.json")) != nil {
		t.Fatal("source state unavailable")
	}
	tasks, probes := fixture.tasks.Load(), fixture.probes.Load()
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, writer.dir,
		RunOptions{Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second})
	if err != nil || result.Verdict != Pass || calls.Load() != 1 ||
		fixture.tasks.Load() != tasks || fixture.probes.Load() != probes {
		t.Fatal("offline artifact grade failed or ran target work", err)
	}
	regraded := comparisonTrial(t, result, 0)
	raw, err := os.ReadFile(filepath.Join(result.ReportDir, regraded.Browser.Artifact.File))
	if err != nil || !bytes.Equal(raw, control.page.file) {
		t.Fatal("regrade did not retain the verified actual file", err)
	}
	if _, err := Compare(writer.dir, result.ReportDir); err != nil {
		t.Fatal("artifact comparison lost evidence", err)
	}

	path := filepath.Join(writer.dir, trial.Browser.Artifact.File)
	original, err := json.Marshal(trial)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "hash", "alias", "mode", "prompt", "metadata-only", "missing-browser"} {
		t.Run(mode, func(t *testing.T) {
			copy, err := decodeDocument[Trial](original, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			if os.Remove(path) != nil {
				t.Fatal("artifact control unavailable")
			}
			defer func() {
				_ = os.Remove(path)
				if os.WriteFile(path, control.page.file, 0o600) != nil {
					t.Error("artifact restoration failed")
				}
			}()
			switch mode {
			case "missing":
			case "hash":
				if os.WriteFile(path, []byte("wrong bytes"), 0o600) != nil {
					t.Fatal("hash control unavailable")
				}
			case "alias":
				if os.Symlink(filepath.Join(result.ReportDir, regraded.Browser.Artifact.File), path) != nil {
					t.Fatal("alias control unavailable")
				}
			default:
				perm := os.FileMode(0o600)
				if mode == "mode" {
					perm = 0o644
				}
				if os.WriteFile(path, control.page.file, perm) != nil {
					t.Fatal("file control unavailable")
				}
			}
			if mode == "prompt" {
				copy.Browser.Artifact.Approval.PromptMessageID = "unobserved-prompt"
			}
			if mode == "metadata-only" {
				copy.Browser.Artifact.Code = "native_metadata_observed"
			}
			if mode == "missing-browser" {
				copy.Browser = nil
			}
			writeAdmissionJSON(t, filepath.Join(writer.dir, name), copy)
			before := calls.Load()
			_, err = Grade(t.Context(), fixture.preset, []LoadedCase{control.item}, writer.dir,
				RunOptions{Home: fixture.home, OutputParent: fixture.output})
			if !errors.Is(err, ErrGrade) || calls.Load() != before {
				t.Fatal("invalid file or receipt reached judge", mode, err)
			}
		})
	}
}
