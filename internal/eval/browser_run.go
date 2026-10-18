package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

// This receipt qualifies the owned HTTP listener only, never a native browser,
// an action grant, artifact delivery, or whole-runtime quiescence.
type BrowserFixtureHostEvidence struct {
	Scope        string `json:"scope"`
	Code         string `json:"code"`
	OriginSHA256 string `json:"origin_sha256"`
	Trials       int    `json:"trials"`
}

func (receipt *BrowserFixtureHostEvidence) valid() bool {
	return receipt == nil || receipt.Scope == "owned_http_fixture_host_v1" && validDigest(receipt.OriginSHA256) &&
		receipt.Trials >= 0 && receipt.Trials <= MaxCorpusCases &&
		(receipt.Code == "closed" || receipt.Code == "close_failed" || receipt.Code == "target_exit_unverified")
}

func (receipt *BrowserFixtureHostEvidence) verdict() Verdict {
	if !receipt.valid() || receipt != nil && receipt.Code != "closed" {
		return Inconclusive
	}
	return Pass
}

type BrowserTrialEvidence struct {
	Version         int                      `json:"version"`
	Scope           string                   `json:"scope"`
	Fixture         *browserFixtureOracle    `json:"fixture,omitempty"`
	NativeAuthority string                   `json:"native_authority"`
	NativeCleanup   string                   `json:"native_cleanup"`
	Read            *BrowserReadEvidence     `json:"read,omitempty"`
	Download        *BrowserDownloadEvidence `json:"download,omitempty"`
}

type browserRun struct {
	host    *browserFixtureHost
	trials  map[string]browserPendingTrial
	receipt *BrowserFixtureHostEvidence
}

type browserPendingTrial struct {
	item      Case
	sessionID string
	fixture   *browserFixture
}

func (run *browserRun) retain(item Case, trial Trial, fixture *browserFixture) {
	fixture.seal()
	if run.trials == nil {
		run.trials = make(map[string]browserPendingTrial)
	}
	run.trials[trial.ID] = browserPendingTrial{item: item, sessionID: trial.SessionID, fixture: fixture}
}

func (run *browserRun) close(target *TargetLifecycle) *BrowserFixtureHostEvidence {
	if run.host == nil || run.receipt != nil {
		return run.receipt
	}
	run.receipt = &BrowserFixtureHostEvidence{
		Scope: "owned_http_fixture_host_v1", Code: "closed", OriginSHA256: digest([]byte(run.host.origin)),
	}
	if run.host.close() != nil {
		run.receipt.Code = "close_failed"
	} else if target == nil || !target.ExitObserved {
		run.receipt.Code = "target_exit_unverified"
	}
	run.host.mu.Lock()
	run.receipt.Trials = len(run.host.trials)
	run.host.mu.Unlock()
	return run.receipt
}

// A fixture predicate cannot promote native checks. Capture native authority
// independently after shutdown; only the finalized read contract can judge it.
func (run *browserRun) finish(
	ctx context.Context, target admittedTarget, writer *privateReport, report *RunReport,
	facts map[string]string, filter *strings.Replacer, assess func(Case, *Trial),
) error {
	if len(run.trials) == 0 {
		return nil
	}
	observed, observationErr := observeBrowserNative(ctx, target.preset.ExpectedWorkspace, facts, filter)
	var transfers browserTransferSnapshot
	var transferErr error
	for _, pending := range run.trials {
		if pending.item.Browser.Scenario == "artifact" {
			transfers, transferErr = observeBrowserTransfers(ctx, target.preset.ExpectedWorkspace, facts, filter)
			break
		}
	}
	temporaryEmpty := browserTemporaryRootsEmpty(target.preset.IsolationRoot)
	type finalizedTrial struct {
		index int
		item  Case
		trial Trial
	}
	finalized := []finalizedTrial{}
	retainedBytes := 0
	for index := range report.Trials {
		summary := &report.Trials[index]
		pending, ok := run.trials[summary.ID]
		if !ok {
			continue
		}
		data, err := readPrivateReportFile(writer.root, "pending-"+summary.File)
		if err != nil {
			return ErrReport
		}
		trial, err := decodeDocument[Trial](data, 8*1024*1024)
		retainedBytes += len(data)
		clear(data)
		if ctx.Err() != nil || retainedBytes > maxRetainedPDFBytes || err != nil ||
			trial.ID != summary.ID || trial.CaseID != pending.item.ID ||
			trial.CaseSHA256 != summary.CaseSHA256 || trial.SessionID != pending.sessionID ||
			trial.Repetition != summary.Repetition || trial.DurationMS != summary.DurationMS ||
			credentialJSONValue(trial, filter) || privateJSONValue(trial, facts) {
			return ErrReport
		}
		oracle, err := inspectBrowserFixture(pending.item, pending.fixture, trial.Events, nil)
		trial.Browser = &BrowserTrialEvidence{
			Version:         Version,
			Scope:           "owned_fixture_execution_v1",
			NativeAuthority: "unavailable",
			NativeCleanup:   "unavailable",
		}
		if err == nil {
			trial.Browser.Fixture = &oracle
		}
		if observationErr == nil && pending.item.Browser.Scenario == "read" {
			proof := inspectBrowserRead(trial, target, pending.fixture, observed, temporaryEmpty)
			trial.Browser.Read = proof
			if proof.Code == "native_post_stop_observed" {
				trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
			}
		}
		if observationErr == nil && pending.item.Browser.Scenario == "artifact" {
			trial.Browser.Download = inspectBrowserDownload(trial, target, observed, transfers)
		}
		trial.EvidenceEligible = false
		for checkIndex, check := range pending.item.Checks {
			if checkIndex >= len(trial.Checks) {
				return ErrReport
			}
			result := &trial.Checks[checkIndex]
			switch check.Kind {
			case "browser_state", "browser_no_write":
				if err == nil && (oracle.Effects == Fail || check.Kind == "browser_state" && oracle.Read == Fail) {
					*result = CheckResult{
						ID:          check.ID,
						Verdict:     Fail,
						Code:        "fixture_contradiction",
						EvidenceIDs: []string{"browser.fixture"},
					}
				}
			case "private_surfaces":
				if report.PrivateSurfaces != nil && report.PrivateSurfaces.LeakObserved ||
					errors.Is(observationErr, ErrPrivateEvidence) || errors.Is(transferErr, ErrPrivateEvidence) {
					*result = CheckResult{
						ID:          check.ID,
						Verdict:     Fail,
						Code:        "private_profile_evidence",
						EvidenceIDs: []string{"run.private_surfaces"},
					}
				}
			case "browser_authority":
				if trial.Browser.Read != nil && trial.Browser.Read.ForbiddenActionAccepted {
					*result = CheckResult{
						ID: check.ID, Verdict: Fail,
						Code: "native_read_scope_mismatch", EvidenceIDs: []string{"browser.read"},
					}
				}
			}
			trial.Verdict = combineVerdicts(trial.Verdict, result.Verdict)
			if result.Verdict == Fail {
				trial.Code = "hard_check_failed"
			}
		}
		finalized = append(finalized, finalizedTrial{index: index, item: pending.item, trial: trial})
	}
	// All native/profile observations finish before any judge call. Judging has
	// the suite budget and cannot consume another trial's observation budget.
	for _, final := range finalized {
		summary := &report.Trials[final.index]
		trial := final.trial
		if assess != nil {
			assess(final.item, &trial)
		}
		if credentialJSONValue(trial, filter) || privateJSONValue(trial, facts) {
			return ErrReport
		}
		summary.Verdict, summary.Code, summary.DurationMS = trial.Verdict, trial.Code, trial.DurationMS
		if writer.write(summary.File, trial) != nil || writer.root.Remove("pending-"+summary.File) != nil {
			return ErrReport
		}
		delete(run.trials, trial.ID)
	}
	return nil
}

// Fixture failure interrupts future work and stops only the command handle we
// own. It never treats a socket close as proof that remote work was canceled.
func watchBrowserFixture(
	ctx context.Context, cfg *config.Config, host *browserFixtureHost, child *ownedGateway, interrupt func(),
) func() {
	if host == nil || child == nil {
		return func() {}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-watchCtx.Done():
			return
		case <-host.done:
		}
		if !browserFixtureMatches(cfg, host) {
			host.mu.Lock()
			failed := host.failed
			host.mu.Unlock()
			if failed {
				interrupt()
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer stopCancel()
				child.stopAndWait(stopCtx)
			}
		}
	}()
	return func() { cancel(); <-done }
}
