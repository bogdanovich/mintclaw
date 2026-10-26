package eval

import (
	"context"
	"strings"
)

type coreEvidenceRun struct {
	trials map[string]corePendingTrial
}

type corePendingTrial struct {
	item              Case
	session           string
	approvalKeySHA256 string
}

func (run *coreEvidenceRun) retain(item Case, trial Trial) {
	if run.trials == nil {
		run.trials = map[string]corePendingTrial{}
	}
	run.trials[trial.ID] = corePendingTrial{item: item, session: trial.SessionID}
	if trial.ApprovalDenied != nil {
		pending := run.trials[trial.ID]
		pending.approvalKeySHA256 = trial.ApprovalDenied.KeySHA256
		run.trials[trial.ID] = pending
	}
}

func (run *coreEvidenceRun) approvalKeyWitness(home string, previous *interactionKeyWitness) *interactionKeyWitness {
	for _, pending := range run.trials {
		if pending.item.ApprovalDenied == nil {
			continue
		}
		if !validDigest(pending.approvalKeySHA256) {
			return &interactionKeyWitness{}
		}
		witness := interactionKeyWitness{
			home:   home,
			name:   "workspace/state/interaction_hmac.key",
			sha256: pending.approvalKeySHA256,
		}
		if previous != nil && *previous != witness {
			return &interactionKeyWitness{}
		}
		previous = &witness
	}
	return previous
}

func deferredCoreEvidence(item Case) bool {
	return item.Steering != nil || item.ReadRecovery != nil || item.NativeStop != nil ||
		item.ApprovalDenied != nil || item.FinalDelivery != nil || item.SkillSelection != nil || item.Restart != nil
}

// One owner captures all bounded core observations after native stop and before
// any assessment/model call. A later trial's unknown budget invalidates earlier
// conversations too; successful visible finals alone cannot make them pass.
func (run *coreEvidenceRun) finish(
	ctx context.Context, target admittedTarget, writer *privateReport, report *RunReport,
	facts map[string]string, filter *strings.Replacer, assess func(Case, *Trial),
) error {
	type finalizedTrial struct {
		index int
		item  Case
		trial Trial
	}
	finalized := []finalizedTrial{}
	retained := 0
	for index := range report.Trials {
		summary := &report.Trials[index]
		pending, ok := run.trials[summary.ID]
		if !ok {
			continue
		}
		data, err := writer.readTrialCheckpoint(summary.File, pending.session)
		if err != nil {
			return ErrReport
		}
		retained += len(data)
		trial, decodeErr := decodeDocument[Trial](data, 8*1024*1024)
		clear(data)
		if ctx.Err() != nil || retained > maxRetainedPDFBytes || decodeErr != nil || trial.ID != summary.ID ||
			trial.CaseSHA256 != summary.CaseSHA256 || trial.SessionID != pending.session ||
			trial.CaseID != pending.item.ID || credentialJSONValue(trial, filter) || privateJSONValue(trial, facts) {
			return ErrReport
		}
		if trial.Steering != nil && trial.ExecutionVerdict == Pass {
			trial.Steering.Code = "owned_scope_unavailable"
			if trial.Isolation != nil && trial.Isolation.Peer.Steering != nil {
				trial.Isolation.Peer.Steering.Code = "owned_scope_unavailable"
			}
			if report.ActiveTool != nil && report.ActiveTool.valid(*report) &&
				report.ActiveTool.verdict(report.TargetLifecycle) == Pass &&
				observedOwnedProfile(report.TargetLifecycle, report.PrivateSurfaces) &&
				report.TargetBudget != nil && report.TargetBudget.verdict() == Pass {
				refreshSteeringScope(ctx, target, &trial, pending.item, facts, filter)
				if pending.item.Steering.Peer != nil && trial.Isolation != nil {
					peer := trial.Isolation.Peer.trial(trial)
					refreshSteeringScope(ctx, target, &peer, isolationPeerCase(pending.item), facts, filter)
					trial.Isolation.Peer = isolationScope(peer)
				}
			}
		}
		if trial.ReadRecovery != nil && trial.ExecutionVerdict == Pass {
			trial.ReadRecovery.Code = "owned_scope_unavailable"
			if readRecoveryOwnedReady(*report) {
				refreshReadRecoveryScope(ctx, target, &trial, facts, filter)
			}
		}
		if trial.NativeStop != nil && trial.ExecutionVerdict == Pass {
			if nativeStopOwnedReady(*report) {
				refreshNativeStopScope(ctx, target, &trial, facts, filter)
			} else {
				trial.NativeStop.Code = "owned_scope_unavailable"
			}
		}
		if trial.ApprovalDenied != nil && trial.ExecutionVerdict == Pass {
			if approvalDeniedOwnedReady(*report) {
				refreshApprovalDeniedScope(ctx, target, &trial, facts, filter)
			} else {
				trial.ApprovalDenied.Code = "owned_scope_unavailable"
			}
		}
		if trial.FinalDelivery != nil && trial.ExecutionVerdict == Pass {
			trial.FinalDelivery.Code = "owned_scope_unavailable"
			if readRecoveryOwnedReady(*report) {
				refreshFinalDeliveryScope(ctx, target, &trial, facts, filter)
			}
		}
		if trial.SkillSelection != nil && trial.ExecutionVerdict == Pass {
			trial.SkillSelection.Code = "owned_scope_unavailable"
			if readRecoveryOwnedReady(*report) {
				refreshSkillSelectionScope(ctx, target, &trial, pending.item, facts, filter)
			}
		}
		if trial.Restart != nil && trial.ExecutionVerdict == Pass {
			trial.Restart.Code = "owned_scope_unavailable"
			if targetGenerationsVerdict(*report) == Pass && len(report.TargetGenerations) >= 2 {
				refreshRestartScope(ctx, target, &trial)
			}
		}
		finalized = append(finalized, finalizedTrial{index: index, item: pending.item, trial: trial})
	}
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
		if writer.write(summary.File, trial) != nil {
			return ErrReport
		}
		delete(run.trials, trial.ID)
	}
	return nil
}

func refreshSteeringScope(
	ctx context.Context, target admittedTarget, trial *Trial, item Case,
	facts map[string]string, filter *strings.Replacer,
) {
	if trial.Steering == nil || trial.ExecutionVerdict != Pass {
		return
	}
	trial.Steering.Code = "post_stop_evidence_unavailable"
	history, historyErr := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
	rows, names, outboxErr := readSteeringOutbox(ctx, target)
	if historyErr != nil || outboxErr != nil || trial.Steering.Trace == "" ||
		len(trial.Steering.Guidance) != len(item.Steering.Updates) {
		return
	}
	trial.Steering.Metadata, trial.Steering.History = history.metadata, history.history
	trial.Steering.MetadataSHA256 = digest([]byte(history.metadata))
	trial.Steering.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	trial.Steering.Outbox, trial.Steering.OutboxNames = rows, names
	trial.Steering.Code = "observed"
}
