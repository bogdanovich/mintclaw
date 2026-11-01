package tools

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

const BrowserProtectedAnswerNamespace = "browser.fill.v1"

type BrowserProtectedInputSource interface {
	BrowserToolSource
	CreateProtectedFillBinding(context.Context, browser.PrepareActionRequest) (string, error)
	InspectProtectedFillBinding(context.Context, browser.Owner, string) (browser.ProtectedFillTarget, error)
	PrepareProtectedFill(context.Context, browser.Owner, string, string) (browser.Preparation, error)
	ProtectedFillPreparation(context.Context, browser.Owner, string) (browser.Preparation, error)
	CloseOwner(context.Context, browser.Owner) ([]browser.Session, error)
}

// BrowserProtectedAnswerSink borrows the current generation's source for each
// call. The interaction coordinator owns claims; the broker owns preparations
// and transient values. No pending-answer map or persistent value store lives here.
type BrowserProtectedAnswerSink struct {
	source func() (BrowserProtectedInputSource, error)
}

func NewBrowserProtectedAnswerSink(
	source func() (BrowserProtectedInputSource, error),
) *BrowserProtectedAnswerSink {
	return &BrowserProtectedAnswerSink{source: source}
}

func (*BrowserProtectedAnswerSink) Namespace() string { return BrowserProtectedAnswerNamespace }

func (sink *BrowserProtectedAnswerSink) Accept(
	ctx context.Context, request interactions.ProtectedAnswerSinkRequest,
) (interactions.ProtectedAnswerReceipt, error) {
	if request.Intent != interactions.ProtectedAnswerValue || request.InteractionID == "" ||
		request.IdempotencyKey == "" {
		return interactions.ProtectedAnswerReceipt{}, browser.ErrInvalid
	}
	source, owner, target, err := sink.target(ctx, request.Workspace, request.Route, request.Origin, request.Binding)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	prepared, err := source.PrepareProtectedFill(ctx, owner, request.Binding.Token, request.Text)
	if err != nil {
		return interactions.ProtectedAnswerReceipt{}, err
	}
	if !browserInputPreparationMatches(prepared, target) {
		return interactions.ProtectedAnswerReceipt{}, browser.ErrConflict
	}
	return interactions.ProtectedAnswerReceipt{
		Reference: "browser_fill." + prepared.Action.ID, State: "prepared",
	}, nil
}

func (sink *BrowserProtectedAnswerSink) Commit(
	ctx context.Context, request interactions.ProtectedAnswerCommitRequest,
) error {
	source, owner, target, err := sink.target(ctx, request.Workspace, request.Route, request.Origin, request.Binding)
	if err != nil {
		return err
	}
	id, err := browserInputPreparedID(request.Receipt.Reference)
	if err != nil || request.Receipt.State != "prepared" || request.InteractionID == "" {
		return browser.ErrInvalid
	}
	prepared, err := source.ProtectedFillPreparation(ctx, owner, id)
	if err != nil {
		return err
	}
	if !browserInputPreparationMatches(prepared, target) {
		return browser.ErrConflict
	}
	return nil
}

func (sink *BrowserProtectedAnswerSink) Discard(
	ctx context.Context, request interactions.ProtectedAnswerDiscardRequest,
) error {
	if !request.Force {
		// A waiting-record recovery read can race the winning answer's stage and
		// claim. Transient slots do not survive restart; never erase them here.
		return nil
	}
	return sink.closeOwner(ctx, request.Workspace, request.Route, request.Origin, request.Binding)
}

func (sink *BrowserProtectedAnswerSink) Cancel(
	ctx context.Context, request interactions.ProtectedAnswerCancelRequest,
) error {
	if request.InteractionID == "" || request.IdempotencyKey == "" {
		return browser.ErrInvalid
	}
	return sink.closeOwner(ctx, request.Workspace, request.Route, request.Origin, request.Binding)
}

func (sink *BrowserProtectedAnswerSink) target(
	ctx context.Context, workspace string, route interactions.Route, origin interactions.ProtectedAnswerOrigin,
	binding interactions.ProtectedAnswerBinding,
) (BrowserProtectedInputSource, browser.Owner, browser.ProtectedFillTarget, error) {
	source, owner, err := sink.owner(workspace, route, origin, binding)
	if err != nil {
		return nil, browser.Owner{}, browser.ProtectedFillTarget{}, err
	}
	target, err := source.InspectProtectedFillBinding(ctx, owner, binding.Token)
	if err != nil || target.Request.RequestID != browser.OpaqueRequestID(origin.ExecutionID, origin.ToolCallID) {
		return nil, browser.Owner{}, browser.ProtectedFillTarget{}, browser.ErrInvalid
	}
	return source, owner, target, nil
}

func (sink *BrowserProtectedAnswerSink) owner(
	workspace string, route interactions.Route, origin interactions.ProtectedAnswerOrigin,
	binding interactions.ProtectedAnswerBinding,
) (BrowserProtectedInputSource, browser.Owner, error) {
	if sink == nil || sink.source == nil || !filepath.IsAbs(workspace) ||
		binding.Namespace != BrowserProtectedAnswerNamespace || origin.ToolName != "browser_input" ||
		strings.TrimSpace(origin.ActorID) == "" || strings.TrimSpace(origin.ExecutionID) == "" ||
		strings.TrimSpace(origin.ToolCallID) == "" || strings.TrimSpace(route.AgentID) == "" ||
		strings.TrimSpace(route.Channel) == "" || strings.TrimSpace(route.SenderID) == "" {
		return nil, browser.Owner{}, browser.ErrInvalid
	}
	key := route.RouteSessionKey
	if key == "" {
		key = route.SessionKey
	}
	if strings.TrimSpace(key) == "" {
		return nil, browser.Owner{}, browser.ErrInvalid
	}
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID(
			origin.ActorID,
		),
		AgentID:     browser.OpaqueAgentID(routing.NormalizeAgentID(route.AgentID)),
		SessionKey:  browser.OpaqueSessionKey(key),
		ExecutionID: browser.OpaqueExecutionID(origin.ExecutionID),
	}
	source, err := sink.source()
	if err != nil || source == nil {
		return nil, browser.Owner{}, browser.ErrWorkerUnavailable
	}
	return source, owner, nil
}

func (sink *BrowserProtectedAnswerSink) closeOwner(
	ctx context.Context, workspace string, route interactions.Route, origin interactions.ProtectedAnswerOrigin,
	binding interactions.ProtectedAnswerBinding,
) error {
	source, owner, err := sink.owner(workspace, route, origin, binding)
	if err != nil {
		return err
	}
	// Cancellation owns the originating execution even after its token's broker
	// generation is gone. Native owner-scoped cleanup needs no value or token decode.
	sessions, err := source.CloseOwner(ctx, owner)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if !session.State.Terminal() {
			return errors.New("protected browser input cleanup is incomplete")
		}
	}
	return nil
}

func browserInputPreparationMatches(prepared browser.Preparation, target browser.ProtectedFillTarget) bool {
	call, request := prepared.Action, target.Request
	return call.Owner == request.Owner && call.RequestID == request.RequestID && call.SessionID == request.SessionID &&
		call.TabID == request.TabID && call.FrameID == request.FrameID && call.SnapshotID == request.SnapshotID &&
		call.SnapshotGeneration == request.SnapshotGeneration && call.ContextCatalogID == request.ContextCatalogID &&
		call.ContextGeneration == request.ContextGeneration && call.Action == request.Action
}

func browserInputPreparedID(reference string) (string, error) {
	id, found := strings.CutPrefix(reference, "browser_fill.")
	if !found || strings.TrimSpace(reference) != reference || !strings.HasPrefix(id, "prepared_") ||
		len(id) != len("prepared_")+32 {
		return "", browser.ErrInvalid
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id, "prepared_")); err != nil {
		return "", browser.ErrInvalid
	}
	return id, nil
}
