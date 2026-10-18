package eval

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
)

var (
	ErrCompare       = errors.New("eval: complete private reports are required for comparison")
	ErrNotComparable = errors.New("eval: compared reports have incompatible case cohorts")
)

type ComparisonCounts struct {
	Trials   int             `json:"trials"`
	Coverage map[Verdict]int `json:"coverage"`
}

type CaseComparison struct {
	CaseID   string           `json:"case_id"`
	Status   string           `json:"status"`
	Baseline ComparisonCounts `json:"baseline"`
	Current  ComparisonCounts `json:"current"`
}

type Comparison struct {
	Version             int              `json:"version"`
	Outcome             string           `json:"outcome"`
	BaselineRun         string           `json:"baseline_run"`
	CurrentRun          string           `json:"current_run"`
	BaselineVerdict     Verdict          `json:"baseline_verdict"`
	CurrentVerdict      Verdict          `json:"current_verdict"`
	CompatibleCases     bool             `json:"compatible_cases"`
	EnvironmentChanges  []string         `json:"environment_changes"`
	EnvironmentUnknowns []string         `json:"environment_unknowns"`
	EnvironmentCoverage string           `json:"environment_coverage"`
	Attribution         string           `json:"quality_attribution"`
	Cases               []CaseComparison `json:"cases"`
}

type comparisonSource struct {
	report  RunReport
	cases   map[string]ComparisonCounts
	digests map[string]string
	roles   []string
}

// Compare performs only bounded, read-only report I/O. Counts describe retained
// observations, not a new grading decision or causal quality attribution.
func Compare(baselineDirectory, currentDirectory string) (Comparison, error) {
	baseline, err := readComparisonSource(baselineDirectory)
	if err != nil {
		return Comparison{}, ErrCompare
	}
	current, err := readComparisonSource(currentDirectory)
	if err != nil {
		return Comparison{}, ErrCompare
	}
	result := Comparison{
		Version: Version, Outcome: "compared", BaselineRun: baseline.report.ID, CurrentRun: current.report.ID,
		BaselineVerdict: baseline.report.Verdict, CurrentVerdict: current.report.Verdict,
		CompatibleCases: true, EnvironmentChanges: []string{}, EnvironmentCoverage: "recorded_runtime_and_roles_only",
		EnvironmentUnknowns: []string{"instructions_and_skills", "target_all_call_models"},
		Attribution:         "not_established", Cases: []CaseComparison{},
	}
	compareWorkspaceInputs(&result, baseline.report.WorkspaceInputs, current.report.WorkspaceInputs)
	for _, source := range []struct {
		name        string
		observation *PrivateSurfaceObservation
	}{{"baseline", baseline.report.PrivateSurfaces}, {"current", current.report.PrivateSurfaces}} {
		if (baseline.report.PrivateSurfaces != nil || current.report.PrivateSurfaces != nil) &&
			(source.observation == nil || source.observation.verdict() != Pass) {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_private_file_coverage")
		}
	}
	if baseline.report.TargetLifecycle.verdict() != Pass {
		result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, "baseline_target_cleanup")
	}
	if current.report.TargetLifecycle.verdict() != Pass {
		result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, "current_target_cleanup")
	}
	if len(baseline.report.Runtime.GitCommit) != 40 || len(current.report.Runtime.GitCommit) != 40 ||
		baseline.report.Runtime.GOOS == "" || current.report.Runtime.GOOS == "" ||
		baseline.report.Runtime.GOARCH == "" || current.report.Runtime.GOARCH == "" {
		result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, "runtime_revision_or_platform")
	}
	for _, difference := range []struct {
		name    string
		changed bool
	}{
		{"preset", baseline.report.PresetID != current.report.PresetID},
		{"runtime_revision", baseline.report.Runtime.GitCommit != current.report.Runtime.GitCommit ||
			baseline.report.Runtime.BuildVersion != current.report.Runtime.BuildVersion},
		{"configuration", baseline.report.Runtime.ConfigSHA256 != current.report.Runtime.ConfigSHA256},
		{"platform", baseline.report.Runtime.GOOS != current.report.Runtime.GOOS ||
			baseline.report.Runtime.GOARCH != current.report.Runtime.GOARCH ||
			baseline.report.Runtime.GoVersion != current.report.Runtime.GoVersion},
		{"grading_mode", baseline.report.Mode != current.report.Mode},
		{"model_roles_or_prompts", !slices.Equal(baseline.roles, current.roles)},
		{"repetitions", effectiveRepeat(baseline.report) != effectiveRepeat(current.report)},
		{"target_lifecycle_mode", targetLifecycleMode(baseline.report.TargetLifecycle) !=
			targetLifecycleMode(current.report.TargetLifecycle)},
	} {
		if difference.changed {
			result.EnvironmentChanges = append(result.EnvironmentChanges, difference.name)
		}
	}
	ids := make([]string, 0, len(baseline.cases)+len(current.cases))
	for id := range baseline.cases {
		ids = append(ids, id)
	}
	for id := range current.cases {
		if _, exists := baseline.cases[id]; !exists {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		before, had := baseline.cases[id]
		after, has := current.cases[id]
		status := "same_case"
		switch {
		case !had:
			status = "added"
		case !has:
			status = "removed"
		case baseline.digests[id] != current.digests[id]:
			status = "case_changed"
		}
		if status != "same_case" {
			result.CompatibleCases = false
		}
		if before.Coverage == nil {
			before.Coverage = map[Verdict]int{}
		}
		if after.Coverage == nil {
			after.Coverage = map[Verdict]int{}
		}
		result.Cases = append(result.Cases, CaseComparison{id, status, before, after})
	}
	if !result.CompatibleCases {
		return result, ErrNotComparable
	}
	return result, nil
}

func effectiveRepeat(report RunReport) int {
	if report.Repeat == 0 {
		return 1
	}
	return report.Repeat
}

func readComparisonSource(directory string) (comparisonSource, error) {
	result := comparisonSource{cases: map[string]ComparisonCounts{}, digests: map[string]string{}}
	physical, err := privateHome(directory)
	if err != nil {
		return result, ErrCompare
	}
	root, err := os.OpenRoot(physical)
	if err != nil {
		return result, ErrCompare
	}
	defer func() { _ = root.Close() }()
	data, err := readPrivateReportFile(root, "run.json")
	if err != nil {
		return result, ErrCompare
	}
	report, err := decodeDocument[RunReport](data, 8*1024*1024)
	if err != nil || report.Version != Version || !report.Complete || !validID(report.ID) ||
		!validID(report.PresetID) || !slices.Contains([]string{"run", "grade"}, report.Mode) ||
		report.Repeat < 0 || report.Repeat > MaxRepeat || len(report.Trials) == 0 ||
		len(report.Trials) > MaxCorpusCases || report.DurationMS < 0 || report.StartedAt.IsZero() ||
		report.Runtime.Version != 1 || !validDigest(report.Runtime.ConfigSHA256) || !report.TargetLifecycle.valid() ||
		!report.WorkspaceInputs.valid() || !report.PrivateSurfaces.valid() || !report.BrowserFixtures.valid() {
		return result, ErrCompare
	}
	seen, sessions, roles := map[string]bool{}, map[string]bool{}, map[string]bool{}
	coverage := map[Verdict]int{}
	verdicts := make([]Verdict, 0, len(report.Trials))
	for _, summary := range report.Trials {
		count := result.cases[summary.CaseID]
		if !validID(summary.ID) || seen[summary.ID] || !validID(summary.CaseID) ||
			!validDigest(summary.CaseSHA256) || !validID(summary.Code) || summary.DurationMS < 0 ||
			!validComparisonVerdict(summary.Verdict) || summary.File != "trial-"+summary.ID+".json" ||
			count.Trials >= effectiveRepeat(report) ||
			report.Repeat == 0 && summary.Repetition != 0 ||
			report.Repeat != 0 && summary.Repetition != count.Trials+1 ||
			result.digests[summary.CaseID] != "" && result.digests[summary.CaseID] != summary.CaseSHA256 {
			return result, ErrCompare
		}
		data, err = readPrivateReportFile(root, summary.File)
		if err != nil {
			return result, ErrCompare
		}
		trial, err := decodeDocument[Trial](data, 8*1024*1024)
		if err != nil || trial.Version != Version || trial.ID != summary.ID || trial.CaseID != summary.CaseID ||
			trial.CaseSHA256 != summary.CaseSHA256 || trial.Repetition != summary.Repetition ||
			trial.Code != summary.Code || trial.Verdict != summary.Verdict || trial.DurationMS != summary.DurationMS ||
			!validID(trial.SessionID) || sessions[trial.SessionID] || !validID(trial.AgentID) ||
			!consistentComparisonVerdict(trial) || !validTrialRoleBoundaries(trial) {
			return result, ErrCompare
		}
		calls := trial.DriverCalls
		if trial.Judge != nil && trial.Judge.Call != nil {
			calls = append(slices.Clone(calls), *trial.Judge.Call)
		}
		for _, call := range calls {
			identity, err := json.Marshal(struct {
				Identity RoleIdentity `json:"identity"`
				Prompt   string       `json:"prompt"`
			}{call.Identity, call.PromptVersion})
			if err != nil {
				return result, ErrCompare
			}
			roles[string(identity)] = true
		}
		if count.Coverage == nil {
			count.Coverage = map[Verdict]int{}
		}
		count.Trials++
		count.Coverage[summary.Verdict]++
		result.cases[summary.CaseID], result.digests[summary.CaseID] = count, summary.CaseSHA256
		seen[summary.ID], sessions[trial.SessionID] = true, true
		coverage[summary.Verdict]++
		verdicts = append(verdicts, summary.Verdict)
	}
	for _, count := range result.cases {
		if count.Trials != effectiveRepeat(report) {
			return result, ErrCompare
		}
	}
	for verdict, count := range report.Coverage {
		if !validComparisonVerdict(verdict) || count < 0 || coverage[verdict] != count {
			return result, ErrCompare
		}
	}
	for verdict, count := range coverage {
		if report.Coverage[verdict] != count {
			return result, ErrCompare
		}
	}
	verdicts = append(
		verdicts,
		report.TargetLifecycle.verdict(),
		report.PrivateSurfaces.verdict(),
		report.BrowserFixtures.verdict(),
	)
	if report.Verdict != combineVerdicts(verdicts...) {
		return result, ErrCompare
	}
	for identity := range roles {
		result.roles = append(result.roles, identity)
	}
	slices.Sort(result.roles)
	result.report = report
	return result, nil
}

func validComparisonVerdict(verdict Verdict) bool {
	return slices.Contains([]Verdict{Pass, Fail, Error, Inconclusive, Skipped}, verdict)
}

func consistentComparisonVerdict(trial Trial) bool {
	verdicts := []Verdict{trial.Verdict}
	addBoundary := func(call *RoleCall) {
		if call == nil || call.Boundary == nil {
			return
		}
		if call.Boundary.Input == "leak_observed" || call.Boundary.Output == "leak_observed" {
			verdicts = append(verdicts, Fail)
		} else if !roleBoundaryEligible(call) {
			verdicts = append(verdicts, Inconclusive)
		}
	}
	for index := range trial.DriverCalls {
		addBoundary(&trial.DriverCalls[index])
	}
	if trial.Judge != nil {
		addBoundary(trial.Judge.Call)
	}
	if trial.ExecutionVerdict != "" {
		if !validComparisonVerdict(trial.ExecutionVerdict) {
			return false
		}
		verdicts = append(verdicts, trial.ExecutionVerdict)
	}
	for _, check := range trial.Checks {
		if !validComparisonVerdict(check.Verdict) {
			return false
		}
		verdicts = append(verdicts, check.Verdict)
	}
	if trial.Judge != nil && trial.Judge.Code == "private_judge_output" {
		verdicts = append(verdicts, Fail)
	}
	return combineVerdicts(verdicts...) == trial.Verdict
}
