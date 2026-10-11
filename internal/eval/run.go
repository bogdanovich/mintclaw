package eval

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
)

var (
	ErrRunOptions = errors.New("eval: invalid run options")
	ErrNotPassed  = errors.New("eval: run did not pass; inspect private report")
)

type RunOptions struct {
	Home         string
	OutputParent string
	SuiteTimeout time.Duration
}

// Run attaches only after prepared-target admission. It never provisions or
// restarts a target, replays a task, or treats disconnected work as canceled.
func Run(ctx context.Context, preset Preset, cases []LoadedCase, options RunOptions) (RunReport, error) {
	if len(cases) == 0 || len(cases) > MaxCorpusCases {
		return RunReport{}, ErrRunOptions
	}
	if options.SuiteTimeout == 0 {
		options.SuiteTimeout = time.Hour
	}
	if options.SuiteTimeout <= 0 || options.SuiteTimeout > 4*time.Hour {
		return RunReport{}, ErrRunOptions
	}
	seen := make(map[string]bool)
	for _, item := range cases {
		if item.Case.Validate() != nil || !validDigest(item.Digest) || seen[item.Case.ID] {
			return RunReport{}, ErrInvalidCase
		}
		seen[item.Case.ID] = true
	}
	suiteCtx, cancel := context.WithTimeout(ctx, options.SuiteTimeout)
	defer cancel()
	target, err := admitPreparedTarget(suiteCtx, preset, options.Home)
	if err != nil {
		return RunReport{}, err
	}
	result := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: preset.ID, Runtime: target.probe,
		Verdict: Pass, StartedAt: time.Now().UTC(), Trials: []TrialSummary{}, Coverage: map[Verdict]int{},
		Usage: "unknown", Cleanup: "local_transports_closed; remote_state_unknown",
	}
	home, _ := privateHome(options.Home)
	writer, err := newPrivateReport(options.OutputParent, home, preset.ExpectedWorkspace, result.ID)
	if err != nil {
		return RunReport{}, err
	}
	defer func() { _ = writer.root.Close() }()
	result.ReportDir = writer.dir
	for _, item := range cases {
		trial := newUnattemptedTrial(item, preset.ExpectedAgent)
		switch {
		case !containsAll(preset.Capabilities, item.Case.Requires):
			trial.Verdict, trial.Code = Skipped, "capability_unavailable"
		case item.Case.Driver.Mode != "scripted":
			trial.Verdict, trial.Code = Skipped, "driver_unavailable"
		case len(item.Case.Fixtures) != 0 || len(item.Case.Driver.Facts) != 0:
			trial.Verdict, trial.Code = Skipped, "domain_evidence_unavailable"
		case suiteCtx.Err() != nil:
			trial.Verdict, trial.Code = Inconclusive, "suite_interrupted"
		default:
			trial = runAdmittedTrial(suiteCtx, target, item, trial.SessionID)
		}
		name := "trial-" + trial.ID + ".json"
		result.Trials = append(result.Trials, TrialSummary{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		})
		result.Coverage[trial.Verdict]++
		result.Verdict = combineVerdicts(result.Verdict, trial.Verdict)
		if writer.write(name, trial) != nil {
			result.Trials[len(result.Trials)-1].File = ""
			result.Verdict = combineVerdicts(result.Verdict, Error)
			result.DurationMS = time.Since(result.StartedAt).Milliseconds()
			return result, ErrReport
		}
	}
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

func runAdmittedTrial(ctx context.Context, target admittedTarget, item LoadedCase, sessionID string) (trial Trial) {
	trial = newUnattemptedTrial(item, target.preset.ExpectedAgent)
	trial.SessionID = sessionID
	started := trial.StartedAt
	defer func() {
		trial.StartedAt = started
		trial.DurationMS = time.Since(started).Milliseconds()
	}()
	caseTimeout, _ := time.ParseDuration(item.Case.Limits.CaseTimeout)
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	// Recheck the running instance between trials before any new task is sent.
	probe, err := requestRuntimeProbe(caseCtx, target.config, target.serviceToken)
	if err != nil || probe != target.probe {
		trial.Verdict, trial.Code = Inconclusive, "target_changed"
		return trial
	}
	client, err := gatewayclient.Connect(caseCtx, target.config, sessionID, gatewayclient.Options{})
	if err != nil {
		trial.Verdict, trial.Code = Inconclusive, "gateway_unavailable"
		trial.DurationMS = time.Since(trial.StartedAt).Milliseconds()
		return trial
	}
	return runScriptedConversation(caseCtx, client, item, sessionID,
		target.preset.ExpectedAgent, target.preset.ExpectedWorkspace)
}

func newUnattemptedTrial(item LoadedCase, agent string) Trial {
	return Trial{
		Version: Version, ID: uuid.NewString(), CaseID: item.Case.ID, CaseSHA256: item.Digest,
		SessionID: uuid.NewString(), AgentID: agent, Verdict: Inconclusive, Code: "not_attempted",
		StartedAt: time.Now().UTC(), Events: []Event{}, Checks: []CheckResult{}, RemoteState: "not_sent",
	}
}

// An explicit home avoids the provider/auth defaults of the production profile.
func RuntimeHome() string { return os.Getenv("MINTCLAW_HOME") }
