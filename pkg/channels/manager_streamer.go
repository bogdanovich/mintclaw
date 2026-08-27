package channels

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

// GetStreamer implements bus.StreamDelegate.
// It checks if the named channel supports streaming and returns a Streamer.
func (m *Manager) GetStreamer(
	ctx context.Context,
	channelName, chatID, sessionKey, requestID string,
	traceScope runtimeevents.TraceScope,
) (bus.Streamer, bool) {
	return m.streamCoordinator().getStreamer(
		ctx, m, channelName, chatID, sessionKey, requestID, traceScope,
	)
}

func (m *Manager) streamSplitOnMarker() bool {
	return m.lifecycle.splitOnMarker()
}

func (m *Manager) streamResponseFooterEnabled() bool {
	return m.lifecycle.responseFooterEnabled()
}

func reasoningStreamerFrom(streamer bus.Streamer) bus.ReasoningStreamer {
	if reasoningStreamer, ok := streamer.(bus.ReasoningStreamer); ok {
		return reasoningStreamer
	}
	return nil
}

type modelNameStreamer interface {
	SetModelName(modelName string)
}

type defaultModelNameStreamer interface {
	SetDefaultModelName(defaultModelName string)
}

type agentIdentityStreamer interface {
	SetAgentID(agentID string)
}

func setStreamerModelName(streamer any, modelName string) {
	setter, ok := streamer.(modelNameStreamer)
	if !ok {
		return
	}
	setter.SetModelName(modelName)
}

func setStreamerDefaultModelName(streamer any, defaultModelName string) {
	setter, ok := streamer.(defaultModelNameStreamer)
	if !ok {
		return
	}
	setter.SetDefaultModelName(defaultModelName)
}

func setStreamerAgentID(streamer any, agentID string) {
	setter, ok := streamer.(agentIdentityStreamer)
	if !ok {
		return
	}
	setter.SetAgentID(agentID)
}

type turnUsageStreamer interface {
	SetTurnUsage(inputTokens, outputTokens int)
}

// setStreamerTurnUsage forwards real per-turn token usage to a streamer that
// supports it, transparently unwrapping the manager's streamer wrappers.
func setStreamerTurnUsage(streamer any, inputTokens, outputTokens int) {
	setter, ok := streamer.(turnUsageStreamer)
	if !ok {
		return
	}
	setter.SetTurnUsage(inputTokens, outputTokens)
}

type responseFooterStreamState struct {
	enabled          bool
	channel          string
	modelName        string
	defaultModelName string
	inputTokens      int
	outputTokens     int
}

func (s responseFooterStreamState) decorate(content string) string {
	if !s.enabled {
		return content
	}
	msg := bus.OutboundMessage{
		Content: content,
		Metadata: bus.OutboundMetadata{
			OutboundKind:      bus.OutboundKindFinal,
			ModelName:         s.modelName,
			DefaultModelName:  s.defaultModelName,
			UsageInputTokens:  s.inputTokens,
			UsageOutputTokens: s.outputTokens,
			UsageTotalTokens:  s.inputTokens + s.outputTokens,
		},
	}
	footer := outboundResponseFooter(msg)
	if footer == "" {
		return content
	}
	return appendOutboundResponseFooter(content, footer, s.channel)
}

// splitMarkerStreamer turns accumulated streaming text containing
// MessageSplitMarker into separate channel stream messages.
type splitMarkerStreamer struct {
	mu               sync.Mutex
	current          bus.Streamer
	reasoning        bus.ReasoningStreamer
	begin            func(context.Context) (bus.Streamer, error)
	completedParts   int
	finalized        bool
	onFinalize       func(context.Context, string)
	clearMarker      func()
	modelName        string
	defaultModelName string
	turnInputTokens  int
	turnOutputTokens int
	agentID          string
	footer           responseFooterStreamState
}

func (s *splitMarkerStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content)
}

func (s *splitMarkerStreamer) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

func (s *splitMarkerStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.finalizeLocked(ctx, content, usage); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content)
	return nil
}

func (s *splitMarkerStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reasoning == nil {
		return nil
	}
	setStreamerModelName(s.reasoning, s.modelName)
	return s.reasoning.UpdateReasoning(ctx, content)
}

func (s *splitMarkerStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reasoning == nil {
		return nil
	}
	setStreamerModelName(s.reasoning, s.modelName)
	return s.reasoning.FinalizeReasoning(ctx, content)
}

func (s *splitMarkerStreamer) SetModelName(modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelName = strings.TrimSpace(modelName)
	s.footer.modelName = s.modelName
	setStreamerModelName(s.current, s.modelName)
	setStreamerModelName(s.reasoning, s.modelName)
}

func (s *splitMarkerStreamer) SetDefaultModelName(defaultModelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultModelName = strings.TrimSpace(defaultModelName)
	s.footer.defaultModelName = s.defaultModelName
	setStreamerDefaultModelName(s.current, s.defaultModelName)
	setStreamerDefaultModelName(s.reasoning, s.defaultModelName)
}

func (s *splitMarkerStreamer) SetAgentID(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentID = strings.TrimSpace(agentID)
	setStreamerAgentID(s.current, s.agentID)
	setStreamerAgentID(s.reasoning, s.agentID)
}

func (s *splitMarkerStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnInputTokens = inputTokens
	s.turnOutputTokens = outputTokens
	s.footer.inputTokens = inputTokens
	s.footer.outputTokens = outputTokens
	setStreamerTurnUsage(s.current, s.turnInputTokens, s.turnOutputTokens)
}

func (s *splitMarkerStreamer) Cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.Cancel(ctx)
	}
}

func (s *splitMarkerStreamer) ClearFinalizedStreamMarker() {
	if s.clearMarker != nil {
		s.clearMarker()
	}
}

func (s *splitMarkerStreamer) updateLocked(ctx context.Context, content string) error {
	parts := strings.Split(content, MessageSplitMarker)
	completedLimit := len(parts) - 1
	active := strings.TrimSpace(parts[len(parts)-1])
	for active == "" && completedLimit > 0 && strings.TrimSpace(parts[completedLimit]) == "" {
		completedLimit--
	}
	if err := s.finalizeCompletedPartsLocked(ctx, parts, completedLimit, nil, false); err != nil {
		return err
	}
	if active == "" {
		return nil
	}
	if err := s.ensureCurrentLocked(ctx); err != nil {
		return err
	}
	return s.current.Update(ctx, active)
}

func (s *splitMarkerStreamer) finalizeLocked(ctx context.Context, content string, usage *bus.ContextUsage) error {
	parts := strings.Split(content, MessageSplitMarker)
	return s.finalizeCompletedPartsLocked(ctx, parts, len(parts), usage, true)
}

func (s *splitMarkerStreamer) finalizeCompletedPartsLocked(
	ctx context.Context,
	parts []string,
	limit int,
	usage *bus.ContextUsage,
	decorateFinal bool,
) error {
	finalPart := -1
	if decorateFinal {
		for idx := s.completedParts; idx < limit; idx++ {
			if strings.TrimSpace(parts[idx]) != "" {
				finalPart = idx
			}
		}
	}
	for s.completedParts < limit {
		content := strings.TrimSpace(parts[s.completedParts])
		isFinalPart := s.completedParts == finalPart
		if content != "" {
			if err := s.ensureCurrentLocked(ctx); err != nil {
				return err
			}
			if isFinalPart {
				content = s.footer.decorate(content)
			}
			if isFinalPart && usage != nil {
				if contextStreamer, ok := s.current.(bus.ContextUsageStreamer); ok {
					if err := contextStreamer.FinalizeWithContext(ctx, content, usage); err != nil {
						return err
					}
				} else if err := s.current.Finalize(ctx, content); err != nil {
					return err
				}
			} else if isFinalPart {
				if err := s.current.Finalize(ctx, content); err != nil {
					return err
				}
			} else if segmentStreamer, ok := s.current.(interface {
				FinalizeSegment(context.Context, string) error
			}); ok {
				if err := segmentStreamer.FinalizeSegment(ctx, content); err != nil {
					return err
				}
			} else if err := s.current.Finalize(ctx, content); err != nil {
				return err
			}
			s.current = nil
		}
		s.completedParts++
	}
	return nil
}

func (s *splitMarkerStreamer) ensureCurrentLocked(ctx context.Context) error {
	if s.current != nil {
		return nil
	}
	if s.begin == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	streamer, err := s.begin(ctx)
	if err != nil {
		return err
	}
	s.current = streamer
	setStreamerModelName(s.current, s.modelName)
	setStreamerDefaultModelName(s.current, s.defaultModelName)
	setStreamerTurnUsage(s.current, s.turnInputTokens, s.turnOutputTokens)
	setStreamerAgentID(s.current, s.agentID)
	return nil
}

func (s *splitMarkerStreamer) runFinalizeHook(ctx context.Context, content string) {
	if s.finalized {
		return
	}
	s.finalized = true
	if s.onFinalize != nil {
		s.onFinalize(ctx, content)
	}
}

// finalizeHookStreamer wraps a bus.Streamer to run a hook on Finalize.
type finalizeHookStreamer struct {
	bus.Streamer
	onFinalize  func(context.Context, string)
	clearMarker func()
	footer      responseFooterStreamState
}

func (s *finalizeHookStreamer) Finalize(ctx context.Context, content string) error {
	content = s.footer.decorate(content)
	if err := s.Streamer.Finalize(ctx, content); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content)
	return nil
}

func (s *finalizeHookStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	content = s.footer.decorate(content)
	if streamer, ok := s.Streamer.(bus.ContextUsageStreamer); ok {
		if err := streamer.FinalizeWithContext(ctx, content, usage); err != nil {
			return err
		}
	} else if err := s.Streamer.Finalize(ctx, content); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content)
	return nil
}

func (s *finalizeHookStreamer) UpdateReasoning(ctx context.Context, content string) error {
	if streamer, ok := s.Streamer.(bus.ReasoningStreamer); ok {
		return streamer.UpdateReasoning(ctx, content)
	}
	return nil
}

func (s *finalizeHookStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	if streamer, ok := s.Streamer.(bus.ReasoningStreamer); ok {
		return streamer.FinalizeReasoning(ctx, content)
	}
	return nil
}

func (s *finalizeHookStreamer) SetModelName(modelName string) {
	s.footer.modelName = strings.TrimSpace(modelName)
	setStreamerModelName(s.Streamer, s.footer.modelName)
}

func (s *finalizeHookStreamer) SetDefaultModelName(defaultModelName string) {
	s.footer.defaultModelName = strings.TrimSpace(defaultModelName)
	setStreamerDefaultModelName(s.Streamer, s.footer.defaultModelName)
}

func (s *finalizeHookStreamer) SetAgentID(agentID string) {
	setStreamerAgentID(s.Streamer, agentID)
}

func (s *finalizeHookStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	s.footer.inputTokens = inputTokens
	s.footer.outputTokens = outputTokens
	setStreamerTurnUsage(s.Streamer, inputTokens, outputTokens)
}

func (s *finalizeHookStreamer) runFinalizeHook(ctx context.Context, content string) {
	if s.onFinalize != nil {
		s.onFinalize(ctx, content)
	}
}

func (s *finalizeHookStreamer) ClearFinalizedStreamMarker() {
	if s.clearMarker != nil {
		s.clearMarker()
	}
}
