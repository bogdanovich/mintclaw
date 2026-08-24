package agent

import runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"

type turnAbortController struct {
	events runtimeEventEmitter
}

func (c *turnAbortController) abortTurn(ts *turnState) (turnResult, error) {
	if ts == nil {
		return turnResult{status: TurnEndStatusAborted}, nil
	}
	ts.setPhase(TurnPhaseAborted)
	if !ts.opts.NoHistory {
		_, err := ts.restoreSessionBeforeToolExecution()
		if err != nil {
			c.emitEvent(
				runtimeevents.KindAgentError,
				ts.eventMeta("abortTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_restore",
					Message: err.Error(),
				},
			)
			return turnResult{}, err
		}
	}
	return turnResult{status: TurnEndStatusAborted}, nil
}

func (c *turnAbortController) emitEvent(kind runtimeevents.Kind, meta HookMeta, payload any) {
	if c == nil || c.events == nil {
		return
	}
	c.events.emitEvent(kind, meta, payload)
}
