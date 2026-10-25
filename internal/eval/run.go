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
	StartTarget     bool
	IncludePending  bool
}

// Run defaults to attachment after prepared-target admission. StartTarget owns
// one fresh gateway child, never adopts/restarts a PID or replays a task.
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
	if err := CheckRunAvailability(cases, options.IncludePending); err != nil {
		return RunReport{}, err
	}
	seen := make(map[string]bool)
	for _, item := range cases {
		if item.Case.Validate() != nil || !validDigest(item.Digest) || seen[item.Case.ID] {
			return RunReport{}, ErrInvalidCase
		}
		if item.Case.PDF != nil && item.Case.PDF.SourceFields != nil &&
			containsAll(preset.Capabilities, item.Case.Requires) && !preset.CaptureModelMetadata {
			return RunReport{}, ErrUnsafeTarget
		}
		if (isBrowserForm(item.Case) || isBrowserAmbiguity(item.Case)) &&
			containsAll(preset.Capabilities, item.Case.Requires) &&
			(!preset.CaptureModelMetadata || !options.StartTarget) {
			return RunReport{}, ErrUnsafeTarget
		}
		seen[item.Case.ID] = true
	}
	suiteCtx, cancel := context.WithTimeout(ctx, options.SuiteTimeout)
	defer cancel()
	var child *ownedGateway
	var fixtures *browserFixtureHost
	var target admittedTarget
	var err error
	if options.StartTarget {
		fixtures, err = bindOwnedBrowserFixture(preset, options.Home)
		if err == nil {
			child, target, err = startOwnedGatewayWithBrowser(suiteCtx, preset, options.Home, fixtures)
		}
	} else {
		target, err = admitPreparedTarget(suiteCtx, preset, options.Home)
	}
	var lifecycle *TargetLifecycle
	var retainedFacts privateFactRetention
	var pendingPDF pdfRunFinalization
	pendingBrowser := browserRun{host: fixtures}
	var privateSurfaces *PrivateSurfaceObservation
	stopTarget := func() {
		if child == nil || lifecycle != nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cleanupCancel()
		receipt := child.stopAndWait(cleanupCtx)
		lifecycle = &receipt
	}
	observeOwnedSurfaces := func() {
		if child == nil || privateSurfaces != nil || retainedFacts.values == nil && !preset.CaptureModelMetadata {
			return
		}
		scanCtx, scanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scanCancel()
		observation := observePrivateSurfaces(scanCtx, child.profile.home, retainedFacts.values,
			child.profile.config.SensitiveDataReplacer(), retainedFacts.documents,
			pendingBrowser.approvalKeyWitness(child.profile.home))
		privateSurfaces = &observation
	}
	defer func() {
		stopTarget()
		pendingBrowser.close(lifecycle)
		// Even report I/O failures must not discard the only private fact set
		// before attempting the post-stop raw observation.
		observeOwnedSurfaces()
		pendingPDF.close()
		retainedFacts.clear()
	}()
	if err != nil {
		stopTarget()
		pendingBrowser.close(lifecycle)
		observeOwnedSurfaces()
		if privateSurfaces != nil {
			if privateSurfaces.LeakObserved {
				err = errors.Join(err, ErrPrivateEvidence)
			}
			// No run was admitted or published. Retain partial cleanup/privacy
			// observations without fabricating an admitted runtime or success.
			return RunReport{
				Version: Version, Mode: "run", Verdict: combineVerdicts(Error, privateSurfaces.verdict()),
				Usage: "unknown", Cleanup: "owned_gateway_startup_failed",
				TargetLifecycle: lifecycle, PrivateSurfaces: privateSurfaces,
			}, err
		}
		return RunReport{}, err
	}
	joinFixtureWatch := watchBrowserFixture(suiteCtx, target.config, fixtures, child, cancel)
	defer joinFixtureWatch()
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
		TargetUsage: newTargetUsage(),
	}
	home, _ := privateHome(options.Home)
	writer, err := newPrivateReport(options.OutputParent, home, preset.ExpectedWorkspace, result.ID)
	if err != nil {
		return RunReport{}, err
	}
	defer func() { _ = writer.root.Close() }()
	result.ReportDir = writer.dir
	if writer.startTrialStaging() != nil {
		return result, ErrReport
	}
	result.WorkspaceInputs = &WorkspaceInputs{
		Scope:  workspaceInputScope,
		Before: observeWorkspaceInputs(suiteCtx, preset.ExpectedWorkspace, cases, filter),
	}
	roles := prepareConversationRoles(target, cases)
	for _, role := range []*ModelRole{roles.user, roles.judge} {
		if role != nil {
			role.retained = &retainedFacts
		}
	}
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
		case item.Case.Browser != nil && fixtures == nil:
			trial.Verdict, trial.Code = Skipped, "browser_evidence_unavailable"
		case item.Case.Browser != nil && len(item.Case.Driver.Facts) != 0 && !isBrowserForm(item.Case):
			trial.Verdict, trial.Code = Skipped, "browser_private_input_unavailable"
		case item.Case.Browser != nil && item.Case.Browser.Scenario != "read" &&
			item.Case.Browser.Scenario != "artifact" && item.Case.Browser.Scenario != "denied" &&
			!isBrowserForm(item.Case) && !isBrowserAmbiguity(item.Case):
			trial.Verdict, trial.Code = Skipped, "browser_action_evidence_unavailable"
		case !containsAll(preset.Capabilities, item.Case.Requires):
			trial.Verdict, trial.Code = Skipped, "capability_unavailable"
		case item.Case.Driver.Mode == "llm_user" && roles.user == nil:
			trial.Verdict, trial.Code = Skipped, "driver_unavailable"
		case item.Case.PDF == nil && item.Case.Browser == nil &&
			(len(item.Case.Fixtures) != 0 || len(item.Case.Driver.Facts) != 0):
			trial.Verdict, trial.Code = Skipped, "domain_evidence_unavailable"
		case suiteCtx.Err() != nil:
			trial.Verdict, trial.Code = Inconclusive, "suite_interrupted"
		default:
			trial = runAdmittedTrial(suiteCtx, target, item, trial.SessionID, roles, options, writer,
				&retainedFacts, &pendingPDF, &pendingBrowser)
		}
		trial.Repetition = ordinal%options.Repeat + 1
		result.TargetUsage.addTrial(trial)
		pendingPDF.observeTrial(trial)
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
		_, pdfPending := pendingPDF.trials[trial.ID]
		_, browserPending := pendingBrowser.trials[trial.ID]
		if writer.checkpointTrial(name, trial, pdfPending || browserPending) != nil {
			result.Trials[len(result.Trials)-1].File = ""
			result.Verdict = combineVerdicts(result.Verdict, Error)
			result.DurationMS = time.Since(result.StartedAt).Milliseconds()
			return result, ErrReport
		}
	}
	result.WorkspaceInputs.After = observeWorkspaceInputs(suiteCtx, preset.ExpectedWorkspace, cases, filter)
	stopTarget()
	result.BrowserFixtures = pendingBrowser.close(lifecycle)
	observeOwnedSurfaces()
	if privateSurfaces != nil {
		result.PrivateSurfaces = privateSurfaces
		if privateSurfaces.LeakObserved {
			result.Verdict = combineVerdicts(result.Verdict, Fail)
		} else if privateSurfaces.Code != "observed" {
			result.Verdict = combineVerdicts(result.Verdict, Inconclusive)
		}
	}
	if lifecycle != nil {
		result.TargetLifecycle = lifecycle
		result.Cleanup = "owned_gateway_stopped; domain_resources_unknown"
		if lifecycle.Code != "native_exit_zero" {
			result.Verdict = combineVerdicts(result.Verdict, Inconclusive)
			result.Cleanup = "owned_gateway_cleanup_uncertain"
		}
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = pendingPDF.finish(finalCtx, writer, &result, lifecycle, retainedFacts.values, filter, privateSurfaces,
		func(item Case, trial *Trial) {
			if item.PDF != nil && item.PDF.SourceFields != nil {
				readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
				trial.PDF.SourceFields = observePDFSourceFields(readCtx, target, lifecycle, privateSurfaces,
					*trial, retainedFacts.values, filter)
				readCancel()
				if trial.PDF.SourceFields.Code == "leak_observed" {
					failPDFArtifact(trial, Fail, "private_native_evidence")
				}
			}
			assessFinalizedPDF(
				suiteCtx,
				roles.judge,
				item,
				trial,
				preset.ExpectedWorkspace,
				lifecycle,
				privateSurfaces,
				writer,
			)
			if trial.Judge != nil && trial.Judge.Call != nil {
				roleCalls = append(roleCalls, *trial.Judge.Call)
			}
		})
	finalCancel()
	browserCtx, browserCancel := context.WithTimeout(context.Background(), 30*time.Second)
	browserErr := pendingBrowser.finish(browserCtx, target, writer, &result, retainedFacts.values, filter,
		func(item Case, trial *Trial) {
			assessFinalizedBrowser(suiteCtx, roles.judge, item, trial, preset.ExpectedWorkspace,
				lifecycle, privateSurfaces, result.BrowserFixtures)
			if trial.Judge != nil && trial.Judge.Call != nil {
				roleCalls = append(roleCalls, *trial.Judge.Call)
			}
		})
	browserCancel()
	if browserErr != nil {
		err = ErrReport
	}
	result.RoleUsage = []RoleUsage{summarizeRoleCalls("user", roleCalls), summarizeRoleCalls("judge", roleCalls)}
	if err != nil {
		result.Verdict = combineVerdicts(result.Verdict, Error)
		result.DurationMS = time.Since(result.StartedAt).Milliseconds()
		return result, ErrReport
	}
	result.TrialScratch, err = writer.finishTrialStaging(result)
	if err != nil {
		result.Verdict = combineVerdicts(result.Verdict, Error)
		return result, ErrReport
	}
	if preset.CaptureModelMetadata {
		bindings, bindingErr := readRunModelBindings(writer, result, preset.ExpectedWorkspace)
		if errors.Is(bindingErr, ErrReport) {
			result.Verdict = combineVerdicts(result.Verdict, Error)
			return result, ErrReport
		}
		if bindingErr != nil {
			result.NativeModels = unavailableNativeModels("native_evidence_unavailable")
		} else {
			modelCtx, modelCancel := context.WithTimeout(context.Background(), 30*time.Second)
			result.NativeModels = observeNativeModels(modelCtx, target, lifecycle, privateSurfaces, bindings,
				retainedFacts.values, filter)
			modelCancel()
		}
	}
	result.Coverage = map[Verdict]int{}
	result.Verdict = combineVerdicts(result.TargetLifecycle.verdict(), result.PrivateSurfaces.verdict(),
		result.BrowserFixtures.verdict(), result.NativeModels.verdict(), result.TrialScratch.verdict())
	for _, summary := range result.Trials {
		result.Coverage[summary.Verdict]++
		result.Verdict = combineVerdicts(result.Verdict, summary.Verdict)
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
	retainedFacts *privateFactRetention,
	pendingPDF *pdfRunFinalization,
	pendingBrowser *browserRun,
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
	if report.beginTrialStaging(caseCtx, sessionID, item.Digest) != nil {
		trial.Verdict, trial.Code = Inconclusive, "trial_scratch_unavailable"
		return trial
	}
	// Recheck the running instance between trials before any new task is sent.
	probe, err := requestRuntimeProbe(caseCtx, target.config, target.serviceToken)
	if err != nil || probe != target.probe {
		trial.Verdict, trial.Code = Inconclusive, "target_changed"
		return trial
	}
	var domain *pdfConversation
	options := gatewayclient.Options{InspectFrame: func(data []byte) error {
		// The previous client's Close joins its inspector before the serial run
		// adds new facts. Late frames must also respect earlier trial canaries.
		return inspectNativeJSON(data, retainedFacts.values, roles.filter)
	}}
	var browser *browserConversation
	if item.Case.Browser != nil {
		name, _ := browserEvalProfile(target.config)
		if pendingBrowser.host == nil || name != item.Case.Browser.Profile ||
			!browserFixtureMatches(target.config, pendingBrowser.host) {
			trial.Verdict, trial.Code = Inconclusive, "browser_preparation_unavailable"
			return trial
		}
		item.Case.Driver.Facts, err = materializePrivateFacts(item.Case.Driver.Facts)
		if err != nil || !retainedFacts.add(item.Case.Driver.Facts) {
			clear(item.Case.Driver.Facts)
			trial.Verdict, trial.Code = Inconclusive, "private_fact_retention_limit"
			return trial
		}
		fixture, fixtureErr := pendingBrowser.host.newTrial(sessionID)
		if fixtureErr != nil {
			trial.Verdict, trial.Code = Inconclusive, "browser_fixture_unavailable"
			return trial
		}
		browser = newBrowserConversation(item.Case, fixture, target, sessionID)
		browser.questions.facts = retainedFacts.values
		options.ObserveMessage = browser.wire.observe
	}
	if item.Case.PDF != nil {
		prepared, prepareErr := preparePDFTrial(item, target.preset.IsolationRoot,
			target.preset.ExpectedWorkspace, sessionID, target.preset.ExpectedAgent)
		if prepareErr != nil {
			trial.Verdict, trial.Code = Inconclusive, "pdf_preparation_unavailable"
			return trial
		}
		domain = &pdfConversation{prepared: prepared, artifacts: &pdfArtifactCollector{
			config: target.config, python: runOptions.PDFOraclePython, retain: report.retainPDFArtifact,
		}, wire: &mediaWireObservation{workspace: target.preset.ExpectedWorkspace}}
		options.ObserveMessage = domain.wire.observe
		prepared.filter = roles.filter
		if !retainedFacts.add(prepared.item.Case.Driver.Facts) {
			_ = prepared.release(true)
			trial.Verdict, trial.Code = Inconclusive, "private_fact_retention_limit"
			return trial
		}
		if runOptions.StartTarget {
			if retainedFacts.documents == nil {
				retainedFacts.documents = make(map[string]bool)
			}
			retainedFacts.documents[item.Case.Fixtures[0].SHA256] = true
		}
		if censusErr := prepared.beginNativeCensus(caseCtx); errors.Is(censusErr, ErrPrivateEvidence) {
			_ = prepared.release(true)
			trial.Verdict, trial.Code = Fail, "private_native_census"
			return trial
		}
		if runOptions.StartTarget && !pendingPDF.canRetain(prepared) {
			_ = prepared.release(true)
			trial.Verdict, trial.Code = Inconclusive, "pdf_finalization_limit"
			return trial
		}
		item = prepared.item
	}
	client, err := gatewayclient.Connect(caseCtx, target.config, sessionID, options)
	if err != nil {
		if browser != nil {
			browser.fixture.seal()
		}
		if domain != nil {
			_ = domain.prepared.release(true)
		}
		trial.Verdict, trial.Code = Inconclusive, "gateway_unavailable"
		trial.DurationMS = time.Since(trial.StartedAt).Milliseconds()
		return trial
	}
	if browser != nil {
		trial = runDomainConversation(caseCtx, client, item, sessionID,
			target.preset.ExpectedAgent, target.preset.ExpectedWorkspace, roles, browser)
		browser.collectArtifact(caseCtx, &trial, report, client.Failure() == nil)
		pendingBrowser.retain(item.Case, trial, browser.fixture)
		pending := pendingBrowser.trials[trial.ID]
		pending.conversation = browser
		pendingBrowser.trials[trial.ID] = pending
		return trial
	}
	if domain != nil {
		trial = runDomainConversation(caseCtx, client, item, sessionID,
			target.preset.ExpectedAgent, target.preset.ExpectedWorkspace, roles, domain)
		// Conversation return has joined the reader, including in-flight media
		// observations that need not have entered the conversation queue.
		domain.concludeWire(&trial, client.Failure() == nil)
		if runOptions.StartTarget && trial.PDF != nil && trial.PDF.Settlement != nil &&
			trial.PDF.Artifact != nil && trial.PDF.Artifact.File != "" {
			retainedFacts.documents[trial.PDF.Settlement.ArtifactSHA256] = true
		}
		if runOptions.StartTarget {
			pendingPDF.retain(domain, trial)
		} else if domain.prepared.release(false) != nil {
			trial.Verdict = combineVerdicts(trial.Verdict, Inconclusive)
			trial.Code = "pdf_local_cleanup_failed"
		}
		return trial
	}
	if containsAll(item.Case.Requires, []string{"interactions"}) {
		questions := &questionConversation{
			task: item.Case.Task, home: target.preset.IsolationRoot,
			workspace: target.preset.ExpectedWorkspace, sessionID: sessionID,
			agent: target.preset.ExpectedAgent, started: started, filter: roles.filter,
			answerCheckID: questionAnswerCheckID(item.Case),
		}
		return runDomainConversation(caseCtx, client, item, sessionID,
			target.preset.ExpectedAgent, target.preset.ExpectedWorkspace, roles, questions)
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
