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
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type Adapter struct {
	projector  *frontend.Projector
	sessionKey string
}

// ProjectThreadMetadata maps the durable catalog descriptor into the
// transport-neutral, bounded frontend projection.
func ProjectThreadMetadata(projector *frontend.Projector, metadata thread.Metadata) error {
	if projector == nil {
		return fmt.Errorf("coding frontend projector is required")
	}
	projector.ThreadMetadataUpdated(frontend.ThreadMetadata{
		Title:       metadata.Title,
		Preview:     metadata.Preview,
		ProjectRoot: metadata.Project.ProjectRoot,
		CWD:         metadata.Project.InvocationCWD,
		Model:       metadata.Model,
		Provider:    metadata.Provider,
		UpdatedAt:   metadata.UpdatedAt,
	})
	return nil
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
			a.projector.ToolStarted(turnID, payload.ToolCallID, payload.Tool, argumentShape(payload.Arguments))
		}
	case runtimeevents.KindAgentToolExecEnd:
		payload, ok := event.Payload.(agent.ToolExecEndPayload)
		if ok {
			result := fmt.Sprintf(
				"result available (%d bytes for model, %d bytes for user)",
				payload.ForLLMLen,
				payload.ForUserLen,
			)
			a.projector.ToolCompleted(
				turnID,
				payload.ToolCallID,
				payload.Tool,
				result,
				payload.Duration,
				payload.IsError,
				projectWriteAudit(payload.WriteAudit),
			)
		}
	case runtimeevents.KindAgentToolExecSkipped:
		payload, ok := event.Payload.(agent.ToolExecSkippedPayload)
		if ok {
			a.projector.ToolCompleted(turnID, payload.ToolCallID, payload.Tool, "tool skipped", 0, true, nil)
		}
	case runtimeevents.KindAgentLLMRetry:
		payload, ok := event.Payload.(agent.LLMRetryPayload)
		if ok {
			a.projector.Warning(
				turnID,
				"",
				fmt.Sprintf(
					"model request retry %d/%d (%s)",
					payload.Attempt,
					payload.MaxRetries,
					safeToken(payload.Reason),
				),
			)
		}
	case runtimeevents.KindAgentLLMFallbackAttempt:
		payload, ok := event.Payload.(agent.LLMFallbackAttemptPayload)
		if ok {
			a.projector.Warning(
				turnID,
				"",
				fmt.Sprintf(
					"model fallback %d: %s/%s %s (%s)",
					payload.Attempt,
					safeToken(payload.Provider),
					safeToken(payload.Model),
					safeToken(payload.Status),
					safeToken(payload.Reason),
				),
			)
		}
	case runtimeevents.KindAgentContextCompress:
		payload, ok := event.Payload.(agent.ContextCompressPayload)
		if strings.TrimSpace(turnID) == "" || ok && payload.Reason == agent.ContextCompressReasonSummarize {
			a.projector.BackgroundCompactionCompleted()
		} else if ok {
			a.projector.CompactionCompleted(fmt.Sprintf("context compacted; %d tokens saved", payload.TokensSaved))
		} else {
			a.projector.CompactionCompleted("context compacted")
		}
	case runtimeevents.KindAgentWorkspaceSnapshot:
		payload, ok := event.Payload.(agent.WorkspaceSnapshotPayload)
		if ok {
			a.projector.WorkspaceUpdated(payload.Snapshot)
		}
	case runtimeevents.KindAgentInterruptReceived:
		a.projector.InterruptRequested()
	case runtimeevents.KindAgentError:
		payload, ok := event.Payload.(agent.ErrorPayload)
		if ok && strings.TrimSpace(payload.Stage) != "" {
			stage := safeToken(payload.Stage)
			status := "agent error during " + stage
			a.projector.Error(turnID, fmt.Sprintf("%s:error:%s", normalizeID(turnID), stage), status)
			a.projector.TurnFailed(status)
		} else {
			a.projector.Error(turnID, normalizeID(turnID)+":error", "agent error")
			a.projector.TurnFailed("agent error")
		}
	}
}

func projectWriteAudit(audit []toolshared.WriteAuditEntry) []frontend.WriteAudit {
	result := make([]frontend.WriteAudit, 0, len(audit))
	for _, entry := range audit {
		if !entry.Success {
			continue
		}
		result = append(result, frontend.WriteAudit{
			Kind: entry.Kind, Target: entry.Target, Action: entry.Action, Success: true,
		})
	}
	return result
}

func safeToken(value string) string {
	value = strings.TrimSpace(value)
	for _, r := range value {
		allowedPunctuation := r == '-' || r == '_' || r == '.' || r == '/' || r == ' '
		allowedLetterOrDigit := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !allowedPunctuation && !allowedLetterOrDigit {
			return "unknown"
		}
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func normalizeID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "current"
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
		a.projector.TurnInterrupted(turnID, "interrupted")
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
