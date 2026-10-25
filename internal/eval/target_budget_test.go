package eval

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func targetBudgetTestLimits() providers.CallBudgetLimits {
	return providers.CallBudgetLimits{
		MaxCalls: 8, MaxInputBytes: 64 << 10, MaxOutputTokens: 128, MaxReportedTokens: 1000, MaxInFlight: 2,
	}
}

func targetBudgetTestProof(t *testing.T, pid int) (*TargetBudgetObservation, evaltarget.Proof) {
	t.Helper()
	spec := evaltarget.Spec{Version: evaltarget.Version, Nonce: uuid.NewString(), Limits: targetBudgetTestLimits()}
	proof := evaltarget.Proof{
		Version: evaltarget.Version, Scope: evaltarget.Scope, Nonce: spec.Nonce, PID: pid, GatewayClean: true,
		Snapshot: providers.CallBudgetSnapshot{
			Limits: spec.Limits, Calls: 1, Completed: 1, ReportedTokens: 9, Sealed: true, UsageKnown: true,
		},
	}
	observation := &TargetBudgetObservation{Version: Version, Spec: spec, Code: "observed"}
	setTargetBudgetTestProof(t, observation, proof)
	return observation, proof
}

func setTargetBudgetTestProof(t *testing.T, observation *TargetBudgetObservation, proof evaltarget.Proof) {
	t.Helper()
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	observation.Proof, observation.ProofSHA256 = string(raw), digest(raw)
}

func targetBudgetTestLifecycle(pid int) *TargetLifecycle {
	zero := 0
	return &TargetLifecycle{
		Mode: "owned_gateway", PID: pid, Admitted: true, StopRequested: true,
		ExitObserved: true, ExitCode: &zero, Code: "native_exit_zero",
	}
}

func TestTargetBudgetVerdictsAndFrozenIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		want Verdict
		edit func(*evaltarget.Proof)
	}{
		{"settled", Pass, func(*evaltarget.Proof) {}},
		{"zero_work", Inconclusive, func(p *evaltarget.Proof) {
			p.Snapshot.Calls, p.Snapshot.Completed, p.Snapshot.ReportedTokens = 0, 0, 0
		}},
		{"unknown_usage", Inconclusive, func(p *evaltarget.Proof) {
			p.Snapshot.UnknownUsage, p.Snapshot.UsageKnown, p.Snapshot.StoppedReason = 1, false, "unknown_usage"
			p.Snapshot.ReportedTokens = 0
		}},
		{"in_flight", Inconclusive, func(p *evaltarget.Proof) {
			p.Snapshot.Calls, p.Snapshot.InFlight, p.Snapshot.UsageKnown = 2, 1, false
		}},
		{"unclean_gateway", Inconclusive, func(p *evaltarget.Proof) { p.GatewayClean = false }},
		{"call_limit", Fail, func(p *evaltarget.Proof) { p.Snapshot.StoppedReason = "calls" }},
		{"input_limit", Fail, func(p *evaltarget.Proof) { p.Snapshot.StoppedReason = "input_limit" }},
		{"output_limit", Fail, func(p *evaltarget.Proof) { p.Snapshot.StoppedReason = "output_limit" }},
		{"reported_limit", Fail, func(p *evaltarget.Proof) {
			p.Snapshot.ReportedTokens, p.Snapshot.StoppedReason = p.Snapshot.Limits.MaxReportedTokens, "reported_tokens"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := comparisonFixture(t, Pass)
			report.Runtime.PID, report.TargetLifecycle = 3456, targetBudgetTestLifecycle(3456)
			report.PrivateSurfaces = &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			observation, proof := targetBudgetTestProof(t, 3456)
			test.edit(&proof)
			setTargetBudgetTestProof(t, observation, proof)
			report.TargetBudget, report.Verdict = observation, test.want
			if !observation.valid(report) || observation.verdict() != test.want {
				t.Fatal("budget metadata lost a hard failure or unknown", observation)
			}
			writeAdmissionJSON(t, filepath.Join(report.ReportDir, "run.json"), report)
			result, err := Compare(report.ReportDir, report.ReportDir)
			if err != nil || result.CurrentVerdict != test.want || result.Cases[0].Current.Coverage[Pass] != 1 {
				t.Fatal("comparison replaced run budget with passing trial/judge", result, err)
			}
			report.Runtime.PID++
			if observation.valid(report) {
				t.Fatal("proof accepted a different frozen runtime PID")
			}
		})
	}
}

func TestTargetBudgetAdmissionBeforeAnyTargetWork(t *testing.T) {
	for _, mode := range []string{"missing_limits", "attach", "missing_capability", "invalid_limits"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			item := loadedTestCase(t, "general.bounded")
			item.Case.Requires = append(item.Case.Requires, boundedCallsCapability)
			preset := fixture.preset
			limits := targetBudgetTestLimits()
			preset.TargetCallBudget = &limits
			preset.Capabilities = append(slices.Clone(preset.Capabilities), boundedCallsCapability)
			start := true
			switch mode {
			case "missing_limits":
				preset.TargetCallBudget = nil
			case "attach":
				start = false
			case "missing_capability":
				preset.Capabilities = fixture.preset.Capabilities
			case "invalid_limits":
				limits.MaxCalls = 0
			}
			_, err := Run(t.Context(), preset, []LoadedCase{item}, RunOptions{
				Home: fixture.home, OutputParent: fixture.output, StartTarget: start, SuiteTimeout: time.Second,
			})
			if !errors.Is(err, ErrUnsafeTarget) || fixture.tasks.Load() != 0 || fixture.probes.Load() != 0 {
				t.Fatal("budget omission reached target work", err)
			}
			if _, err = os.Lstat(filepath.Join(fixture.home, ".eval-gateway-owner")); !os.IsNotExist(err) {
				t.Fatal("refused run tried to own a process")
			}
		})
	}
	fixture := newAdmissionFixture(t)
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget = &limits
	fixture.preset.Capabilities = append(fixture.preset.Capabilities, boundedCallsCapability, "pdf")
	if validateConversationPolicy(fixture.cfg, fixture.preset, fixture.preset.ExpectedWorkspace) == nil {
		t.Fatal("independent document auditor was admitted into the agent-only budget scope")
	}
}

func TestTargetBudgetRawReceiptPrivacyAndUnavailableControls(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "wrong_nonce", "wrong_pid", "symlink", "public_mode", "private_duplicate"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0o700); err != nil {
				t.Fatal(err)
			}
			observation, proof := targetBudgetTestProof(t, os.Getpid())
			done := make(chan struct{})
			close(done)
			child := &ownedGateway{
				profile: preparedProfile{home: home}, command: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}},
				done: done, budgetSpec: &observation.Spec,
			}
			name, _ := observation.Spec.ReceiptName()
			switch mode {
			case "wrong_nonce":
				proof.Nonce = uuid.NewString()
			case "wrong_pid":
				proof.PID++
			}
			raw, _ := json.Marshal(proof)
			if mode == "private_duplicate" {
				raw = []byte(
					strings.TrimSuffix(string(raw), "}") + `,"hidden":"private-receipt-canary","hidden":"safe"}`,
				)
			}
			if mode != "missing" {
				if err := os.WriteFile(filepath.Join(home, name), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Rename(filepath.Join(home, name), filepath.Join(home, "other")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other", filepath.Join(home, name)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "public_mode" {
				if err := os.Chmod(filepath.Join(home, name), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := observeTargetBudget(child, nil, strings.NewReplacer("private-receipt-canary", "[REDACTED]"))
			want := Inconclusive
			switch mode {
			case "valid":
				want = Pass
			case "private_duplicate":
				want = Fail
			}
			if got.verdict() != want || mode != "valid" && (got.Proof != "" || got.ProofSHA256 != "") {
				t.Fatal("raw observation hid missing evidence or retained rejected bytes", mode, got)
			}
		})
	}
}

func TestTargetBudgetFrozenGradeCannotBypassRequiredProofOrUnknown(t *testing.T) {
	for _, mode := range []string{
		"pass", "missing", "unknown", "exhausted", "corrupt", "omitted_trial_requirement", "omitted_privacy",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture, item, source, calls := gradeFixture(t, false)
			item.Case.Requires = append(item.Case.Requires, boundedCallsCapability)
			raw, _ := json.Marshal(item.Case)
			item.Digest = digest(raw)
			source.TargetLifecycle = targetBudgetTestLifecycle(source.Runtime.PID)
			source.PrivateSurfaces = &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			observation, proof := targetBudgetTestProof(t, source.Runtime.PID)
			source.TargetBudget = observation
			switch mode {
			case "missing":
				source.TargetBudget = nil
			case "unknown":
				proof.Snapshot.UnknownUsage, proof.Snapshot.UsageKnown, proof.Snapshot.StoppedReason = 1, false, "unknown_usage"
				proof.Snapshot.ReportedTokens = 0
			case "exhausted":
				proof.Snapshot.StoppedReason = "calls"
			case "corrupt":
				proof.Nonce = uuid.NewString()
			case "omitted_privacy":
				source.PrivateSurfaces = nil
			}
			setTargetBudgetTestProof(t, observation, proof)
			trial := comparisonTrial(t, source, 0)
			trial.TargetBudgetRequired, trial.CaseSHA256 = mode != "omitted_trial_requirement", item.Digest
			source.Trials[0].CaseSHA256 = item.Digest
			if source.TrialScratch != nil {
				source.TrialScratch.Trials[0].CaseSHA256 = item.Digest
			}
			writeAdmissionJSON(t, filepath.Join(source.ReportDir, source.Trials[0].File), trial)
			writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
			result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
				Home: fixture.home, OutputParent: fixture.output,
			})
			if mode == "pass" {
				if err != nil || result.Verdict != Pass || calls.Load() != 1 || result.TargetBudget == nil {
					t.Fatal("frozen bound source could not be graded without a current gateway", err)
				}
				if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
					t.Fatal("graded budget receipt was lost", err)
				}
			} else if !errors.Is(err, ErrGrade) || calls.Load() != 0 {
				t.Fatal("invalid/unknown mandatory source budget reached a paid judge", err, calls.Load())
			}
		})
	}
}

func TestTargetBudgetCompareRequiresProofAndReportsLimitsDrift(t *testing.T) {
	before, after := comparisonFixture(t, Pass), comparisonFixture(t, Pass)
	for _, report := range []*RunReport{&before, &after} {
		report.Runtime.PID, report.TargetLifecycle = 3456, targetBudgetTestLifecycle(3456)
		report.PrivateSurfaces = &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
		report.TargetBudget, _ = targetBudgetTestProof(t, 3456)
		trial := comparisonTrial(t, *report, 0)
		trial.TargetBudgetRequired = true
		writeAdmissionJSON(t, filepath.Join(report.ReportDir, report.Trials[0].File), trial)
		writeAdmissionJSON(t, filepath.Join(report.ReportDir, "run.json"), report)
	}
	result, err := Compare(before.ReportDir, after.ReportDir)
	if err != nil || slices.Contains(result.EnvironmentChanges, "target_call_budget") {
		t.Fatal("fresh nonces were confused with budget environment drift", err, result)
	}
	observation, proof := targetBudgetTestProof(t, 3456)
	observation.Spec.Limits.MaxCalls++
	proof.Snapshot.Limits, proof.Nonce = observation.Spec.Limits, observation.Spec.Nonce
	setTargetBudgetTestProof(t, observation, proof)
	after.TargetBudget = observation
	writeAdmissionJSON(t, filepath.Join(after.ReportDir, "run.json"), after)
	result, err = Compare(before.ReportDir, after.ReportDir)
	if err != nil || !slices.Contains(result.EnvironmentChanges, "target_call_budget") {
		t.Fatal("changed limits were hidden as equal quality environments", err, result)
	}
	after.PrivateSurfaces = nil
	writeAdmissionJSON(t, filepath.Join(after.ReportDir, "run.json"), after)
	if _, err = Compare(before.ReportDir, after.ReportDir); !errors.Is(err, ErrCompare) {
		t.Fatal("new bounded report lost its mandatory raw privacy observation", err)
	}
	after.PrivateSurfaces = before.PrivateSurfaces
	after.TargetBudget = nil
	writeAdmissionJSON(t, filepath.Join(after.ReportDir, "run.json"), after)
	if _, err = Compare(before.ReportDir, after.ReportDir); !errors.Is(err, ErrCompare) {
		t.Fatal("mandatory trial budget omission was accepted", err)
	}
}
