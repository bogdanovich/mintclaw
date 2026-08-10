// Package agentadapter projects stable observations from the shared agent
// runtime into the coding frontend protocol.
package agentadapter

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

type Adapter struct {
	projector  *frontend.Projector
	sessionKey string
}

// WrapBus synchronously projects coding lifecycle observations before
// forwarding them to the ordinary runtime bus. The projector does no I/O and
// its frontend watches are non-blocking, so this preserves event order without
// making a lossy event-bus subscription authoritative.
func WrapBus(
	delegate runtimeevents.Bus,
	projector *frontend.Projector,
	sessionKey string,
) (runtimeevents.Bus, error) {
	if delegate == nil {
		return nil, fmt.Errorf("coding frontend runtime event bus is required")
	}
	if projector == nil {
		return nil, fmt.Errorf("coding frontend projector is required")
	}
	return &projectingBus{
		delegate: delegate,
		adapter: &Adapter{
			projector:  projector,
			sessionKey: strings.TrimSpace(sessionKey),
		},
	}, nil
}

type projectingBus struct {
	delegate runtimeevents.Bus
	adapter  *Adapter
}

var _ runtimeevents.Bus = (*projectingBus)(nil)

func (b *projectingBus) Publish(ctx context.Context, event runtimeevents.Event) runtimeevents.PublishResult {
	b.adapter.project(event)
	return b.delegate.Publish(ctx, event)
}

func (b *projectingBus) PublishNonBlocking(event runtimeevents.Event) runtimeevents.PublishResult {
	b.adapter.project(event)
	return b.delegate.PublishNonBlocking(event)
}

func (b *projectingBus) Channel() runtimeevents.EventChannel {
	return b.delegate.Channel()
}

func (b *projectingBus) Close() error {
	return b.delegate.Close()
}

func (b *projectingBus) Stats() runtimeevents.Stats {
	return b.delegate.Stats()
}

func (a *Adapter) project(event runtimeevents.Event) {
	if a == nil || a.projector == nil || event.Source.Component != "agent" {
		return
	}
	if a.sessionKey != "" && event.Scope.SessionKey != a.sessionKey {
		return
	}
	turnID := event.Scope.TurnID
	switch event.Kind {
	case runtimeevents.KindAgentTurnStart:
		payload, ok := event.Payload.(agent.TurnStartPayload)
		if ok {
			a.projector.TurnStarted(turnID, payload.UserMessage)
		} else {
			a.projector.TurnStarted(turnID, "")
		}
	case runtimeevents.KindAgentTurnEnd:
		a.projectTurnEnd(turnID, event.Payload)
	case runtimeevents.KindAgentToolExecStart:
		payload, ok := event.Payload.(agent.ToolExecStartPayload)
		if ok {
			a.projector.ToolStarted(payload.ToolCallID, payload.Tool, argumentShape(payload.Arguments))
		}
	case runtimeevents.KindAgentToolExecEnd:
		payload, ok := event.Payload.(agent.ToolExecEndPayload)
		if ok {
			result := fmt.Sprintf(
				"result available (%d bytes for model, %d bytes for user)",
				payload.ForLLMLen,
				payload.ForUserLen,
			)
			a.projector.ToolCompleted(payload.ToolCallID, payload.Tool, result, payload.Duration, payload.IsError)
		}
	case runtimeevents.KindAgentToolExecSkipped:
		payload, ok := event.Payload.(agent.ToolExecSkippedPayload)
		if ok {
			a.projector.ToolCompleted(payload.ToolCallID, payload.Tool, payload.Reason, 0, true)
		}
	case runtimeevents.KindAgentContextCompress:
		payload, ok := event.Payload.(agent.ContextCompressPayload)
		if ok {
			a.projector.CompactionCompleted(fmt.Sprintf("context compacted; %d tokens saved", payload.TokensSaved))
		} else {
			a.projector.CompactionCompleted("context compacted")
		}
	case runtimeevents.KindAgentInterruptReceived:
		a.projector.InterruptRequested()
	case runtimeevents.KindAgentError:
		a.projector.TurnFailed("agent error")
	}
}

func (a *Adapter) projectTurnEnd(turnID string, value any) {
	payload, ok := value.(agent.TurnEndPayload)
	if !ok {
		a.projector.TurnCompleted("turn ended")
		return
	}
	if payload.FinalContent != "" {
		a.projector.AssistantAccumulated(turnID, payload.FinalContent, true)
	}
	switch payload.Status {
	case agent.TurnEndStatusCompleted:
		a.projector.TurnCompleted("completed")
	case agent.TurnEndStatusAborted:
		a.projector.TurnInterrupted("interrupted")
	case agent.TurnEndStatusError:
		a.projector.TurnFailed("turn failed")
	case agent.TurnEndStatusSuspended:
		a.projector.TurnCompleted("waiting for input")
	default:
		a.projector.TurnCompleted("turn ended")
	}
}

func argumentShape(arguments map[string]any) string {
	if len(arguments) == 0 {
		return ""
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return "fields: " + strings.Join(keys, ", ")
}
