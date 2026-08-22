// MintClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/constants"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/state"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

type AgentLoop struct {
	// Core dependencies
	bus      interfaces.MessageBus
	cfg      *config.Config
	registry *AgentRegistry
	// runtimeProfile marks a pre-construction loop whose updates require restart.
	// Nil selects the legacy config-only loop with its existing reload behavior.
	runtimeProfile *RuntimeProfile
	state          *state.Manager

	// Runtime event system
	runtimeEvents      runtimeevents.Bus
	ownsRuntimeEvents  bool
	runtimeEventLogMu  sync.RWMutex
	runtimeEventLogger *runtimeEventLogger
	runtimeEventLogSub runtimeevents.Subscription
	traceCapture       *traceCaptureManager
	hooks              *HookManager

	// Runtime state
	running atomic.Bool
	// startupResult carries the outcome of Run's initialization phase (hooks
	// and MCP). Buffered so Run never blocks when no caller waits for startup.
	startupResult              chan error
	contextManager             ContextManager
	contextManagerInitErr      error
	runtimeProfileInitErr      error
	fallback                   *providers.FallbackChain
	modelExecution             *modelExecutionManager
	channelManager             interfaces.ChannelManager
	mediaStore                 media.MediaStore
	outboundOutbox             *outbox.Coordinator
	transcriber                asr.Transcriber
	cmdRegistry                *commands.Registry
	mcp                        mcpRuntime
	hookRuntime                hookRuntime
	steering                   *steeringQueue
	compactionRunner           *backgroundCompactionRunner
	pendingSkills              sync.Map
	pendingStops               sync.Map
	asyncCompletions           sync.Map
	taskRegistries             sync.Map
	interactionRegistries      sync.Map
	interactionResolutions     sync.Map
	interactionResumeFlights   sync.Map
	interactionCatalog         *interactions.WorkspaceCatalog
	interactionCatalogMu       sync.Mutex
	interactionRecoveryRunning atomic.Bool
	runtimeTools               map[string]RuntimeToolFactory
	runtimeAgentTools          map[string]RuntimeAgentToolFactory
	runtimeToolDecorators      map[string]RuntimeToolDecoratorFactory
	mu                         sync.RWMutex

	isolatedToolBootstrap  bool
	isolatedSkillBootstrap bool

	// workerSem limits concurrent turn processing workers.
	workerSem chan struct{}
	// agentTurnAdmissions applies optional per-agent limits across every turn entry path.
	agentTurnAdmissions *agentTurnAdmissionController

	// activeTurnStates tracks active turns per session to prevent duplicates.
	activeTurnStates    sync.Map
	activeRouteSessions sync.Map
	sessionNow          func() time.Time
	subTurnCounter      atomic.Int64

	turnSeq atomic.Uint64

	activeRequests *activeRequestCounter

	reloadFunc func() error

	providerFactory func(*config.ModelConfig) (providers.LLMProvider, string, error)
}

// processOptions configures how a message is processed
type processOptions struct {
	Dispatch                     DispatchRequest // Normalized routed request boundary for this turn
	ModelBinding                 effectiveModelBinding
	SessionKey                   string   // Session identifier for history/context
	SessionAliases               []string // Compatibility aliases for the session key
	TaskID                       string   // Durable task owning this turn, when one exists
	ObjectiveChecklist           []runtimeObjectiveItem
	InteractionWorkspace         string              // Workspace owning inbound interaction routing
	InteractionSessionKey        string              // User-facing session that owns interaction answers
	InteractionRouteKey          string              // Routed scope key that owns interaction answers
	InteractionOriginExecution   string              // Original non-approval execution identity for a continuation
	InteractionOriginContext     *bus.InboundContext // Original tool identity for a continuation
	TurnStatus                   *TurnEndStatus
	TurnResult                   *turnResult         // Optional caller-owned terminal snapshot
	ApprovalGrant                *ToolApprovalGrant  // Internal one-time durable approval capability
	Channel                      string              // Target channel for tool execution
	ChatID                       string              // Target chat ID for tool execution
	MessageID                    string              // Current inbound platform message ID
	ReplyToMessageID             string              // Current inbound reply target message ID
	SenderID                     string              // Current sender ID for dynamic context
	SenderDisplayName            string              // Current sender display name for dynamic context
	CodingContext                CodingPromptContext // Runtime-owned coding identity for prompt assembly
	UserMessage                  string              // User message content (may include prefix)
	ForcedSkills                 []string            // Skills explicitly requested for this message
	TurnProfile                  config.EffectiveTurnProfile
	SystemPromptOverride         string                    // Override the default system prompt (Used by SubTurns)
	Media                        []string                  // media:// refs from inbound message
	InitialSteeringMessages      []providers.Message       // Steering messages from refactor/agent
	ActiveGoal                   string                    // Dynamic session goal reminder for normal LLM turns
	DefaultResponse              string                    // Response when LLM returns empty
	EnableSummary                bool                      // Whether to trigger summarization
	SuppressBackgroundCompaction bool                      // Whether this short-lived caller can outlive background work
	TreatInputAsPrompt           bool                      // Whether slash-prefixed input bypasses personal command dispatch
	SendResponse                 bool                      // Whether to send response via bus
	ExpectFinalDelivery          bool                      // Whether an outer coordinator will publish the final response
	FinalDeliveryObservation     *finalDeliveryObservation // Collects state settled by an outer final response
	AllowInterimMintClawPublish  bool                      // Whether mintclaw tool-call interim text can be published when SendResponse is false
	DirectStreaming              bool                      // Whether a direct frontend supplies its own stream delegate
	OnTurnReady                  func()                    // Signals that direct turn controls can target the registered owner
	SuppressToolUserDelivery     bool                      // Whether direct user-facing delivery from tools is suppressed for this turn
	SuppressToolFeedback         bool                      // Whether to suppress inline tool feedback messages
	NoHistory                    bool                      // If true, don't load session history (for heartbeat)
	ExcludeInheritedNodeFiles    bool                      // Remove inherited node file tools from this internal turn
	SkipInitialSteeringPoll      bool                      // If true, skip the steering poll at loop start (used by Continue)
	InboundContext               *bus.InboundContext       // Normalized inbound facts for events/hooks
	RouteResult                  *routing.ResolvedRoute    // Route decision snapshot for events/hooks
	SessionScope                 *session.SessionScope     // Session scope snapshot for events/hooks
}

type continuationTarget struct {
	finalDeliveryObservation
	AgentID                string
	SessionKey             string
	Channel                string
	ChatID                 string
	Workspace              string
	holdSteeringSettlement bool
}

type finalDeliveryObservation struct {
	traceScopes       []runtimeevents.TraceScope
	responseMetadata  bus.OutboundMetadata
	unsettledSteering []providers.Message
}

const (
	defaultResponse            = "The model returned an empty response. This may indicate a provider error or token limit."
	toolLimitResponse          = "I've reached `max_tool_iterations` without a final response. Increase `max_tool_iterations` in config.json if this task needs more tool steps."
	handledToolResponseSummary = "Requested output delivered via tool attachment."
	sessionKeyAgentPrefix      = "agent:"
	pendingTurnPrefix          = "pending-"
	metadataKeyMessageKind     = bus.OutboundMetadataKeyMessageKind
	metadataKeyToolCalls       = bus.OutboundMetadataKeyToolCalls
	metadataKeyOutboundKind    = bus.OutboundMetadataKeyOutboundKind
	metadataKeyModelName       = bus.OutboundMetadataKeyModelName
	metadataKeyDefaultModel    = bus.OutboundMetadataKeyDefaultModel
	metadataKeyUsageInput      = bus.OutboundMetadataKeyUsageInput
	metadataKeyUsageOutput     = bus.OutboundMetadataKeyUsageOutput
	metadataKeyUsageTotal      = bus.OutboundMetadataKeyUsageTotal
	messageKindThought         = bus.OutboundMessageKindThought
	messageKindToolFeedback    = bus.OutboundMessageKindToolFeedback
	messageKindToolCalls       = bus.OutboundMessageKindToolCalls
	messageKindFinalReply      = bus.OutboundMessageKindFinalReply
	outboundKindFinal          = bus.OutboundKindFinal
	metadataKeyAccountID       = "account_id"
	metadataKeyGuildID         = "guild_id"
	metadataKeyTeamID          = "team_id"
	metadataKeyReplyToMessage  = "reply_to_message_id"
	metadataKeyParentPeerKind  = "parent_peer_kind"
	metadataKeyParentPeerID    = "parent_peer_id"
)

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).

func (al *AgentLoop) Run(ctx context.Context) error {
	if al.contextManagerInitErr != nil {
		al.signalStartup(al.contextManagerInitErr)
		return al.contextManagerInitErr
	}
	al.running.Store(true)

	if err := al.ensureHooksInitialized(ctx); err != nil {
		al.signalStartup(err)
		return err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		al.signalStartup(err)
		return err
	}
	al.signalStartup(nil)
	if reconciler, ok := al.contextManager.(interface {
		StartBackgroundReconciliation(context.Context)
	}); ok {
		reconciler.StartBackgroundReconciliation(ctx)
	}

	ingress := newInboundTurnCoordinator(al)
	idleTicker := time.NewTicker(100 * time.Millisecond)
	defer idleTicker.Stop()
	interactionTicker := time.NewTicker(time.Minute)
	defer interactionTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idleTicker.C:
			if !al.running.Load() {
				return nil
			}
		case <-interactionTicker.C:
			al.scheduleHumanInteractionRecovery(ctx)
		case msg, ok := <-al.bus.InboundChan():
			if !ok {
				return nil
			}
			ingress.handleInbound(ctx, msg)
		case msg, ok := <-al.bus.ObservedChan():
			if !ok {
				return nil
			}
			al.observeMessage(ctx, msg)
		}
	}
}

// signalStartup records the outcome of Run's initialization phase. It is a
// no-op when no startup waiter is configured (non-gateway callers) and never
// blocks, so repeated Run invocations cannot stall.
func (al *AgentLoop) signalStartup(err error) {
	if al.startupResult == nil {
		return
	}
	select {
	case al.startupResult <- err:
	default:
	}
}

// WaitStartup blocks until Run completes initialization (hooks/MCP) or ctx is
// canceled, returning the init outcome so callers can fail before readiness.
func (al *AgentLoop) WaitStartup(ctx context.Context) error {
	if al.startupResult == nil {
		return nil
	}
	select {
	case err := <-al.startupResult:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processMessageSync processes a message synchronously (for non-routable/system messages).

// runTurnWithSteering runs a complete turn for a message and drains its steering queue.

// maybePublishError publishes an error response unless the error is context.Canceled.
// Returns true if processing should continue (non-cancellation error or no error),
// false if context was canceled and the caller should return.

// publishResponseOrError publishes the response, or an error message if processing failed.

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

// Close releases resources held by agent session stores. Call after Stop.
func (al *AgentLoop) Close() {
	_ = al.CloseContext(context.Background())
}

// CloseContext releases resources while bounding MCP operation drain and
// process cleanup by ctx.
func (al *AgentLoop) CloseContext(ctx context.Context) error {
	var closeErrors []error
	if err := al.closeOutboundOutbox(); err != nil {
		logger.ErrorCF("agent", "Failed to close outbound outbox", map[string]any{"error": err.Error()})
		closeErrors = append(closeErrors, fmt.Errorf("close outbound outbox: %w", err))
	}
	mcpManager := al.mcp.takeManager()

	if mcpManager != nil {
		if err := mcpManager.CloseContext(ctx); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
			al.mcp.restoreManager(mcpManager)
			closeErrors = append(closeErrors, fmt.Errorf("close MCP manager: %w", err))
		}
	}
	if err := closeContextManager(al.contextManager); err != nil {
		logger.ErrorCF("agent", "Failed to close context manager", map[string]any{
			"error": err.Error(),
		})
		closeErrors = append(closeErrors, fmt.Errorf("close context manager: %w", err))
	}
	al.GetRegistry().Close()
	if al.hooks != nil {
		al.hooks.Close()
	}
	al.closeRuntimeEventLogger()
	if al.traceCapture != nil {
		al.traceCapture.close()
	}
	if al.runtimeEvents != nil && al.ownsRuntimeEvents {
		if err := al.runtimeEvents.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close runtime event bus",
				map[string]any{
					"error": err.Error(),
				})
			closeErrors = append(closeErrors, fmt.Errorf("close runtime event bus: %w", err))
		}
	}
	return errors.Join(closeErrors...)
}

// MountHook registers an in-process hook on the agent loop.

// UnmountHook removes a previously registered in-process hook.

type turnEventScope struct {
	agentID    string
	workspace  string
	sessionKey string
	turnID     string
	context    *TurnContext
}

// ValidateConfigReload reports restart-only changes without mutating live runtime state.
func (al *AgentLoop) ValidateConfigReload(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}
	if al.runtimeProfile != nil {
		return fmt.Errorf("runtime-profile loops require restart; hot reload is not supported")
	}
	if _, stateless := al.contextManager.(*noneContextManager); !stateless ||
		contextManagerConfigName(cfg) != "none" {
		return fmt.Errorf("context manager changes require restart; hot reload is supported only for none")
	}
	return nil
}

// ReloadProviderAndConfig atomically swaps the provider and config with proper synchronization.
// It uses a context to allow timeout control from the caller.
// Returns an error if the reload fails or context is canceled.
func (al *AgentLoop) ReloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) error {
	resumeTurns, err := al.QuiesceTurns(ctx)
	if err != nil {
		return fmt.Errorf("quiesce turns for config reload: %w", err)
	}
	defer resumeTurns()

	prepared, err := al.PrepareConfigReload(ctx, provider, cfg)
	if err != nil {
		return err
	}
	defer prepared.Abort()
	return prepared.Commit(ctx)
}

// GetRegistry returns the current registry (thread-safe)

// GetConfig returns the current config (thread-safe)

// SetMediaStore injects a MediaStore for media lifecycle management.

// SetTranscriber injects a voice transcriber for agent-level audio transcription.

// SetReloadFunc sets the callback function for triggering config reload.

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.

// sendTranscriptionFeedback sends feedback to the user with the result of
// audio transcription if the option is enabled. It uses Manager.SendMessage
// which executes synchronously (rate limiting, splitting, retry) so that
// ordering with the subsequent placeholder is guaranteed.

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.

// runAgentLoop remains the top-level shell that starts a turn and publishes
// any post-turn work. runTurn owns the full turn lifecycle.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	return al.runAgentLoopWithExecution(ctx, agent, opts, nil)
}

type pipelineTurnExecutionFunc func(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	host turnRuntimeHost,
	pipeline *Pipeline,
) (turnResult, TurnEndStatus, error)

func (al *AgentLoop) runAgentLoopWithExecution(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	execute pipelineTurnExecutionFunc,
) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("agent is unavailable")
	}
	admittedCtx, releaseAdmission, err := al.acquireAgentTurn(ctx, agent.ID)
	if err != nil {
		return "", err
	}
	defer releaseAdmission()
	ctx = admittedCtx

	currentAgent, changed, err := al.currentAgentGeneration(agent)
	if err != nil {
		return "", err
	}
	if changed {
		agent = currentAgent
		if opts.ExcludeInheritedNodeFiles {
			agent = agentWithoutInheritedNodeFileTools(agent)
		}
		binding := al.bindEffectiveModel(opts.ModelBinding.RouteSessionKey, agent)
		defer binding.Cleanup()
		opts.ModelBinding = binding
	}

	opts = normalizeProcessOptions(opts)
	opts, err = resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return "", err
	}
	al.applyActiveGoalPrompt(&opts)

	// Record last channel for heartbeat notifications (skip internal channels and cli)
	if opts.Dispatch.Channel() != "" &&
		opts.Dispatch.ChatID() != "" &&
		!constants.IsInternalChannel(opts.Dispatch.Channel()) {
		channelKey := fmt.Sprintf("%s:%s", opts.Dispatch.Channel(), opts.Dispatch.ChatID())
		if recordErr := al.RecordLastChannel(channelKey); recordErr != nil {
			logger.WarnCF(
				"agent",
				"Failed to record last channel",
				map[string]any{"error": recordErr.Error()},
			)
		}
	}

	ensureSessionMetadata(
		agent.Sessions,
		opts.Dispatch.SessionKey,
		opts.Dispatch.SessionScope,
		opts.Dispatch.SessionAliases,
	)

	turnScope := al.newTurnEventScope(
		agent.ID,
		agent.Workspace,
		opts.Dispatch.SessionKey,
		newTurnContext(
			opts.Dispatch.InboundContext,
			opts.Dispatch.RouteResult,
			opts.Dispatch.SessionScope,
		),
	)
	ts := newTurnState(agent, opts, turnScope)
	if bindErr := bindNodeFileMediaOwner(al.mediaStore, ts, opts.Dispatch.Media); bindErr != nil {
		logger.WarnCF("media", "Failed to bind inbound media ownership", map[string]any{
			"agent_id":    agent.ID,
			"media_count": len(opts.Dispatch.Media),
		})
	}
	if opts.FinalDeliveryObservation != nil {
		opts.FinalDeliveryObservation.observeTurn(
			runtimeevents.NewTraceScope(turnScope.workspace, turnScope.turnID),
		)
	}
	pipeline := NewPipeline(al)
	var result turnResult
	if execute == nil {
		result, err = al.runTurn(ctx, ts, pipeline)
	} else {
		result, err = al.runTurnLifecycle(ctx, ts, pipeline, execute)
	}
	if err != nil {
		return "", err
	}
	if opts.TurnResult != nil {
		*opts.TurnResult = result
	}
	if opts.TurnStatus != nil {
		*opts.TurnStatus = result.status
	}
	if opts.FinalDeliveryObservation != nil &&
		result.status != TurnEndStatusAborted &&
		result.status != TurnEndStatusSuspended {
		opts.FinalDeliveryObservation.observeResponse(outboundMetadataForTurnResult(result))
	}
	if result.status == TurnEndStatusAborted {
		return "", nil
	}
	if result.status == TurnEndStatusSuspended {
		return "", nil
	}

	for _, followUp := range result.followUps {
		if pubErr := al.bus.PublishInbound(ctx, followUp); pubErr != nil {
			logger.WarnCF("agent", "Failed to publish follow-up after turn",
				map[string]any{
					"turn_id": ts.turnID,
					"error":   pubErr.Error(),
				})
		}
	}

	al.deliverFinalTurnResult(
		ctx,
		runtimeevents.NewTraceScope(turnScope.workspace, turnScope.turnID),
		agent,
		opts,
		result,
	)

	if result.finalContent != "" {
		responsePreview := utils.Truncate(result.finalContent, 120)
		logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
			map[string]any{
				"agent_id":     agent.ID,
				"session_key":  opts.Dispatch.SessionKey,
				"iterations":   ts.currentIteration(),
				"final_length": len(result.finalContent),
			})
	}

	al.compactAfterFinalDelivery(ctx, agent, opts, result)

	return result.finalContent, nil
}

func (al *AgentLoop) compactAfterFinalDelivery(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	result turnResult,
) {
	if !result.compactAfterDelivery || al.contextManager == nil || agent == nil {
		return
	}
	sessionKey := opts.Dispatch.SessionKey
	if sessionKey == "" {
		return
	}
	al.scheduleBackgroundCompaction(
		agent,
		sessionKey,
		ContextCompressReasonSummarize,
		agent.ContextWindow,
		"final_reply",
	)
}

func (al *AgentLoop) scheduleBackgroundCompaction(
	agent *AgentInstance,
	sessionKey string,
	reason ContextCompressReason,
	budget int,
	messageKind string,
) {
	runner := al.backgroundCompactionRunner()
	if runner == nil {
		return
	}
	runner.scheduleBackgroundCompaction(agent, sessionKey, reason, budget, messageKind)
}

func agentMessageToolSentToTurnTarget(
	agent *AgentInstance,
	sessionKey string,
	dispatch DispatchRequest,
) bool {
	if agent == nil || agent.Tools == nil || strings.TrimSpace(sessionKey) == "" {
		return false
	}
	tool, ok := agent.Tools.Get("message")
	if !ok {
		return false
	}
	tracker, ok := tool.(interface {
		HasSentTo(sessionKey, channel, chatID string) bool
	})
	if !ok {
		return false
	}
	return tracker.HasSentTo(sessionKey, dispatch.Channel(), dispatch.ChatID())
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn. When model routing is configured and the incoming
// message scores below the complexity threshold, it returns the light model
// candidates instead of the primary ones.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn — tool follow-up iterations use the same tier as the initial call so
// that a multi-step tool chain doesn't switch models mid-way.

// resolveContextManager selects the ContextManager implementation based on config.

// GetStartupInfo returns information about loaded tools and skills for logging.

// formatMessagesForLog formats messages for logging

// formatToolsForLog formats tool definitions for logging

// summarizeSession summarizes the conversation history for a session.
// findNearestUserMessage finds the nearest user message to the given index.
// It searches backward first, then forward if no user message is found.
// retryLLMCall calls the LLM with retry logic.
// summarizeBatch summarizes a batch of messages.
// estimateTokens estimates the number of tokens in a message list.
// Counts Content, ToolCalls arguments, and ToolCallID metadata so that
// tool-heavy conversations are not systematically undercounted.

// askSideQuestion handles /btw commands by creating an isolated provider instance
// that doesn't share state with the main conversation provider.

// shallowCloneLLMOptions creates a shallow copy of LLM options map.
// Note: This is a shallow copy - nested maps/slices are shared.

// hasMediaRefs checks if any message has media references.

// isolatedSideQuestionProvider creates a separate provider instance for /btw commands
// to avoid sharing state with the main conversation provider.

// sideQuestionModelConfig resolves the model config for side questions.

// sideQuestionModelName determines which model name to use for side questions.

// modelNameFromIdentityKey extracts the model name from an identity key.

// closeProviderIfStateful closes a provider if it implements StatefulProvider.

// makePendingTurnID generates a unique turn ID for placeholder turns.
// Format: "pending-{sessionKey}-{sequence}"

// isNativeSearchProvider reports whether the given LLM provider implements
// provider capabilities and returns true for native search support.

// filterClientWebSearch returns a copy of tools with the client-side
// web_search tool removed. Used when native provider search is preferred.

// Helper to extract provider from registry for cleanup
