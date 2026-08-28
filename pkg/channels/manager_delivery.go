package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"golang.org/x/time/rate"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/constants"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

// newChannelWorker creates a channelWorker with a rate limiter configured
// for the given channel type. channelType is used for rate limit lookup.
func newChannelWorker(name string, ch Channel, channelType string) *channelWorker {
	rateVal := float64(defaultRateLimit)
	if r, ok := channelRateConfig[channelType]; ok {
		rateVal = r
	}
	burst := int(math.Max(1, math.Ceil(rateVal/2)))

	return &channelWorker{
		ch:         ch,
		queue:      make(chan bus.OutboundMessage, defaultChannelQueueSize),
		mediaQueue: make(chan bus.OutboundMediaMessage, defaultChannelQueueSize),
		done:       make(chan struct{}),
		mediaDone:  make(chan struct{}),
		limiter:    rate.NewLimiter(rate.Limit(rateVal), burst),
	}
}

func newDeliveryOwner(name string, ch Channel, channelType string) *deliveryOwner {
	return &deliveryOwner{
		name:      name,
		ch:        ch,
		worker:    newChannelWorker(name, ch, channelType),
		closedCh:  make(chan struct{}),
		closeDone: make(chan struct{}),
	}
}

func (o *deliveryOwner) Worker() *channelWorker {
	if o == nil {
		return nil
	}
	return o.worker
}

func (o *deliveryOwner) active() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.closed && o.worker != nil
}

func (o *deliveryOwner) borrowWorkerForSend() (*channelWorker, func(), error) {
	if o == nil || o.worker == nil {
		return nil, nil, errDeliveryClosed
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, nil, errDeliveryClosed
	}
	return o.worker, o.mu.Unlock, nil
}

func (o *deliveryOwner) StartDelivery(ctx context.Context, runtime *DeliveryRuntime) {
	if o == nil || o.worker == nil {
		return
	}
	go runtime.runWorkerOwned(ctx, o.name, o.worker, o.closeAdmission)
	go runtime.runMediaWorkerOwned(ctx, o.name, o.worker, o.closeAdmission)
}

func (o *deliveryOwner) Enqueue(ctx context.Context, msg bus.OutboundMessage) (bool, error) {
	if o == nil || o.worker == nil {
		return false, errDeliveryClosed
	}
	closedCh, ok := o.beginEnqueue()
	if !ok {
		return false, errDeliveryClosed
	}
	defer o.finishEnqueue()

	select {
	case o.worker.queue <- msg:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-closedCh:
		return false, errDeliveryClosed
	}
}

func (o *deliveryOwner) EnqueueMedia(
	ctx context.Context,
	msg bus.OutboundMediaMessage,
) (bool, error) {
	if o == nil || o.worker == nil {
		return false, errDeliveryClosed
	}
	closedCh, ok := o.beginEnqueue()
	if !ok {
		return false, errDeliveryClosed
	}
	defer o.finishEnqueue()

	select {
	case o.worker.mediaQueue <- msg:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-closedCh:
		return false, errDeliveryClosed
	}
}

func (o *deliveryOwner) beginEnqueue() (<-chan struct{}, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, false
	}
	o.enqueueWG.Add(1)
	o.inflightEnqueues++
	return o.closedCh, true
}

func (o *deliveryOwner) finishEnqueue() {
	o.mu.Lock()
	o.inflightEnqueues--
	o.mu.Unlock()
	o.enqueueWG.Done()
}

func (o *deliveryOwner) CloseDeliveryAndWait() {
	if o == nil || o.worker == nil {
		return
	}
	o.closeAdmission()
	<-o.worker.done
	<-o.worker.mediaDone
}

func (o *deliveryOwner) closeAdmission() {
	if o == nil || o.worker == nil {
		return
	}
	o.mu.Lock()
	if o.closed {
		closeDone := o.closeDone
		o.mu.Unlock()
		<-closeDone
		return
	}
	o.closed = true
	closeDone := o.closeDone
	close(o.closedCh)
	o.mu.Unlock()

	o.enqueueWG.Wait()
	close(o.worker.queue)
	close(o.worker.mediaQueue)
	close(closeDone)
}

// runWorker processes outbound messages for a single channel.
// Message processing follows this order:
//  1. SplitByMarker (if enabled in config) - LLM semantic marker-based splitting
//  2. SplitMessage - channel-specific length-based splitting (MaxMessageLength)
func (r *DeliveryRuntime) runWorker(ctx context.Context, name string, w *channelWorker) {
	r.runWorkerOwned(ctx, name, w, nil)
}

func (r *DeliveryRuntime) runWorkerOwned(
	ctx context.Context,
	name string,
	w *channelWorker,
	closeAdmission func(),
) {
	defer close(w.done)
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			r.deliverQueuedMessage(ctx, name, w, msg)
		case <-ctx.Done():
			if closeAdmission != nil {
				closeAdmission()
			}
			r.failPendingOutbound(name, w.queue, ctx.Err())
			return
		}
	}
}

func (r *DeliveryRuntime) deliverQueuedMessage(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMessage,
) {
	m := r.host
	msg = m.decorateOutboundResponseFooter(msg)
	maxLen := 0
	if mlp, ok := w.ch.(MessageLengthProvider); ok {
		maxLen = mlp.MaxMessageLength()
	}
	var chunks []string
	if m.finalizedStreamActiveForMessage(name, msg) {
		chunks = []string{msg.Content}
	} else if m.deliverySplitOnMarker() && !outboundMessageIsToolFeedback(msg) {
		if markerChunks := SplitByMarker(msg.Content); len(markerChunks) > 1 {
			for _, chunk := range markerChunks {
				chunkMsg := msg
				chunkMsg.Content = chunk
				chunks = append(chunks, splitOutboundMessageContent(chunkMsg, maxLen)...)
			}
		}
	}
	if len(chunks) == 0 {
		chunks = splitOutboundMessageContent(msg, maxLen)
	}

	if err := m.beginDurableOutbound(msg.DeliveryID); err != nil {
		m.publishOutboundFailed(name, msg, err, false)
		return
	}
	terminals := m.beginOutboundToolFeedbackTerminals(name, w.ch, msg)
	result := r.sendTextChunksWithRetry(ctx, name, w, msg, chunks)
	m.completeToolFeedbackTerminals(ctx, terminals, result.Delivered())
	if !result.Delivered() {
		outcome := durableOutcome(result, nil)
		if persistErr := m.persistDurableOutbound(msg.DeliveryID, outcome); persistErr != nil {
			result.Err = errors.Join(result.Err, persistErr)
		}
		m.publishOutboundFailed(name, msg, result.Err, false)
		return
	}
	outcome := OutboundDeliveryOutcome{
		Status:             OutboundDeliveryDelivered,
		PlatformMessageIDs: result.MessageIDs,
	}
	if err := m.persistDurableOutbound(msg.DeliveryID, outcome); err != nil {
		m.publishOutboundFailed(name, msg, err, false)
		return
	}
	m.publishOutboundSent(name, msg, result.MessageIDs)
}

func (r *DeliveryRuntime) sendTextChunksWithRetry(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMessage,
	chunks []string,
) DeliveryResult[bus.OutboundMessage] {
	if len(chunks) == 0 {
		return RejectedDelivery[bus.OutboundMessage](errors.New("outbound text chunks are empty"))
	}

	var confirmedIDs []string
	confirmedChunks := 0
	totalAttempts := 0
	var unresolved *DeliveryResult[bus.OutboundMessage]
	for index, chunk := range chunks {
		chunkMsg := msg
		chunkMsg.Content = chunk
		result := r.sendWithRetryPolicy(ctx, name, w, chunkMsg, publishNoOutcome)
		confirmedIDs = append(confirmedIDs, result.MessageIDs...)
		totalAttempts += max(result.Attempts, 1)
		if result.Delivered() {
			confirmedChunks++
			continue
		}

		if result.Remaining != nil || !result.MayHaveDelivered() {
			if result.Remaining == nil && result.DefinitelyNotSent() {
				result.Remaining = []bus.OutboundMessage{chunkMsg}
			}
			result.Remaining = append(
				result.Remaining,
				outboundMessagesForTextChunks(msg, chunks[index+1:])...,
			)
			if unresolved != nil {
				result = combineTextChunkFailures(*unresolved, result)
			}
			return finalizeTextChunkSequence(result, confirmedIDs, confirmedChunks, totalAttempts)
		}
		if unresolved == nil {
			copyResult := result
			unresolved = &copyResult
		} else {
			combined := combineTextChunkFailures(*unresolved, result)
			unresolved = &combined
		}
		if index == len(chunks)-1 {
			break
		}
		unresolved.UnresolvedPartial = true
	}

	if unresolved != nil {
		return finalizeTextChunkSequence(*unresolved, confirmedIDs, confirmedChunks, totalAttempts)
	}
	result := SuccessfulDelivery[bus.OutboundMessage](confirmedIDs)
	result.Attempts = totalAttempts
	return result
}

func finalizeTextChunkSequence(
	result DeliveryResult[bus.OutboundMessage],
	confirmedIDs []string,
	confirmedChunks int,
	totalAttempts int,
) DeliveryResult[bus.OutboundMessage] {
	result.MessageIDs = append([]string(nil), confirmedIDs...)
	result.Remaining = append([]bus.OutboundMessage(nil), result.Remaining...)
	result.Attempts = max(totalAttempts, 1)
	if confirmedChunks > 0 || len(confirmedIDs) > 0 {
		result.Status = DeliveryPartial
	} else {
		result.Status = DeliveryFailed
	}
	return result
}

func combineTextChunkFailures(
	left DeliveryResult[bus.OutboundMessage],
	right DeliveryResult[bus.OutboundMessage],
) DeliveryResult[bus.OutboundMessage] {
	left.Acceptance = combineDeliveryAcceptance(left.Acceptance, right.Acceptance)
	left.Remaining = append([]bus.OutboundMessage(nil), right.Remaining...)
	left.UnresolvedPartial = left.UnresolvedPartial || right.UnresolvedPartial
	left.Err = errors.Join(left.Err, right.Err)
	if !right.RetryAt.IsZero() || right.RetryAfter > 0 {
		left.RetryAfter = right.RetryAfter
		left.RetryAt = right.RetryAt
	}
	return left
}

func outboundMessagesForTextChunks(msg bus.OutboundMessage, chunks []string) []bus.OutboundMessage {
	messages := make([]bus.OutboundMessage, 0, len(chunks))
	for _, chunk := range chunks {
		chunkMsg := msg
		chunkMsg.Content = chunk
		messages = append(messages, chunkMsg)
	}
	return messages
}

func (r *DeliveryRuntime) failPendingOutbound(
	name string,
	queue <-chan bus.OutboundMessage,
	err error,
) {
	m := r.host
	for {
		select {
		case msg, ok := <-queue:
			if !ok {
				return
			}
			failureErr := m.persistDurableRejection(msg.DeliveryID, err)
			m.publishOutboundFailed(name, msg, failureErr, false)
		default:
			return
		}
	}
}

func (m *Manager) finalizedStreamActiveForMessage(channelName string, msg bus.OutboundMessage) bool {
	if m == nil || !outboundMessageIsFinal(msg) {
		return false
	}
	chatID := outboundMessageChatID(msg)
	if strings.TrimSpace(channelName) == "" || strings.TrimSpace(chatID) == "" {
		return false
	}
	_, active := m.stream.activeKey(
		channelName, chatID, msg.SessionKey, primaryTraceScope(msg.TraceScopes),
	)
	return active
}

// splitOutboundMessageContent splits regular outbound content by maxLen, but
// keeps tool feedback in a single message by truncating the explanation body.
func splitOutboundMessageContent(msg bus.OutboundMessage, maxLen int) []string {
	if maxLen > 0 {
		if outboundMessageIsToolFeedback(msg) {
			animationSafeLen := maxLen - MaxToolFeedbackAnimationFrameLength()
			if animationSafeLen <= 0 {
				animationSafeLen = maxLen
			}
			if len([]rune(msg.Content)) > animationSafeLen {
				return []string{utils.FitToolFeedbackMessage(msg.Content, animationSafeLen)}
			}
			return []string{msg.Content}
		}
		if len([]rune(msg.Content)) > maxLen {
			return SplitMessage(msg.Content, maxLen)
		}
	}
	return []string{msg.Content}
}

// sendWithRetry sends a message through the channel with rate limiting and
// retry logic. It classifies errors to determine the retry strategy:
//   - ErrNotRunning / ErrSendFailed: permanent, no retry
//   - ErrRateLimit / rejected transient: bounded retry
//   - ambiguous temporary / unknown: no retry
func (r *DeliveryRuntime) sendWithRetry(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMessage,
) DeliveryResult[bus.OutboundMessage] {
	m := r.host
	terminals := m.beginOutboundToolFeedbackTerminals(name, w.ch, msg)
	result := r.sendWithRetryPolicy(ctx, name, w, msg, publishDefinitiveOutcome)
	m.completeToolFeedbackTerminals(ctx, terminals, result.Delivered())
	return result
}

func (r *DeliveryRuntime) sendWithRetryPolicy(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMessage,
	outcome outcomePublication,
) DeliveryResult[bus.OutboundMessage] {
	m := r.host
	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		// ctx canceled, shutting down
		m.publishChannelEvent(
			runtimeevents.KindChannelRateLimited,
			name,
			scopeFromOutboundContext(msg.Context),
			runtimeevents.SeverityWarn,
			ChannelOutboundPayload{
				DeliveryID:       msg.DeliveryID,
				TraceScopes:      append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
				TraceSettlement:  msg.TraceSettlement,
				ContentLen:       len([]rune(msg.Content)),
				ReplyToMessageID: msg.ReplyToMessageID,
				Error:            err.Error(),
			},
		)
		if outcome.failure(false) {
			m.publishOutboundFailed(name, msg, err, false)
		}
		return RejectedDelivery[bus.OutboundMessage](err)
	}

	isToolFeedback := outboundMessageIsToolFeedback(msg)

	// Pre-send: stop typing and try to edit placeholder
	if msgIDs, handled := m.preSend(ctx, name, msg, w.ch); handled {
		if outcome.success() {
			m.publishOutboundSent(name, msg, msgIDs)
		}
		return SuccessfulDelivery[bus.OutboundMessage](msgIDs)
	}

	deliverKnownRemainderDirectly := false
	result := DeliverWithRetry(
		ctx,
		[]bus.OutboundMessage{msg},
		DeliveryRetryPolicy{
			MaxRetries:     maxRetries,
			RateLimitDelay: rateLimitDelay,
			BaseBackoff:    baseBackoff,
			MaxBackoff:     maxBackoff,
		},
		func(ctx context.Context, pending []bus.OutboundMessage) DeliveryResult[bus.OutboundMessage] {
			attemptMsg := pending[0]
			if isToolFeedback && m.deliveryToolFeedbackEnabled() && !deliverKnownRemainderDirectly {
				// The coordinator must own interim sends so it can retain the
				// platform message ID and edit the same progress message later.
				delivery := m.deliverToolFeedback(ctx, name, w.ch, attemptMsg)
				// Once that send confirms a carrier and identifies an unsent
				// remainder, retry the remainder as transport payload. Re-entering
				// the coordinator would edit the confirmed carrier and drop it.
				deliverKnownRemainderDirectly = len(delivery.MessageIDs) > 0 && delivery.Remaining != nil
				return delivery
			}
			return w.ch.DeliverText(ctx, pending)
		},
		func(attempt DeliveryAttempt) {
			if attempt.Err == nil {
				if attempt.Number > 1 {
					logger.InfoCF("channels", "Outbound send recovered after retry", map[string]any{
						"channel":        name,
						"chat_id":        outboundMessageChatID(msg),
						"attempt":        attempt.Number,
						"max_attempts":   maxRetries + 1,
						"duration_ms":    attempt.Duration.Milliseconds(),
						"classification": "success_after_retry",
					})
				}
				return
			}
			logger.WarnCF("channels", "Outbound send attempt failed", map[string]any{
				"channel":        name,
				"chat_id":        outboundMessageChatID(msg),
				"attempt":        attempt.Number,
				"max_attempts":   maxRetries + 1,
				"duration_ms":    attempt.Duration.Milliseconds(),
				"classification": classifySendError(attempt.Err),
				"error":          attempt.Err.Error(),
			})
		},
	)
	if result.Delivered() {
		if outcome.success() {
			m.publishOutboundSent(name, msg, result.MessageIDs)
		}
		return result
	}

	lastErr := result.Err
	if lastErr == nil {
		lastErr = fmt.Errorf("channel delivery failed")
		result.Err = lastErr
	}

	// All retries exhausted or permanent failure.
	logger.ErrorCF("channels", "Send failed", map[string]any{
		"channel": name,
		"chat_id": outboundMessageChatID(msg),
		"error":   lastErr.Error(),
		"retries": max(0, result.Attempts-1),
	})
	if outcome.failure(result.MayHaveDelivered()) {
		m.publishOutboundFailed(name, msg, lastErr, false)
	}

	return result
}

func classifySendError(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrNotRunning):
		return "not_running"
	case errors.Is(err, ErrSendFailed):
		return "permanent"
	case errors.Is(err, ErrRateLimit):
		return "rate_limit"
	case errors.Is(err, ErrTemporary):
		return "temporary"
	default:
		return "unknown"
	}
}

func dispatchLoop[M any](
	ctx context.Context,
	runtime *DeliveryRuntime,
	ch <-chan M,
	getChannel func(M) string,
	requiresOutcome func(M) bool,
	enqueue func(context.Context, *deliveryOwner, M) bool,
	reject func(M, error),
	startMsg, stopMsg, unknownMsg, noWorkerMsg string,
) {
	logger.InfoC("channels", startMsg)

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("channels", stopMsg)
			return

		case msg, ok := <-ch:
			if !ok {
				logger.InfoC("channels", stopMsg)
				return
			}

			channel := getChannel(msg)

			// Internal traffic has no external delivery owner. Ignore it unless
			// this message explicitly promises a terminal delivery outcome.
			if constants.IsInternalChannel(channel) {
				if requiresOutcome(msg) {
					reject(msg, fmt.Errorf("internal channel %s has no external delivery owner", channel))
				}
				continue
			}

			_, exists := runtime.host.deliveryChannel(channel)
			owner := runtime.owner(channel)

			if !exists {
				logger.WarnCF("channels", unknownMsg, map[string]any{"channel": channel})
				reject(msg, fmt.Errorf("channel %s not found", channel))
				continue
			}

			if owner != nil && owner.Worker() != nil {
				if !enqueue(ctx, owner, msg) {
					return
				}
			} else if exists {
				logger.WarnCF("channels", noWorkerMsg, map[string]any{"channel": channel})
				reject(msg, fmt.Errorf("channel %s has no active worker", channel))
			}
		}
	}
}

func (r *DeliveryRuntime) dispatchOutbound(ctx context.Context) {
	m := r.host
	dispatchLoop(
		ctx, r,
		m.deliveryTextSource(),
		func(msg bus.OutboundMessage) string { return outboundMessageChannel(msg) },
		func(msg bus.OutboundMessage) bool { return msg.TraceSettlement },
		func(ctx context.Context, owner *deliveryOwner, msg bus.OutboundMessage) bool {
			queued, err := owner.Enqueue(ctx, msg)
			if queued {
				m.publishOutboundQueued(outboundMessageChannel(msg), msg)
				return true
			}
			if err != nil {
				err = m.persistDurableRejection(msg.DeliveryID, err)
				m.publishOutboundFailed(outboundMessageChannel(msg), msg, err, false)
				return errors.Is(err, errDeliveryClosed)
			}
			return false
		},
		func(msg bus.OutboundMessage, err error) {
			err = m.persistDurableRejection(msg.DeliveryID, err)
			m.publishOutboundFailed(outboundMessageChannel(msg), msg, err, false)
		},
		"Outbound dispatcher started",
		"Outbound dispatcher stopped",
		"Unknown channel for outbound message",
		"Channel has no active worker, skipping message",
	)
}

func (r *DeliveryRuntime) dispatchOutboundMedia(ctx context.Context) {
	m := r.host
	dispatchLoop(
		ctx, r,
		m.deliveryMediaSource(),
		func(msg bus.OutboundMediaMessage) string { return outboundMediaChannel(msg) },
		func(msg bus.OutboundMediaMessage) bool { return msg.TraceSettlement },
		func(ctx context.Context, owner *deliveryOwner, msg bus.OutboundMediaMessage) bool {
			queued, err := owner.EnqueueMedia(ctx, msg)
			if queued {
				m.publishOutboundMediaQueued(outboundMediaChannel(msg), msg)
				return true
			}
			if err != nil {
				err = m.persistDurableRejection(msg.DeliveryID, err)
				m.publishOutboundMediaFailed(outboundMediaChannel(msg), msg, err)
				return errors.Is(err, errDeliveryClosed)
			}
			return false
		},
		func(msg bus.OutboundMediaMessage, err error) {
			err = m.persistDurableRejection(msg.DeliveryID, err)
			m.publishOutboundMediaFailed(outboundMediaChannel(msg), msg, err)
		},
		"Outbound media dispatcher started",
		"Outbound media dispatcher stopped",
		"Unknown channel for outbound media message",
		"Channel has no active worker, skipping media message",
	)
}

// runMediaWorker processes outbound media messages for a single channel.
func (r *DeliveryRuntime) runMediaWorkerOwned(
	ctx context.Context,
	name string,
	w *channelWorker,
	closeAdmission func(),
) {
	defer close(w.mediaDone)
	for {
		select {
		case msg, ok := <-w.mediaQueue:
			if !ok {
				return
			}
			r.deliverQueuedMedia(ctx, name, w, msg)
		case <-ctx.Done():
			if closeAdmission != nil {
				closeAdmission()
			}
			r.failPendingOutboundMedia(name, w.mediaQueue, ctx.Err())
			return
		}
	}
}

func (r *DeliveryRuntime) deliverQueuedMedia(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMediaMessage,
) {
	m := r.host
	if err := m.beginDurableOutbound(msg.DeliveryID); err != nil {
		m.publishOutboundMediaFailed(name, msg, err)
		return
	}
	result := r.sendMediaWithRetryPolicy(ctx, name, w, msg, publishNoOutcome)
	outcome := durableOutcome(result, nil)
	if persistErr := m.persistDurableOutbound(msg.DeliveryID, outcome); persistErr != nil {
		result.Err = errors.Join(result.Err, persistErr)
	}
	if result.Delivered() && result.Err == nil {
		m.publishOutboundMediaSent(name, msg, result.MessageIDs)
		return
	}
	m.publishOutboundMediaFailed(name, msg, result.Err)
}

func (r *DeliveryRuntime) failPendingOutboundMedia(
	name string,
	queue <-chan bus.OutboundMediaMessage,
	err error,
) {
	m := r.host
	for {
		select {
		case msg, ok := <-queue:
			if !ok {
				return
			}
			failureErr := m.persistDurableRejection(msg.DeliveryID, err)
			m.publishOutboundMediaFailed(name, msg, failureErr)
		default:
			return
		}
	}
}

// sendMediaWithRetry sends a media message through the channel with rate limiting and
// retry logic. It returns the typed delivery result after retries, including
// when the channel does not support MediaSender.
func (r *DeliveryRuntime) sendMediaWithRetry(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMediaMessage,
) DeliveryResult[bus.OutboundMediaMessage] {
	return r.sendMediaWithRetryPolicy(ctx, name, w, msg, publishDefinitiveOutcome)
}

func (r *DeliveryRuntime) sendMediaWithRetryPolicy(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMediaMessage,
	outcome outcomePublication,
) DeliveryResult[bus.OutboundMediaMessage] {
	m := r.host
	ms, ok := w.ch.(MediaSender)
	if !ok {
		err := fmt.Errorf("channel %q does not support media sending", name)
		logger.WarnCF("channels", "Channel does not support MediaSender", map[string]any{
			"channel": name,
			"error":   err.Error(),
		})
		if outcome.failure(false) {
			m.publishOutboundMediaFailed(name, msg, err)
		}
		return RejectedDelivery[bus.OutboundMediaMessage](err)
	}

	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		m.publishChannelEvent(
			runtimeevents.KindChannelRateLimited,
			name,
			scopeFromOutboundContext(msg.Context),
			runtimeevents.SeverityWarn,
			ChannelOutboundPayload{
				DeliveryID:      msg.DeliveryID,
				TraceScopes:     append([]runtimeevents.TraceScope(nil), msg.TraceScopes...),
				TraceSettlement: msg.TraceSettlement,
				Media:           true,
				Error:           err.Error(),
			},
		)
		if outcome.failure(false) {
			m.publishOutboundMediaFailed(name, msg, err)
		}
		return RejectedDelivery[bus.OutboundMediaMessage](err)
	}

	terminalSucceeded := false
	var terminals []*toolFeedbackTerminal
	if m.deliveryToolFeedbackEnabled() {
		terminals = m.beginToolFeedbackTerminals(
			name,
			w.ch,
			outboundMediaChatID(msg),
			&msg.Context,
			msg.SessionKey,
			msg.TraceScopes,
			msg.Metadata.IsInterim(),
		)
		defer func() {
			m.completeToolFeedbackTerminals(ctx, terminals, terminalSucceeded)
		}()
	}

	// Pre-send: stop typing and clean up any placeholder before sending media.
	m.preSendMedia(ctx, name, msg, w.ch)

	result := DeliverWithRetry(
		ctx,
		[]bus.OutboundMediaMessage{msg},
		DeliveryRetryPolicy{
			MaxRetries:     maxRetries,
			RateLimitDelay: rateLimitDelay,
			BaseBackoff:    baseBackoff,
			MaxBackoff:     maxBackoff,
		},
		func(ctx context.Context, pending []bus.OutboundMediaMessage) DeliveryResult[bus.OutboundMediaMessage] {
			return ms.DeliverMedia(ctx, pending)
		},
		nil,
	)
	if result.Delivered() {
		terminalSucceeded = true
		if outcome.success() {
			m.publishOutboundMediaSent(name, msg, result.MessageIDs)
		}
		return result
	}

	lastErr := result.Err
	if lastErr == nil {
		lastErr = fmt.Errorf("channel media delivery failed")
		result.Err = lastErr
	}

	// All retries exhausted or permanent failure
	logger.ErrorCF("channels", "SendMedia failed", map[string]any{
		"channel": name,
		"chat_id": outboundMediaChatID(msg),
		"error":   lastErr.Error(),
		"retries": max(0, result.Attempts-1),
	})
	if outcome.failure(result.MayHaveDelivered()) {
		m.publishOutboundMediaFailed(name, msg, lastErr)
	}
	return result
}
