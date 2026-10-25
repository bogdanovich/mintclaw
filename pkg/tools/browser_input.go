package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type BrowserInputTool struct{ runtime *browserToolRuntime }

func NewBrowserInputTool(options BrowserToolOptions, source BrowserToolSource) *BrowserInputTool {
	return &BrowserInputTool{runtime: newBrowserToolRuntime(options, source)}
}

func (*BrowserInputTool) Name() string { return "browser_input" }

func (*BrowserInputTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	return []runtimecap.CapabilityID{runtimecap.CapabilityBrowserInput}
}

func (tool *BrowserInputTool) ToolEnabledForAgent(agentID string) bool {
	if tool == nil || !tool.runtime.enabledForAgent(agentID) ||
		tool.runtime.config.Limits.Effective().PreparedSeconds <= 60 {
		return false
	}
	_, ok := tool.runtime.source.(BrowserProtectedInputSource)
	return ok
}

func (*BrowserInputTool) Description() string {
	return "Ask the user privately for one browser text-field value and fill that exact fresh observed field. " +
		"For a text CAPTCHA before private login, send its current image with browser_capture or browser_observe " +
		"screenshot=true first, then ask for the code here. This question retains the same browser while waiting; " +
		"do not close it or use an ordinary request_user_input question for this field. Interactive puzzles need " +
		"a configured visible browser handoff instead. Private-login sessions retain their existing input denial. " +
		"Use operation ask with the current browser session, tab, snapshot, field ref and a self-contained question " +
		"in the user's language. Never repeat entered values in the question. " +
		"Do not put the value in arguments or ask for it through ordinary chat. " +
		"This does not submit the form or grant permission for a later action. Operation resume consumes only the " +
		"exact native protected answer_ref after the runtime resumes this question; do not invent a reference."
}

func (*BrowserInputTool) Parameters() map[string]any {
	properties := map[string]any{
		"operation":           map[string]any{"type": "string", "enum": []string{"ask", "resume"}},
		"snapshot_generation": map[string]any{"type": "integer", "minimum": 1},
		"context_generation":  map[string]any{"type": "integer", "minimum": 1},
		"question": map[string]any{
			"type": "string", "minLength": 1, "maxLength": interactions.MaxQuestionLength,
		},
		"answer_ref": map[string]any{
			"type": "string", "minLength": 1, "maxLength": interactions.MaxProtectedReference,
		},
	}
	for _, name := range []string{
		"browser_session_id", "tab_id", "snapshot_id", "frame_id", "context_catalog_id", "ref",
	} {
		properties[name] = map[string]any{"type": "string", "minLength": 1, "maxLength": browser.MaxIdentifierBytes}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "properties": properties,
		"required": []string{"operation"},
	}
}

func (*BrowserInputTool) ToolLoopSemantics() loopguard.Semantics        { return loopguard.SemanticsMutating }
func (*BrowserInputTool) ProtectedDurableArguments(map[string]any) bool { return true }
func (*BrowserInputTool) ProtectedDurableResult(map[string]any) bool    { return true }

func (*BrowserInputTool) DurableArguments(args map[string]any) (map[string]any, error) {
	if args["operation"] == "resume" {
		reference, _ := args["answer_ref"].(string)
		if _, err := browserInputPreparedID(reference); err != nil {
			reference = "browser_fill.prepared_redacted"
		}
		// Native opaque identity is value-free and must survive a separate
		// approval suspension. It still confers no execution authority by itself.
		return map[string]any{"operation": "resume", "answer_ref": reference}, nil
	}
	return map[string]any{
		"operation": "ask", "browser_session_id": "protected", "tab_id": "protected",
		"snapshot_id": "protected", "snapshot_generation": 1, "ref": "protected",
		"question": "Protected browser form input.",
	}, nil
}

func (*BrowserInputTool) CanonicalArguments(args map[string]any) (map[string]any, error) {
	canonical, err := cloneBrowserToolArguments(args)
	if err != nil {
		return nil, err
	}
	// Provider compatibility schemas may encode absent optional fields as null.
	for _, field := range []string{
		"question", "answer_ref", "browser_session_id", "tab_id", "snapshot_id", "snapshot_generation", "ref",
		"frame_id", "context_catalog_id", "context_generation",
	} {
		if value, present := canonical[field]; present && value == nil {
			delete(canonical, field)
		}
	}
	return canonical, nil
}

func (*BrowserInputTool) ProtectedAnswerContinuationArguments(reference string) (map[string]any, error) {
	if _, err := browserInputPreparedID(reference); err != nil {
		return nil, err
	}
	return map[string]any{"operation": "resume", "answer_ref": reference}, nil
}

func (tool *BrowserInputTool) source(ctx context.Context) (BrowserProtectedInputSource, browser.Owner, error) {
	capabilities, available := toolshared.RuntimeCapabilities(ctx)
	if !available || capabilities.Kind() != runtimecap.KindGateway ||
		!tool.ToolEnabledForAgent(toolshared.ToolAgentID(ctx)) || toolshared.ToolHistoryDisabled(ctx) {
		return nil, browser.Owner{}, browser.ErrDenied
	}
	source, ok := tool.runtime.source.(BrowserProtectedInputSource)
	if !ok {
		return nil, browser.Owner{}, browser.ErrDriverIncompatible
	}
	owner, err := browserOwnerFromContext(ctx)
	return source, owner, err
}

func (tool *BrowserInputTool) ApprovalArguments(ctx context.Context, args map[string]any) (map[string]any, error) {
	if args["operation"] == "ask" {
		return tool.CanonicalArguments(args)
	}
	prepared, _, _, err := tool.resumePreparation(ctx, args)
	if err != nil {
		return nil, &browserActionSafeDenialError{cause: err}
	}
	return BrowserActionApprovalArguments(prepared), nil
}

func (tool *BrowserInputTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	args, err := tool.CanonicalArguments(args)
	if err != nil {
		return browserActionToolError(browser.ErrInvalid)
	}
	source, owner, err := tool.source(ctx)
	if err != nil {
		return browserActionToolError(err)
	}
	switch args["operation"] {
	case "ask":
		return tool.ask(ctx, source, owner, args)
	case "resume":
		return tool.resume(ctx, args)
	default:
		return browserActionToolError(browser.ErrInvalid)
	}
}

func (tool *BrowserInputTool) ask(
	ctx context.Context, source BrowserProtectedInputSource, owner browser.Owner, args map[string]any,
) *toolshared.ToolResult {
	question, ok := args["question"].(string)
	if !ok || strings.TrimSpace(question) == "" || browserActionContextAuthorityInvalid(args) {
		return browserActionToolError(browser.ErrInvalid)
	}
	for key := range args {
		switch key {
		case "operation", "question", "browser_session_id", "tab_id", "snapshot_id", "snapshot_generation", "ref",
			"frame_id", "context_catalog_id", "context_generation":
		default:
			return browserActionToolError(browser.ErrInvalid)
		}
	}
	generation, valid := browserInteger(args["snapshot_generation"])
	if !valid || generation < 1 {
		return browserActionToolError(browser.ErrInvalid)
	}
	contextGeneration := 0
	if raw, found := args["context_generation"]; found {
		contextGeneration, valid = browserInteger(raw)
		if !valid || contextGeneration < 1 {
			return browserActionToolError(browser.ErrInvalid)
		}
	}
	requestID, err := browserRequestID(ctx)
	if err != nil {
		return browserActionToolError(browser.ErrInvalid)
	}
	request := browser.PrepareActionRequest{
		Owner:              owner,
		RequestID:          requestID,
		SessionID:          browserStringArgument(args, "browser_session_id"),
		TabID:              browserStringArgument(args, "tab_id"),
		SnapshotID:         browserStringArgument(args, "snapshot_id"),
		SnapshotGeneration: uint64(generation),
		FrameID:            browserStringArgument(args, "frame_id"),
		ContextCatalogID: browserStringArgument(
			args,
			"context_catalog_id",
		),
		ContextGeneration: uint64(contextGeneration),
		Action:            browser.Action{Kind: browser.ActionFill, Ref: browserStringArgument(args, "ref")},
	}
	token, err := source.CreateProtectedFillBinding(ctx, request)
	if err != nil {
		return browserActionToolError(err)
	}
	target, err := source.InspectProtectedFillBinding(ctx, owner, token)
	if err != nil {
		return browserActionToolError(err)
	}
	deadline := time.Unix(0, target.ExpiresAt)
	remaining := time.Until(deadline)
	if remaining < time.Minute {
		return browserActionToolError(browser.ErrStale)
	}
	suspension := &interactions.SuspensionRequest{
		Kind: interactions.KindQuestion, Questions: []interactions.Question{{ID: "browser_value", Question: question}},
		Timeout: remaining, Deadline: deadline,
		ProtectedAnswer: &interactions.ProtectedAnswerBinding{Namespace: BrowserProtectedAnswerNamespace, Token: token},
	}
	if interactions.ValidateSuspensionRequest(*suspension) != nil {
		return browserActionToolError(browser.ErrInvalid)
	}
	result := toolshared.NewToolResult("Protected browser form input is waiting.")
	result.BrowserInputTarget = target.Observation
	result.Control.Suspension = suspension
	retainBrowserInputResource(result, source, owner, request.SessionID)
	return result.WithDeliveryIntent(toolshared.DeliverySilent)
}

func (tool *BrowserInputTool) resumePreparation(
	ctx context.Context, args map[string]any,
) (browser.Preparation, BrowserProtectedInputSource, browser.Owner, error) {
	source, owner, err := tool.source(ctx)
	if err != nil {
		return browser.Preparation{}, nil, browser.Owner{}, err
	}
	reference, ok := args["answer_ref"].(string)
	if !ok || len(args) != 2 || args["operation"] != "resume" ||
		(toolshared.ToolProtectedAnswerContinuation(ctx) != reference && !toolshared.ToolApprovalContinuation(ctx)) {
		return browser.Preparation{}, nil, browser.Owner{}, browser.ErrDenied
	}
	id, err := browserInputPreparedID(reference)
	if err != nil {
		return browser.Preparation{}, nil, browser.Owner{}, err
	}
	prepared, err := source.ProtectedFillPreparation(ctx, owner, id)
	return prepared, source, owner, err
}

func (tool *BrowserInputTool) resume(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	prepared, source, owner, err := tool.resumePreparation(ctx, args)
	if err != nil {
		return browserActionToolError(err)
	}
	reference, _ := args["answer_ref"].(string)
	if toolshared.ToolProtectedAnswerContinuation(ctx) != reference {
		approved, found := toolshared.ToolApprovalArguments(ctx)
		if !found || !reflect.DeepEqual(approved, BrowserActionApprovalArguments(prepared)) {
			return browserActionToolError(browser.ErrDenied)
		}
	}
	if prepared.RequiresApproval && !toolshared.ToolApprovalContinuation(ctx) && !toolshared.ToolApprovalBypass(ctx) {
		result := toolshared.NewToolResult("Protected fill requires native approval.")
		result.Control.Suspension = &interactions.SuspensionRequest{
			Kind: interactions.KindApproval, PromptSummary: browserApprovalDescription(prepared.Action),
			Timeout:  time.Duration(tool.runtime.config.Limits.Effective().PreparedSeconds) * time.Second,
			Deadline: time.Unix(0, prepared.Action.ExpiresAt),
		}
		retainBrowserInputResource(result, source, owner, prepared.Action.SessionID)
		return result.WithDeliveryIntent(toolshared.DeliverySilent)
	}
	var approval *browser.ApprovalBinding
	if prepared.RequiresApproval {
		approval = &prepared.Approval
	}
	invocation, err := source.ExecuteAction(ctx, owner, prepared.Action.ID, approval)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		return browserActionToolError(errors.Join(err, browser.ErrDriverRejected))
	}
	observation, err := source.Observe(ctx, owner, prepared.Action.SessionID, prepared.Action.TabID)
	if err != nil {
		return tool.runtime.result(map[string]any{
			"invocation_id": invocation.ID, "state": invocation.State, "effect": invocation.Effect,
			"observation_unavailable": true, "recovery": "observe_same_session_without_replaying_fill",
		})
	}
	return tool.runtime.result(map[string]any{
		"invocation_id": invocation.ID, "state": invocation.State, "effect": invocation.Effect,
		"observation": tool.runtime.observationResult(observation),
	})
}

func retainBrowserInputResource(
	result *toolshared.ToolResult, source BrowserProtectedInputSource, owner browser.Owner, sessionID string,
) {
	handoff := toolshared.LiveResourceHandoff{ResourceKind: "browser_input", ResourceID: sessionID}
	result.Control.LiveHandoff = &handoff
	result.Control.ResolveSuspension = func(resolutionCtx context.Context, outcome interactions.Outcome) error {
		return resolveBrowserInputResource(resolutionCtx, source, owner, handoff,
			toolshared.LiveResourceHandoffDispositionForOutcome(outcome))
	}
}

func (tool *BrowserInputTool) ResolveLiveResourceHandoff(
	ctx context.Context, handoff toolshared.LiveResourceHandoff, disposition toolshared.LiveResourceHandoffDisposition,
) error {
	source, owner, err := tool.source(ctx)
	if err != nil {
		return err
	}
	return resolveBrowserInputResource(ctx, source, owner, handoff, disposition)
}

func resolveBrowserInputResource(
	ctx context.Context, source BrowserProtectedInputSource, owner browser.Owner,
	handoff toolshared.LiveResourceHandoff, disposition toolshared.LiveResourceHandoffDisposition,
) error {
	if handoff.ResourceKind != "browser_input" || handoff.ResourceID == "" {
		return browser.ErrInvalid
	}
	if disposition == toolshared.LiveResourceHandoffAbandon {
		session, err := source.Close(ctx, owner, handoff.ResourceID)
		if err != nil || !session.State.Terminal() {
			return errors.Join(browser.ErrWorkerUnavailable, err)
		}
		return nil
	}
	if disposition != toolshared.LiveResourceHandoffResume {
		return browser.ErrInvalid
	}
	session, err := source.Status(ctx, owner, handoff.ResourceID)
	if err != nil || session.State != browser.SessionReady || session.EffectiveController() != browser.ControllerAgent {
		return errors.Join(browser.ErrWorkerUnavailable, err)
	}
	return nil
}
