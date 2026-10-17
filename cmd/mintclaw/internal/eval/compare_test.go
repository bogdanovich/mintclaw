package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func TestCompareCLIIsOfflineAndDoesNotClaimPassForSkippedEvidence(t *testing.T) {
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), "nonexistent-profile"))
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	trial := manualeval.Trial{
		Version: 1, ID: "cli-trial", CaseID: "general.compare", CaseSHA256: strings.Repeat("2", 64),
		SessionID: "cli-session", AgentID: "main", Verdict: manualeval.Skipped, Code: "capability_unavailable",
		Events: []manualeval.Event{{ID: "event-1", Turn: 1, Kind: "final", Content: "private-evidence-canary"}},
		Checks: []manualeval.CheckResult{},
	}
	report := manualeval.RunReport{
		Version: 1, ID: "cli-report", PresetID: "cli-preset", Mode: "run", StartedAt: time.Now().UTC(),
		Complete: true, Verdict: manualeval.Skipped, Coverage: map[manualeval.Verdict]int{manualeval.Skipped: 1},
		Runtime: health.RuntimeProbe{Version: 1, ConfigSHA256: strings.Repeat("1", 64)},
		Trials: []manualeval.TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Verdict: trial.Verdict,
			Code: trial.Code, File: "trial-" + trial.ID + ".json",
		}},
	}
	for name, value := range map[string]any{"run.json": report, "trial-cli-trial.json": trial} {
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(filepath.Join(directory, name), data, 0o600) != nil {
			t.Fatal("fixture write", err)
		}
	}
	for _, jsonOutput := range []bool{true, false} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		args := []string{"compare", directory, directory}
		if jsonOutput {
			args = append(args, "--json")
		}
		command.SetArgs(args)
		if err := command.Execute(); err != nil || strings.Contains(output.String(), "private-evidence-canary") {
			t.Fatal("offline comparison required a profile or exposed evidence", err)
		}
		if jsonOutput {
			var result manualeval.Comparison
			if json.Unmarshal(output.Bytes(), &result) != nil || result.Outcome != "compared" ||
				result.BaselineVerdict != manualeval.Skipped || result.CurrentVerdict != manualeval.Skipped ||
				result.Cases[0].Current.Coverage[manualeval.Skipped] != 1 || result.Cases[0].Current.Trials != 1 {
				t.Fatal("JSON comparison lost skipped coverage")
			}
		} else if !strings.Contains(output.String(), "0/1") || !strings.Contains(output.String(), "skipped=1") ||
			!strings.Contains(output.String(), "Run verdicts: baseline=skipped current=skipped") ||
			!strings.Contains(output.String(), "not established") {
			t.Fatal("text comparison hid uncertainty", output.String())
		}
	}
	current := t.TempDir()
	if err := os.Chmod(current, 0o700); err != nil {
		t.Fatal(err)
	}
	trial.CaseSHA256, report.Trials[0].CaseSHA256 = strings.Repeat("3", 64), strings.Repeat("3", 64)
	for name, value := range map[string]any{"run.json": report, "trial-cli-trial.json": trial} {
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(filepath.Join(current, name), data, 0o600) != nil {
			t.Fatal("fixture write", err)
		}
	}
	command := NewEvalCommand()
	var output bytes.Buffer
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"compare", directory, current, "--json"})
	var result manualeval.Comparison
	if err := command.Execute(); !errors.Is(err, manualeval.ErrNotComparable) ||
		json.Unmarshal(output.Bytes(), &result) != nil || result.CompatibleCases ||
		result.Cases[0].Status != "case_changed" {
		t.Fatal("incompatible CLI result was lost or exited successfully", err)
	}
}

func TestCompareCLIRejectsMissingReportsWithoutEchoingContents(t *testing.T) {
	command := NewEvalCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"compare", "/missing/private-evidence-canary", "/missing/current", "--json"})
	if err := command.Execute(); !errors.Is(err, manualeval.ErrCompare) ||
		strings.Contains(output.String(), "private-evidence-canary") || strings.Contains(output.String(), "compared") {
		t.Fatal("missing report was compared or echoed", err)
	}
}
