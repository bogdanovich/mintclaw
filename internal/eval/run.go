package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
)

var (
	ErrRunOptions = errors.New("eval: invalid run options")
	ErrNotPassed  = errors.New("eval: run did not pass; inspect private report")
)

const MaxRepeat = 64

type RunOptions struct {
	Home            string
	OutputParent    string
	SuiteTimeout    time.Duration
	PDFOraclePython string
	Repeat          int
}

// Run attaches only after prepared-target admission. It never provisions or
// restarts a target, replays a task, or treats disconnected work as canceled.
func Run(ctx context.Context, preset Preset, cases []LoadedCase, options RunOptions) (RunReport, error) {
	if len(cases) == 0 || len(cases) > MaxCorpusCases {
		return RunReport{}, ErrRunOptions
	}
	if options.Repeat == 0 {
		options.Repeat = 1
	}
	if options.Repeat < 1 || options.Repeat > MaxRepeat || options.Repeat > MaxCorpusCases/len(cases) {
		return RunReport{}, ErrRunOptions
	}
	if options.SuiteTimeout == 0 {
		options.SuiteTimeout = time.Hour
	}
	if options.SuiteTimeout <= 0 || options.SuiteTimeout > 4*time.Hour ||
		options.PDFOraclePython != "" && !filepath.IsAbs(options.PDFOraclePython) {
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
	filter := target.config.SensitiveDataReplacer()
	if credentialJSONValue(preset, filter) || credentialJSONValue(target.probe, filter) {
		return RunReport{}, ErrUnsafeTarget
	}
	for _, item := range cases {
		if credentialJSONValue(item.Case, filter) {
			return RunReport{}, ErrPrivateEvidence
		}
	}
	result := RunReport{
		Version: Version, ID: uuid.NewString(), PresetID: preset.ID, Runtime: target.probe, Mode: "run",
		Repeat:  options.Repeat,
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
	roles := prepareConversationRoles(target, cases)
	if roles.user != nil {
		defer roles.user.Close()
	}
	if roles.judge != nil {
		defer roles.judge.Close()
	}
	roleCalls := []RoleCall{}
	// Case-major repetitions are independent trials, not retries of one session.
	for ordinal := range len(cases) * options.Repeat {
		item := cases[ordinal/options.Repeat]
		trial := newUnattemptedTrial(item, preset.ExpectedAgent)
		switch {
		case !containsAll(preset.Capabilities, item.Case.Requires):
			trial.Verdict, trial.Code = Skipped, "capability_unavailable"
		case item.Case.Driver.Mode == "llm_user" && roles.user == nil:
			trial.Verdict, trial.Code = Skipped, "driver_unavailable"
		case item.Case.PDF == nil && (len(item.Case.Fixtures) != 0 || len(item.Case.Driver.Facts) != 0):
			trial.Verdict, trial.Code = Skipped, "domain_evidence_unavailable"
		case suiteCtx.Err() != nil:
			trial.Verdict, trial.Code = Inconclusive, "suite_interrupted"
		default:
			trial = runAdmittedTrial(suiteCtx, target, item, trial.SessionID, roles, options, writer)
		}
		trial.Repetition = ordinal%options.Repeat + 1
		name := "trial-" + trial.ID + ".json"
		roleCalls = append(roleCalls, trial.DriverCalls...)
		if trial.Judge != nil && trial.Judge.Call != nil {
			roleCalls = append(roleCalls, *trial.Judge.Call)
		}
		result.RoleUsage = []RoleUsage{summarizeRoleCalls("user", roleCalls), summarizeRoleCalls("judge", roleCalls)}
		result.Trials = append(result.Trials, TrialSummary{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
			Repetition: trial.Repetition,
			Verdict:    trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
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

func prepareConversationRoles(target admittedTarget, cases []LoadedCase) conversationRoles {
	roles := conversationRoles{filter: target.config.SensitiveDataReplacer()}
	needsUser, needsJudge := false, false
	for _, item := range cases {
		needsUser = needsUser || item.Case.Driver.Mode == "llm_user"
		needsJudge = needsJudge || len(item.Case.Rubric) > 0
	}
	if needsUser && target.preset.UserModel != "" {
		roles.user, _ = NewModelRole(target.config, target.preset.UserModel, "user")
	}
	if needsJudge && target.preset.JudgeModel != "" {
		roles.judge, _ = NewModelRole(target.config, target.preset.JudgeModel, "judge")
	}
	for _, role := range []*ModelRole{roles.user, roles.judge} {
		if role != nil && credentialJSONValue(role.identity, roles.filter) {
			role.Close()
			if role == roles.user {
				roles.user = nil
			} else {
				roles.judge = nil
			}
		}
	}
	return roles
}

func runAdmittedTrial(
	ctx context.Context, target admittedTarget, item LoadedCase, sessionID string, roles conversationRoles,
	runOptions RunOptions, report *privateReport,
) (trial Trial) {
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
	var domain *pdfConversation
	options := gatewayclient.Options{}
	if item.Case.PDF != nil {
		prepared, prepareErr := preparePDFTrial(item, target.preset.IsolationRoot,
			target.preset.ExpectedWorkspace, sessionID, target.preset.ExpectedAgent)
		if prepareErr != nil {
			trial.Verdict, trial.Code = Inconclusive, "pdf_preparation_unavailable"
			return trial
		}
		domain = &pdfConversation{prepared: prepared, artifacts: &pdfArtifactCollector{
			config: target.config, python: runOptions.PDFOraclePython, retain: report.retainPDFArtifact,
		}}
		prepared.filter = roles.filter
		item = prepared.item
		options.InspectFrame = func(data []byte) error {
			return inspectPDFWire(data, item.Case.Driver.Facts, roles.filter)
		}
	}
	client, err := gatewayclient.Connect(caseCtx, target.config, sessionID, options)
	if err != nil {
		if domain != nil {
			_ = domain.prepared.release(true)
		}
		trial.Verdict, trial.Code = Inconclusive, "gateway_unavailable"
		trial.DurationMS = time.Since(trial.StartedAt).Milliseconds()
		return trial
	}
	if domain != nil {
		trial = runDomainConversation(caseCtx, client, item, sessionID,
			target.preset.ExpectedAgent, target.preset.ExpectedWorkspace, roles, domain)
		if domain.prepared.release(false) != nil {
			trial.Verdict = combineVerdicts(trial.Verdict, Inconclusive)
			trial.Code = "pdf_local_cleanup_failed"
		}
		return trial
	}
	return runConversation(caseCtx, client, item, sessionID,
		target.preset.ExpectedAgent, target.preset.ExpectedWorkspace, roles)
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
