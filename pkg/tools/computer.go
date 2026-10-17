package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// ComputerToolSource is an independent first-party computer grant. It cannot
// dispatch input, open arbitrary applications or interpret natural-language
// finish phrases. Implementations retain the original resource and full owner.
type ComputerToolSource interface {
	Available() bool
	Targets(context.Context, runtimecap.Principal, string) ([]computer.NamedTarget, error)
	Open(context.Context, runtimecap.Principal, string, string) (computer.Session, error)
	Status(context.Context, runtimecap.Principal, string, string) (computer.Session, error)
	Control(context.Context, runtimecap.Principal, string, string, computer.ControlEvent) (computer.Session, error)
	Observe(context.Context, runtimecap.Principal, string, string) (computer.Observation, error)
	OwnedSessions(context.Context, runtimecap.Principal) ([]computer.Session, error)
}

type ComputerToolOptions struct{ policy config.ComputerToolsConfig }

func NewComputerToolOptions(policy config.ComputerToolsConfig) ComputerToolOptions {
	encoded, _ := json.Marshal(policy)
	var cloned config.ComputerToolsConfig
	_ = json.Unmarshal(encoded, &cloned)
	return ComputerToolOptions{policy: cloned}
}

type computerToolRuntime struct {
	options ComputerToolOptions
	source  ComputerToolSource
}
type (
	ComputerTargetsTool struct{ runtime computerToolRuntime }
	ComputerSessionTool struct{ runtime computerToolRuntime }
	ComputerObserveTool struct{ runtime computerToolRuntime }
)

func NewComputerTargetsTool(options ComputerToolOptions, source ComputerToolSource) *ComputerTargetsTool {
	return &ComputerTargetsTool{runtime: computerToolRuntime{options: options, source: source}}
}

func NewComputerSessionTool(options ComputerToolOptions, source ComputerToolSource) *ComputerSessionTool {
	return &ComputerSessionTool{runtime: computerToolRuntime{options: options, source: source}}
}

func NewComputerObserveTool(options ComputerToolOptions, source ComputerToolSource) *ComputerObserveTool {
	return &ComputerObserveTool{runtime: computerToolRuntime{options: options, source: source}}
}

func (runtime computerToolRuntime) enabled(agent string) bool {
	if !runtime.options.policy.Enabled || runtime.source == nil {
		return false
	}
	for _, target := range runtime.options.policy.Targets {
		if slices.Contains(target.Agents, agent) {
			return true
		}
	}
	return false
}

func (tool *ComputerTargetsTool) ToolEnabledForAgent(agent string) bool {
	return tool.runtime.enabled(agent)
}

func (tool *ComputerSessionTool) ToolEnabledForAgent(agent string) bool {
	return tool.runtime.enabled(agent)
}

func (tool *ComputerObserveTool) ToolEnabledForAgent(agent string) bool {
	return tool.runtime.enabled(agent)
}

func computerPrincipal(ctx context.Context) (runtimecap.Principal, error) {
	runtime, found := toolshared.RuntimeCapabilities(ctx)
	principal, bound := runtime.Principal()
	if !found || !bound || principal.Validate() != nil {
		return runtimecap.Principal{}, computer.ErrDenied
	}
	return principal, nil
}

func computerToolRequestID(ctx context.Context) (string, error) {
	id := toolshared.ToolCallID(ctx)
	if id == "" {
		return "", computer.ErrDenied
	}
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:]), nil
}

func computerToolResult(value any) *toolshared.ToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return computerToolError(computer.ErrInvalid)
	}
	return toolshared.NewToolResult(string(encoded))
}

func computerToolError(err error) *toolshared.ToolResult {
	message := "Computer operation could not be confirmed. Do not assume success or open a replacement session."
	for _, known := range []error{computer.ErrDenied, computer.ErrStale, computer.ErrExpired, computer.ErrConflict, computer.ErrBusy} {
		if errors.Is(err, known) {
			message = known.Error()
			break
		}
	}
	return toolshared.ErrorResult(message)
}

func (*ComputerTargetsTool) Name() string { return "computer_targets" }
func (*ComputerTargetsTool) Description() string {
	return "Discover explicitly granted native computer windows. This is independent of browser access and does not capture pixels or launch a desktop worker. Select only a returned configured target alias."
}

func (*ComputerTargetsTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (*ComputerTargetsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}
func (*ComputerTargetsTool) ProtectedDurableResult(map[string]any) bool { return true }
func (tool *ComputerTargetsTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	principal, err := computerPrincipal(ctx)
	if err != nil || len(args) != 0 || !tool.runtime.enabled(principal.AgentID) {
		return computerToolError(computer.ErrDenied)
	}
	request, err := computerToolRequestID(ctx)
	if err != nil {
		return computerToolError(err)
	}
	targets, err := tool.runtime.source.Targets(ctx, principal, request)
	if err != nil {
		return computerToolError(err)
	}
	return computerToolResult(map[string]any{"targets": targets, "input_available": false})
}

func (*ComputerSessionTool) Name() string { return "computer_session" }
func (*ComputerSessionTool) Description() string {
	return "Open an explicitly granted computer target, inspect automation control, release automation, or hand the same native window to the user and suspend this turn. Handoff requires a useful self-contained prompt and the root user's interaction_language. Release ends automation only; never claim that the application closed or remains open. Replies are interpreted by the existing interaction lifecycle; do not reopen a replacement or capture before deciding whether to finish. No clicking, typing, scrolling or application closing is available."
}

func (*ComputerSessionTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"operation": map[string]any{"type": "string", "enum": []string{"open", "status", "handoff", "release"}},
		"target":    map[string]any{"type": "string"}, "computer_session_id": map[string]any{"type": "string"},
		"handoff_prompt": browserHandoffPromptSchema(),
		"interaction_language": map[string]any{
			"type": "string", "maxLength": interactions.MaxPromptLanguageLength,
			"description": "BCP-47 language of the root user command, regardless of the language of internal guidance.",
		},
	}, "required": []string{"operation"}, "additionalProperties": false}
}

func (*ComputerSessionTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func computerSessionView(session computer.Session) map[string]any {
	return map[string]any{
		"computer_session_id": session.Binding.SessionID, "control": session.Control,
		"profile": session.Binding.Target.Alias, "expires_at": session.ExpiresAt,
		"application_state": "not_checked",
	}
}

func (tool *ComputerSessionTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	principal, err := computerPrincipal(ctx)
	if err != nil || !tool.runtime.enabled(principal.AgentID) {
		return computerToolError(computer.ErrDenied)
	}
	request, err := computerToolRequestID(ctx)
	if err != nil {
		return computerToolError(err)
	}
	operation, _ := args["operation"].(string)
	id, _ := args["computer_session_id"].(string)
	var session computer.Session
	switch operation {
	case "open":
		target, ok := args["target"].(string)
		if !ok || target == "" || len(args) != 2 {
			return computerToolError(computer.ErrInvalid)
		}
		session, err = tool.runtime.source.Open(ctx, principal, target, request)
	case "status":
		if id == "" || len(args) != 2 {
			return computerToolError(computer.ErrInvalid)
		}
		session, err = tool.runtime.source.Status(ctx, principal, id, request)
	case "release":
		if id == "" || len(args) != 2 {
			return computerToolError(computer.ErrInvalid)
		}
		session, err = tool.release(ctx, principal, id, request)
	case "handoff":
		question, promptErr := parseLiveResourceHandoffPrompt(args["handoff_prompt"], "continue_computer")
		language, languageErr := interactions.CanonicalPromptLanguage(
			browserStringArgument(args, "interaction_language"),
		)
		if id == "" || len(args) != 4 || promptErr != nil || languageErr != nil {
			return computerToolError(computer.ErrInvalid)
		}
		session, err = tool.runtime.source.Control(ctx, principal, id, request, computer.EventHandoff)
		if err != nil {
			return computerToolError(err)
		}
		if !computerSessionOwned(session, principal, id) || session.Control != computer.ControlHuman ||
			!time.Now().Before(session.ExpiresAt) {
			return computerToolError(computer.ErrStale)
		}
		result := computerToolResult(computerSessionView(session))
		handoff := toolshared.LiveResourceHandoff{ResourceKind: "computer_session", ResourceID: id}
		result.Control.LiveHandoff = &handoff
		result.Control.Suspension = &interactions.SuspensionRequest{
			Kind:           interactions.KindQuestion,
			Questions:      []interactions.Question{question},
			PromptSummary:  question.Question,
			PromptLanguage: language,
			Timeout:        time.Until(session.ExpiresAt),
			Deadline:       session.ExpiresAt,
		}
		result.Control.ResolveSuspension = func(resolution context.Context, outcome interactions.Outcome) error {
			_, resolutionErr := tool.resolveHandoff(
				resolution,
				principal,
				handoff,
				toolshared.LiveResourceHandoffDispositionForOutcome(outcome),
			)
			return resolutionErr
		}
		return result
	default:
		return computerToolError(computer.ErrInvalid)
	}
	if err != nil {
		return computerToolError(err)
	}
	if !computerSessionOwned(session, principal, id) {
		return computerToolError(computer.ErrStale)
	}
	result := computerToolResult(computerSessionView(session))
	if operation == "release" {
		receipt, err := computerCleanupReceipt(session)
		if err != nil {
			return computerToolError(err)
		}
		result.WithDeliverable(&taskresult.Deliverable{LifecycleReceipts: []taskresult.Receipt{receipt}})
	}
	return result
}

func (*ComputerObserveTool) Name() string { return "computer_observe" }
func (*ComputerObserveTool) Description() string {
	return "Obtain a fresh scoped computer observation from the existing agent-controlled native window. Pixels and accessibility text are protected one-shot model context, not a saved file or chat attachment. Observe again after human control; never reuse earlier pixels as evidence of current state. No native input is available."
}

func (*ComputerObserveTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{"computer_session_id": map[string]any{"type": "string"}},
		"required": []string{"computer_session_id"}, "additionalProperties": false,
	}
}

func (*ComputerObserveTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}
func (*ComputerObserveTool) ProtectedDurableResult(map[string]any) bool { return true }
func (tool *ComputerObserveTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	principal, err := computerPrincipal(ctx)
	id, _ := args["computer_session_id"].(string)
	if err != nil || id == "" || len(args) != 1 || !tool.runtime.enabled(principal.AgentID) {
		return computerToolError(computer.ErrDenied)
	}
	request, err := computerToolRequestID(ctx)
	if err != nil {
		return computerToolError(err)
	}
	observation, err := tool.runtime.source.Observe(ctx, principal, id, request)
	if err != nil {
		return computerToolError(err)
	}
	defer clear(observation.Screenshot)
	if observation.Binding.Principal != principal || observation.Binding.SessionID != id ||
		observation.Validate(computer.Session{
			Binding: observation.Binding, Control: computer.ControlAgent,
			ExpiresAt: observation.ExpiresAt,
		}, time.Now()) != nil {
		return computerToolError(computer.ErrStale)
	}
	result := computerToolResult(map[string]any{
		"computer_session_id": id, "observation_id": observation.ID,
		"geometry": observation.Geometry, "captured_at": observation.CapturedAt, "expires_at": observation.ExpiresAt,
	})
	result.ContextText = observation.AccessibilityText
	if len(observation.Screenshot) > 0 {
		if observation.ContentType != "image/png" {
			return computerToolError(computer.ErrInvalid)
		}
		// Existing live-context projection accepts an already-resolved data URL.
		// No media index, filesystem path, outbound artifact or durable result owns it.
		result.ContextMedia = []string{
			"data:image/png;base64," + base64.StdEncoding.EncodeToString(observation.Screenshot),
		}
		result.ContextMediaRequiresVision = true
	}
	return result
}

func computerCleanupReceipt(session computer.Session) (taskresult.Receipt, error) {
	if session.Validate() != nil || !session.Control.Terminal() {
		return taskresult.Receipt{}, computer.ErrStale
	}
	return taskresult.Receipt{
		ID:   computerCleanupID(session.Binding.Principal, session.Binding.SessionID),
		Kind: taskresult.ReceiptKindResourceCleanup, Target: "computer:" + session.Binding.Target.Alias,
		Action: "release", Tool: "computer_session",
		Summary:  "Computer automation authority ended. Application state was not checked.",
		Metadata: map[string]string{"state": string(session.Control), "profile": session.Binding.Target.Alias},
	}, nil
}

func computerSessionOwned(session computer.Session, principal runtimecap.Principal, id string) bool {
	return session.Validate() == nil && session.Binding.Principal == principal &&
		(id == "" || session.Binding.SessionID == id)
}

func computerCleanupID(principal runtimecap.Principal, id string) string {
	encoded, _ := json.Marshal(struct {
		Domain    string
		ID        string
		Principal runtimecap.Principal
	}{"computer-cleanup", id, principal})
	digest := sha256.Sum256(encoded)
	return "computer_cleanup_" + hex.EncodeToString(digest[:16])
}

func (tool *ComputerSessionTool) release(
	ctx context.Context,
	principal runtimecap.Principal,
	id, request string,
) (computer.Session, error) {
	session, err := tool.runtime.source.Control(ctx, principal, id, request, computer.EventRelease)
	if err != nil {
		// Query-only reconciliation may prove a timed-out release or lease loss.
		// Never send another ownership-changing call from this error path.
		status, statusErr := tool.runtime.source.Status(ctx, principal, id, uuid.NewString())
		if statusErr == nil && computerSessionOwned(status, principal, id) && status.Control.Terminal() {
			return status, nil
		}
		return computer.Session{}, err
	}
	if !computerSessionOwned(session, principal, id) || !session.Control.Terminal() {
		return computer.Session{}, computer.ErrStale
	}
	return session, nil
}

func (tool *ComputerSessionTool) ResolveLiveResourceHandoff(
	ctx context.Context, handoff toolshared.LiveResourceHandoff, disposition toolshared.LiveResourceHandoffDisposition,
) error {
	_, err := tool.ResolveLiveResourceHandoffWithResult(ctx, handoff, disposition)
	return err
}

func (tool *ComputerSessionTool) ResolveLiveResourceHandoffWithResult(
	ctx context.Context, handoff toolshared.LiveResourceHandoff, disposition toolshared.LiveResourceHandoffDisposition,
) (TurnCleanupResult, error) {
	principal, err := computerPrincipal(ctx)
	if err != nil {
		return TurnCleanupResult{}, err
	}
	return tool.resolveHandoff(ctx, principal, handoff, disposition)
}

func (tool *ComputerSessionTool) resolveHandoff(
	ctx context.Context, principal runtimecap.Principal, handoff toolshared.LiveResourceHandoff,
	disposition toolshared.LiveResourceHandoffDisposition,
) (TurnCleanupResult, error) {
	if tool == nil || tool.runtime.source == nil || handoff.ResourceKind != "computer_session" ||
		handoff.ResourceID == "" {
		return TurnCleanupResult{}, computer.ErrDenied
	}
	id := handoff.ResourceID
	if disposition == toolshared.LiveResourceHandoffAbandon {
		session, err := tool.release(ctx, principal, id, uuid.NewString())
		if err != nil {
			return TurnCleanupResult{}, err
		}
		receipt, err := computerCleanupReceipt(session)
		if err != nil {
			return TurnCleanupResult{}, err
		}
		return TurnCleanupResult{Receipts: []taskresult.Receipt{receipt}}, nil
	}
	if disposition != toolshared.LiveResourceHandoffResume {
		return TurnCleanupResult{}, computer.ErrDenied
	}
	session, err := tool.runtime.source.Status(ctx, principal, id, uuid.NewString())
	if err != nil {
		return TurnCleanupResult{}, err
	}
	if session.Control == computer.ControlHuman {
		session, err = tool.runtime.source.Control(ctx, principal, id, uuid.NewString(), computer.EventRequestResume)
		if err != nil {
			return TurnCleanupResult{}, err
		}
	}
	if session.Control == computer.ControlResumePending {
		session, err = tool.runtime.source.Control(ctx, principal, id, uuid.NewString(), computer.EventResume)
		if err != nil {
			return TurnCleanupResult{}, err
		}
	}
	if session.AgentAuthority(time.Now()) != nil {
		return TurnCleanupResult{}, computer.ErrStale
	}
	return TurnCleanupResult{}, nil // No capture until the resumed model explicitly asks.
}

func (tool *ComputerSessionTool) CleanupTurn(ctx context.Context) error {
	_, err := tool.CleanupTurnWithResult(ctx)
	return err
}

func (tool *ComputerSessionTool) CleanupTurnWithResult(ctx context.Context) (TurnCleanupResult, error) {
	if tool == nil || tool.runtime.source == nil {
		return TurnCleanupResult{}, nil
	}
	principal, err := computerPrincipal(ctx)
	if err != nil {
		return TurnCleanupResult{}, err
	}
	sessions, err := tool.runtime.source.OwnedSessions(ctx, principal)
	if err != nil {
		return TurnCleanupResult{}, err
	}
	var result TurnCleanupResult
	var cleanupErr error
	for _, session := range sessions {
		if session.Control.Terminal() {
			continue
		}
		closed, err := tool.resolveHandoff(ctx, principal, toolshared.LiveResourceHandoff{
			ResourceKind: "computer_session", ResourceID: session.Binding.SessionID,
		}, toolshared.LiveResourceHandoffAbandon)
		result.Receipts = append(result.Receipts, closed.Receipts...)
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return result, cleanupErr
}

func (tool *ComputerSessionTool) TurnFinalizationRequirement(
	ctx context.Context,
) (TurnFinalizationRequirement, bool, error) {
	if tool == nil || tool.runtime.source == nil {
		return TurnFinalizationRequirement{}, false, nil
	}
	principal, err := computerPrincipal(ctx)
	if err != nil {
		return TurnFinalizationRequirement{}, false, err
	}
	sessions, err := tool.runtime.source.OwnedSessions(ctx, principal)
	if err != nil {
		return TurnFinalizationRequirement{}, false, err
	}
	live := 0
	for _, session := range sessions {
		if !session.Control.Terminal() {
			live++
		}
	}
	if live == 0 {
		return TurnFinalizationRequirement{}, false, nil
	}
	return TurnFinalizationRequirement{
		RecoveryKind: taskresult.ObjectiveKindResourceDisposition,
		Instruction: fmt.Sprintf(
			"%d computer sessions remain owned by this turn. Use computer_session to release each, or hand an existing session to the user with useful results and a localized open question. Release ends automation only; application state is not checked. Do not open replacements or claim an application remains open. Handoff suspends the same turn.",
			live,
		),
	}, true, nil
}

func (tool *ComputerSessionTool) VerifyResourceDisposition(
	ctx context.Context, args map[string]any, result *toolshared.ToolResult,
) bool {
	principal, err := computerPrincipal(ctx)
	if err != nil {
		return false
	}
	id, _ := args["computer_session_id"].(string)
	if args["operation"] != "release" || len(args) != 2 || id == "" || result == nil || result.Deliverable == nil ||
		result.IsError {
		return false
	}
	for _, receipt := range result.Deliverable.LifecycleReceipts {
		if receipt.ID == computerCleanupID(principal, id) &&
			receipt.Kind == taskresult.ReceiptKindResourceCleanup && receipt.Tool == tool.Name() && receipt.Action == "release" &&
			computer.ControlState(receipt.Metadata["state"]).
				Terminal() &&
			receipt.Target == "computer:"+receipt.Metadata["profile"] {
			return true
		}
	}
	return false
}

func (tool *ComputerSessionTool) ObjectiveRecoveryParameters(kind string) (map[string]any, bool) {
	if kind != taskresult.ObjectiveKindResourceDisposition && kind != taskresult.ObjectiveKindLiveHandoff {
		return nil, false
	}
	schema := tool.Parameters()
	properties := schema["properties"].(map[string]any)
	delete(properties, "target")
	operations := []string{"handoff"}
	if kind == taskresult.ObjectiveKindResourceDisposition {
		operations = append(operations, "release")
	}
	properties["operation"] = map[string]any{"type": "string", "enum": operations}
	schema["required"] = []string{"operation", "computer_session_id"}
	return schema, true
}
