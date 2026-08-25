package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const (
	toolFeedbackTerminalTombstoneTTL   = 30 * time.Second
	toolFeedbackCleanupRetryDelay      = 5 * time.Second
	toolFeedbackCleanupRetention       = 30 * time.Second
	toolFeedbackGenerationHistoryLimit = 64
)

type toolFeedbackOperations struct {
	edit   func(context.Context, string, string, string) error
	delete func(context.Context, string, string) error
}

type toolFeedbackSendResult struct {
	messageIDs []string
	editable   bool
	delivery   *DeliveryResult[bus.OutboundMessage]
}

type trackedToolFeedbackMessage struct {
	chatID     string
	messageID  string
	editable   bool
	content    string
	operations toolFeedbackOperations
}

type pendingToolFeedbackCleanup struct {
	message   trackedToolFeedbackMessage
	expiresAt time.Time
	lastError string
}

type toolFeedbackTerminalSuccess uint8

const (
	toolFeedbackTerminalSuccessNone toolFeedbackTerminalSuccess = iota
	toolFeedbackTerminalSuccessTransient
	toolFeedbackTerminalSuccessRetained
)

type toolFeedbackGenerationClaim struct {
	pending  int
	admitted bool
}

type toolFeedbackEntry struct {
	opMu sync.Mutex
	mu   sync.Mutex

	terminalGeneration      uint64
	terminal                bool
	terminalUntil           time.Time
	terminalPending         int
	terminalRetained        int
	terminalSuccess         toolFeedbackTerminalSuccess
	retired                 bool
	sending                 bool
	paused                  bool
	activeGenerations       map[string]struct{}
	generationClaims        map[string]toolFeedbackGenerationClaim
	terminalizedGenerations map[string]struct{}
	terminalizedOrder       []string
	current                 trackedToolFeedbackMessage
	pendingCleanup          []pendingToolFeedbackCleanup
}

type toolFeedbackTerminal struct {
	key                string
	entry              *toolFeedbackEntry
	generation         uint64
	retain             bool
	absorbed           bool
	completed          bool
	traceGenerations   []string
	claimedGenerations []string
}

// ToolFeedbackCoordinator is the single owner of editable tool-feedback
// message state. Channel adapters provide only send, edit, and delete
// operations; lifecycle transitions are serialized here.
type ToolFeedbackCoordinator struct {
	mu       sync.Mutex
	entries  map[string]*toolFeedbackEntry
	animator *ToolFeedbackAnimator
	separate bool
	stopped  bool
}

func NewToolFeedbackCoordinator(cfg ToolFeedbackAnimatorConfig, separate bool) *ToolFeedbackCoordinator {
	c := &ToolFeedbackCoordinator{
		entries:  make(map[string]*toolFeedbackEntry),
		separate: separate,
	}
	c.animator = NewToolFeedbackAnimator(c.editAnimated)
	c.animator.Configure(cfg)
	return c
}

func (c *ToolFeedbackCoordinator) Configure(cfg ToolFeedbackAnimatorConfig, separate bool) {
	if c == nil {
		return
	}
	c.animator.Configure(cfg)
	c.mu.Lock()
	c.separate = separate
	c.mu.Unlock()
}

func (c *ToolFeedbackCoordinator) Deliver(
	ctx context.Context,
	key string,
	chatID string,
	content string,
	operations toolFeedbackOperations,
	send func(context.Context, string) ([]string, error),
) ([]string, error) {
	if send == nil {
		return nil, ErrSendFailed
	}
	result, err := c.deliver(ctx, key, "", chatID, content, operations, func(
		sendCtx context.Context,
		prepared string,
	) (toolFeedbackSendResult, error) {
		messageIDs, err := send(sendCtx, prepared)
		return toolFeedbackSendResult{messageIDs: messageIDs, editable: operations.edit != nil}, err
	})
	return result.messageIDs, err
}

func (c *ToolFeedbackCoordinator) deliver(
	ctx context.Context,
	key string,
	generation string,
	chatID string,
	content string,
	operations toolFeedbackOperations,
	send func(context.Context, string) (toolFeedbackSendResult, error),
) (toolFeedbackSendResult, error) {
	if send == nil {
		return toolFeedbackSendResult{}, ErrSendFailed
	}
	if c == nil || strings.TrimSpace(key) == "" {
		result, err := send(ctx, content)
		return result, err
	}
	key = strings.TrimSpace(key)
	content = strings.TrimSpace(content)
	separate := c.separateMessages()
	entry := c.lockEntry(key)
	if entry == nil {
		return toolFeedbackSendResult{}, ErrNotRunning
	}
	defer entry.opMu.Unlock()
	c.retryPendingCleanup(ctx, key, entry)

	entry.mu.Lock()
	generation = strings.TrimSpace(generation)
	if _, terminalized := entry.terminalizedGenerations[generation]; generation != "" && terminalized {
		entry.mu.Unlock()
		return toolFeedbackSendResult{}, nil
	}
	if entry.terminal {
		_, generationActive := entry.activeGenerations[generation]
		freshGeneration := generation != "" && !generationActive
		if !freshGeneration && (entry.terminalUntil.IsZero() || time.Now().Before(entry.terminalUntil)) {
			entry.mu.Unlock()
			return toolFeedbackSendResult{}, nil
		}
		resetToolFeedbackTerminal(entry)
	}
	if generation != "" {
		if entry.activeGenerations == nil {
			entry.activeGenerations = make(map[string]struct{})
		}
		entry.activeGenerations[generation] = struct{}{}
	}
	if separate && entry.current.messageID != "" {
		entry.current = trackedToolFeedbackMessage{}
		entry.mu.Unlock()
		c.animator.Clear(key)
		entry.mu.Lock()
		if entry.terminal {
			entry.mu.Unlock()
			return toolFeedbackSendResult{}, nil
		}
	}
	if entry.current.messageID != "" {
		current := entry.current
		wasPaused := entry.paused
		entry.paused = false
		if !current.editable {
			entry.mu.Unlock()
			return c.replaceTrackedMessage(ctx, key, entry, current, chatID, content, operations, send)
		}
		mergedContent := content
		if isWorkingSummaryToolFeedback(current.content) || isWorkingSummaryToolFeedback(content) {
			mergedContent = mergeToolFeedbackContent(current.content, content)
		}
		entry.mu.Unlock()
		if wasPaused {
			c.animator.Record(key, current.messageID, current.content)
		}

		updatedID, handled, err := c.animator.Update(ctx, key, content)
		entry.mu.Lock()
		terminal := entry.terminal
		retired := entry.retired
		unchanged := entry.current.messageID == current.messageID
		if err == nil && handled && unchanged && !terminal && !retired {
			entry.current.chatID = chatID
			entry.current.content = mergedContent
			entry.current.operations = operations
		}
		entry.mu.Unlock()
		if terminal || retired {
			c.animator.Clear(key)
		}
		if !handled {
			return toolFeedbackSendResult{messageIDs: []string{current.messageID}}, nil
		}
		if err == nil {
			return toolFeedbackSendResult{messageIDs: []string{updatedID}}, nil
		}
		if !errors.Is(err, ErrSendFailed) || current.operations.delete == nil ||
			terminal || retired || !unchanged {
			return toolFeedbackSendResult{}, err
		}
		return c.replaceTrackedMessage(ctx, key, entry, current, chatID, content, operations, send)
	}

	entry.sending = true
	entry.mu.Unlock()

	result, err := send(ctx, InitialAnimatedToolFeedbackContent(content))
	messageIDs := result.messageIDs
	entry.mu.Lock()
	entry.sending = false
	terminal := entry.terminal
	retired := entry.retired
	trackable := (result.editable && operations.edit != nil) || operations.delete != nil
	if len(messageIDs) > 0 && trackable && !terminal && !retired {
		entry.current = trackedToolFeedbackMessage{
			chatID: chatID, messageID: messageIDs[0],
			editable: result.editable && operations.edit != nil,
			content:  content, operations: operations,
		}
		entry.mu.Unlock()
		if result.editable && operations.edit != nil {
			c.animator.RecordEdited(key, messageIDs[0], content)
		}
		return result, err
	}
	entry.mu.Unlock()

	if len(messageIDs) > 0 && (terminal || retired) {
		c.cleanupLateMessage(ctx, key, entry, trackedToolFeedbackMessage{
			chatID: chatID, messageID: messageIDs[0], operations: operations,
		})
		result.messageIDs = nil
		if result.delivery != nil {
			result.delivery.MessageIDs = nil
		}
	}
	if !terminal && !retired {
		c.retireIdleEntryLocked(key, entry)
	}
	return result, err
}

func (c *ToolFeedbackCoordinator) replaceTrackedMessage(
	ctx context.Context,
	key string,
	entry *toolFeedbackEntry,
	current trackedToolFeedbackMessage,
	chatID string,
	content string,
	operations toolFeedbackOperations,
	send func(context.Context, string) (toolFeedbackSendResult, error),
) (toolFeedbackSendResult, error) {
	entry.mu.Lock()
	if entry.terminal || entry.retired || entry.current.messageID != current.messageID {
		entry.mu.Unlock()
		return toolFeedbackSendResult{}, nil
	}
	entry.sending = true
	entry.mu.Unlock()

	result, sendErr := send(ctx, InitialAnimatedToolFeedbackContent(content))
	messageIDs := result.messageIDs
	trackable := (result.editable && operations.edit != nil) || operations.delete != nil
	entry.mu.Lock()
	entry.sending = false
	terminal := entry.terminal
	retired := entry.retired
	unchanged := entry.current.messageID == current.messageID
	if len(messageIDs) == 0 || !trackable || terminal || retired || !unchanged {
		entry.mu.Unlock()
		if len(messageIDs) > 0 && (terminal || retired || !unchanged) {
			c.cleanupLateMessage(ctx, key, entry, trackedToolFeedbackMessage{
				chatID: chatID, messageID: messageIDs[0], operations: operations,
			})
			result.messageIDs = nil
			if result.delivery != nil {
				result.delivery.MessageIDs = nil
			}
		}
		return result, sendErr
	}
	replacement := trackedToolFeedbackMessage{
		chatID: chatID, messageID: messageIDs[0],
		editable: result.editable && operations.edit != nil,
		content:  content, operations: operations,
	}
	entry.current = replacement
	entry.mu.Unlock()

	c.animator.Clear(key)
	if replacement.editable {
		c.animator.RecordEdited(key, replacement.messageID, replacement.content)
	}
	if cleanupErr := tryDeleteToolFeedbackMessage(
		ctx, current.operations.delete, current.chatID, current.messageID,
	); cleanupErr != nil {
		entry.mu.Lock()
		entry.pendingCleanup = append(entry.pendingCleanup, newPendingToolFeedbackCleanup(current, cleanupErr))
		entry.mu.Unlock()
		c.scheduleCleanupMaintenance(key, entry, toolFeedbackCleanupRetryDelay)
		return result, sendErr
	}
	return result, sendErr
}

func (c *ToolFeedbackCoordinator) retryPendingCleanup(
	ctx context.Context,
	key string,
	entry *toolFeedbackEntry,
) {
	entry.mu.Lock()
	pending := append([]pendingToolFeedbackCleanup(nil), entry.pendingCleanup...)
	entry.pendingCleanup = nil
	entry.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	remaining := make([]pendingToolFeedbackCleanup, 0, len(pending))
	for _, cleanup := range pending {
		if !time.Now().Before(cleanup.expiresAt) {
			logToolFeedbackCleanupExhausted(key, cleanup, "retention_expired")
			continue
		}
		message := cleanup.message
		if err := tryDeleteToolFeedbackMessage(
			ctx, message.operations.delete, message.chatID, message.messageID,
		); err != nil {
			cleanup.lastError = err.Error()
			if errors.Is(err, ErrSendFailed) || errors.Is(err, ErrNotRunning) ||
				!time.Now().Before(cleanup.expiresAt) {
				logToolFeedbackCleanupExhausted(key, cleanup, "non_retryable")
				continue
			}
			remaining = append(remaining, cleanup)
		}
	}
	entry.mu.Lock()
	entry.pendingCleanup = append(remaining, entry.pendingCleanup...)
	entry.mu.Unlock()
}

func newPendingToolFeedbackCleanup(
	message trackedToolFeedbackMessage,
	cause error,
) pendingToolFeedbackCleanup {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	return pendingToolFeedbackCleanup{
		message:   message,
		expiresAt: time.Now().Add(toolFeedbackCleanupRetention),
		lastError: lastError,
	}
}

func logToolFeedbackCleanupExhausted(
	key string,
	cleanup pendingToolFeedbackCleanup,
	reason string,
) {
	messageHash := sha256.Sum256([]byte(cleanup.message.messageID))
	logger.ErrorCF("channels", "Tool feedback cleanup exhausted", map[string]any{
		"coordinator_key": strings.TrimSpace(key),
		"message_id_hash": hex.EncodeToString(messageHash[:6]),
		"reason":          reason,
		"error":           cleanup.lastError,
	})
}

func (c *ToolFeedbackCoordinator) cleanupLateMessage(
	ctx context.Context,
	key string,
	entry *toolFeedbackEntry,
	message trackedToolFeedbackMessage,
) {
	err := tryDeleteToolFeedbackMessage(
		ctx, message.operations.delete, message.chatID, message.messageID,
	)
	if err == nil || errors.Is(err, ErrSendFailed) || errors.Is(err, ErrNotRunning) {
		return
	}
	entry.mu.Lock()
	if entry.retired {
		entry.mu.Unlock()
		return
	}
	entry.pendingCleanup = append(entry.pendingCleanup, newPendingToolFeedbackCleanup(message, err))
	entry.mu.Unlock()
	c.scheduleCleanupMaintenance(key, entry, toolFeedbackCleanupRetryDelay)
}

func (c *ToolFeedbackCoordinator) BeginTerminal(key string) *toolFeedbackTerminal {
	return c.beginTerminal(key, true, nil)
}

func (c *ToolFeedbackCoordinator) BeginTransientTerminal(key string) *toolFeedbackTerminal {
	return c.beginTerminal(key, false, nil)
}

func (c *ToolFeedbackCoordinator) beginTerminal(
	key string,
	retain bool,
	generations []string,
) *toolFeedbackTerminal {
	if c == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	key = strings.TrimSpace(key)
	for {
		entry := c.getOrCreateEntry(key)
		if entry == nil {
			return nil
		}
		entry.mu.Lock()
		traceGenerations := normalizeToolFeedbackGenerations(generations)
		claimedGenerations := append([]string(nil), traceGenerations...)
		entry.claimTerminalGenerations(claimedGenerations)
		if retain && (!entry.terminal || len(traceGenerations) == 0) {
			traceGenerations = entry.activeGenerationsSnapshot()
		}
		if entry.retired {
			entry.mu.Unlock()
			c.removeEntry(key, entry)
			continue
		}
		if entry.terminal && entry.terminalSuccess != toolFeedbackTerminalSuccessNone &&
			(!retain || entry.terminalSuccess == toolFeedbackTerminalSuccessRetained) {
			generation := entry.terminalGeneration
			entry.mu.Unlock()
			return &toolFeedbackTerminal{
				key: key, entry: entry, generation: generation, retain: retain, absorbed: true,
				traceGenerations: traceGenerations, claimedGenerations: claimedGenerations,
			}
		}
		if !entry.terminal {
			entry.terminalGeneration++
			entry.terminalPending = 0
			entry.terminalRetained = 0
			entry.terminalSuccess = toolFeedbackTerminalSuccessNone
		}
		entry.terminal = true
		entry.terminalUntil = time.Time{}
		entry.terminalPending++
		if retain {
			entry.terminalRetained++
		}
		generation := entry.terminalGeneration
		entry.mu.Unlock()

		return &toolFeedbackTerminal{
			key: key, entry: entry, generation: generation, retain: retain,
			traceGenerations: traceGenerations, claimedGenerations: claimedGenerations,
		}
	}
}

func normalizeToolFeedbackGenerations(generations []string) []string {
	normalized := make([]string, 0, len(generations))
	seen := make(map[string]struct{}, len(generations))
	for _, generation := range generations {
		generation = strings.TrimSpace(generation)
		if generation == "" {
			continue
		}
		if _, exists := seen[generation]; exists {
			continue
		}
		seen[generation] = struct{}{}
		normalized = append(normalized, generation)
	}
	return normalized
}

func (entry *toolFeedbackEntry) claimTerminalGenerations(generations []string) {
	for _, generation := range generations {
		if entry.activeGenerations == nil {
			entry.activeGenerations = make(map[string]struct{})
		}
		_, active := entry.activeGenerations[generation]
		if !active {
			entry.activeGenerations[generation] = struct{}{}
		}
		if entry.generationClaims == nil {
			entry.generationClaims = make(map[string]toolFeedbackGenerationClaim)
		}
		claim := entry.generationClaims[generation]
		claim.pending++
		claim.admitted = claim.admitted || !active
		entry.generationClaims[generation] = claim
	}
}

func (entry *toolFeedbackEntry) activeGenerationsSnapshot() []string {
	generations := make([]string, 0, len(entry.activeGenerations))
	for generation := range entry.activeGenerations {
		generations = append(generations, generation)
	}
	return generations
}

func (entry *toolFeedbackEntry) terminalizeGenerations(generations []string) {
	for _, generation := range generations {
		if _, exists := entry.terminalizedGenerations[generation]; exists {
			delete(entry.activeGenerations, generation)
			delete(entry.generationClaims, generation)
			continue
		}
		if entry.terminalizedGenerations == nil {
			entry.terminalizedGenerations = make(map[string]struct{})
		}
		entry.terminalizedGenerations[generation] = struct{}{}
		entry.terminalizedOrder = append(entry.terminalizedOrder, generation)
		delete(entry.activeGenerations, generation)
		delete(entry.generationClaims, generation)
	}
	for len(entry.terminalizedOrder) > toolFeedbackGenerationHistoryLimit {
		oldest := entry.terminalizedOrder[0]
		entry.terminalizedOrder = entry.terminalizedOrder[1:]
		delete(entry.terminalizedGenerations, oldest)
	}
}

func (entry *toolFeedbackEntry) releaseTerminalClaims(generations []string) {
	for _, generation := range generations {
		claim, exists := entry.generationClaims[generation]
		if !exists {
			continue
		}
		claim.pending--
		if claim.pending > 0 {
			entry.generationClaims[generation] = claim
			continue
		}
		if claim.admitted {
			delete(entry.activeGenerations, generation)
		}
		delete(entry.generationClaims, generation)
	}
}

func (entry *toolFeedbackEntry) settleTerminalGenerations(terminal *toolFeedbackTerminal, success bool) {
	if success && terminal.retain {
		entry.terminalizeGenerations(terminal.traceGenerations)
		return
	}
	entry.releaseTerminalClaims(terminal.claimedGenerations)
}

func resetToolFeedbackTerminal(entry *toolFeedbackEntry) {
	entry.terminal = false
	entry.terminalUntil = time.Time{}
	entry.terminalPending = 0
	entry.terminalRetained = 0
	entry.terminalSuccess = toolFeedbackTerminalSuccessNone
	entry.terminalGeneration++
}

func (c *ToolFeedbackCoordinator) CompleteTerminal(
	ctx context.Context,
	terminal *toolFeedbackTerminal,
	success bool,
) {
	if c == nil || terminal == nil || terminal.entry == nil {
		return
	}
	separate := c.separateMessages()
	entry := terminal.entry
	entry.opMu.Lock()
	c.retryPendingCleanup(ctx, terminal.key, entry)
	entry.mu.Lock()
	if terminal.completed || entry.retired {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	terminal.completed = true
	if terminal.absorbed {
		entry.settleTerminalGenerations(terminal, success)
		refreshRetainedTombstone := success && terminal.retain && entry.terminal &&
			entry.terminalGeneration == terminal.generation &&
			entry.terminalSuccess == toolFeedbackTerminalSuccessRetained
		if refreshRetainedTombstone {
			entry.terminalUntil = time.Now().Add(toolFeedbackTerminalTombstoneTTL)
		}
		entry.mu.Unlock()
		entry.opMu.Unlock()
		if refreshRetainedTombstone {
			c.scheduleTerminalMaintenance(terminal, toolFeedbackTerminalTombstoneTTL)
		}
		return
	}
	if !entry.terminal || entry.terminalGeneration != terminal.generation {
		entry.settleTerminalGenerations(terminal, success)
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	entry.settleTerminalGenerations(terminal, success)
	if entry.terminalPending > 0 {
		entry.terminalPending--
	}
	if terminal.retain && entry.terminalRetained > 0 {
		entry.terminalRetained--
	}
	previousSuccess := entry.terminalSuccess
	if previousSuccess == toolFeedbackTerminalSuccessRetained {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	if success {
		if terminal.retain {
			entry.terminalSuccess = toolFeedbackTerminalSuccessRetained
			entry.terminalUntil = time.Now().Add(toolFeedbackTerminalTombstoneTTL)
		} else if previousSuccess == toolFeedbackTerminalSuccessNone {
			entry.terminalSuccess = toolFeedbackTerminalSuccessTransient
		}
	} else if previousSuccess == toolFeedbackTerminalSuccessNone {
		if entry.terminalPending > 0 {
			entry.mu.Unlock()
			entry.opMu.Unlock()
			return
		}
		entry.terminal = false
		entry.terminalUntil = time.Time{}
		current := entry.current
		entry.mu.Unlock()
		if current.messageID != "" && current.editable {
			c.animator.Record(terminal.key, current.messageID, current.content)
		} else if current.messageID == "" {
			c.retireIdleEntryLocked(terminal.key, entry)
		}
		entry.opMu.Unlock()
		return
	}

	clearCurrent := success && previousSuccess == toolFeedbackTerminalSuccessNone
	if clearCurrent {
		current := entry.current
		entry.current = trackedToolFeedbackMessage{}
		if !separate && current.messageID != "" && current.operations.delete != nil {
			entry.pendingCleanup = append(entry.pendingCleanup, newPendingToolFeedbackCleanup(current, nil))
		}
	}
	entry.mu.Unlock()
	if clearCurrent {
		c.animator.Clear(terminal.key)
		c.retryPendingCleanup(ctx, terminal.key, entry)
	}
	entry.mu.Lock()
	pendingCleanup := len(entry.pendingCleanup) != 0
	if entry.terminalSuccess == toolFeedbackTerminalSuccessTransient && entry.terminalRetained > 0 {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		if pendingCleanup {
			c.scheduleCleanupMaintenance(terminal.key, entry, toolFeedbackCleanupRetryDelay)
		}
		return
	}
	retained := entry.terminalSuccess == toolFeedbackTerminalSuccessRetained
	if !retained {
		entry.terminal = false
		entry.terminalUntil = time.Time{}
		entry.terminalPending = 0
		entry.terminalRetained = 0
		entry.terminalSuccess = toolFeedbackTerminalSuccessNone
		if !pendingCleanup {
			entry.retired = true
		}
	}
	entry.mu.Unlock()
	entry.opMu.Unlock()

	if pendingCleanup {
		c.scheduleCleanupMaintenance(terminal.key, entry, toolFeedbackCleanupRetryDelay)
	}
	if retained {
		c.scheduleTerminalMaintenance(terminal, toolFeedbackTerminalTombstoneTTL)
	} else if !pendingCleanup {
		c.removeEntry(terminal.key, entry)
	}
}

func (c *ToolFeedbackCoordinator) Dismiss(ctx context.Context, key string) {
	terminal := c.BeginTerminal(key)
	c.CompleteTerminal(ctx, terminal, true)
}

func (c *ToolFeedbackCoordinator) DismissTransient(ctx context.Context, key string) {
	terminal := c.BeginTransientTerminal(key)
	c.CompleteTerminal(ctx, terminal, true)
}

// Pause retains the current carrier but stops animation until the next
// delivery for the same logical key resumes it.
func (c *ToolFeedbackCoordinator) Pause(key string) {
	if c == nil || strings.TrimSpace(key) == "" {
		return
	}
	key = strings.TrimSpace(key)
	entry := c.findEntry(key)
	if entry == nil {
		return
	}
	entry.opMu.Lock()
	entry.mu.Lock()
	if entry.retired || entry.terminal || entry.current.messageID == "" {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	entry.paused = true
	entry.mu.Unlock()
	c.animator.Clear(key)
	entry.opMu.Unlock()
}

func (c *ToolFeedbackCoordinator) ReleaseTerminal(key string) {
	if c == nil || strings.TrimSpace(key) == "" {
		return
	}
	key = strings.TrimSpace(key)
	entry := c.findEntry(key)
	if entry == nil {
		return
	}
	entry.opMu.Lock()
	entry.mu.Lock()
	if entry.retired || !entry.terminal || entry.current.messageID != "" ||
		entry.sending || len(entry.pendingCleanup) != 0 || len(entry.generationClaims) != 0 {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	entry.retired = true
	entry.mu.Unlock()
	entry.opMu.Unlock()
	c.removeEntry(key, entry)
}

func (c *ToolFeedbackCoordinator) RetireChannel(ctx context.Context, channelName string) {
	if c == nil || strings.TrimSpace(channelName) == "" {
		return
	}
	prefix := strings.TrimSpace(channelName) + ":"
	type retiredFeedback struct {
		key       string
		chatID    string
		messageID string
		delete    func(context.Context, string, string) error
	}
	var retired []retiredFeedback
	type keyedEntry struct {
		key   string
		entry *toolFeedbackEntry
	}
	var entries []keyedEntry
	c.mu.Lock()
	for key, entry := range c.entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		entries = append(entries, keyedEntry{key: key, entry: entry})
	}
	c.mu.Unlock()
	for _, candidate := range entries {
		key, entry := candidate.key, candidate.entry
		entry.opMu.Lock()
		entry.mu.Lock()
		entry.retired = true
		pending := append([]pendingToolFeedbackCleanup(nil), entry.pendingCleanup...)
		messages := make([]trackedToolFeedbackMessage, 0, len(pending)+1)
		for _, cleanup := range pending {
			messages = append(messages, cleanup.message)
		}
		messages = append(messages, entry.current)
		entry.current = trackedToolFeedbackMessage{}
		entry.pendingCleanup = nil
		entry.mu.Unlock()
		entry.opMu.Unlock()
		for _, message := range messages {
			retired = append(retired, retiredFeedback{
				key: key, chatID: message.chatID, messageID: message.messageID,
				delete: message.operations.delete,
			})
		}
		c.removeEntry(key, entry)
	}
	for _, feedback := range retired {
		c.animator.Clear(feedback.key)
		deleteToolFeedbackMessage(ctx, feedback.delete, feedback.chatID, feedback.messageID)
	}
}

func (c *ToolFeedbackCoordinator) StopAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.stopped = true
	for _, entry := range c.entries {
		entry.mu.Lock()
		entry.retired = true
		entry.mu.Unlock()
	}
	c.entries = make(map[string]*toolFeedbackEntry)
	c.mu.Unlock()
	c.animator.StopAll()
}

func (c *ToolFeedbackCoordinator) ActiveCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, entry := range c.entries {
		entry.mu.Lock()
		if !entry.retired && (entry.sending || entry.current.messageID != "" ||
			len(entry.pendingCleanup) != 0) {
			count++
		}
		entry.mu.Unlock()
	}
	return count
}

func (c *ToolFeedbackCoordinator) editAnimated(
	ctx context.Context,
	key string,
	messageID string,
	content string,
) error {
	entry := c.findEntry(key)
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	if entry.retired || entry.terminal || entry.current.messageID != messageID {
		entry.mu.Unlock()
		return nil
	}
	chatID := entry.current.chatID
	editFn := entry.current.operations.edit
	entry.mu.Unlock()
	if editFn == nil {
		return nil
	}
	return editFn(ctx, chatID, messageID, content)
}

func (c *ToolFeedbackCoordinator) lockEntry(key string) *toolFeedbackEntry {
	for {
		c.mu.Lock()
		if c.stopped {
			c.mu.Unlock()
			return nil
		}
		entry := c.entries[key]
		if entry == nil {
			entry = &toolFeedbackEntry{}
			c.entries[key] = entry
		}
		c.mu.Unlock()
		entry.opMu.Lock()
		entry.mu.Lock()
		retired := entry.retired
		entry.mu.Unlock()
		if !retired {
			return entry
		}
		entry.opMu.Unlock()
	}
}

func (c *ToolFeedbackCoordinator) getOrCreateEntry(key string) *toolFeedbackEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &toolFeedbackEntry{}
		c.entries[key] = entry
	}
	return entry
}

func (c *ToolFeedbackCoordinator) findEntry(key string) *toolFeedbackEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key]
}

func (c *ToolFeedbackCoordinator) retireIdleEntryLocked(key string, entry *toolFeedbackEntry) {
	entry.mu.Lock()
	if entry.terminal || entry.sending || entry.current.messageID != "" ||
		len(entry.pendingCleanup) != 0 || len(entry.generationClaims) != 0 {
		entry.mu.Unlock()
		return
	}
	entry.retired = true
	entry.mu.Unlock()
	c.removeEntry(key, entry)
}

func deleteToolFeedbackMessage(
	ctx context.Context,
	deleteFn func(context.Context, string, string) error,
	chatID string,
	messageID string,
) {
	_ = tryDeleteToolFeedbackMessage(ctx, deleteFn, chatID, messageID)
}

func tryDeleteToolFeedbackMessage(
	ctx context.Context,
	deleteFn func(context.Context, string, string) error,
	chatID string,
	messageID string,
) error {
	if deleteFn == nil || strings.TrimSpace(chatID) == "" || strings.TrimSpace(messageID) == "" {
		return nil
	}
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return deleteFn(deleteCtx, chatID, messageID)
}

func (c *ToolFeedbackCoordinator) removeEntry(key string, entry *toolFeedbackEntry) {
	c.mu.Lock()
	if c.entries[key] == entry {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

func (c *ToolFeedbackCoordinator) scheduleTerminalMaintenance(
	terminal *toolFeedbackTerminal,
	delay time.Duration,
) {
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() { c.maintainTerminal(terminal) })
}

func (c *ToolFeedbackCoordinator) maintainTerminal(terminal *toolFeedbackTerminal) {
	if c == nil || terminal == nil || terminal.entry == nil || !terminal.retain {
		return
	}
	entry := terminal.entry
	entry.opMu.Lock()
	entry.mu.Lock()
	if entry.retired || !entry.terminal || entry.terminalSuccess != toolFeedbackTerminalSuccessRetained ||
		entry.terminalGeneration != terminal.generation {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	if time.Now().Before(entry.terminalUntil) {
		delay := time.Until(entry.terminalUntil)
		entry.mu.Unlock()
		entry.opMu.Unlock()
		c.scheduleTerminalMaintenance(terminal, delay)
		return
	}
	if len(entry.pendingCleanup) != 0 || len(entry.generationClaims) != 0 {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		c.scheduleTerminalMaintenance(terminal, toolFeedbackCleanupRetryDelay)
		return
	}
	entry.retired = true
	entry.mu.Unlock()
	entry.opMu.Unlock()
	c.removeEntry(terminal.key, entry)
}

func (c *ToolFeedbackCoordinator) scheduleCleanupMaintenance(
	key string,
	entry *toolFeedbackEntry,
	delay time.Duration,
) {
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() { c.maintainCleanup(key, entry) })
}

func (c *ToolFeedbackCoordinator) maintainCleanup(key string, entry *toolFeedbackEntry) {
	if c == nil || entry == nil {
		return
	}
	entry.opMu.Lock()
	entry.mu.Lock()
	if entry.retired {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		return
	}
	entry.mu.Unlock()
	c.retryPendingCleanup(context.Background(), key, entry)
	entry.mu.Lock()
	if len(entry.pendingCleanup) != 0 {
		entry.mu.Unlock()
		entry.opMu.Unlock()
		c.scheduleCleanupMaintenance(key, entry, toolFeedbackCleanupRetryDelay)
		return
	}
	retire := !entry.terminal && !entry.sending && entry.current.messageID == "" &&
		len(entry.generationClaims) == 0
	pendingClaims := len(entry.generationClaims) != 0
	if retire {
		entry.retired = true
	}
	entry.mu.Unlock()
	entry.opMu.Unlock()
	if retire {
		c.removeEntry(key, entry)
	} else if pendingClaims {
		c.scheduleCleanupMaintenance(key, entry, toolFeedbackCleanupRetryDelay)
	}
}

func (c *ToolFeedbackCoordinator) separateMessages() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.separate
}
