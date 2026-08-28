package channels

import (
	"context"
	"errors"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

// SendMessage sends an outbound message synchronously through the channel
// worker's rate limiter and retry logic. It blocks until the message is
// delivered (or all retries are exhausted), which preserves ordering when
// a subsequent operation depends on the message having been sent.
func (m *Manager) SendMessage(ctx context.Context, msg bus.OutboundMessage) error {
	return m.delivery.sendMessageWithRetryPolicy(ctx, msg, publishDefinitiveOutcome)
}

// SendMessageProvisional suppresses a definitely-not-sent failure outcome so
// the caller can try a fallback. Success and ambiguous failure remain terminal.
// Callers must check DeliveryDefinitelyNotSent before attempting the fallback.
func (m *Manager) SendMessageProvisional(ctx context.Context, msg bus.OutboundMessage) error {
	return m.delivery.sendMessageWithRetryPolicy(ctx, msg, publishSuccessOnly)
}

func (r *DeliveryRuntime) sendMessageWithRetryPolicy(
	ctx context.Context,
	msg bus.OutboundMessage,
	outcome outcomePublication,
) error {
	m := r.host
	var err error
	msg, err = bus.NormalizeOutboundMessage(msg)
	if err != nil {
		return newDeliveryError(err, false)
	}
	msg = m.decorateOutboundResponseFooter(msg)
	channelName := outboundMessageChannel(msg)

	_, exists := m.deliveryChannel(channelName)
	owner := r.owner(channelName)

	if !exists {
		return r.rejectMessageBeforeSend(
			outcome, channelName, msg, fmt.Errorf("channel %s not found", channelName),
		)
	}
	var w *channelWorker
	if owner != nil {
		var release func()
		var borrowErr error
		w, release, borrowErr = owner.borrowWorkerForSend()
		if borrowErr != nil {
			return r.rejectMessageBeforeSend(outcome, channelName, msg, borrowErr)
		}
		defer release()
	}
	if w == nil {
		return r.rejectMessageBeforeSend(
			outcome, channelName, msg, fmt.Errorf("channel %s has no active worker", channelName),
		)
	}
	terminals := m.beginOutboundToolFeedbackTerminals(channelName, w.ch, msg)
	terminalSucceeded := false
	defer func() {
		m.completeToolFeedbackTerminals(ctx, terminals, terminalSucceeded)
	}()

	maxLen := 0
	if mlp, ok := w.ch.(MessageLengthProvider); ok {
		maxLen = mlp.MaxMessageLength()
	}
	if chunks := splitOutboundMessageContent(msg, maxLen); len(chunks) > 1 {
		deliveredChunks := 0
		var messageIDs []string
		for _, chunk := range chunks {
			chunkMsg := msg
			chunkMsg.Content = chunk
			result := r.sendWithRetryPolicy(ctx, channelName, w, chunkMsg, publishNoOutcome)
			if !result.Delivered() {
				logicalAmbiguous := result.MayHaveDelivered() || deliveredChunks > 0
				if outcome.failure(logicalAmbiguous) {
					m.publishOutboundFailed(channelName, msg, result.Err, false)
				}
				return newDeliveryError(
					fmt.Errorf("channel %s failed to deliver message: %w", channelName, result.Err),
					logicalAmbiguous,
				)
			}
			messageIDs = append(messageIDs, result.MessageIDs...)
			deliveredChunks++
		}
		if outcome.success() {
			m.publishOutboundSent(channelName, msg, messageIDs)
		}
		terminalSucceeded = true
	} else {
		if len(chunks) == 1 {
			msg.Content = chunks[0]
		}
		result := r.sendWithRetryPolicy(ctx, channelName, w, msg, outcome)
		if !result.Delivered() {
			return newDeliveryError(
				fmt.Errorf("channel %s failed to deliver message: %w", channelName, result.Err),
				result.MayHaveDelivered(),
			)
		}
		terminalSucceeded = true
	}
	return nil
}

func (r *DeliveryRuntime) rejectMessageBeforeSend(
	outcome outcomePublication,
	channelName string,
	msg bus.OutboundMessage,
	err error,
) error {
	m := r.host
	if outcome.failure(false) {
		m.publishOutboundFailed(channelName, msg, err, false)
	}
	return newDeliveryError(err, false)
}

// SendMedia sends outbound media synchronously through the channel worker's
// rate limiter and retry logic. It blocks until the media is delivered (or all
// retries are exhausted), which preserves ordering when later agent behavior
// depends on actual media delivery.
func (m *Manager) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if err := m.PreflightMedia(ctx, msg); err != nil {
		return newDeliveryError(err, false)
	}
	return m.delivery.sendMedia(ctx, msg, publishDefinitiveOutcome)
}

// SendMediaProvisional suppresses a definitely-not-sent failure outcome so the
// caller can try a fallback. Success and ambiguous failure remain terminal.
// Callers must check DeliveryDefinitelyNotSent before attempting the fallback.
func (m *Manager) SendMediaProvisional(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if err := m.PreflightMedia(ctx, msg); err != nil {
		return newDeliveryError(err, false)
	}
	return m.delivery.sendMedia(ctx, msg, publishSuccessOnly)
}

// PreflightMedia applies deterministic channel-owned media constraints before
// the message reaches durable admission or a transport worker.
func (m *Manager) PreflightMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if m == nil {
		return errors.New("channel manager is unavailable")
	}
	channelName := outboundMediaChannel(msg)
	channel, ok := m.deliveryChannel(channelName)
	if !ok {
		// Preserve the normal send path's definitive rejection and runtime event.
		return nil
	}
	preflighter, ok := channel.(MediaPreflighter)
	if !ok {
		return nil
	}
	return preflighter.PreflightMedia(ctx, msg)
}

func (r *DeliveryRuntime) sendMedia(
	ctx context.Context,
	msg bus.OutboundMediaMessage,
	outcome outcomePublication,
) error {
	m := r.host
	var err error
	msg, err = bus.NormalizeOutboundMediaMessage(msg)
	if err != nil {
		return newDeliveryError(err, false)
	}
	channelName := outboundMediaChannel(msg)

	_, exists := m.deliveryChannel(channelName)
	owner := r.owner(channelName)

	if !exists {
		return r.rejectMediaBeforeSend(
			outcome, channelName, msg, fmt.Errorf("channel %s not found", channelName),
		)
	}
	var w *channelWorker
	if owner != nil {
		var release func()
		var borrowErr error
		w, release, borrowErr = owner.borrowWorkerForSend()
		if borrowErr != nil {
			return r.rejectMediaBeforeSend(outcome, channelName, msg, borrowErr)
		}
		defer release()
	}
	if w == nil {
		return r.rejectMediaBeforeSend(
			outcome, channelName, msg, fmt.Errorf("channel %s has no active worker", channelName),
		)
	}

	result := r.sendMediaWithRetryPolicy(ctx, channelName, w, msg, outcome)
	if !result.Delivered() {
		return newDeliveryError(result.Err, result.MayHaveDelivered())
	}
	return nil
}

func (r *DeliveryRuntime) rejectMediaBeforeSend(
	outcome outcomePublication,
	channelName string,
	msg bus.OutboundMediaMessage,
	err error,
) error {
	m := r.host
	if outcome.failure(false) {
		m.publishOutboundMediaFailed(channelName, msg, err)
	}
	return newDeliveryError(err, false)
}
