package mintclaw

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

// mintclawConn represents a single WebSocket connection.

type mintclawStreamer struct {
	channel          *MintClawChannel
	chatID           string
	sessionKey       string
	requestID        string
	traceScope       runtimeevents.TraceScope
	agentID          string
	modelName        string
	turnInputTokens  int
	turnOutputTokens int
	messageID        string
	reasoningID      string
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	lastContent      string
	reasoningLastLen int
	reasoningLastAt  time.Time
	reasoningContent string
	mu               sync.Mutex
}

func (s *mintclawStreamer) SetModelName(modelName string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelName = strings.TrimSpace(modelName)
}

func (s *mintclawStreamer) SetAgentID(agentID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentID = strings.TrimSpace(agentID)
}

// SetTurnUsage records the real per-turn LLM token usage to emit on finalize.
func (s *mintclawStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnInputTokens = inputTokens
	s.turnOutputTokens = outputTokens
}

func (s *mintclawStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content, false, nil)
}

func (s *mintclawStreamer) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

// FinalizeSegment flushes a completed split-stream segment without marking it
// as the terminal response for the owning agent turn.
func (s *mintclawStreamer) FinalizeSegment(ctx context.Context, content string) error {
	if s == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	return s.sendLockedWithFinal(ctx, content, false, nil)
}

func (s *mintclawStreamer) FinalizeWithContext(
	ctx context.Context,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content, true, contextUsage)
}

func (s *mintclawStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateReasoningLocked(ctx, content, false)
}

func (s *mintclawStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateReasoningLocked(ctx, content, true)
}

func (s *mintclawStreamer) Cancel(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel == nil || s.messageID == "" {
		if s.channel != nil && s.reasoningID != "" {
			_ = s.channel.DeleteMessage(ctx, s.chatID, s.reasoningID)
			s.reasoningID = ""
		}
		return
	}
	_ = s.channel.DeleteMessage(ctx, s.chatID, s.messageID)
	s.messageID = ""
	if s.reasoningID != "" {
		_ = s.channel.DeleteMessage(ctx, s.chatID, s.reasoningID)
		s.reasoningID = ""
	}
}

func (s *mintclawStreamer) updateLocked(
	ctx context.Context,
	content string,
	force bool,
	contextUsage *bus.ContextUsage,
) error {
	if s == nil || s.channel == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	if strings.TrimSpace(content) == "" && s.messageID == "" {
		return nil
	}

	now := time.Now()
	contentLen := len([]rune(content))
	if s.messageID != "" && !force {
		growth := contentLen - s.lastLen
		if now.Sub(s.lastAt) < s.throttleInterval || growth < s.minGrowth {
			return nil
		}
	}

	return s.sendLockedWithFinal(ctx, content, force, contextUsage)
}

func (s *mintclawStreamer) updateReasoningLocked(ctx context.Context, content string, force bool) error {
	if s == nil || s.channel == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	if strings.TrimSpace(content) == "" && s.reasoningID == "" {
		return nil
	}

	now := time.Now()
	contentLen := len([]rune(content))
	if s.reasoningID != "" && !force {
		growth := contentLen - s.reasoningLastLen
		if now.Sub(s.reasoningLastAt) < s.throttleInterval || growth < s.minGrowth {
			return nil
		}
	}

	return s.sendReasoningLocked(ctx, content)
}

func (s *mintclawStreamer) sendLocked(
	ctx context.Context,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	return s.sendLockedWithFinal(ctx, content, false, contextUsage)
}

func (s *mintclawStreamer) sendLockedWithFinal(
	ctx context.Context,
	content string,
	final bool,
	contextUsage *bus.ContextUsage,
) error {
	now := time.Now()
	contentLen := len([]rune(content))

	if s.messageID == "" {
		messageID := uuid.New().String()
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      messageID,
		}
		setStreamingIdentityPayload(payload, s.sessionKey, s.traceScope)
		setStreamingRequestPayload(payload, s.requestID)
		setStreamingAgentPayload(payload, s.agentID)
		if final {
			payload[PayloadKeyFinal] = true
			payload[PayloadKeyKind] = MessageKindFinalReply
			payload[PayloadKeyOutbound] = bus.OutboundKindFinal
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		setContextUsagePayload(payload, contextUsage)
		setTurnUsagePayload(payload, s.turnInputTokens, s.turnOutputTokens)
		outMsg := newMessage(TypeMessageCreate, payload)
		if err := s.channel.broadcast(ctx, s.chatID, outMsg); err != nil {
			return err
		}
		s.messageID = messageID
	} else if content != s.lastContent || contextUsage != nil || final {
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.messageID,
		}
		setStreamingIdentityPayload(payload, s.sessionKey, s.traceScope)
		setStreamingRequestPayload(payload, s.requestID)
		setStreamingAgentPayload(payload, s.agentID)
		if final {
			payload[PayloadKeyFinal] = true
			payload[PayloadKeyKind] = MessageKindFinalReply
			payload[PayloadKeyOutbound] = bus.OutboundKindFinal
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		setTurnUsagePayload(payload, s.turnInputTokens, s.turnOutputTokens)
		if err := s.channel.editMessagePayload(ctx, s.chatID, s.messageID, payload, contextUsage); err != nil {
			return err
		}
	}

	s.lastContent = content
	s.lastLen = contentLen
	s.lastAt = now
	return nil
}

func (s *mintclawStreamer) sendReasoningLocked(ctx context.Context, content string) error {
	now := time.Now()
	contentLen := len([]rune(content))

	if s.reasoningID == "" {
		reasoningID := uuid.New().String()
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      reasoningID,
			PayloadKeyKind:    MessageKindThought,
			PayloadKeyThought: true,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		outMsg := newMessage(TypeMessageCreate, payload)
		if err := s.channel.broadcast(ctx, s.chatID, outMsg); err != nil {
			return err
		}
		s.reasoningID = reasoningID
	} else if content != s.reasoningContent {
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.reasoningID,
			PayloadKeyKind:    MessageKindThought,
			PayloadKeyThought: true,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		outMsg := newMessage(TypeMessageUpdate, payload)
		if err := s.channel.broadcast(ctx, s.chatID, outMsg); err != nil {
			return err
		}
	}

	s.reasoningContent = content
	s.reasoningLastLen = contentLen
	s.reasoningLastAt = now
	return nil
}

// SendMedia implements channels.MediaSender for the MintClaw web UI.
// Media is delivered as a normal assistant message carrying structured
// attachments plus an authenticated same-origin download URL.
