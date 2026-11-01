package eval

import (
	"context"
	"encoding/json"
	"os"
	"time"
)

const trialCheckpointName = "checkpoint.json"

type trialStaging struct {
	sessionID     string
	caseSHA256    string
	directory     string
	trialID       string
	createdAt     int64
	directoryInfo os.FileInfo
	checkpoint    os.FileInfo
	sha256        string
}

func (report *privateReport) startTrialStaging() error {
	info, err := report.root.Stat(".")
	if err != nil || report.staging != nil {
		return ErrReport
	}
	report.stagingRoot = info
	if !report.stagingRootBound() {
		return ErrReport
	}
	report.staging = make(map[string]*trialStaging)
	return nil
}

func (report *privateReport) stagingRootBound() bool {
	current, err := os.Lstat(report.dir)
	opened, statErr := report.root.Stat(".")
	return err == nil && statErr == nil && current.IsDir() && current.Mode().Perm() == 0o700 &&
		os.SameFile(report.stagingRoot, current) && os.SameFile(report.stagingRoot, opened)
}

// Allocation precedes the trial probe/task. No handle is retained per trial;
// the report root anchors later reads, so repeats do not exhaust descriptors.
func (report *privateReport) beginTrialStaging(ctx context.Context, sessionID, caseSHA256 string) error {
	if report.staging == nil || !validID(sessionID) || !validDigest(caseSHA256) ||
		report.staging[sessionID] != nil || len(report.staging) >= MaxCorpusCases {
		return ErrReport
	}
	entry := &trialStaging{sessionID: sessionID, caseSHA256: caseSHA256, directory: "scratch-" + sessionID}
	report.staging[sessionID] = entry
	if ctx == nil || ctx.Err() != nil || !report.stagingRootBound() ||
		report.root.Mkdir(entry.directory, 0o700) != nil {
		return ErrReport
	}
	info, err := report.root.Lstat(entry.directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return ErrReport
	}
	root, err := report.root.OpenRoot(entry.directory)
	if err != nil {
		return ErrReport
	}
	opened, statErr := root.Stat(".")
	closeErr := root.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(info, opened) {
		return ErrReport
	}
	entry.directoryInfo, entry.createdAt = info, time.Now().UnixMilli()
	return nil
}

func (report *privateReport) openTrialStaging(entry *trialStaging) (*os.Root, error) {
	if entry == nil || entry.directoryInfo == nil || !report.stagingRootBound() {
		return nil, ErrReport
	}
	info, err := report.root.Lstat(entry.directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !os.SameFile(entry.directoryInfo, info) {
		return nil, ErrReport
	}
	root, err := report.root.OpenRoot(entry.directory)
	if err != nil {
		return nil, ErrReport
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(entry.directoryInfo, opened) {
		_ = root.Close()
		return nil, ErrReport
	}
	return root, nil
}

func (report *privateReport) checkpointTrial(name string, trial Trial, pending bool) error {
	if name != "trial-"+trial.ID+".json" {
		return ErrReport
	}
	entry := report.staging[trial.SessionID]
	if entry == nil {
		if pending || len(trial.Events) != 0 || trial.Verdict == Pass {
			return ErrReport
		}
		return report.write(name, trial)
	}
	if entry.caseSHA256 != trial.CaseSHA256 || entry.trialID != "" {
		return ErrReport
	}
	entry.trialID = trial.ID
	if entry.directoryInfo == nil {
		if pending || len(trial.Events) != 0 || trial.Verdict == Pass {
			return ErrReport
		}
		return report.write(name, trial)
	}
	root, err := report.openTrialStaging(entry)
	if err != nil {
		return ErrReport
	}
	defer func() { _ = root.Close() }()
	data, err := json.Marshal(trial)
	if err != nil || len(data)+1 > 8*1024*1024 {
		return ErrReport
	}
	data = append(data, '\n')
	defer clear(data)
	child := privateReport{root: root}
	if child.writeBytes(trialCheckpointName, data) != nil {
		return ErrReport
	}
	observed, info, err := readCensusFile(root, trialCheckpointName, 8*1024*1024)
	defer clear(observed)
	if err != nil || digest(observed) != digest(data) {
		return ErrReport
	}
	entry.checkpoint, entry.sha256 = info, digest(observed)
	if !pending {
		return report.writeBytes(name, data)
	}
	return nil
}

func (report *privateReport) readTrialCheckpoint(name, sessionID string) ([]byte, error) {
	if report.staging == nil {
		return readPrivateReportFile(report.root, "pending-"+name)
	}
	entry := report.staging[sessionID]
	if entry == nil || name != "trial-"+entry.trialID+".json" || !validDigest(entry.sha256) {
		return nil, ErrReport
	}
	root, err := report.openTrialStaging(entry)
	if err != nil {
		return nil, ErrReport
	}
	defer func() { _ = root.Close() }()
	data, info, err := readCensusFile(root, trialCheckpointName, 8*1024*1024)
	if err != nil || !sameCensusFile(entry.checkpoint, info) || digest(data) != entry.sha256 {
		clear(data)
		return nil, ErrReport
	}
	return data, nil
}

// Canonical evidence must already exist before retiring its sole checkpoint.
// Failure retains the owned staging or reports incomplete cleanup, never
// recursively removes contents or interprets local cleanup as remote stop.
func (report *privateReport) finishTrialStaging(result RunReport) (*TrialScratchObservation, error) {
	if report.staging == nil || !report.stagingRootBound() {
		return nil, ErrReport
	}
	observation := &TrialScratchObservation{Scope: trialScratchScope, Trials: []TrialScratchEvidence{}}
	staged := 0
	for _, summary := range result.Trials {
		data, err := readPrivateReportFile(report.root, summary.File)
		if err != nil {
			return nil, ErrReport
		}
		trial, err := decodeDocument[Trial](data, 8*1024*1024)
		clear(data)
		if err != nil || trial.ID != summary.ID || trial.CaseSHA256 != summary.CaseSHA256 ||
			trial.CaseID != summary.CaseID || trial.Code != summary.Code || trial.Verdict != summary.Verdict {
			return nil, ErrReport
		}
		evidence := TrialScratchEvidence{
			TrialID: trial.ID, SessionID: trial.SessionID, CaseSHA256: trial.CaseSHA256, Code: "not_attempted",
		}
		if entry := report.staging[trial.SessionID]; entry != nil {
			staged++
			if entry.trialID != trial.ID || entry.caseSHA256 != trial.CaseSHA256 {
				return nil, ErrReport
			}
			evidence.Code = "unavailable"
			if entry.checkpoint != nil && validDigest(entry.sha256) {
				evidence.Code, evidence.CreatedAt, evidence.CheckpointSHA256 = "retained", entry.createdAt, entry.sha256
				if report.removeTrialStaging(entry) == nil {
					evidence.Code = "removed"
				}
			}
		}
		observation.Trials = append(observation.Trials, evidence)
		if !observation.matches(trial) {
			return nil, ErrReport
		}
	}
	if staged != len(report.staging) || !observation.validFor(result) {
		return nil, ErrReport
	}
	return observation, nil
}

func (report *privateReport) removeTrialStaging(entry *trialStaging) error {
	root, err := report.openTrialStaging(entry)
	if err != nil {
		return ErrReport
	}
	defer func() { _ = root.Close() }()
	entries, err := censusDirectoryEntries(root, 1)
	if err != nil || len(entries) != 1 || entries[0].Name() != trialCheckpointName {
		return ErrReport
	}
	data, info, err := readCensusFile(root, trialCheckpointName, 8*1024*1024)
	defer clear(data)
	if err != nil || !sameCensusFile(entry.checkpoint, info) || digest(data) != entry.sha256 ||
		!report.stagingRootBound() || root.Remove(trialCheckpointName) != nil || root.Close() != nil {
		return ErrReport
	}
	current, err := report.root.Lstat(entry.directory)
	if err != nil || !current.IsDir() || !os.SameFile(entry.directoryInfo, current) ||
		report.root.Remove(entry.directory) != nil {
		return ErrReport
	}
	return nil
}
