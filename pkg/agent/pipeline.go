// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Pipeline holds the immutable runtime-generation snapshot used by turn
// execution. The owning turnRunner is replaced when runtime wiring changes.
type Pipeline struct {
	Cfg                  *config.Config
	Runtime              PipelineRuntimeServices
	Context              PipelineContextServices
	Interaction          PipelineInteractionServices
	retrySleeper         retrySleeper
	trustAllTools        bool
	durableToolLifecycle bool
	hashArguments        func(string, map[string]any) (string, error)
}

type PipelineRuntimeServices struct {
	Bus            pipelineBus
	Events         runtimeEventEmitter
	ActiveRequests activeRequestTracker
	TurnControl    turnController
}

func (p *Pipeline) hashToolArguments(workspace string, arguments map[string]any) (string, error) {
	if p != nil && p.hashArguments != nil {
		return p.hashArguments(workspace, arguments)
	}
	return interactions.HashArguments(workspace, arguments)
}

type PipelineContextServices struct {
	Runtime              pipelineContextRuntime
	BackgroundCompaction backgroundCompactionScheduler
	ModelExecution       modelExecutionResolver
	Steering             steeringDequeuer
	MediaResolver        mediaResolver
	TerminalTasks        terminalTaskContextProvider
}

type PipelineInteractionServices struct {
	Reasoning        reasoningPublisher
	ToolFeedback     toolFeedbackManager
	SyncToolDelivery syncToolResultDeliveryManager
	ToolDelivery     toolDeliveryManager
	Hooks            hookInterceptor
	Fallback         fallbackExecutor
	Suspension       toolSuspensionManager
}

type ToolSuspensionRequest struct {
	Workspace        string
	Prompt           interactions.SuspensionRequest
	Route            interactions.Route
	Origin           interactions.Origin
	ApprovalAction   string
	ExecutionContext *bus.InboundContext
	Resolution       func(context.Context, interactions.Outcome) error
}

// ToolSuspensionDisposition distinguishes a durable handoff from a failure
// that is still safe to return to the model as an ordinary tool error.
type ToolSuspensionDisposition struct {
	InteractionID string
	Durable       bool
}

type ToolApprovalGrant struct {
	InteractionID     string
	Revision          int64
	OriginExecutionID string
	// OriginArgumentHash is the trusted binding stored with the suspended
	// interaction. Fresh approval arguments may differ after retained
	// time-bound tool state expires, but consumption must spend this binding.
	OriginArgumentHash string
}

type ToolApprovalConsumptionRequest struct {
	Workspace     string
	InteractionID string
	Revision      int64
	Origin        interactions.Origin
}

type toolSuspensionManager interface {
	SuspendToolCall(
		ctx context.Context,
		request ToolSuspensionRequest,
	) (ToolSuspensionDisposition, error)
	ConsumeApproval(ctx context.Context, request ToolApprovalConsumptionRequest) error
}

type runtimeEventEmitter interface {
	emitEvent(kind runtimeevents.Kind, meta HookMeta, payload any)
}

type pipelineBus interface {
	PublishOutbound(ctx context.Context, msg bus.OutboundMessage) error
	GetStreamer(
		ctx context.Context,
		channel, chatID, sessionKey, requestID string,
		traceScope runtimeevents.TraceScope,
	) (bus.Streamer, bool)
}

type retrySleeper interface {
	Sleep(ctx context.Context, delay time.Duration) error
}

type pipelineContextRuntime interface {
	Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error)
	Compact(ctx context.Context, req *CompactRequest) error
	Ingest(ctx context.Context, req *IngestRequest) error
}

type terminalTaskContextProvider interface {
	terminalTaskContextForTurn(ts *turnState) []providers.Message
}

type backgroundCompactionScheduler interface {
	scheduleBackgroundCompaction(
		agent *AgentInstance,
		sessionKey string,
		reason ContextCompressReason,
		budget int,
		messageKind string,
	)
}

type activeRequestTracker interface {
	activeRequestsInc()
	activeRequestsDec()
}

type modelExecutionResolver interface {
	selectCandidates(
		execution effectiveExecutionState,
		userMsg string,
		history []providers.Message,
		routeSessionKey string,
	) modelSelectionDecision
	maybeBuildVisionExecutionState(
		baseAgent *AgentInstance,
		execution effectiveExecutionState,
		messages []providers.Message,
	) (effectiveExecutionState, func(), string, bool, error)
	maybeApplyVisionExecutionState(baseAgent *AgentInstance, exec *turnExecution) (bool, error)
	buildExecutionStateForModel(
		baseAgent *AgentInstance,
		modelName string,
		fallbacks []string,
	) (effectiveExecutionState, func(), error)
	updateAutoFallbackSelection(
		routeSessionKey string,
		selectedCandidates []providers.FallbackCandidate,
		result *providers.FallbackResult,
		usedLight bool,
	)
}

type steeringDequeuer interface {
	dequeueSteeringMessagesForTurn(scope runtimeSessionScope, senderID string) []providers.Message
	returnSteeringMessagesForTurn(scope runtimeSessionScope, messages []providers.Message)
}

type reasoningPublisher interface {
	targetReasoningChannelID(channelName string) string
	publishMintClawReasoning(
		ctx context.Context,
		reasoningContent, chatID, sessionKey, modelName string,
	)
	publishMintClawToolCallInterim(
		ctx context.Context,
		ts *turnState,
		modelName string,
		reasoningContent string,
		content string,
		toolCalls []providers.ToolCall,
	)
	handleReasoning(ctx context.Context, reasoningContent, channelName, channelID string)
}

type toolDeliveryManager interface {
	deliverAsyncToolCompletion(req AsyncDeliveryRequest)
}

type syncToolResultDeliveryManager interface {
	applySyncToolResultDelivery(
		ctx context.Context,
		ts *turnState,
		result *toolshared.ToolResult,
		toolName string,
	) ([]providers.Attachment, *toolshared.ToolResult)
}

type toolFeedbackManager interface {
	publishToolFeedbackForCall(
		ctx context.Context,
		ts *turnState,
		response *providers.LLMResponse,
		toolCall providers.ToolCall,
		toolName string,
		toolArgs map[string]any,
		messages []providers.Message,
	)
	dismissToolFeedbackForTurn(ctx context.Context, ts *turnState)
	pauseToolFeedbackForTurn(ctx context.Context, ts *turnState)
	shouldPublishToolFeedback(ts *turnState) bool
}

type turnController interface {
	abortTurn(ts *turnState) (turnResult, error)
}

type hookInterceptor interface {
	BeforeLLM(ctx context.Context, req *LLMHookRequest) (*LLMHookRequest, HookDecision)
	AfterLLM(ctx context.Context, resp *LLMHookResponse) (*LLMHookResponse, HookDecision)
	BeforeTool(ctx context.Context, req *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision)
	AfterTool(
		ctx context.Context,
		resp *ToolResultHookResponse,
	) (*ToolResultHookResponse, HookDecision)
	ApproveTool(ctx context.Context, req *ToolApprovalRequest) ApprovalDecision
}

type fallbackExecutor interface {
	ExecuteCandidate(
		ctx context.Context,
		candidates []providers.FallbackCandidate,
		run func(context.Context, providers.FallbackCandidate) (*providers.LLMResponse, error),
	) (*providers.FallbackResult, error)
}

type observedFallbackExecutor interface {
	ExecuteCandidateObserved(
		ctx context.Context,
		candidates []providers.FallbackCandidate,
		run func(context.Context, providers.FallbackCandidate) (*providers.LLMResponse, error),
		observer providers.FallbackAttemptObserver,
	) (*providers.FallbackResult, error)
}

func executeFallbackWithObserver(
	executor fallbackExecutor,
	ctx context.Context,
	candidates []providers.FallbackCandidate,
	run func(context.Context, providers.FallbackCandidate) (*providers.LLMResponse, error),
	observer providers.FallbackAttemptObserver,
) (*providers.FallbackResult, error) {
	if observed, ok := executor.(observedFallbackExecutor); ok {
		return observed.ExecuteCandidateObserved(ctx, candidates, run, observer)
	}
	return executor.ExecuteCandidate(ctx, candidates, run)
}

type mediaResolver interface {
	ResolveWithMeta(ref string) (localPath string, meta media.MediaMeta, err error)
}

func (p *Pipeline) emitEvent(kind runtimeevents.Kind, meta HookMeta, payload any) {
	if p == nil || p.Runtime.Events == nil {
		return
	}
	p.Runtime.Events.emitEvent(kind, meta, payload)
}

func (p *Pipeline) trackActiveRequest() func() {
	if p == nil || p.Runtime.ActiveRequests == nil {
		return func() {}
	}
	p.Runtime.ActiveRequests.activeRequestsInc()
	return p.Runtime.ActiveRequests.activeRequestsDec
}
