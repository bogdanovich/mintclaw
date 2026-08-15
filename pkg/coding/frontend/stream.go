package frontend

import (
	"context"
	"strings"
	"sync"

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
	return &projectedStream{
		projector: d.projector,
		turnID:    turnID,
		baseline:  d.projector.captureStreamBaseline(turnID),
	}, true
}

type projectedStream struct {
	projector *Projector
	turnID    string
	baseline  streamBaseline
	mu        sync.Mutex
	owned     streamOwnedEntries
}

var (
	_ bus.Streamer             = (*projectedStream)(nil)
	_ bus.ReasoningStreamer    = (*projectedStream)(nil)
	_ bus.ContextUsageStreamer = (*projectedStream)(nil)
)

func (s *projectedStream) Update(ctx context.Context, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.projector == nil {
		return nil
	}
	_, owned := s.projector.upsertStreamEntry(DeltaAssistant, s.turnID, EntryAssistant, content, false)
	s.recordOwned(owned)
	return nil
}

func (s *projectedStream) Finalize(ctx context.Context, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.projector == nil {
		return nil
	}
	_, owned := s.projector.upsertStreamEntry(DeltaAssistant, s.turnID, EntryAssistant, content, true)
	s.recordOwned(owned)
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

func (s *projectedStream) UpdateReasoning(ctx context.Context, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.projector == nil {
		return nil
	}
	_, owned := s.projector.upsertStreamEntry(DeltaReasoning, s.turnID, EntryReasoning, content, false)
	s.recordOwned(owned)
	return nil
}

func (s *projectedStream) FinalizeReasoning(ctx context.Context, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.projector == nil {
		return nil
	}
	_, owned := s.projector.upsertStreamEntry(DeltaReasoning, s.turnID, EntryReasoning, content, true)
	s.recordOwned(owned)
	return nil
}

// Cancel discards provisional content from this provider attempt. Turn
// lifecycle remains authoritative in the runtime event adapter, which can
// distinguish a pre-visible fallback from an actual interrupted turn.
func (s *projectedStream) Cancel(context.Context) {
	if s == nil || s.projector == nil {
		return
	}
	s.mu.Lock()
	owned := make(streamOwnedEntries, len(s.owned))
	for id, entry := range s.owned {
		owned[id] = entry
	}
	s.mu.Unlock()
	s.projector.discardOwnedStream(s.turnID, s.baseline, owned)
}

func (s *projectedStream) recordOwned(owned streamOwnedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owned == nil {
		s.owned = make(streamOwnedEntries)
	}
	s.owned[owned.entry.ID] = owned
}
