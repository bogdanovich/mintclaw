package frontend

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

// StreamDelegate projects the existing accumulated-content streaming seam.
// It does not alter provider or turn behavior and can be installed on the
// dedicated coding message bus by the future coding runtime.
type StreamDelegate struct {
	projector *Projector
	threadID  string
}

var _ bus.StreamDelegate = (*StreamDelegate)(nil)

func NewStreamDelegate(projector *Projector, threadID string) *StreamDelegate {
	return &StreamDelegate{projector: projector, threadID: strings.TrimSpace(threadID)}
}

func (d *StreamDelegate) GetStreamer(
	_ context.Context,
	_, _, sessionKey, _ string,
	traceScope runtimeevents.TraceScope,
) (bus.Streamer, bool) {
	if d == nil || d.projector == nil {
		return nil, false
	}
	if d.threadID != "" && strings.TrimSpace(sessionKey) != d.threadID {
		return nil, false
	}
	turnID := strings.TrimSpace(traceScope.TurnID)
	if turnID == "" {
		return nil, false
	}
	return &projectedStream{projector: d.projector, turnID: turnID}, true
}

type projectedStream struct {
	projector *Projector
	turnID    string
}

var (
	_ bus.Streamer             = (*projectedStream)(nil)
	_ bus.ReasoningStreamer    = (*projectedStream)(nil)
	_ bus.ContextUsageStreamer = (*projectedStream)(nil)
)

func (s *projectedStream) Update(_ context.Context, content string) error {
	s.projector.AssistantAccumulated(s.turnID, content, false)
	return nil
}

func (s *projectedStream) Finalize(_ context.Context, content string) error {
	s.projector.AssistantAccumulated(s.turnID, content, true)
	return nil
}

func (s *projectedStream) FinalizeWithContext(
	ctx context.Context,
	content string,
	usage *bus.ContextUsage,
) error {
	if err := s.Finalize(ctx, content); err != nil {
		return err
	}
	if usage != nil {
		s.projector.ContextUsage(usage.UsedTokens, usage.TotalTokens)
	}
	return nil
}

func (s *projectedStream) UpdateReasoning(_ context.Context, content string) error {
	s.projector.ReasoningAccumulated(s.turnID, content, false)
	return nil
}

func (s *projectedStream) FinalizeReasoning(_ context.Context, content string) error {
	s.projector.ReasoningAccumulated(s.turnID, content, true)
	return nil
}

func (s *projectedStream) Cancel(context.Context) {
	s.projector.TurnInterrupted(s.turnID, "stream canceled")
}
