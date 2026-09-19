package agent

import runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"

type agentRuntimeEventEmitter struct {
	events runtimeevents.Bus
}

func (e *agentRuntimeEventEmitter) emitEvent(kind runtimeevents.Kind, meta HookMeta, payload any) {
	clonedMeta := cloneHookMeta(meta)
	eventCtx := cloneTurnContext(clonedMeta.turnContext)
	safePayload := runtimeEventFanoutSafePayload(payload)
	evt := runtimeevents.Event{
		Kind:        kind,
		Source:      runtimeevents.Source{Component: "agent", Name: clonedMeta.AgentID},
		Scope:       runtimeScopeFromHookMeta(clonedMeta, eventCtx),
		Correlation: runtimeCorrelationFromHookMeta(clonedMeta),
		Severity:    runtimeSeverityForAgentEvent(kind, safePayload),
		Payload:     safePayload,
		Attrs:       runtimeAttrsFromHookMeta(clonedMeta),
	}

	e.publishRuntimeEvent(evt)
}

// runtimeEventFanoutSafePayload projects live-only document selectors before
// an event can reach any in-process subscriber. Callers may continue building
// payloads from exact live state; this boundary owns the privacy guarantee.
func runtimeEventFanoutSafePayload(payload any) any {
	switch value := payload.(type) {
	case TurnStartPayload:
		value.UserMessage = projectDocumentUserMessageForDurableBoundary(value.UserMessage)
		return value
	case *TurnStartPayload:
		if value == nil {
			return value
		}
		safe := *value
		safe.UserMessage = projectDocumentUserMessageForDurableBoundary(safe.UserMessage)
		return &safe
	case TurnEndPayload:
		value.UserMessage = projectDocumentUserMessageForDurableBoundary(value.UserMessage)
		return value
	case *TurnEndPayload:
		if value == nil {
			return value
		}
		safe := *value
		safe.UserMessage = projectDocumentUserMessageForDurableBoundary(safe.UserMessage)
		return &safe
	case InterruptReceivedPayload:
		return runtimeEventFanoutSafeInterrupt(value)
	case *InterruptReceivedPayload:
		if value == nil {
			return value
		}
		safe := runtimeEventFanoutSafeInterrupt(*value)
		return &safe
	default:
		return payload
	}
}

func runtimeEventFanoutSafeInterrupt(payload InterruptReceivedPayload) InterruptReceivedPayload {
	if messageMentionsLocalPDFPath(payload.CodingSteerText) {
		payload.DiagnosticContent = ""
	}
	payload.CodingSteerText = projectDocumentUserMessageForDurableBoundary(payload.CodingSteerText)
	return payload
}

func (e *agentRuntimeEventEmitter) publishRuntimeEvent(evt runtimeevents.Event) {
	if e == nil || e.events == nil {
		return
	}

	e.events.PublishNonBlocking(evt)
}
