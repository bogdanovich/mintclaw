package eval

import (
	"context"
	"strings"
)

type steeringRun struct {
	trials map[string]steeringPendingTrial
}

type steeringPendingTrial struct {
	item    Case
	session string
}

func (run *steeringRun) retain(item Case, trial Trial) {
	if run.trials == nil {
		run.trials = map[string]steeringPendingTrial{}
	}
	run.trials[trial.ID] = steeringPendingTrial{item: item, session: trial.SessionID}
}

// Re-read canonical history and the full outbox census only after native stop.
// Late duplicate delivery/replay cannot hide behind a successful first final.
// Finish every bounded observation before allowing any assessment/model call.
func (run *steeringRun) finish(
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
			if report.ActiveTool != nil && report.ActiveTool.valid(*report) &&
				report.ActiveTool.verdict(report.TargetLifecycle) == Pass &&
				observedOwnedProfile(report.TargetLifecycle, report.PrivateSurfaces) &&
				report.TargetBudget != nil && report.TargetBudget.verdict() == Pass {
				history, historyErr := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
					target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
				rows, names, outboxErr := readSteeringOutbox(ctx, target)
				if historyErr == nil && outboxErr == nil && trial.Steering.Trace != "" &&
					len(trial.Steering.Guidance) == len(pending.item.Steering.Updates) {
					trial.Steering.Metadata, trial.Steering.History = history.metadata, history.history
					trial.Steering.MetadataSHA256 = digest([]byte(history.metadata))
					trial.Steering.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
					trial.Steering.Outbox, trial.Steering.OutboxNames = rows, names
					trial.Steering.Code = "observed"
				} else {
					trial.Steering.Code = "post_stop_evidence_unavailable"
				}
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
