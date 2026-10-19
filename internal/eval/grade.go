package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

var ErrGrade = errors.New("eval: retained evidence or judge profile not eligible for grading")

// Grade reads eligible private evidence and calls only a tools-disabled judge.
// It does not connect to a gateway, probe a process, send tasks or rerun tools.
func Grade(
	ctx context.Context, preset Preset, cases []LoadedCase, sourceDirectory string, options RunOptions,
) (RunReport, error) {
	cfg, err := gradeConfig(preset, options.Home)
	if err != nil || preset.JudgeModel == "" || len(cases) == 0 || len(cases) > MaxCorpusCases {
		return RunReport{}, ErrGrade
	}
	if options.SuiteTimeout == 0 {
		options.SuiteTimeout = time.Hour
	}
	if options.SuiteTimeout <= 0 || options.SuiteTimeout > 4*time.Hour {
		return RunReport{}, ErrGrade
	}
	ctx, cancel := context.WithTimeout(ctx, options.SuiteTimeout)
	defer cancel()
	filter := cfg.SensitiveDataReplacer()
	selected := make(map[string]LoadedCase, len(cases))
	for _, item := range cases {
		if item.Case.Validate() != nil || !validDigest(item.Digest) || selected[item.Case.ID].Digest != "" ||
			credentialJSONValue(item.Case, filter) {
			return RunReport{}, ErrGrade
		}
		selected[item.Case.ID] = item
	}
	home, _ := privateHome(options.Home)
	source, err := privateHome(sourceDirectory)
	workspace, workspaceErr := filepath.EvalSymlinks(preset.ExpectedWorkspace)
	if err != nil || workspaceErr != nil || !pathWithin(home, source) ||
		source == workspace || pathWithin(workspace, source) || pathWithin(source, workspace) {
		return RunReport{}, ErrGrade
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return RunReport{}, ErrGrade
	}
	defer func() { _ = root.Close() }()
	data, err := readPrivateReportFile(root, "run.json")
	if err != nil {
		return RunReport{}, ErrGrade
	}
	original, err := decodeDocument[RunReport](data, 8*1024*1024)
	if err != nil || original.Version != Version || !original.Complete || original.Mode != "run" ||
		!validID(original.ID) || original.PresetID != preset.ID || len(original.Trials) == 0 ||
		len(original.Trials) > MaxCorpusCases || original.Repeat < 0 || original.Repeat > MaxRepeat ||
		credentialJSONValue(
			original,
			filter,
		) || !original.TargetLifecycle.valid() || !original.WorkspaceInputs.valid() ||
		!original.PrivateSurfaces.valid() || !original.BrowserFixtures.valid() || !original.TargetUsage.valid() ||
		original.PrivateSurfaces != nil && original.PrivateSurfaces.LeakObserved {
		return RunReport{}, ErrGrade
	}
	repeat := original.Repeat
	if repeat == 0 {
		repeat = 1
	}
	// Admit every selected source before making a billable judge call.
	trials := make([]Trial, 0, len(original.Trials))
	seen := make(map[string]int)
	identities := make(map[string]bool)
	sessions := make(map[string]bool)
	artifacts := make(map[string][]byte)
	artifactBytes := 0
	defer func() {
		for _, data := range artifacts {
			clear(data)
		}
	}()
	for _, summary := range original.Trials {
		item, wanted := selected[summary.CaseID]
		if !wanted {
			continue
		}
		if seen[summary.CaseID] >= repeat || !validID(summary.ID) || identities[summary.ID] ||
			summary.CaseSHA256 != item.Digest ||
			original.Repeat == 0 && summary.Repetition != 0 ||
			original.Repeat != 0 && summary.Repetition != seen[summary.CaseID]+1 ||
			summary.File != "trial-"+summary.ID+".json" {
			return RunReport{}, ErrGrade
		}
		data, err = readPrivateReportFile(root, summary.File)
		if err != nil {
			return RunReport{}, ErrGrade
		}
		trial, decodeErr := decodeDocument[Trial](data, 8*1024*1024)
		if decodeErr != nil || !gradeEligible(trial, item, summary, preset, workspace) ||
			(item.Case.PDF != nil || item.Case.Browser != nil) &&
				!observedOwnedProfile(original.TargetLifecycle, original.PrivateSurfaces) ||
			item.Case.Browser != nil &&
				!observedBrowserHost(trial.Browser, original.BrowserFixtures) ||
			sessions[trial.SessionID] || credentialJSONValue(trial, filter) {
			return RunReport{}, ErrGrade
		}
		if item.Case.Browser != nil && item.Case.Browser.Scenario == "artifact" {
			raw, artifactErr := browserArtifactForGrade(root, trial, filter)
			if artifactErr != nil {
				return RunReport{}, ErrGrade
			}
			artifacts[trial.ID] = raw
			artifactBytes += len(raw)
			if artifactBytes > maxRetainedPDFBytes {
				return RunReport{}, ErrGrade
			}
		}
		trials = append(trials, trial)
		seen[summary.CaseID]++
		identities[summary.ID] = true
		sessions[trial.SessionID] = true
	}
	if len(seen) != len(selected) {
		return RunReport{}, ErrGrade
	}
	for _, count := range seen {
		if count != repeat {
			return RunReport{}, ErrGrade
		}
	}
	judge, err := NewModelRole(cfg, preset.JudgeModel, "judge")
	if err != nil {
		return RunReport{}, ErrGrade
	}
	defer judge.Close()
	if credentialJSONValue(judge.identity, filter) {
		return RunReport{}, ErrGrade
	}
	result := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: preset.ID, Runtime: original.Runtime,
		Mode: "grade", SourceRun: original.ID, Verdict: Pass, StartedAt: time.Now().UTC(),
		Repeat: original.Repeat,
		Trials: []TrialSummary{}, Coverage: map[Verdict]int{}, Usage: "unknown", Cleanup: "no_target_or_tool_work",
		TargetLifecycle: original.TargetLifecycle,
		WorkspaceInputs: original.WorkspaceInputs,
		PrivateSurfaces: original.PrivateSurfaces,
		BrowserFixtures: original.BrowserFixtures,
		TargetUsage:     newTargetUsage(),
	}
	writer, err := newPrivateReport(options.OutputParent, home, workspace, result.ID)
	if err != nil {
		return RunReport{}, ErrReport
	}
	defer func() { _ = writer.root.Close() }()
	result.ReportDir = writer.dir
	calls := []RoleCall{}
	for _, trial := range trials {
		result.TargetUsage.addTrial(trial)
		if raw := artifacts[trial.ID]; raw != nil {
			if _, err = writer.retainBrowserArtifact(trial.ID, raw); err != nil {
				return result, ErrReport
			}
		}
		item := selected[trial.CaseID]
		last := lastOutcome(trial.Events)
		trial.Checks = gradeConversation(item.Case.Checks, trial.Events, last)
		if item.Case.PDF != nil {
			trial.Checks = gradeFinalizedPDFChecks(item.Case, trial)
		}
		if item.Case.Browser != nil {
			trial.Checks = gradeFinalizedBrowserChecks(item.Case, trial)
		}
		trial.Verdict, trial.Code = trial.ExecutionVerdict, trial.ExecutionCode
		for _, check := range trial.Checks {
			trial.Verdict = combineVerdicts(trial.Verdict, check.Verdict)
		}
		judgement := judgeConversation(ctx, judge, item.Case, trial.Events)
		if credentialJSONValue(judgement, filter) {
			judgement.Findings = JudgeFindings{Version: Version, Criteria: []JudgeFinding{}}
			judgement.Verdict, judgement.Code = Fail, "private_judge_output"
		}
		trial.Judge = &judgement
		retainPDFJudgePrivacy(item.Case, &trial)
		retainBrowserJudgePrivacy(item.Case, &trial)
		if !roleBoundaryEligible(judgement.Call) {
			trial.EvidenceEligible = false
		}
		if judgement.Code == "private_judge_output" {
			trial.EvidenceEligible = false
			trial.ExecutionVerdict, trial.ExecutionCode = Fail, judgement.Code
		}
		if requiredRubric(item.Case.Rubric) || judgement.Code == "private_judge_output" ||
			!roleBoundaryEligible(judgement.Call) {
			trial.Verdict = combineVerdicts(trial.Verdict, judgement.Verdict)
		}
		if trial.Verdict == Fail {
			trial.Code = "required_assessment_failed"
		} else {
			trial.Code = judgement.Code
		}
		if judgement.Call != nil {
			calls = append(calls, *judgement.Call)
		}
		result.RoleUsage = []RoleUsage{summarizeRoleCalls("judge", calls)}
		name := "trial-" + trial.ID + ".json"
		result.Trials = append(result.Trials, TrialSummary{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Verdict: trial.Verdict,
			Repetition: trial.Repetition,
			Code:       trial.Code, DurationMS: trial.DurationMS, File: name,
		})
		result.Coverage[trial.Verdict]++
		result.Verdict = combineVerdicts(result.Verdict, trial.Verdict)
		if writer.write(name, trial) != nil {
			result.Trials[len(result.Trials)-1].File = ""
			result.Verdict = combineVerdicts(result.Verdict, Error)
			return result, ErrReport
		}
	}
	result.Verdict = combineVerdicts(
		result.Verdict,
		original.TargetLifecycle.verdict(),
		original.PrivateSurfaces.verdict(),
		original.BrowserFixtures.verdict(),
	)
	result.DurationMS = time.Since(result.StartedAt).Milliseconds()
	result.Complete = true
	if writer.finish(result) != nil {
		result.Complete = false
		result.Verdict = combineVerdicts(result.Verdict, Error)
		return result, ErrReport
	}
	if result.Verdict != Pass {
		return result, ErrNotPassed
	}
	return result, nil
}

func gradeConfig(preset Preset, home string) (*config.Config, error) {
	profile, err := readPreparedProfile(preset, home)
	if err != nil {
		return nil, ErrGrade
	}
	return profile.config, nil
}

func readPrivateReportFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrGrade
	}
	return readBoundedFile(root, name, 8*1024*1024)
}

func gradeEligible(trial Trial, item LoadedCase, summary TrialSummary, preset Preset, workspace string) bool {
	if !validTrialRoleBoundaries(trial) || trial.Judge != nil && !roleBoundaryEligible(trial.Judge.Call) {
		return false
	}
	for index := range trial.DriverCalls {
		if !roleBoundaryEligible(&trial.DriverCalls[index]) {
			return false
		}
	}
	if trial.Version != Version || trial.ID != summary.ID || trial.CaseID != item.Case.ID ||
		trial.Repetition != summary.Repetition ||
		trial.Verdict != summary.Verdict || trial.Code != summary.Code ||
		trial.CaseSHA256 != item.Digest || trial.AgentID != preset.ExpectedAgent || !validID(trial.SessionID) ||
		!trial.TransportClosed || !trial.EvidenceEligible || trial.ExecutionVerdict != Pass ||
		len(item.Case.Driver.Facts) != 0 || item.Case.PDF == nil && len(item.Case.Fixtures) != 0 ||
		(item.Case.Browser == nil) != (trial.Browser == nil) ||
		len(item.Case.Rubric) == 0 ||
		len(trial.Events) == 0 || len(trial.Events) > 256 {
		return false
	}
	if trial.Judge != nil && trial.Judge.Code == "private_judge_output" {
		return false
	}
	if item.Case.PDF != nil {
		return validPDFNoWriteEvidence(trial, item.Case, workspace) &&
			trial.PDF.PostStop.AssessmentScope == pdfNoWriteAssessmentScope
	}
	if item.Case.Browser != nil {
		return validBrowserEvidence(trial, item.Case, workspace)
	}
	return validTrialConversation(trial, preset.ExpectedAgent, workspace)
}

func validTrialConversation(trial Trial, agent, workspace string) bool {
	if _, err := visibleConversation(trial.Events, nil); err != nil {
		return false
	}
	for _, event := range trial.Events {
		if !validEventUsage(event) {
			return false
		}
		if event.Kind == "input" {
			continue
		}
		if event.AgentID != "" && event.AgentID != agent ||
			event.SessionKey != "" && event.SessionKey != trial.SessionKey {
			return false
		}
		for _, scope := range event.TraceScopes {
			if !scope.Complete() || scope.Workspace != workspace {
				return false
			}
		}
		if event.Kind == "canceled" && !validQuestionCancellation(trial, event, workspace) &&
			!validBrowserCancellation(trial, event, workspace) {
			return false
		}
		if event.Kind != "stream" && (event.AgentID == "" || event.SessionKey == "" ||
			len(event.TraceScopes) == 0 && event.Kind != "canceled") {
			return false
		}
	}
	return lastOutcome(trial.Events).ID != ""
}

func lastOutcome(events []Event) Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == "final" || events[index].Kind == "question" || events[index].Kind == "approval" ||
			events[index].Kind == "canceled" {
			return events[index]
		}
	}
	return Event{}
}
