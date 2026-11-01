package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func trialStagingFixture(t *testing.T) (*privateReport, Trial, RunReport) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	report := &privateReport{root: root, dir: directory}
	if report.startTrialStaging() != nil {
		t.Fatal("private staging unavailable")
	}
	trial := Trial{
		Version: Version, ID: uuid.NewString(), SessionID: uuid.NewString(), AgentID: "main",
		CaseID: "general.staging", CaseSHA256: strings.Repeat("a", 64),
		Verdict: Pass, ExecutionVerdict: Pass, Code: "completed",
		Events: []Event{{ID: "event-1", Kind: "final", Content: "synthetic"}},
		Checks: []CheckResult{},
	}
	if report.beginTrialStaging(t.Context(), trial.SessionID, trial.CaseSHA256) != nil {
		t.Fatal("fresh staging allocation failed")
	}
	result := RunReport{Trials: []TrialSummary{{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
		Verdict: trial.Verdict, Code: trial.Code, File: "trial-" + trial.ID + ".json",
	}}}
	return report, trial, result
}

func TestTrialStagingRetiresOnlyAfterCanonicalEvidenceExists(t *testing.T) {
	report, trial, result := trialStagingFixture(t)
	name := result.Trials[0].File
	if report.checkpointTrial(name, trial, true) != nil {
		t.Fatal("pending evidence was not staged")
	}
	data, err := report.readTrialCheckpoint(name, trial.SessionID)
	if err != nil || !strings.Contains(string(data), trial.ID) {
		t.Fatal("owned checkpoint unavailable", err)
	}
	if _, err = report.finishTrialStaging(result); !errors.Is(err, ErrReport) {
		t.Fatal("sole pending checkpoint was retired without final evidence")
	}
	entry := report.staging[trial.SessionID]
	if _, err = os.Stat(filepath.Join(report.dir, entry.directory, trialCheckpointName)); err != nil {
		t.Fatal("pending evidence was lost", err)
	}
	if report.write(name, trial) != nil {
		t.Fatal("canonical evidence unavailable")
	}
	observation, err := report.finishTrialStaging(result)
	if err != nil || !observation.validFor(result) || !observation.matches(trial) ||
		observation.verdict() != Pass || observation.Trials[0].Code != "removed" {
		t.Fatal("private staging cleanup was not evidenced", err, observation)
	}
	if _, err = os.Stat(filepath.Join(report.dir, entry.directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned empty staging was retained", err)
	}
	if _, err = os.Stat(filepath.Join(report.dir, name)); err != nil {
		t.Fatal("canonical evidence was deleted", err)
	}
}

func TestTrialStagingDoesNotAdoptOrDeleteChangedResources(t *testing.T) {
	for _, change := range []string{"extra", "content", "checkpoint-swap", "directory-swap", "symlink", "mode"} {
		t.Run(change, func(t *testing.T) {
			report, trial, result := trialStagingFixture(t)
			name := result.Trials[0].File
			if report.checkpointTrial(name, trial, false) != nil {
				t.Fatal("checkpoint unavailable")
			}
			entry := report.staging[trial.SessionID]
			directory := filepath.Join(report.dir, entry.directory)
			path := filepath.Join(directory, trialCheckpointName)
			sentinel := path
			switch change {
			case "extra":
				sentinel = filepath.Join(directory, "foreign.txt")
				if os.WriteFile(sentinel, []byte("foreign"), 0o600) != nil {
					t.Fatal("foreign evidence setup failed")
				}
			case "content":
				if os.WriteFile(path, []byte("changed"), 0o600) != nil {
					t.Fatal("checkpoint mutation setup failed")
				}
			case "checkpoint-swap":
				if os.Rename(path, path+".original") != nil || os.WriteFile(path, []byte("foreign"), 0o600) != nil {
					t.Fatal("checkpoint replacement setup failed")
				}
			case "directory-swap", "symlink":
				if os.Rename(directory, directory+".original") != nil {
					t.Fatal("directory replacement setup failed")
				}
				if change == "symlink" {
					if os.Symlink(directory+".original", directory) != nil {
						t.Fatal("directory alias setup failed")
					}
				} else if os.Mkdir(directory, 0o700) != nil || os.WriteFile(path, []byte("foreign"), 0o600) != nil {
					t.Fatal("foreign directory setup failed")
				}
			case "mode":
				if os.Chmod(directory, 0o755) != nil {
					t.Fatal("mode change setup failed")
				}
			}
			if change != "extra" {
				if _, err := report.readTrialCheckpoint(name, trial.SessionID); !errors.Is(err, ErrReport) {
					t.Fatal("changed checkpoint was adopted", err)
				}
			}
			observation, err := report.finishTrialStaging(result)
			if err != nil || observation.verdict() != Inconclusive || observation.Trials[0].Code != "retained" {
				t.Fatal("uncertain cleanup became success", err, observation)
			}
			if _, err = os.Stat(sentinel); err != nil {
				t.Fatal("unowned or changed evidence was deleted", err)
			}
		})
	}
}

func TestTrialStagingRefusesAllocationReuseAndKeepsCheckpointOnWriteFailure(t *testing.T) {
	report, trial, result := trialStagingFixture(t)
	for _, id := range []string{trial.SessionID, "../escape", ""} {
		if !errors.Is(report.beginTrialStaging(t.Context(), id, trial.CaseSHA256), ErrReport) {
			t.Fatal("staging reused an allocation or escaped the report")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(report.beginTrialStaging(ctx, uuid.NewString(), trial.CaseSHA256), ErrReport) {
		t.Fatal("canceled trial allocated staging")
	}
	name := result.Trials[0].File
	if report.writeBytes(name, []byte("foreign")) != nil ||
		!errors.Is(report.checkpointTrial(name, trial, false), ErrReport) {
		t.Fatal("canonical collision was overwritten")
	}
	if _, err := report.readTrialCheckpoint(name, trial.SessionID); err != nil {
		t.Fatal("write failure discarded the sole owned checkpoint", err)
	}
	if _, err := report.finishTrialStaging(result); !errors.Is(err, ErrReport) {
		t.Fatal("foreign canonical evidence was accepted")
	}
}

func TestTrialStagingRefusesReplacedReportRoot(t *testing.T) {
	report, trial, result := trialStagingFixture(t)
	if report.checkpointTrial(result.Trials[0].File, trial, false) != nil {
		t.Fatal("checkpoint unavailable")
	}
	original := report.dir + ".original"
	if os.Rename(report.dir, original) != nil || os.Mkdir(report.dir, 0o700) != nil {
		t.Fatal("report replacement setup failed")
	}
	t.Cleanup(func() { _ = os.RemoveAll(original) })
	if _, err := report.readTrialCheckpoint(result.Trials[0].File, trial.SessionID); !errors.Is(err, ErrReport) {
		t.Fatal("replaced report root was adopted", err)
	}
	if _, err := report.finishTrialStaging(result); !errors.Is(err, ErrReport) {
		t.Fatal("replaced report root was cleaned", err)
	}
	if _, err := os.Stat(filepath.Join(original, "scratch-"+trial.SessionID, trialCheckpointName)); err != nil {
		t.Fatal("root replacement lost the original checkpoint", err)
	}
}

func TestTrialStagingAllocationFailureStopsBeforeProbeAndRetainsForeignDirectory(t *testing.T) {
	report, occupied, _ := trialStagingFixture(t)
	// Remove the test ownership receipt to model a pre-existing, unowned path.
	report.staging = make(map[string]*trialStaging)
	item := loadedTestCase(t, "general.staging.collision")
	sessionID := occupied.SessionID
	trial := runAdmittedTrial(t.Context(), admittedTarget{}, item, sessionID, conversationRoles{},
		RunOptions{}, report, nil, nil, nil)
	if trial.Code != "trial_scratch_unavailable" || trial.Verdict != Inconclusive || len(trial.Events) != 0 {
		t.Fatal("allocation failure reached the probe/task instead of refusing admission")
	}
	name := "trial-" + trial.ID + ".json"
	if report.checkpointTrial(name, trial, false) != nil {
		t.Fatal("unattempted negative evidence unavailable")
	}
	result := RunReport{Trials: []TrialSummary{{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
		Verdict: trial.Verdict, Code: trial.Code, File: name,
	}}}
	observation, err := report.finishTrialStaging(result)
	if err != nil || observation.Trials[0].Code != "unavailable" || observation.verdict() != Inconclusive {
		t.Fatal("allocation failure became a successful cleanup receipt", err)
	}
	if _, err = report.root.Stat("scratch-" + sessionID); err != nil {
		t.Fatal("pre-existing unowned directory was removed", err)
	}
}

func TestRepeatedRunAllocatesPrivateStagingBeforeEveryTrialProbe(t *testing.T) {
	fixture := newAdmissionFixture(t)
	observations := make(chan bool, 6)
	fixture.mutateProbe = func(_ *health.RuntimeProbe, number int32) {
		if number == 1 {
			return
		}
		roots, err := os.ReadDir(fixture.output)
		if err != nil || len(roots) != 1 {
			observations <- false
			return
		}
		root := filepath.Join(fixture.output, roots[0].Name())
		entries, err := os.ReadDir(root)
		count, empty := 0, 0
		valid := err == nil
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "scratch-") {
				continue
			}
			count++
			info, statErr := entry.Info()
			children, readErr := os.ReadDir(filepath.Join(root, entry.Name()))
			valid = valid && statErr == nil && info.IsDir() && info.Mode().Perm() == 0o700 && readErr == nil
			if len(children) == 0 {
				empty++
			}
		}
		observations <- valid && count == int(number-1) && empty == 1
	}
	result, err := Run(t.Context(), fixture.preset,
		[]LoadedCase{loadedTestCase(t, "general.staging.one"), loadedTestCase(t, "general.staging.two")},
		RunOptions{Home: fixture.home, OutputParent: fixture.output, Repeat: 3})
	if err != nil || result.TrialScratch == nil || !result.TrialScratch.validFor(result) ||
		len(result.TrialScratch.Trials) != 6 || fixture.tasks.Load() != 12 {
		t.Fatal("repeats lost fresh staging or task bounds", err)
	}
	for range 6 {
		if !<-observations {
			t.Fatal("private staging was not fresh before the trial probe")
		}
	}
	for _, receipt := range result.TrialScratch.Trials {
		if receipt.Code != "removed" || receipt.CreatedAt < result.StartedAt.UnixMilli() ||
			!validDigest(receipt.CheckpointSHA256) {
			t.Fatal("staging receipt lost trial identity or cleanup")
		}
	}
	assertPrivateReport(t, result)
}

func TestRunCannotPassWhenPrivateStagingContainsUnownedFiles(t *testing.T) {
	fixture := newAdmissionFixture(t)
	setup := make(chan error, 1)
	fixture.respond = func(request protocol.MintClawMessage, _ *protocol.MintClawMessage) {
		if fixture.tasks.Load() != 1 {
			return
		}
		roots, err := os.ReadDir(fixture.output)
		if err == nil && len(roots) == 1 {
			err = os.WriteFile(filepath.Join(fixture.output, roots[0].Name(),
				"scratch-"+request.SessionID, "foreign.txt"), []byte("foreign"), 0o600)
		} else {
			err = ErrReport
		}
		setup <- err
	}
	result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.staging.uncertain"))
	if setupErr := <-setup; setupErr != nil {
		t.Fatal(setupErr)
	}
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Inconclusive ||
		result.Coverage[Pass] != 1 || result.TrialScratch == nil || result.TrialScratch.Trials[0].Code != "retained" {
		t.Fatal("passing task silently hid staging cleanup uncertainty", err)
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal("comparison lost run-level cleanup uncertainty", err)
	}
}
