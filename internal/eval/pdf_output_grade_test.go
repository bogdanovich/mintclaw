package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func pdfGradeWorkspaceSnapshot(t *testing.T, workspace string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = digest(data)
		clear(data)
		return nil
	})
	if err != nil {
		t.Fatal("native fixture snapshot unavailable", err)
	}
	return result
}

// The native stores/wire are real controls; oracle/process/privacy receipts
// and the HTTP judge are stubs. This is not a live PDF qualification result.
func TestGradeCompletedPDFOutputPreservesArtifactWithoutTargetOrOracleWork(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private prepared grading supports Linux and macOS")
	}
	trial, materialized, workspace, writer := finalizedOutputFixture(t)
	home := filepath.Dir(workspace)
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	assessFinalizedPDF(t.Context(), nil, materialized, &trial, workspace, cleanPDFLifecycle(), surfaces, writer)
	if !trial.EvidenceEligible || trial.PDF.PostStop.AssessmentScope != pdfOutputAssessmentScope {
		t.Fatal("complete output control unavailable", trial.Code)
	}
	trial.Repetition = 1
	name := "trial-" + trial.ID + ".json"
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
	item := loadPDFTestCase(t, trial.CaseID)
	var calls, unexpected atomic.Int32
	var response atomic.Value
	response.Store(adaptiveJudgePass)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			unexpected.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil || generatedFactJSONValue(body) {
			t.Error("judge received tools or a generated private value")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": response.Load().(string)},
			"finish_reason": "stop",
		}}})
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port = endpoint.Hostname(), port
	cfg.Agents.Defaults.Workspace = workspace
	cfg.ModelList = config.SecureModelList{{
		ModelName: "eval-model", Provider: "openai", Model: "synthetic-model", Enabled: true, APIBase: server.URL,
	}}
	preset := Preset{
		Version: Version, ID: "pdf-grade-control", ConfigPath: filepath.Join(home, "config.json"),
		ExpectedAgent: trial.AgentID, ExpectedWorkspace: workspace, IsolationRoot: home,
		Capabilities: []string{"conversation", "interactions", "pdf"}, JudgeModel: "eval-model",
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, preset.ConfigPath)
	writeAdmissionJSON(t, preset.ConfigPath, cfg)
	if os.WriteFile(filepath.Join(home, "eval-grading-key"), []byte("synthetic-grading-credential"), 0o600) != nil ||
		os.WriteFile(filepath.Join(home, ".security.yml"),
			[]byte("model_list:\n  eval-model:\n    api_keys:\n      - file://eval-grading-key\n"), 0o600) != nil {
		t.Fatal("synthetic judge credential unavailable")
	}
	source := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: preset.ID, Mode: "run", Repeat: 1,
		Complete: true, Verdict: trial.Verdict, StartedAt: trial.StartedAt,
		TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: surfaces,
		Runtime: health.RuntimeProbe{
			Version: 1, ConfigSHA256: digest([]byte("public-control")),
			GitCommit: "unknown", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		},
		Coverage: map[Verdict]int{trial.Verdict: 1},
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	before := pdfGradeWorkspaceSnapshot(t, workspace)
	original, err := os.ReadFile(filepath.Join(writer.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(writer.dir, trial.PDF.Artifact.File)
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	options := RunOptions{
		Home: home, OutputParent: filepath.Join(home, "reports"), SuiteTimeout: time.Minute,
		StartTarget: true, PDFOraclePython: "/not-an-eval-oracle",
	}
	result, err := Grade(t.Context(), preset, []LoadedCase{item}, writer.dir, options)
	if err != nil || result.Verdict != Pass || calls.Load() != 1 || unexpected.Load() != 0 {
		t.Fatal("offline output grade failed or probed a target", err)
	}
	graded := comparisonTrial(t, result, 0)
	raw, err := os.ReadFile(filepath.Join(result.ReportDir, graded.PDF.Artifact.File))
	if err != nil || !bytes.Equal(raw, artifact) || graded.Judge.Call.Boundary.FactScope != generatedFactGuardScope {
		t.Fatal("regrade changed binary evidence or claimed an original-value guard", err)
	}
	if _, err = Compare(writer.dir, result.ReportDir); err != nil {
		t.Fatal("output regrade could not be compared", err)
	}
	assertPrivateReport(t, result)
	unchanged, err := os.ReadFile(filepath.Join(writer.dir, name))
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("successful regrade changed the original trial", err)
	}
	hard := loadPDFTestCase(t, item.Case.ID)
	hard.Case.Checks = append(hard.Case.Checks,
		Check{ID: "contradiction", Kind: "response_contains", Expected: "not present in this conversation"})
	encoded, err := json.Marshal(hard.Case)
	if err != nil {
		t.Fatal(err)
	}
	hard.Digest = digest(encoded)
	hardTrial := cloneOutputTrial(t, trial)
	hardTrial.CaseSHA256, hardTrial.Verdict, hardTrial.Code = hard.Digest, Fail, "hard_check_failed"
	hardTrial.Checks = gradeFinalizedPDFChecks(hard.Case, hardTrial)
	hardSource := source
	hardSource.Trials = append([]TrialSummary(nil), source.Trials...)
	hardSource.Verdict, hardSource.Coverage = Fail, map[Verdict]int{Fail: 1}
	hardSource.Trials[0].CaseSHA256 = hard.Digest
	hardSource.Trials[0].Verdict, hardSource.Trials[0].Code = hardTrial.Verdict, hardTrial.Code
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), hardTrial)
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), hardSource)
	hardResult, err := Grade(t.Context(), preset, []LoadedCase{hard}, writer.dir, options)
	if !errors.Is(err, ErrNotPassed) || hardResult.Verdict != Fail || calls.Load() != 2 || unexpected.Load() != 0 {
		t.Fatal("positive judge overrode a PDF hard failure", err)
	}
	writeAdmissionJSON(t, filepath.Join(writer.dir, name), trial)
	writeAdmissionJSON(t, filepath.Join(writer.dir, "run.json"), source)
	for _, mode := range []string{
		"missing", "file-hash", "size", "mode", "symlink", "scope", "oracle", "delivery", "legacy", "literal", "stream", "metadata",
	} {
		t.Run(mode, func(t *testing.T) {
			changed := cloneOutputTrial(t, trial)
			selected := loadPDFTestCase(t, item.Case.ID)
			switch mode {
			case "missing":
				if err := os.Remove(artifactPath); err != nil {
					t.Fatal(err)
				}
			case "file-hash":
				if err := os.WriteFile(artifactPath, []byte("different"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "size":
				changed.PDF.Settlement.ArtifactSize++
			case "mode":
				if err := os.Chmod(artifactPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if os.Remove(artifactPath) != nil ||
					os.Symlink(filepath.Join(result.ReportDir, graded.PDF.Artifact.File), artifactPath) != nil {
					t.Fatal("artifact alias control unavailable")
				}
			case "scope":
				changed.PDF.PostStop.AssessmentScope = ""
			case "oracle":
				changed.PDF.Artifact.Oracle.FieldsMatched = false
			case "delivery":
				changed.PDF.Wire.Complete = false
			case "legacy":
				changed.EvidenceEligible = false
			case "literal":
				selected.Case.Driver.Facts["contact-reference"] = "unknown-private-format"
			case "stream":
				changed.Events = append(changed.Events,
					Event{ID: uuid.NewString(), Turn: 3, Kind: "stream", Content: "MC_EVAL_0123456789abcdef"})
			case "metadata":
				changed.Events[0].Model = "MC_EVAL_0123456789abcdef"
			}
			writeAdmissionJSON(t, filepath.Join(writer.dir, name), changed)
			count := calls.Load()
			_, err := Grade(t.Context(), preset, []LoadedCase{selected}, writer.dir, options)
			if !errors.Is(err, ErrGrade) || calls.Load() != count || unexpected.Load() != 0 {
				t.Fatal("invalid PDF evidence reached a judge or target", mode, err)
			}
			if err := os.Remove(artifactPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifactPath, artifact, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err = os.WriteFile(filepath.Join(writer.dir, name), original, 0o600); err != nil {
		t.Fatal(err)
	}
	response.Store("MC_EVAL_fedcba9876543210")
	leaked, err := Grade(t.Context(), preset, []LoadedCase{item}, writer.dir, options)
	if !errors.Is(err, ErrNotPassed) || leaked.Verdict != Fail {
		t.Fatal("private judge output did not fail", err)
	}
	failed := comparisonTrial(t, leaked, 0)
	if failed.EvidenceEligible || failed.PDF.PostStop.AssessmentScope != "" ||
		failed.Judge.Code != "private_judge_output" {
		t.Fatal("private judge output retained output eligibility")
	}
	data, err := os.ReadFile(filepath.Join(leaked.ReportDir, leaked.Trials[0].File))
	if err != nil || generatedPrivateFactPattern.Match(data) {
		t.Fatal("private judge output reached JSON evidence", err)
	}
	after, err := os.ReadFile(filepath.Join(writer.dir, name))
	if err != nil || !bytes.Equal(original, after) || unexpected.Load() != 0 ||
		!maps.Equal(before, pdfGradeWorkspaceSnapshot(t, workspace)) {
		t.Fatal("offline grading changed source evidence or native stores", err)
	}
	if _, err = os.Stat(filepath.Join(home, ".mintclaw.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline grading created target process state")
	}
}

func TestPDFOutputGradeReaderBindsPrivateBinaryAndRejectsCredentialsAndBounds(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	data := []byte("%PDF-1.7\nMC_EVAL_0123456789abcdef\n%%EOF\n")
	trial := Trial{ID: uuid.NewString(), PDF: &PDFTrialEvidence{
		Artifact: &PDFArtifactEvidence{}, Settlement: &PDFSettlementEvidence{
			ArtifactSHA256: digest(data), ArtifactSize: int64(len(data)),
		},
	}}
	trial.PDF.Artifact.File = "artifact-" + trial.ID + ".pdf"
	if err = root.WriteFile(trial.PDF.Artifact.File, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := pdfOutputArtifactForGrade(root, trial, nil)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("authorized binary was treated as judge/JSON evidence", err)
	}
	filter := strings.NewReplacer("MC_EVAL_0123456789abcdef", "hidden-credential")
	if got, err = pdfOutputArtifactForGrade(root, trial, filter); !errors.Is(err, ErrGrade) || got != nil {
		t.Fatal("a known credential literal was copied")
	}
	trial.PDF.Settlement.ArtifactSize = maxPDFBytes + 1
	if got, err = pdfOutputArtifactForGrade(root, trial, nil); !errors.Is(err, ErrGrade) || got != nil {
		t.Fatal("oversized native artifact was admitted")
	}
}
