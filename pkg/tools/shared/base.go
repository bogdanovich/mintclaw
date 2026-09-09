package toolshared

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
)

// Tool is the interface that all tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// ObjectiveRecoveryProvider identifies a tool that can recover a missing
// runtime-verifiable objective without exposing unrelated tools or operations
// during the bounded repair pass. The returned schema is both model-visible
// and runtime-enforced; implementations still return ordinary ToolResults and
// the runtime validates their control-plane evidence independently.
type ObjectiveRecoveryProvider interface {
	ObjectiveRecoveryParameters(kind string) (map[string]any, bool)
}

// ArgumentsCanonicalizer returns a cloned, semantically equivalent argument
// map for schema validation and execution. Implementations must not mutate the
// provider-owned input map.
type ArgumentsCanonicalizer interface {
	CanonicalArguments(map[string]any) (map[string]any, error)
}

// DurableArgumentsProvider projects model-authored arguments into the exact
// schema-valid form that may be retained after the current tool execution.
// The original map remains available only to the in-memory execution path.
type DurableArgumentsProvider interface {
	DurableArguments(map[string]any) (map[string]any, error)
}

// ProtectedDurableArgumentsProvider marks projections whose surrounding
// assistant-response text must also be excluded from durable state.
type ProtectedDurableArgumentsProvider interface {
	ProtectedDurableArguments(map[string]any) bool
}

// ProtectedDurableResultProvider marks tool results that may be consumed by
// the current in-memory tool loop but must be replaced in durable state. This
// is intentionally separate from ProtectedDurableArgumentsProvider: hiding a
// result must not impose singleton batching or strip the assistant envelope
// when the model-authored arguments are safe to retain.
type ProtectedDurableResultProvider interface {
	ProtectedDurableResult(map[string]any) bool
}

// LoopSemanticsProvider explicitly classifies tool side-effect behavior for
// loop detection. Tools without this optional capability remain unknown.
type LoopSemanticsProvider interface {
	ToolLoopSemantics() loopguard.Semantics
}

const (
	ToolPromptLayerCapability = "capability"
	ToolPromptSlotTooling     = "tooling"
	ToolPromptSlotMCP         = "mcp"
	ToolPromptSourceRegistry  = "tool_registry:native"
	ToolPromptSourceDiscovery = "tool_registry:discovery"
)

type PromptMetadata struct {
	Layer  string
	Slot   string
	Source string
}

type PromptMetadataProvider interface {
	PromptMetadata() PromptMetadata
}

// --- Request-scoped tool context (channel / chatID) ---
//
// Carried via context.Value so that concurrent tool calls each receive
// their own immutable copy — no mutable state on singleton tool instances.
//
// Keys are unexported pointer-typed vars — guaranteed collision-free,
// and only accessible through the helper functions below.

type toolCtxKey struct{ name string }

var (
	ctxKeyChannel             = &toolCtxKey{"channel"}
	ctxKeyChatID              = &toolCtxKey{"chatID"}
	ctxKeyTopicID             = &toolCtxKey{"topicID"}
	ctxKeyMessageID           = &toolCtxKey{"messageID"}
	ctxKeyReplyToMessageID    = &toolCtxKey{"replyToMessageID"}
	ctxKeyInboundContext      = &toolCtxKey{"inboundContext"}
	ctxKeyAgentID             = &toolCtxKey{"agentID"}
	ctxKeySessionKey          = &toolCtxKey{"sessionKey"}
	ctxKeyRouteSessionKey     = &toolCtxKey{"routeSessionKey"}
	ctxKeySessionScope        = &toolCtxKey{"sessionScope"}
	ctxKeyToolCallID          = &toolCtxKey{"toolCallID"}
	ctxKeyExecutionID         = &toolCtxKey{"executionID"}
	ctxKeyWorkspace           = &toolCtxKey{"workspace"}
	ctxKeyApprovalResume      = &toolCtxKey{"approvalResume"}
	ctxKeyApprovalArguments   = &toolCtxKey{"approvalArguments"}
	ctxKeyApprovalBypass      = &toolCtxKey{"approvalBypass"}
	ctxKeyRecoverableOutbound = &toolCtxKey{"recoverableOutbound"}
	ctxKeyHistoryDisabled     = &toolCtxKey{"historyDisabled"}
	ctxKeyCommandObservation  = &toolCtxKey{"commandObservation"}
	ctxKeyDocumentMediaRefs   = &toolCtxKey{"documentMediaRefs"}
	ctxKeyDocumentVision      = &toolCtxKey{"documentVision"}
)

type commandObservationSink func(CommandObservation)

// WithToolContext returns a child context carrying channel and chatID.
func WithToolContext(ctx context.Context, channel, chatID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyChannel, channel)
	ctx = context.WithValue(ctx, ctxKeyChatID, chatID)
	return ctx
}

// WithToolTopicID returns a child context carrying the inbound topic/thread id.
func WithToolTopicID(ctx context.Context, topicID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyTopicID, topicID)
	return ctx
}

// WithToolMessageContext returns a child context carrying inbound message IDs.
func WithToolMessageContext(ctx context.Context, messageID, replyToMessageID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyMessageID, messageID)
	ctx = context.WithValue(ctx, ctxKeyReplyToMessageID, replyToMessageID)
	return ctx
}

// WithToolInboundContext returns a child context carrying channel/chat and inbound IDs.
func WithToolInboundContext(
	ctx context.Context,
	channel, chatID, messageID, replyToMessageID string,
) context.Context {
	ctx = WithToolContext(ctx, channel, chatID)
	ctx = WithToolMessageContext(ctx, messageID, replyToMessageID)
	ctx = WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel:          channel,
		ChatID:           chatID,
		MessageID:        messageID,
		ReplyToMessageID: replyToMessageID,
	})
	return ctx
}

// WithToolInboundMetadata returns a child context carrying the full normalized
// inbound identity/source metadata available to tools.
func WithToolInboundMetadata(ctx context.Context, inbound bus.InboundContext) context.Context {
	return context.WithValue(ctx, ctxKeyInboundContext, cloneToolInboundContext(bus.NormalizeInboundContext(inbound)))
}

// WithToolSessionContext returns a child context carrying turn-scoped session metadata.
func WithToolSessionContext(
	ctx context.Context,
	agentID, sessionKey string,
	scope *session.SessionScope,
) context.Context {
	ctx = context.WithValue(ctx, ctxKeyAgentID, agentID)
	ctx = context.WithValue(ctx, ctxKeySessionKey, sessionKey)
	ctx = context.WithValue(ctx, ctxKeySessionScope, session.CloneScope(scope))
	return ctx
}

// WithToolHistoryDisabled carries the originating turn's durable history policy.
func WithToolHistoryDisabled(ctx context.Context, disabled bool) context.Context {
	return context.WithValue(ctx, ctxKeyHistoryDisabled, disabled)
}

// WithToolRouteSessionKey carries the canonical routed conversation key used
// for durable session-scoped state. It can differ from ToolSessionKey after a
// user starts a fresh history session through /new or /reset.
func WithToolRouteSessionKey(ctx context.Context, routeSessionKey string) context.Context {
	return context.WithValue(ctx, ctxKeyRouteSessionKey, routeSessionKey)
}

// WithToolCallID carries the provider-assigned identity of the active tool
// call. Durable pre-execution state uses it to recover the same call after a
// human-approval restart.
func WithToolCallID(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, ctxKeyToolCallID, toolCallID)
}

// WithCommandObservationSink installs a request-scoped, coding-only progress
// sink. Tools publish safe observations through PublishCommandObservation;
// the sink is never retained on a singleton tool instance.
func WithCommandObservationSink(
	ctx context.Context,
	sink func(CommandObservation),
) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyCommandObservation, commandObservationSink(sink))
}

// PublishCommandObservation sanitizes and clones a command observation before
// delivering it to the optional coding presentation sink.
func PublishCommandObservation(ctx context.Context, observation CommandObservation) {
	if ctx == nil {
		return
	}
	sink, ok := ctx.Value(ctxKeyCommandObservation).(commandObservationSink)
	if !ok || sink == nil {
		return
	}
	safe := SanitizeToolObservation(&ToolObservation{Command: &observation})
	if safe != nil && safe.Command != nil {
		sink(*safe.Command)
	}
}

// WithToolExecutionIdentity carries the stable logical turn identity and
// workspace namespace for durable tool operations.
func WithToolExecutionIdentity(ctx context.Context, workspace, executionID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyWorkspace, workspace)
	return context.WithValue(ctx, ctxKeyExecutionID, executionID)
}

// WithToolRecoverableOutbound marks a tool execution whose outbound results
// are owned by a durable, replayable delivery transaction.
func WithToolRecoverableOutbound(ctx context.Context, recoverable bool) context.Context {
	return context.WithValue(ctx, ctxKeyRecoverableOutbound, recoverable)
}

// WithToolApprovalContinuation marks execution resumed from a one-time human
// approval. Durable tools use it to fail closed when retained authority expired.
func WithToolApprovalContinuation(ctx context.Context, resumed bool) context.Context {
	return context.WithValue(ctx, ctxKeyApprovalResume, resumed)
}

// WithToolApprovalArguments carries the trusted arguments whose durable hash
// was consumed for this one approval continuation. The copy prevents a tool
// from observing later top-level mutation of the pipeline-owned map.
func WithToolApprovalArguments(ctx context.Context, arguments map[string]any) context.Context {
	cloned := make(map[string]any, len(arguments))
	for key, value := range arguments {
		cloned[key] = value
	}
	return context.WithValue(ctx, ctxKeyApprovalArguments, cloned)
}

// WithToolApprovalBypass marks execution as authorized by the configured
// allow-all approval policy. It does not represent a consumed human grant.
func WithToolApprovalBypass(ctx context.Context, bypass bool) context.Context {
	return context.WithValue(ctx, ctxKeyApprovalBypass, bypass)
}

// WithToolDocumentContext carries the exact current-turn document refs and
// whether the selected model route has an explicitly configured image path.
func WithToolDocumentContext(ctx context.Context, refs []string, visionAvailable bool) context.Context {
	allowed := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref != "" {
			allowed[ref] = struct{}{}
		}
	}
	ctx = context.WithValue(ctx, ctxKeyDocumentMediaRefs, allowed)
	return context.WithValue(ctx, ctxKeyDocumentVision, visionAvailable)
}

// ToolChannel extracts the channel from ctx, or "" if unset.
func ToolChannel(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyChannel).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolChatID extracts the chatID from ctx, or "" if unset.
func ToolChatID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyChatID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolTopicID extracts the inbound topic/thread id from ctx, or "" if unset.
func ToolTopicID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyTopicID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolMessageID extracts the current inbound message ID from ctx, or "" if unset.
func ToolMessageID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyMessageID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolReplyToMessageID extracts the current inbound reply target from ctx, or "" if unset.
func ToolReplyToMessageID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyReplyToMessageID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolInboundContext extracts the full inbound metadata from ctx, or zero value if unset.
func ToolInboundContext(ctx context.Context) bus.InboundContext {
	v, ok := ctx.Value(ctxKeyInboundContext).(bus.InboundContext)
	if !ok {
		return bus.InboundContext{}
	}
	return cloneToolInboundContext(v)
}

func cloneToolInboundContext(inbound bus.InboundContext) bus.InboundContext {
	inbound.MediaGroup.MessageIDs = append([]string(nil), inbound.MediaGroup.MessageIDs...)
	if inbound.Interaction.OptionIndex != nil {
		optionIndex := *inbound.Interaction.OptionIndex
		inbound.Interaction.OptionIndex = &optionIndex
	}
	inbound.ReplyHandles = cloneToolStringMap(inbound.ReplyHandles)
	inbound.Raw = cloneToolStringMap(inbound.Raw)
	return inbound
}

func cloneToolStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// ToolSenderID extracts the transport sender ID from ctx.
func ToolSenderID(ctx context.Context) string {
	return ToolInboundContext(ctx).SenderID
}

// ToolActorID extracts the effective actor ID from ctx. It usually equals sender ID.
func ToolActorID(ctx context.Context) string {
	return ToolInboundContext(ctx).ActorID
}

// ToolOriginID extracts the optional content origin ID from ctx.
func ToolOriginID(ctx context.Context) string {
	return ToolInboundContext(ctx).OriginID
}

// ToolOriginType extracts the optional content origin type from ctx.
func ToolOriginType(ctx context.Context) string {
	return ToolInboundContext(ctx).OriginType
}

// ToolSourceRef extracts the stable inbound source reference from ctx.
func ToolSourceRef(ctx context.Context) string {
	return ToolInboundContext(ctx).SourceRef
}

// ToolAgentID extracts the active turn's agent ID from ctx, or "" if unset.
func ToolAgentID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyAgentID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolSessionKey extracts the active turn's session key from ctx, or "" if unset.
func ToolSessionKey(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeySessionKey).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolHistoryDisabled reports whether the originating turn forbids history writes.
func ToolHistoryDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(ctxKeyHistoryDisabled).(bool)
	return disabled
}

// ToolCallID extracts the active provider tool-call identity from ctx.
func ToolCallID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyToolCallID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolExecutionID extracts the stable logical turn identity from ctx.
func ToolExecutionID(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyExecutionID).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolRecoverableOutbound reports whether durable delivery admission can
// recover an outbound tool result after process interruption.
func ToolRecoverableOutbound(ctx context.Context) bool {
	recoverable, _ := ctx.Value(ctxKeyRecoverableOutbound).(bool)
	return recoverable
}

// ToolWorkspace extracts the workspace namespace from ctx.
func ToolWorkspace(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyWorkspace).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolApprovalContinuation reports whether this call resumes human approval.
func ToolApprovalContinuation(ctx context.Context) bool {
	resumed, _ := ctx.Value(ctxKeyApprovalResume).(bool)
	return resumed
}

// ToolApprovalArguments returns a copy of the trusted arguments consumed for
// this approval continuation. Ordinary and policy-bypassed calls have none.
func ToolApprovalArguments(ctx context.Context) (map[string]any, bool) {
	arguments, ok := ctx.Value(ctxKeyApprovalArguments).(map[string]any)
	if !ok {
		return nil, false
	}
	cloned := make(map[string]any, len(arguments))
	for key, value := range arguments {
		cloned[key] = value
	}
	return cloned, true
}

// ToolApprovalBypass reports whether the configured policy bypasses approval.
func ToolApprovalBypass(ctx context.Context) bool {
	bypass, _ := ctx.Value(ctxKeyApprovalBypass).(bool)
	return bypass
}

// ToolDocumentRefAllowed reports whether ref was authoritatively projected
// from the current inbound turn. Guessing an older route-owned ref is denied.
func ToolDocumentRefAllowed(ctx context.Context, ref string) bool {
	allowed, ok := ctx.Value(ctxKeyDocumentMediaRefs).(map[string]struct{})
	if !ok {
		return false
	}
	_, ok = allowed[ref]
	return ok
}

// ToolDocumentVisionAvailable reports whether rendered document pages can be
// passed through the selected route's configured image-input path.
func ToolDocumentVisionAvailable(ctx context.Context) bool {
	available, _ := ctx.Value(ctxKeyDocumentVision).(bool)
	return available
}

// ToolRouteSessionKey extracts the canonical routed conversation key from ctx.
func ToolRouteSessionKey(ctx context.Context) string {
	v, ok := ctx.Value(ctxKeyRouteSessionKey).(string)
	if !ok {
		return ""
	}
	return v
}

// ToolSessionScope extracts the active turn's structured session scope from ctx.
func ToolSessionScope(ctx context.Context) *session.SessionScope {
	scope, ok := ctx.Value(ctxKeySessionScope).(*session.SessionScope)
	if !ok {
		return nil
	}
	return session.CloneScope(scope)
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
// The result parameter contains the tool's execution result.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Unlike the old AsyncTool pattern (SetCallback + Execute), AsyncExecutor
// receives the callback as a parameter of ExecuteAsync. This eliminates the
// data race where concurrent calls could overwrite each other's callbacks
// on a shared tool instance.
//
// This is useful for:
//   - Long-running operations that shouldn't block the agent loop
//   - Subagent spawns that complete independently
//   - Background tasks that need to report results later
//
// Example:
//
//	func (t *SpawnTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
//	    go func() {
//	        result := t.runSubagent(ctx, args)
//	        if cb != nil { cb(ctx, result) }
//	    }()
//	    return AsyncResult("Subagent spawned, will report back")
//	}
type AsyncExecutor interface {
	Tool
	// ExecuteAsync runs the tool asynchronously. The callback cb will be
	// invoked (possibly from another goroutine) when the async operation
	// completes. cb is guaranteed to be non-nil by the caller (registry).
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
