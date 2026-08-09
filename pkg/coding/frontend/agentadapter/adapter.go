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
	projector    *frontend.Projector
	subscription runtimeevents.Subscription
}

func Subscribe(
	ctx context.Context,
	channel runtimeevents.EventChannel,
	projector *frontend.Projector,
	sessionKey string,
) (*Adapter, error) {
	if channel == nil {
		return nil, fmt.Errorf("coding frontend runtime event channel is required")
	}
	if projector == nil {
		return nil, fmt.Errorf("coding frontend projector is required")
	}
	adapter := &Adapter{projector: projector}
	filtered := channel.Source("agent")
	if strings.TrimSpace(sessionKey) != "" {
		filtered = filtered.Scope(runtimeevents.ScopeFilter{SessionKey: sessionKey})
	}
	subscription, err := filtered.Subscribe(ctx, runtimeevents.SubscribeOptions{
		Name:         "coding-frontend-projector",
		Buffer:       128,
		Concurrency:  runtimeevents.Locked,
		Backpressure: runtimeevents.DropOldest,
	}, adapter.handle)
	if err != nil {
		return nil, err
	}
	adapter.subscription = subscription
	return adapter, nil
}

func (a *Adapter) Close() error {
	if a == nil || a.subscription == nil {
		return nil
	}
	return a.subscription.Close()
}

func (a *Adapter) handle(_ context.Context, event runtimeevents.Event) error {
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
	return nil
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
