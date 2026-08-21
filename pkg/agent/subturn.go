package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/messageutil"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// ====================== Config & Constants ======================
const (
	// Default values for SubTurn configuration (used when config is not set or is zero)
	defaultMaxSubTurnDepth       = 3
	defaultMaxConcurrentSubTurns = 5
	defaultConcurrencyTimeout    = 30 * time.Second
	defaultSubTurnTimeout        = 5 * time.Minute
	// maxEphemeralHistorySize limits the number of messages stored in ephemeral sessions.
	// This prevents memory accumulation in long-running sub-turns.
	maxEphemeralHistorySize = 50
)

var (
	ErrDepthLimitExceeded   = errors.New("sub-turn depth limit exceeded")
	ErrInvalidSubTurnConfig = errors.New("invalid sub-turn config")
	ErrConcurrencyTimeout   = errors.New("timeout waiting for concurrency slot")
)

// getSubTurnConfig returns the effective SubTurn configuration with defaults applied.
func (al *AgentLoop) getSubTurnConfig() subTurnRuntimeConfig {
	cfg := al.cfg.Agents.Defaults.SubTurn

	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubTurnDepth
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentSubTurns
	}

	concurrencyTimeout := time.Duration(cfg.ConcurrencyTimeoutSec) * time.Second
	if concurrencyTimeout <= 0 {
		concurrencyTimeout = defaultConcurrencyTimeout
	}

	defaultTimeout := time.Duration(cfg.DefaultTimeoutMinutes) * time.Minute
	if defaultTimeout <= 0 {
		defaultTimeout = defaultSubTurnTimeout
	}

	return subTurnRuntimeConfig{
		maxDepth:           maxDepth,
		maxConcurrent:      maxConcurrent,
		concurrencyTimeout: concurrencyTimeout,
		defaultTimeout:     defaultTimeout,
		defaultTokenBudget: cfg.DefaultTokenBudget,
	}
}

// subTurnRuntimeConfig holds the effective runtime configuration for SubTurn execution.
type subTurnRuntimeConfig struct {
	maxDepth           int
	maxConcurrent      int
	concurrencyTimeout time.Duration
	defaultTimeout     time.Duration
	defaultTokenBudget int
}

// ====================== SubTurn Config ======================

// SubTurnConfig configures the execution of a child sub-turn.
//
// Usage Examples:
//
// Synchronous sub-turn (Async=false):
//
//	cfg := SubTurnConfig{
//	    Model: "gpt-4o-mini",
//	    SystemPrompt: "Analyze this code",
//	    Async: false,  // Result returned immediately
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Use result directly here
//	processResult(result)
//
// Asynchronous sub-turn (Async=true):
//
//	cfg := SubTurnConfig{
//	    Model: "gpt-4o-mini",
//	    SystemPrompt: "Background analysis",
//	    Async: true,  // Result delivered to channel
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Result also available in parent's pendingResults channel
//	// Parent turn will poll and process it in a later iteration
type SubTurnConfig struct {
	Model        string
	Tools        []toolshared.Tool
	SystemPrompt string
	MaxTokens    int

	// Async controls the result delivery mechanism:
	//
	// When Async = false (synchronous sub-turn):
	//   - The caller blocks until the sub-turn completes
	//   - The result is ONLY returned via the function return value
	//   - The result is NOT delivered to the parent's pendingResults channel
	//   - This prevents double delivery: caller gets result immediately, no need for channel
	//   - Use case: When the caller needs the result immediately to continue execution
	//   - Example: A tool that needs to process the sub-turn result before returning
	//
	// When Async = true (asynchronous sub-turn):
	//   - The sub-turn runs in the background (still blocks the caller, but semantically async)
	//   - The result is delivered to the parent's pendingResults channel
	//   - The result is ALSO returned via the function return value (for consistency)
	//   - The parent turn can poll pendingResults in later iterations to process results
	//   - Use case: Fire-and-forget operations, or when results are processed in batches
	//   - Example: Spawning multiple sub-turns in parallel and collecting results later
	//
	// IMPORTANT: The Async flag does NOT make the call non-blocking. It only controls
	// whether the result is delivered via the channel. For true non-blocking execution,
	// the caller must spawn the sub-turn in a separate goroutine.
	Async bool

	// Critical indicates this SubTurn's result is important and should continue
	// running even after the parent turn finishes gracefully.
	//
	// When parent finishes gracefully (Finish(false)):
	//   - Critical=true: SubTurn continues running, delivers result as orphan
	//   - Critical=false: SubTurn exits gracefully without error
	//
	// When parent finishes with hard abort (Finish(true)):
	//   - All SubTurns are canceled regardless of Critical flag
	Critical bool

	// Timeout is the maximum duration for this SubTurn.
	// If the SubTurn runs longer than this, it will be canceled.
	// Default is 5 minutes (defaultSubTurnTimeout) if not specified.
	Timeout time.Duration

	// MaxContextRunes limits the context size (in runes) passed to the SubTurn.
	// This prevents context window overflow by truncating message history before LLM calls.
	//
	// Values:
	//   0  = Auto-calculate based on model's ContextWindow * 0.75 (default, recommended)
	//   -1 = No limit (disable soft truncation, rely only on hard context errors)
	//   >0 = Use specified rune limit
	//
	// The soft limit acts as a first line of defense before hitting the provider's
	// hard context window limit. When exceeded, older messages are intelligently
	// truncated while preserving system messages and recent context.
	MaxContextRunes int

	// ActualSystemPrompt is injected as the true 'system' role message for the childAgent.
	// The legacy SystemPrompt field is actually used as the first 'user' message (task description).
	ActualSystemPrompt string

	// InitialMessages preloads the ephemeral session history before the agent loop starts.
	// Used by evaluator-optimizer patterns to pass the full worker context across multiple iterations.
	InitialMessages []providers.Message

	// InitialTokenBudget is a shared atomic counter for tracking remaining tokens.
	// If set, the SubTurn will inherit this budget and deduct tokens after each LLM call.
	// If nil, the SubTurn will inherit the parent's tokenBudget (if any).
	// Used by team tool to enforce token limits across all team members.
	InitialTokenBudget *atomic.Int64

	// TargetAgentID, when set, runs the sub-turn as the specified agent.
	// The target agent's workspace, model, tools, and system prompt are used
	// instead of the caller's. If empty, the sub-turn runs as the parent agent.
	TargetAgentID string

	// DeliveryMode controls user-facing delivery ownership for synchronous
	// delegate/sub-turn flows. Reuses the same enum names as async spawn:
	// parent_only, user_only, user_and_parent.
	DeliveryMode   toolshared.AsyncDeliveryMode
	TaskID         string
	ObjectiveItems []toolshared.ObjectiveSpec
}

// ====================== Context Keys ======================
type agentLoopKeyType struct{}

var agentLoopKey = agentLoopKeyType{}

// WithAgentLoop injects AgentLoop into context for tool access
func WithAgentLoop(ctx context.Context, al *AgentLoop) context.Context {
	return context.WithValue(ctx, agentLoopKey, al)
}

// AgentLoopFromContext retrieves AgentLoop from context
func AgentLoopFromContext(ctx context.Context) *AgentLoop {
	al, _ := ctx.Value(agentLoopKey).(*AgentLoop)
	return al
}

// ====================== Helper Functions ======================

func (al *AgentLoop) generateSubTurnID() string {
	return fmt.Sprintf("subturn-%d", al.subTurnCounter.Add(1))
}

// ====================== Core Function: spawnSubTurn ======================

// AgentLoopSpawner implements tools.SubTurnSpawner interface.
// This allows tools to spawn sub-turns without circular dependency.
type AgentLoopSpawner struct {
	al *AgentLoop
}

// SpawnSubTurn implements tools.SubTurnSpawner interface.
func (s *AgentLoopSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg tools.SubTurnConfig,
) (*toolshared.ToolResult, error) {
	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	// Convert tools.SubTurnConfig to agent.SubTurnConfig
	agentCfg := SubTurnConfig{
		Model:              cfg.Model,
		Tools:              cfg.Tools,
		SystemPrompt:       cfg.SystemPrompt,
		ActualSystemPrompt: cfg.ActualSystemPrompt,
		InitialMessages:    cfg.InitialMessages,
		InitialTokenBudget: cfg.InitialTokenBudget,
		MaxTokens:          cfg.MaxTokens,
		Async:              cfg.Async,
		Critical:           cfg.Critical,
		Timeout:            cfg.Timeout,
		MaxContextRunes:    cfg.MaxContextRunes,
		TargetAgentID:      cfg.TargetAgentID,
		DeliveryMode:       cfg.DeliveryMode,
		TaskID:             cfg.TaskID,
		ObjectiveItems:     append([]toolshared.ObjectiveSpec(nil), cfg.ObjectiveItems...),
	}

	return spawnSubTurn(ctx, s.al, parentTS, agentCfg)
}

// NewSubTurnSpawner creates a SubTurnSpawner for the given AgentLoop.
func NewSubTurnSpawner(al *AgentLoop) *AgentLoopSpawner {
	return &AgentLoopSpawner{al: al}
}

// SpawnSubTurn is the exported entry point for tools to spawn sub-turns.
// It retrieves AgentLoop and parent turnState from context and delegates to spawnSubTurn.
func SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*toolshared.ToolResult, error) {
	al := AgentLoopFromContext(ctx)
	if al == nil {
		return nil, errors.New(
			"AgentLoop not found in context - ensure context is properly initialized",
		)
	}

	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	return spawnSubTurn(ctx, al, parentTS, cfg)
}

func removeUserDeliveryTools(registry *tools.ToolRegistry) {
	if registry == nil {
		return
	}
	for _, name := range registry.List() {
		if isUserDeliveryToolName(name) {
			registry.Unregister(name)
		}
	}
}

func removeDurableInteractionTools(registry *tools.ToolRegistry) {
	if registry == nil {
		return
	}
	registry.Unregister("request_user_input")
}

func removeInheritedNodeFileTools(registry *tools.ToolRegistry) {
	if registry == nil {
		return
	}
	for _, name := range []string{
		"nodes_file_info",
		"nodes_upload",
		"nodes_download",
	} {
		registry.Unregister(name)
	}
}

func isUserDeliveryToolName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "message", "send_file", "send_tts", "reaction":
		return true
	}
	return false
}

func effectiveSubTurnDeliveryMode(cfg SubTurnConfig) toolshared.AsyncDeliveryMode {
	if cfg.DeliveryMode != "" {
		return cfg.DeliveryMode
	}
	if cfg.Async {
		return toolshared.AsyncDeliveryUserOnly
	}
	return toolshared.AsyncDeliveryParentOnly
}

func (al *AgentLoop) emitSubTurnAdmission(
	parentTS *turnState,
	childTurnID string,
	agentID string,
	stage string,
	state string,
	active int,
	limit int,
	waitStarted time.Time,
	timeout time.Duration,
) {
	al.emitEvent(
		runtimeevents.KindAgentSubTurnAdmission,
		parentTS.eventMeta("spawnSubTurn", "subturn.admission"),
		SubTurnAdmissionPayload{
			AgentID:      agentID,
			ChildTurnID:  childTurnID,
			ParentTurnID: parentTS.turnID,
			Stage:        stage,
			State:        state,
			Active:       active,
			Limit:        limit,
			WaitDuration: time.Since(waitStarted),
			WaitTimeout:  timeout,
		},
	)
}

func (al *AgentLoop) acquireSubTurnAgentAdmission(
	waitCtx context.Context,
	parentCtx context.Context,
	parentTS *turnState,
	childTurnID string,
	agentID string,
	timeout time.Duration,
) (context.Context, func(), error) {
	waitStarted := time.Now()
	active := 0
	limit := 0
	emitAdmission := func(state string) {
		al.emitSubTurnAdmission(
			parentTS, childTurnID, agentID, "target_agent", state,
			active, limit, waitStarted, timeout,
		)
	}
	admittedCtx, releaseAdmission, err := al.acquireAgentTurnObserved(
		waitCtx,
		agentID,
		func(currentActive, currentLimit int) {
			active = currentActive
			limit = currentLimit
			emitAdmission("queued")
			go al.toolFeedbackPublisher().publishSubTurnAdmissionWait(
				waitCtx, parentTS, agentID+" agent", timeout,
			)
		},
	)
	if err != nil {
		if parentCtx.Err() != nil {
			emitAdmission("canceled")
			return parentCtx, nil, parentCtx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			emitAdmission("timed_out")
			return parentCtx, nil, fmt.Errorf(
				"%w: agent %q remained busy for %v: %w",
				ErrConcurrencyTimeout,
				agentID,
				timeout,
				context.DeadlineExceeded,
			)
		}
		emitAdmission("failed")
		return parentCtx, nil, err
	}
	if waitCtx.Err() != nil {
		releaseAdmission()
		if parentCtx.Err() != nil {
			emitAdmission("canceled")
			return parentCtx, nil, parentCtx.Err()
		}
		emitAdmission("timed_out")
		return parentCtx, nil, fmt.Errorf(
			"%w: agent %q became available after %v: %w",
			ErrConcurrencyTimeout,
			agentID,
			timeout,
			context.DeadlineExceeded,
		)
	}
	emitAdmission("admitted")

	// Copy the acquired leases onto a context without the admission deadline.
	// The child execution timeout starts only after capacity becomes available.
	executionCtx, releaseExecutionAdmissions := inheritAgentTurnAdmissions(
		context.Background(),
		admittedCtx,
	)
	releaseAdmission()
	return executionCtx, releaseExecutionAdmissions, nil
}

func spawnSubTurn(
	ctx context.Context,
	al *AgentLoop,
	parentTS *turnState,
	cfg SubTurnConfig,
) (result *toolshared.ToolResult, err error) {
	deliveryMode := effectiveSubTurnDeliveryMode(cfg)

	// Get effective SubTurn configuration
	rtCfg := al.getSubTurnConfig()
	admissionCtx, cancelAdmission := context.WithTimeout(ctx, rtCfg.concurrencyTimeout)
	defer cancelAdmission()
	childID := al.generateSubTurnID()

	// 0. Acquire concurrency semaphore FIRST to ensure it's released even if early validation fails.
	// Blocks if parent already has maxConcurrentSubTurns running, with a timeout to prevent indefinite blocking.
	// Also respects context cancellation so we don't block forever if parent is aborted.
	// NOTE: The semaphore is released immediately after runTurn completes (not in a defer) to
	// ensure it is freed before the cleanup phase (async result delivery), which may block on
	// a full pendingResults channel. Holding the semaphore through cleanup would allow the
	// parent's goroutine to be blocked waiting for a semaphore slot while child turns are
	// blocked delivering results — a deadlock.
	var semAcquired bool
	if parentTS.concurrencySem != nil {
		waitStarted := time.Now()
		active := len(parentTS.concurrencySem)
		limit := cap(parentTS.concurrencySem)
		emitParentAdmission := func(state string) {
			al.emitSubTurnAdmission(
				parentTS, childID, "", "parent_capacity", state,
				active, limit, waitStarted, rtCfg.concurrencyTimeout,
			)
		}
		select {
		case parentTS.concurrencySem <- struct{}{}:
			semAcquired = true
		default:
			emitParentAdmission("queued")
			go al.toolFeedbackPublisher().publishSubTurnAdmissionWait(
				admissionCtx, parentTS, "parent subturn capacity", rtCfg.concurrencyTimeout,
			)
			select {
			case parentTS.concurrencySem <- struct{}{}:
				semAcquired = true
			case <-admissionCtx.Done():
			}
		}
		if semAcquired && admissionCtx.Err() != nil {
			<-parentTS.concurrencySem
			semAcquired = false
		}
		if !semAcquired {
			state := "timed_out"
			if ctx.Err() != nil {
				state = "canceled"
			}
			emitParentAdmission(state)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: all %d slots occupied for %v: %w",
				ErrConcurrencyTimeout,
				limit,
				rtCfg.concurrencyTimeout,
				context.DeadlineExceeded,
			)
		}
		emitParentAdmission("admitted")
		defer func() {
			if semAcquired {
				<-parentTS.concurrencySem
			}
		}()
	}

	// 1. Depth limit check
	if parentTS.depth >= rtCfg.maxDepth {
		logger.WarnCF("subturn", "Depth limit exceeded", map[string]any{
			"parent_id": parentTS.turnID,
			"depth":     parentTS.depth,
			"max_depth": rtCfg.maxDepth,
		})
		return nil, ErrDepthLimitExceeded
	}

	// 2. Config validation: Model is required unless TargetAgentID is set
	//    (the target agent provides its own model).
	if cfg.Model == "" && cfg.TargetAgentID == "" {
		return nil, ErrInvalidSubTurnConfig
	}

	// 3. Determine timeout for child SubTurn
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = rtCfg.defaultTimeout
	}

	// Resolve the agent instance for the child turn.
	// When TargetAgentID is set, look up that agent from the registry so the
	// child runs with the target's workspace, model, tools, and system prompt.
	// Otherwise fall back to the parent's agent (existing behavior).
	var baseAgent *AgentInstance
	if cfg.TargetAgentID != "" {
		var ok bool
		baseAgent, ok = al.registry.GetAgent(cfg.TargetAgentID)
		if !ok {
			return nil, fmt.Errorf("target agent %q not found in registry", cfg.TargetAgentID)
		}
	} else {
		baseAgent = parentTS.agent
		if baseAgent == nil {
			baseAgent = al.registry.GetDefaultAgent()
		}
	}
	if baseAgent == nil {
		return nil, errors.New("parent turnState has no agent instance")
	}

	// Wait for target-agent capacity independently from the execution timeout.
	// Same-agent children inherit the work-tree admission immediately. A child
	// targeting another agent fails as busy before any turn work starts.
	executionBase, releaseAdmissions, err := al.acquireSubTurnAgentAdmission(
		admissionCtx,
		ctx,
		parentTS,
		childID,
		baseAgent.ID,
		rtCfg.concurrencyTimeout,
	)
	if err != nil {
		return nil, err
	}
	cancelAdmission()
	defer releaseAdmissions()
	executionBase = inheritOutboundTransaction(executionBase, ctx)
	childCtx, cancel := context.WithTimeout(executionBase, timeout)
	defer cancel()

	modelBinding, err := al.buildSubagentChildBinding(parentTS, baseAgent)
	if err != nil {
		return nil, err
	}
	durableTask := strings.TrimSpace(cfg.TaskID) != ""
	ephemeralStore := newEphemeralSession(nil)
	agent := *baseAgent // shallow copy
	if durableTask {
		agent.Sessions = baseAgent.Sessions
	} else {
		agent.Sessions = ephemeralStore
	}
	if modelBinding.WorkspaceAgent == nil {
		modelBinding.WorkspaceAgent = &agent
	}
	executionState := modelBinding.ExecutionState()
	if executionState.Model != "" {
		agent.Model = executionState.Model
	}
	if executionState.Provider != nil {
		agent.Provider = executionState.Provider
	}
	if len(executionState.Candidates) > 0 {
		agent.Candidates = append([]providers.FallbackCandidate(nil), executionState.Candidates...)
	}
	if executionState.CandidateProviders != nil {
		agent.CandidateProviders = cloneCandidateProviderMap(executionState.CandidateProviders)
	}
	if executionState.Router != nil {
		agent.Router = executionState.Router
	}
	if len(executionState.LightCandidates) > 0 {
		agent.LightCandidates = append([]providers.FallbackCandidate(nil), executionState.LightCandidates...)
	}
	if executionState.LightProvider != nil {
		agent.LightProvider = executionState.LightProvider
	}
	agent.ThinkingLevel = executionState.ThinkingLevel
	agent.ThinkingLevelConfigured = executionState.ThinkingLevelConfigured
	modelBinding.WorkspaceAgent = &agent
	// Clone the tool registry so child turn's tool registrations
	// don't pollute the parent's registry.
	if baseAgent.Tools != nil {
		agent.Tools = baseAgent.Tools.Clone()
		removeInheritedNodeFileTools(agent.Tools)
		if !durableTask {
			removeDurableInteractionTools(agent.Tools)
		}
	}
	requireObjectiveOutcome := agent.Tools != nil && agent.Tools.HasRegistered("browser_act")
	objectiveChecklist := normalizeObjectiveChecklist(cfg.ObjectiveItems)
	if requireObjectiveOutcome && len(objectiveChecklist) == 0 {
		outcome := blockedObjectiveOutcome("a valid declared objective checklist is required before browser execution")
		projection := objectiveOutcomeUserContent("", outcome)
		return (&toolshared.ToolResult{ForLLM: projection, ForUser: projection}).
			WithDeliverable(&taskresult.Deliverable{
				Text: projection, ObjectiveOutcome: cloneObjectiveOutcome(outcome),
			}), nil
	}
	if agent.Tools != nil && (requireObjectiveOutcome ||
		(!cfg.Async && deliveryMode == toolshared.AsyncDeliveryParentOnly)) {
		removeUserDeliveryTools(agent.Tools)
	}
	childTask := cfg.SystemPrompt
	if requireObjectiveOutcome {
		childTask = browserObjectiveOutcomeInstruction(childTask, objectiveChecklist)
	}

	// Create processOptions for the child turn
	childSessionKey := childID
	if durableTask {
		childSessionKey = durableTaskSessionKey(parentTS.workspace, cfg.TaskID)
	}
	childSessionScope := session.CloneScope(parentTS.opts.Dispatch.SessionScope)
	if childSessionScope != nil {
		// The route remains owned by the parent conversation, but the durable
		// continuation is stored and compacted by the target agent. Persist that
		// runtime ownership so context provenance remains stable across resumes.
		childSessionScope.AgentID = agent.ID
	}
	dispatch := DispatchRequest{
		RouteSessionKey: parentTS.opts.Dispatch.RouteSessionKey,
		SessionKey:      childSessionKey,
		SessionAliases:  append([]string(nil), parentTS.opts.Dispatch.SessionAliases...),
		UserMessage:     childTask,
		Media:           nil,
		InboundContext:  cloneInboundContext(parentTS.opts.Dispatch.InboundContext),
		RouteResult:     cloneResolvedRoute(parentTS.opts.Dispatch.RouteResult),
		SessionScope:    childSessionScope,
	}
	if durableTask {
		ensureSessionMetadata(agent.Sessions, childSessionKey, childSessionScope, dispatch.SessionAliases)
	}
	opts := processOptions{
		TaskID:                  strings.TrimSpace(cfg.TaskID),
		ObjectiveChecklist:      objectiveChecklist,
		InteractionWorkspace:    parentTS.workspace,
		InteractionSessionKey:   parentTS.sessionKey,
		InteractionRouteKey:     parentTS.opts.Dispatch.RouteSessionKey,
		ModelBinding:            modelBinding,
		Dispatch:                dispatch,
		SenderID:                parentTS.opts.Dispatch.SenderID(),
		SenderDisplayName:       parentTS.opts.SenderDisplayName,
		TurnProfile:             parentTS.profile,
		SystemPromptOverride:    cfg.ActualSystemPrompt,
		InitialSteeringMessages: cfg.InitialMessages,
		DefaultResponse:         "",
		EnableSummary:           false,
		SendResponse: !requireObjectiveOutcome && !hasOutboundTransaction(childCtx) && !cfg.Async &&
			(deliveryMode == toolshared.AsyncDeliveryUserOnly || deliveryMode == toolshared.AsyncDeliveryUserAndParent),
		SuppressToolUserDelivery: requireObjectiveOutcome ||
			(!cfg.Async && deliveryMode == toolshared.AsyncDeliveryParentOnly),
		SuppressToolFeedback:    parentTS.opts.SuppressToolFeedback,
		NoHistory:               !durableTask,
		SkipInitialSteeringPoll: true,
	}
	if !opts.TurnProfile.Enabled {
		opts.TurnProfile = parentTS.opts.TurnProfile
	}

	// Create event scope for the child turn
	scope := al.newTurnEventScope(
		agent.ID,
		agent.Workspace,
		childID,
		newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope),
	)

	// Create child turnState using the new API
	childTS := newTurnState(&agent, opts, scope)

	// Set SubTurn-specific fields
	childTS.cancelFunc = cancel
	childTS.critical = cfg.Critical
	childTS.depth = parentTS.depth + 1
	childTS.parentTurnID = parentTS.turnID
	childTS.parentTurnState = parentTS
	childTS.pendingResults = make(chan *toolshared.ToolResult, 16)
	childTS.concurrencySem = make(chan struct{}, rtCfg.maxConcurrent)
	childTS.al = al // back-ref for hard abort cascade
	childTS.session = agent.Sessions

	// Token budget initialization/inheritance
	// If InitialTokenBudget is explicitly provided (e.g., by team tool), use it.
	// Otherwise, inherit from parent's tokenBudget (for nested SubTurns).
	if cfg.InitialTokenBudget != nil {
		childTS.tokenBudget = cfg.InitialTokenBudget
	} else if parentTS.tokenBudget != nil {
		childTS.tokenBudget = parentTS.tokenBudget
	} else if rtCfg.defaultTokenBudget > 0 {
		// Apply default token budget from config if no budget is set
		budget := &atomic.Int64{}
		budget.Store(int64(rtCfg.defaultTokenBudget))
		childTS.tokenBudget = budget
	}

	// IMPORTANT: Put childTS into childCtx so that code inside runTurn can retrieve it
	childCtx = withTurnState(childCtx, childTS)
	childCtx = WithAgentLoop(childCtx, al) // Propagate AgentLoop to child turn

	childTS.ctx = childCtx

	// Register child turn state so GetAllActiveTurns/Subagents can find it
	childScope := newRuntimeSubTurnScope(childTS.workspace, childID)
	al.activeTurnStates.Store(childScope, childTS)
	defer al.activeTurnStates.Delete(childScope)

	// 5. Establish parent-child relationship (thread-safe)
	parentTS.mu.Lock()
	parentTS.childTurnIDs = append(parentTS.childTurnIDs, childID)
	parentTS.mu.Unlock()

	// 6. Emit Spawn event
	al.emitEvent(runtimeevents.KindAgentSubTurnSpawn,
		childTS.eventMeta("spawnSubTurn", "subturn.spawn"),
		SubTurnSpawnPayload{
			AgentID:      childTS.agentID,
			Label:        childID,
			ParentTurnID: parentTS.turnID,
		},
	)

	// 7. Defer cleanup: deliver result (for async), emit End event, and recover from panics
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverPanicNoExit(r)
			err = fmt.Errorf("subturn panicked: %v", r)
			result = nil
			logger.ErrorCF("subturn", "SubTurn panicked", map[string]any{
				"child_id":  childID,
				"parent_id": parentTS.turnID,
				"panic":     r,
			})
		}

		// Child turns publish session-scoped working feedback. Dismiss that
		// feedback when the child finishes regardless of async/sync delivery;
		// synchronous delegate/subagent calls return their result inline to the
		// parent and should not leave an orphaned animator behind.
		if al != nil && al.channelManager != nil && childTS.channel != "" {
			dismissCtx, dismissCancel := context.WithTimeout(context.Background(), 5*time.Second)
			al.channelManager.DismissToolFeedback(dismissCtx, toolFeedbackTargetForSession(
				childTS.channel,
				childTS.chatID,
				childTS.opts.Dispatch.InboundContext,
				childTS.sessionKey,
				[]runtimeevents.TraceScope{runtimeevents.NewTraceScope(childTS.workspace, childTS.turnID)},
			))
			dismissCancel()
		}

		// Result Delivery Strategy (Async vs Sync)
		if cfg.Async {
			deliverSubTurnResult(al, parentTS, childID, result)
		}

		status := "completed"
		if err != nil {
			status = "error"
		}
		al.emitEvent(runtimeevents.KindAgentSubTurnEnd,
			childTS.eventMeta("spawnSubTurn", "subturn.end"),
			SubTurnEndPayload{
				AgentID: childTS.agentID,
				Status:  status,
			},
		)
	}()

	// 8. Execute sub-turn via the real agent loop.
	pipeline := NewPipeline(al)
	turnRes, turnErr := al.runTurn(childCtx, childTS, pipeline)
	var objectiveOutcome *taskresult.Outcome
	if turnErr == nil && turnRes.status != TurnEndStatusSuspended {
		turnRes.finalContent, objectiveOutcome = extractObjectiveOutcome(
			turnRes.finalContent,
			turnRes.writeAudit,
			requireObjectiveOutcome,
			objectiveChecklist,
		)
	}

	// Release the concurrency semaphore immediately after runTurn completes,
	// before the cleanup defer runs. This prevents a deadlock where:
	// - All semaphore slots are held by sub-turns in their cleanup phase
	// - Cleanup blocks on a full pendingResults channel
	// - The parent goroutine is blocked waiting for a semaphore slot
	// - The parent cannot consume pendingResults because it is blocked on the semaphore
	if semAcquired {
		<-parentTS.concurrencySem
		semAcquired = false // prevent the defer from double-releasing
	}

	// Convert turnResult to tools.ToolResult
	if turnErr != nil {
		err = turnErr
		result = &toolshared.ToolResult{
			Err:    turnErr,
			ForLLM: fmt.Sprintf("SubTurn failed: %v", turnErr),
		}
	} else if turnRes.status == TurnEndStatusSuspended {
		result = &toolshared.ToolResult{Control: toolshared.ToolControl{TaskSuspended: true}}
	} else {
		userContent := objectiveOutcomeUserContent(turnRes.finalContent, objectiveOutcome)
		parentContent := turnRes.finalContent
		if objectiveOutcome != nil && objectiveOutcome.Status != taskresult.OutcomeSucceeded {
			parentContent = userContent
		}
		result = &toolshared.ToolResult{
			ForLLM:  parentContent,
			ForUser: userContent,
		}
		result.WriteAudit = cloneWriteAuditEntries(turnRes.writeAudit)
		if strings.TrimSpace(turnRes.finalContent) != "" || turnRes.deliverable != nil || objectiveOutcome != nil {
			deliverable := taskresult.CloneDeliverable(turnRes.deliverable)
			if deliverable == nil {
				deliverable = &taskresult.Deliverable{}
			}
			if strings.TrimSpace(deliverable.Text) == "" {
				deliverable.Text = parentContent
			}
			if objectiveOutcome != nil {
				deliverable.ObjectiveOutcome = cloneObjectiveOutcome(objectiveOutcome)
			}
			result.WithDeliverable(deliverable)
			result.Media = append(result.Media, mediaArtifactRefs(deliverable.Artifacts)...)
		}
		if !cfg.Async {
			switch deliveryMode {
			case toolshared.AsyncDeliveryParentOnly:
				result.ForUser = ""
			case toolshared.AsyncDeliveryUserOnly:
				result.WithDeliveryIntent(toolshared.DeliveryFinalHandled)
			case toolshared.AsyncDeliveryUserAndParent:
				if hasOutboundTransaction(childCtx) {
					result.WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
				}
			}
		}
	}

	return result, err
}

func cloneWriteAuditEntries(entries []toolshared.WriteAuditEntry) []toolshared.WriteAuditEntry {
	cloned := make([]toolshared.WriteAuditEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = entry
		cloned[index].Metadata = copyObjectiveMetadata(entry.Metadata)
	}
	return cloned
}

func durableTaskSessionKey(ownerWorkspace, taskID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(ownerWorkspace)))
	return "task:" + hex.EncodeToString(sum[:8]) + ":" + strings.TrimSpace(taskID)
}

func mediaArtifactRefs(items []taskresult.Artifact) []string {
	refs := make([]string, 0, len(items))
	for _, item := range items {
		if item.Delivered {
			continue
		}
		ref := strings.TrimSpace(item.Ref)
		if strings.HasPrefix(ref, "media://") {
			refs = append(refs, ref)
		}
	}
	return refs
}

// ====================== Result Delivery ======================

// deliverSubTurnResult delivers a sub-turn result to the parent turn's pendingResults channel.
//
// IMPORTANT: This function is ONLY called for asynchronous sub-turns (Async=true).
// For synchronous sub-turns (Async=false), results are returned directly via the function
// return value to avoid double delivery.
//
// Delivery behavior:
//   - If parent turn is still running: waits for capacity and delivers to pendingResults
//   - If parent turn has finished: emits agent.subturn.orphan (late arrival)
//
// Thread safety:
//   - The finish check and send are serialized by the parent lifecycle lock
//   - pendingResults is never closed because child deliveries may outlive the parent
//
// Event emissions:
//   - agent.subturn.result_delivered: successful delivery to channel
//   - agent.subturn.orphan: delivery failed (parent finished or channel full)
func deliverSubTurnResult(al *AgentLoop, parentTS *turnState, childID string, result *toolshared.ToolResult) {
	if parentTS.enqueuePendingResult(result) {
		if al != nil {
			contentLen := 0
			if result != nil {
				contentLen = len(result.ForLLM)
			}
			al.emitEvent(runtimeevents.KindAgentSubTurnResultDelivered,
				parentTS.eventMeta("deliverSubTurnResult", "subturn.result_delivered"),
				SubTurnResultDeliveredPayload{ContentLen: contentLen},
			)
		}
		return
	}

	logger.WarnCF("subturn", "parent finished before result could be delivered", map[string]any{
		"parent_id": parentTS.turnID,
		"child_id":  childID,
	})
	if result != nil && al != nil {
		al.emitEvent(runtimeevents.KindAgentSubTurnOrphan,
			parentTS.eventMeta("deliverSubTurnResult", "subturn.orphan"),
			SubTurnOrphanPayload{ParentTurnID: parentTS.turnID, ChildTurnID: childID, Reason: "parent_finished"},
		)
	}
}

// ====================== Other Types ======================

// ephemeralSessionStore is an in-memory session.SessionStore used by SubTurns.
// It does not persist to disk and auto-truncates history to maxEphemeralHistorySize.
type ephemeralSessionStore struct {
	mu      sync.Mutex
	history []providers.Message
	summary string
}

func newEphemeralSession(initial []providers.Message) ephemeralSessionStoreIface {
	s := &ephemeralSessionStore{}
	if len(initial) > 0 {
		s.history = append(s.history, initial...)
	}
	return s
}

// ephemeralSessionStoreIface is satisfied by *ephemeralSessionStore.
// Declared so newEphemeralSession can return a typed interface.
type ephemeralSessionStoreIface interface {
	AppendTurnMessage(ctx context.Context, sessionKey string, msg providers.Message) error
	ReadTurnHistory(ctx context.Context, sessionKey string) ([]providers.Message, error)
	ReplaceTurnHistory(ctx context.Context, sessionKey string, history []providers.Message) error
	MutateTurnHistory(
		ctx context.Context,
		sessionKey string,
		mutate func([]providers.Message) ([]providers.Message, bool, error),
	) (bool, error)
	ClearSession(ctx context.Context, sessionKey string) error
	RestoreTurnSnapshot(ctx context.Context, sessionKey string, history []providers.Message, summary string) error
	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg providers.Message)
	GetHistory(key string) []providers.Message
	GetSummary(key string) string
	SetSummary(key, summary string)
	SetHistory(key string, history []providers.Message)
	TruncateHistory(key string, keepLast int)
	Save(key string) error
	ListSessions() []string
	Close() error
}

func (e *ephemeralSessionStore) AddMessage(_, role, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, providers.Message{Role: role, Content: content})
	e.truncateLocked()
}

func (e *ephemeralSessionStore) AppendTurnMessage(
	ctx context.Context,
	_ string,
	msg providers.Message,
) error {
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return err
		}
	}
	e.AddFullMessage("", msg)
	return nil
}

func (e *ephemeralSessionStore) RestoreTurnSnapshot(
	ctx context.Context,
	_ string,
	history []providers.Message,
	summary string,
) error {
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = messageutil.FilterInvalidHistoryMessages(append([]providers.Message(nil), history...))
	e.summary = summary
	e.truncateLocked()
	return nil
}

func (e *ephemeralSessionStore) ReplaceTurnHistory(
	ctx context.Context,
	_ string,
	history []providers.Message,
) error {
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = messageutil.FilterInvalidHistoryMessages(append([]providers.Message(nil), history...))
	e.truncateLocked()
	return nil
}

func (e *ephemeralSessionStore) MutateTurnHistory(
	ctx context.Context,
	_ string,
	mutate func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return false, err
		}
	}
	if mutate == nil {
		return false, fmt.Errorf("history mutation callback is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	next, changed, err := mutate(append([]providers.Message(nil), e.history...))
	if err != nil || !changed {
		return false, err
	}
	e.history = messageutil.FilterInvalidHistoryMessages(next)
	e.truncateLocked()
	return true, nil
}

func (e *ephemeralSessionStore) ReadTurnHistory(
	ctx context.Context,
	_ string,
) ([]providers.Message, error) {
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]providers.Message(nil), e.history...), nil
}

func (e *ephemeralSessionStore) ClearSession(ctx context.Context, sessionKey string) error {
	return e.RestoreTurnSnapshot(ctx, sessionKey, nil, "")
}

func (e *ephemeralSessionStore) AddFullMessage(_ string, msg providers.Message) {
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, msg)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) GetHistory(_ string) []providers.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]providers.Message, len(e.history))
	copy(out, e.history)
	return out
}

func (e *ephemeralSessionStore) GetSummary(_ string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.summary
}

func (e *ephemeralSessionStore) SetSummary(_, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.summary = summary
}

func (e *ephemeralSessionStore) SetHistory(_ string, history []providers.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	history = messageutil.FilterInvalidHistoryMessages(history)
	e.history = make([]providers.Message, len(history))
	copy(e.history, history)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) TruncateHistory(_ string, keepLast int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if keepLast <= 0 {
		e.history = nil
		return
	}

	if keepLast >= len(e.history) {
		return
	}
	e.history = e.history[len(e.history)-keepLast:]
}

func (e *ephemeralSessionStore) Save(_ string) error    { return nil }
func (e *ephemeralSessionStore) Close() error           { return nil }
func (e *ephemeralSessionStore) ListSessions() []string { return nil }

func (e *ephemeralSessionStore) truncateLocked() {
	if len(e.history) > maxEphemeralHistorySize {
		e.history = e.history[len(e.history)-maxEphemeralHistorySize:]
	}
}
