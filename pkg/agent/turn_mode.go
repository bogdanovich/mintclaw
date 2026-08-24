package agent

// turnMode selects one admitted execution contract. The constructor owns the
// fixed behavior for each entrypoint so callers do not rebuild boolean
// combinations from historical defaults.
type turnMode uint8

const (
	turnModeUnspecified turnMode = iota
	turnModeInbound
	turnModeCoding
	turnModeScheduled
	turnModeHeartbeat
	turnModeSystem
	turnModeAsyncCompletion
	turnModeRecovery
	turnModeSteering
	turnModeInteractionContinuation
	turnModeChild
)

func newTurnSpec(mode turnMode, dispatch DispatchRequest, binding effectiveModelBinding) turnSpec {
	spec := turnSpec{mode: mode, Dispatch: dispatch, ModelBinding: binding}
	switch mode {
	case turnModeInbound:
		spec.DefaultResponse = defaultResponse
		spec.EnableSummary = true
		spec.ExpectFinalDelivery = true
		spec.AllowInterimMintClawPublish = true
	case turnModeCoding:
		spec.DefaultResponse = defaultResponse
		spec.ExpectFinalDelivery = true
	case turnModeScheduled, turnModeHeartbeat:
		spec.DefaultResponse = defaultResponse
		spec.SuppressToolFeedback = true
		spec.NoHistory = true
	case turnModeSystem:
		spec.DefaultResponse = "Background task completed."
	case turnModeAsyncCompletion:
		spec.SuppressToolFeedback = true
		spec.NoHistory = true
	case turnModeRecovery:
		spec.DefaultResponse = defaultResponse
		spec.EnableSummary = true
		spec.SendResponse = true
		spec.AllowInterimMintClawPublish = true
	case turnModeSteering:
		spec.DefaultResponse = defaultResponse
		spec.EnableSummary = true
	case turnModeInteractionContinuation:
		spec.DefaultResponse = defaultResponse
		spec.EnableSummary = true
		spec.AllowInterimMintClawPublish = true
	case turnModeChild:
	default:
		panic("unsupported turn mode")
	}
	return spec
}

func (m turnMode) skipsInitialSteeringPoll() bool {
	return m == turnModeSteering || m == turnModeInteractionContinuation || m == turnModeChild
}
