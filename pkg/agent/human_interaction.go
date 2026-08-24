package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	interactionMessageKind = "human_interaction"
	interactionIDMetadata  = bus.OutboundMetadataKeyInteractionID
	interactionShortIDMeta = bus.OutboundMetadataKeyInteractionShortID
)

type humanInteractionRuntime struct {
	al *AgentLoop
}

func (al *AgentLoop) cleanupInteractionOriginTools(
	ctx context.Context,
	agent *AgentInstance,
	record interactions.Record,
) {
	if agent == nil || agent.Tools == nil || strings.TrimSpace(record.Origin.ExecutionID) == "" {
		return
	}
	inbound := cloneInboundContext(record.Origin.ExecutionContext)
	if inbound == nil {
		fallback := inboundContextForInteraction(record.Route)
		inbound = &fallback
	}
	routeSessionKey := strings.TrimSpace(record.Route.RouteSessionKey)
	if routeSessionKey == "" {
		routeSessionKey = strings.TrimSpace(record.Route.SessionKey)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	cleanupCtx = toolshared.WithToolInboundContext(
		cleanupCtx,
		inbound.Channel,
		inbound.ChatID,
		inbound.MessageID,
		inbound.ReplyToMessageID,
	)
	cleanupCtx = toolshared.WithToolInboundMetadata(cleanupCtx, *inbound)
	cleanupCtx = toolshared.WithToolTopicID(cleanupCtx, originTopicID(inbound))
	cleanupCtx = toolshared.WithToolSessionContext(
		cleanupCtx,
		agent.ID,
		record.Route.SessionKey,
		nil,
	)
	cleanupCtx = toolshared.WithToolRouteSessionKey(cleanupCtx, routeSessionKey)
	cleanupCtx = toolshared.WithToolExecutionIdentity(
		cleanupCtx,
		agent.Workspace,
		record.Origin.ExecutionID,
	)
	if err := agent.Tools.CleanupTurn(cleanupCtx); err != nil {
		logger.WarnCF("agent", "Terminal interaction resource cleanup failed", map[string]any{
			"agent_id":       agent.ID,
			"interaction_id": record.ID,
		})
	}
}

type InteractionEventPayload struct {
	InteractionID string                 `json:"interaction_id"`
	ShortID       string                 `json:"short_id,omitempty"`
	Kind          interactions.Kind      `json:"kind"`
	Event         interactions.EventType `json:"event"`
	Status        interactions.Status    `json:"status"`
	Outcome       interactions.Outcome   `json:"outcome,omitempty"`
	Revision      int64                  `json:"revision"`
	Code          string                 `json:"code,omitempty"`
	Success       *bool                  `json:"success,omitempty"`
}

func (al *AgentLoop) humanInteractionRuntime() *humanInteractionRuntime {
	if al == nil {
		return nil
	}
	return &humanInteractionRuntime{al: al}
}

func (al *AgentLoop) interactionRegistryForWorkspace(workspace string) *interactions.Registry {
	if al == nil {
		return nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	if existing, ok := al.interactionRegistries.Load(workspace); ok {
		registry, _ := existing.(*interactions.Registry)
		return registry
	}
	options := interactions.Options{}
	if cfg := al.GetConfig(); cfg != nil {
		options.TerminalRetention = cfg.Tools.RequestUserInput.Retention()
	}
	storePath := interactions.WorkspaceStorePath(workspace)
	if layout, ok := al.codingLayoutForWorkspace(workspace); ok {
		storePath = layout.StatePaths().InteractionFile
	}
	registry := interactions.NewRegistryWithOptions(
		storePath,
		options,
	)
	if al.codingProfile != nil && registry.LastLoadError() != nil {
		al.runtimeInitErr = fmt.Errorf("load coding interaction registry: %w", registry.LastLoadError())
	}
	actual, loaded := al.interactionRegistries.LoadOrStore(workspace, registry)
	stored, _ := actual.(*interactions.Registry)
	if stored == nil {
		stored = registry
	}
	if !loaded {
		stored.Subscribe(func(observation interactions.EventObservation) {
			al.observeInteractionEvent(workspace, observation)
		})
		stats := stored.Stats()
		logger.InfoCF("agent", "Loaded human interaction registry", map[string]any{
			"workspace":       workspace,
			"records":         stats.RecordCount,
			"nonterminal":     stats.NonterminalCount,
			"retention_hours": int(stats.Retention / time.Hour),
			"load_error":      errString(stored.LastLoadError()),
		})
	}
	return stored
}

func (al *AgentLoop) observeInteractionEvent(
	workspace string,
	observation interactions.EventObservation,
) {
	if al == nil {
		return
	}
	al.resolveInteractionDomainState(observation)
	if observation.Record.Status != interactions.StatusCreated {
		al.abandonInactiveInteractionPrompt(observation.Record)
	}
	kind := runtimeKindForInteractionEvent(observation.Event.Type)
	if kind == "" {
		return
	}
	record := observation.Record
	chained := observation.Event.Code == "continued_with_next_interaction"
	if !chained && (record.Status == interactions.StatusResolved || record.Status == interactions.StatusCancelled ||
		record.Status == interactions.StatusFailed) {
		al.dismissTerminalInteractionToolFeedback(record)
	}
	al.emitEvent(kind, HookMeta{
		TraceScope: runtimeevents.NewTraceScope(workspace, record.Origin.TurnID),
		AgentID:    record.Route.AgentID,
		SessionKey: record.Route.SessionKey,
		Source:     "interaction_registry",
	}, InteractionEventPayload{
		InteractionID: record.ID,
		ShortID:       record.ShortID,
		Kind:          record.Kind,
		Event:         observation.Event.Type,
		Status:        record.Status,
		Outcome:       record.Outcome,
		Revision:      record.Revision,
		Code:          observation.Event.Code,
		Success:       observation.Event.Success,
	})
}

func (al *AgentLoop) abandonInactiveInteractionPrompt(record interactions.Record) {
	deliveryID := strings.TrimSpace(record.PromptDeliveryID)
	coordinator := al.outboundCoordinator()
	if deliveryID == "" || coordinator == nil {
		return
	}
	if _, err := coordinator.Abandon(deliveryID, outbox.Outcome{
		Error: "interaction prompt is no longer active",
	}); err != nil {
		logger.WarnCF("agent", "Failed to abandon inactive interaction prompt", map[string]any{
			"interaction_id": record.ID,
			"delivery_id":    deliveryID,
			"error":          err.Error(),
		})
	}
}

func (al *AgentLoop) dismissTerminalInteractionToolFeedback(record interactions.Record) {
	if al == nil || al.channelManager == nil || strings.TrimSpace(record.Route.Channel) == "" ||
		strings.TrimSpace(record.Route.ChatID) == "" {
		return
	}
	target := toolFeedbackTargetForSession(
		record.Route.Channel,
		record.Route.ChatID,
		record.Origin.ExecutionContext,
		interactionContinuationSessionKey(record),
		nil,
	)
	al.toolFeedbackPublisher().dismissToolFeedback(context.Background(), target)
}

func (al *AgentLoop) resolveInteractionDomainState(observation interactions.EventObservation) {
	if al == nil {
		return
	}
	switch observation.Event.Type {
	case interactions.EventAnswerClaimed, interactions.EventCancelled, interactions.EventFailed:
	default:
		return
	}
	value, ok := al.interactionResolutions.LoadAndDelete(observation.Record.ID)
	if !ok {
		return
	}
	resolve, ok := value.(func(context.Context, interactions.Outcome) error)
	if !ok || resolve == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := resolve(ctx, observation.Record.Outcome); err != nil {
		logger.WarnCF("agent", "Human interaction domain resolution failed", map[string]any{
			"interaction_id": observation.Record.ID,
			"error":          err.Error(),
		})
	}
}

func runtimeKindForInteractionEvent(event interactions.EventType) runtimeevents.Kind {
	switch event {
	case interactions.EventCreated:
		return runtimeevents.KindAgentInteractionCreated
	case interactions.EventPromptDelivery, interactions.EventFinalDelivery:
		return runtimeevents.KindAgentInteractionDelivery
	case interactions.EventWaiting:
		return runtimeevents.KindAgentInteractionWaiting
	case interactions.EventAnswerClaimed:
		return runtimeevents.KindAgentInteractionAnswer
	case interactions.EventResumeStarted, interactions.EventApprovalConsumed,
		interactions.EventApprovalExpired,
		interactions.EventRecoveryObserved, interactions.EventCanceling:
		return runtimeevents.KindAgentInteractionResume
	case interactions.EventResolved, interactions.EventCancelled, interactions.EventFailed:
		return runtimeevents.KindAgentInteractionEnd
	default:
		return ""
	}
}

func (runtime *humanInteractionRuntime) SuspendToolCall(
	ctx context.Context,
	request ToolSuspensionRequest,
) (ToolSuspensionDisposition, error) {
	if runtime == nil || runtime.al == nil {
		return ToolSuspensionDisposition{}, interactions.ErrStoreUnavailable
	}
	registry := runtime.al.interactionRegistryForWorkspace(request.Workspace)
	if registry == nil {
		return ToolSuspensionDisposition{}, interactions.ErrStoreUnavailable
	}
	catalogLocked := false
	if runtime.al.interactionCatalog != nil {
		runtime.al.interactionCatalogMu.Lock()
		catalogLocked = true
		if err := runtime.al.interactionCatalog.Register(request.Workspace); err != nil {
			runtime.al.interactionCatalogMu.Unlock()
			return ToolSuspensionDisposition{}, fmt.Errorf(
				"register interaction workspace: %w",
				err,
			)
		}
	}
	executionContext := cloneInboundContext(request.ExecutionContext)
	approvalAction := ""
	if request.Prompt.Kind == interactions.KindApproval {
		approvalAction = request.ApprovalAction
	}
	record, err := registry.Create(interactions.CreateRequest{
		Kind:  request.Prompt.Kind,
		Route: request.Route,
		Origin: interactions.Origin{
			TurnID:                 request.Origin.TurnID,
			ExecutionID:            request.Origin.ExecutionID,
			ToolCallID:             request.Origin.ToolCallID,
			ToolName:               request.Origin.ToolName,
			TaskID:                 request.Origin.TaskID,
			ContinuationSessionKey: request.Origin.ContinuationSessionKey,
			ArgumentHash:           request.Origin.ArgumentHash,
			ExecutionContext:       executionContext,
			ObjectiveChecklist: append(
				[]interactions.ObjectiveChecklistItem(nil),
				request.Origin.ObjectiveChecklist...),
		},
		Questions:      request.Prompt.Questions,
		PromptSummary:  request.Prompt.PromptSummary,
		ApprovalAction: approvalAction,
		ExpiresAt:      time.Now().Add(request.Prompt.Timeout),
	})
	if catalogLocked {
		runtime.al.interactionCatalogMu.Unlock()
	}
	if err != nil {
		return ToolSuspensionDisposition{}, err
	}
	if request.Resolution != nil {
		runtime.al.interactionResolutions.Store(record.ID, request.Resolution)
	}
	disposition := ToolSuspensionDisposition{InteractionID: record.ID, Durable: true}
	_, _, err = runtime.al.deliverInteractionPrompt(ctx, registry, request.Workspace, record)
	if err != nil {
		return disposition, err
	}
	return disposition, nil
}

func (runtime *humanInteractionRuntime) ConsumeApproval(
	_ context.Context,
	request ToolApprovalConsumptionRequest,
) error {
	if runtime == nil || runtime.al == nil {
		return interactions.ErrStoreUnavailable
	}
	registry := runtime.al.interactionRegistryForWorkspace(request.Workspace)
	if registry == nil {
		return interactions.ErrStoreUnavailable
	}
	_, err := registry.ConsumeApproval(
		request.InteractionID,
		request.Revision,
		request.Origin.ToolCallID,
		request.Origin.ToolName,
		request.Origin.ArgumentHash,
	)
	return err
}

func (runtime *humanInteractionRuntime) publishPrompt(
	ctx context.Context,
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
) (outbox.Intent, error) {
	if runtime == nil || runtime.al == nil || runtime.al.bus == nil {
		return outbox.Intent{}, fmt.Errorf("message bus unavailable")
	}
	if !supportsDurableDeliveryReceipts(runtime.al.channelManager) {
		return outbox.Intent{}, fmt.Errorf("durable channel delivery receipts are unavailable")
	}
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	if runtime.al.registry != nil {
		if agent, ok := runtime.al.registry.GetAgent(record.Route.AgentID); ok && agent != nil {
			traceScope := runtimeevents.NewTraceScope(agent.Workspace, record.Origin.TurnID)
			if traceScope.Complete() {
				if err := bus.SetOutboundTraceScopes(&message, []runtimeevents.TraceScope{traceScope}); err != nil {
					return outbox.Intent{}, err
				}
			}
		}
	}
	ctx = withOutboundTransaction(ctx, interactionDeliveryKey(record.ID, "prompt"))
	receipt, err := runtime.al.publishTransactionMessageReceiptAtBoundary(
		ctx,
		workspace,
		message,
		func(context.Context) error {
			return runtime.al.validateInteractionPromptPublication(
				registry,
				workspace,
				record,
				time.Now().UTC(),
			)
		},
	)
	if err != nil {
		return outbox.Intent{}, err
	}
	if receipt.deliveryID != record.PromptDeliveryID {
		return outbox.Intent{}, fmt.Errorf(
			"interaction prompt delivery ID %q does not match bound outbox ID %q",
			receipt.deliveryID,
			record.PromptDeliveryID,
		)
	}
	intent, err := receipt.awaitTerminal(ctx)
	if err != nil {
		return outbox.Intent{}, err
	}
	if intent.Status == outbox.StatusDelivered {
		return intent, nil
	}
	detail := strings.TrimSpace(intent.LastError)
	if detail == "" {
		detail = "delivery did not reach the remote channel"
	}
	return intent, fmt.Errorf("interaction prompt delivery is %s: %s", intent.Status, detail)
}

func interactionPromptDeliveryIdentity(record interactions.Record) outbox.Identity {
	return outbox.Identity{
		SourceID:   interactionDeliveryKey(record.ID, "prompt"),
		Ordinal:    0,
		Kind:       outbox.KindMessage,
		Channel:    record.Route.Channel,
		ChatID:     record.Route.ChatID,
		SessionKey: record.Route.SessionKey,
	}
}

func bindInteractionPromptDelivery(
	registry *interactions.Registry,
	record interactions.Record,
) (interactions.Record, error) {
	deliveryID, err := outbox.DeliveryID(interactionPromptDeliveryIdentity(record))
	if err != nil {
		return record, err
	}
	if record.PromptDeliveryID != "" {
		if record.PromptDeliveryID != deliveryID {
			return record, fmt.Errorf(
				"interaction prompt is bound to outbox ID %q, want %q",
				record.PromptDeliveryID,
				deliveryID,
			)
		}
		return record, nil
	}
	return registry.BindPromptDelivery(record.ID, record.Revision, deliveryID)
}

func (al *AgentLoop) deliverInteractionPrompt(
	ctx context.Context,
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
) (interactions.Record, outbox.Intent, error) {
	record, err := bindInteractionPromptDelivery(registry, record)
	if err != nil {
		return record, outbox.Intent{}, fmt.Errorf("bind interaction prompt delivery: %w", err)
	}
	intent, deliveryErr := al.humanInteractionRuntime().publishPrompt(ctx, registry, workspace, record)
	if deliveryErr != nil {
		return record, intent, deliveryErr
	}
	updated, err := registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		return record, intent, fmt.Errorf("mark interaction waiting: %w", err)
	}
	return updated, intent, nil
}

func interactionPromptMessage(record interactions.Record) bus.OutboundMessage {
	outboundContext := bus.InboundContext{
		Channel:   record.Route.Channel,
		Account:   record.Route.AccountID,
		ChatID:    record.Route.ChatID,
		ChatType:  record.Route.ChatType,
		SenderID:  record.Route.SenderID,
		TopicID:   record.Route.TopicID,
		SpaceID:   record.Route.SpaceID,
		SpaceType: record.Route.SpaceType,
		Raw: map[string]string{
			metadataKeyMessageKind: interactionMessageKind,
			interactionIDMetadata:  record.ID,
			interactionShortIDMeta: record.ShortID,
			"delivery_key":         interactionDeliveryKey(record.ID, "prompt"),
		},
	}
	replyToMessageID := ""
	requestID := ""
	if record.Origin.ExecutionContext != nil {
		requestID = strings.TrimSpace(record.Origin.ExecutionContext.MessageID)
	}
	if requestID != "" {
		outboundContext.Raw[bus.OutboundMetadataKeyRequestID] = requestID
	}
	if strings.EqualFold(strings.TrimSpace(record.Route.Channel), "telegram") {
		replyToMessageID = requestID
	}
	switch record.Kind {
	case interactions.KindApproval:
		bus.OutboundMetadata{
			InteractionKind:     bus.OutboundInteractionApproval,
			InteractionControls: bus.OutboundInteractionControlsPrompt,
		}.ApplyToContext(&outboundContext)
	case interactions.KindQuestion:
		choices := []string(nil)
		if len(record.Questions) == 1 {
			choices = make([]string, 0, len(record.Questions[0].Options))
			for _, option := range record.Questions[0].Options {
				choices = append(choices, option.Label)
			}
		}
		metadata := bus.OutboundMetadata{
			InteractionKind:     bus.OutboundInteractionQuestion,
			InteractionControls: bus.OutboundInteractionControlsPrompt,
		}
		metadata = metadata.WithInteractionChoices(choices)
		metadata.ApplyToContext(&outboundContext)
	}
	return bus.OutboundMessage{
		Channel:          record.Route.Channel,
		ChatID:           record.Route.ChatID,
		Context:          outboundContext,
		AgentID:          record.Route.AgentID,
		SessionKey:       record.Route.SessionKey,
		ReplyToMessageID: replyToMessageID,
	}
}

func interactionDeliveryKey(interactionID, kind string) string {
	return "interaction:" + strings.TrimSpace(interactionID) + ":" + strings.TrimSpace(kind)
}

func (al *AgentLoop) withInteractionFinalTransaction(
	ctx context.Context,
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
) context.Context {
	sourceID := interactionDeliveryKey(record.ID, "final")
	return withBoundOutboundTransaction(
		ctx,
		sourceID,
		func(deliveryID string) error {
			return bindInteractionFinalDelivery(registry, record.ID, deliveryID)
		},
		func(deliveryID string) error {
			return al.validateInteractionFinalPublication(
				registry,
				workspace,
				record.ID,
				deliveryID,
			)
		},
	)
}

func bindInteractionFinalDelivery(
	registry *interactions.Registry,
	interactionID string,
	deliveryID string,
) error {
	if registry == nil {
		return interactions.ErrStoreUnavailable
	}
	var lastErr error
	for range 4 {
		record, found := registry.Get(interactionID)
		if !found {
			return interactions.ErrNotFound
		}
		if interactionHasFinalDelivery(record, deliveryID) {
			return nil
		}
		if record.Status != interactions.StatusResuming {
			return fmt.Errorf(
				"%w: interaction %q cannot bind final delivery from %s",
				interactions.ErrConflict,
				interactionID,
				record.Status,
			)
		}
		if _, err := registry.BindFinalDelivery(record.ID, record.Revision, deliveryID); err == nil {
			return nil
		} else if !errors.Is(err, interactions.ErrConflict) {
			return err
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("bind final delivery for interaction %q: %w", interactionID, lastErr)
}

func interactionHasFinalDelivery(record interactions.Record, deliveryID string) bool {
	deliveryID = strings.TrimSpace(deliveryID)
	for _, existing := range record.FinalDeliveryIDs {
		if existing == deliveryID {
			return true
		}
	}
	return false
}

func renderInteractionPrompt(record interactions.Record) string {
	var builder strings.Builder
	if record.Kind == interactions.KindApproval {
		builder.WriteString("`")
		builder.WriteString(strings.TrimSpace(record.Origin.ToolName))
		builder.WriteString("`\nAllow this action?")
		if objective := soleExternalActionObjective(record.Origin.ObjectiveChecklist); objective != "" {
			builder.WriteString("\n\nRequested outcome: ")
			builder.WriteString(objective)
		}
		builder.WriteString("\n\nExact action: ")
		builder.WriteString(strings.TrimSpace(record.ApprovalAction))
		fmt.Fprintf(
			&builder,
			"\n\n`/answer %s allow_once`\n`/answer %s deny`",
			record.ShortID,
			record.ShortID,
		)
		return builder.String()
	}
	if len(record.Questions) == 1 {
		renderSingleInteractionQuestion(&builder, record.Questions[0])
		fmt.Fprintf(&builder, "\n\n`/answer %s …`\n`/stop`", record.ShortID)
		return builder.String()
	}
	for index, question := range record.Questions {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "%d. `%s`", index+1, question.ID)
		if question.Header != "" {
			fmt.Fprintf(&builder, " %s", question.Header)
		}
		builder.WriteString("\n")
		builder.WriteString(question.Question)
		renderInteractionOptions(&builder, question.Options)
	}
	fmt.Fprintf(&builder, "\n\n`/answer %s`", record.ShortID)
	for _, question := range record.Questions {
		fmt.Fprintf(&builder, "\n`%s: …`", question.ID)
	}
	return builder.String()
}

func soleExternalActionObjective(checklist []interactions.ObjectiveChecklistItem) string {
	objective := ""
	externalActions := 0
	for _, item := range checklist {
		if strings.TrimSpace(item.Kind) != "external_action" {
			continue
		}
		externalActions++
		if externalActions > 1 {
			return ""
		}
		objective = strings.TrimSpace(item.Item)
	}
	return objective
}

func renderSingleInteractionQuestion(builder *strings.Builder, question interactions.Question) {
	if question.Header != "" {
		builder.WriteString(question.Header)
		builder.WriteString("\n\n")
	}
	builder.WriteString(question.Question)
	renderInteractionOptions(builder, question.Options)
}

func renderInteractionOptions(builder *strings.Builder, options []interactions.Option) {
	if len(options) > 0 {
		builder.WriteString("\n")
	}
	for _, option := range options {
		fmt.Fprintf(builder, "\n• %s", option.Label)
		if option.Description != "" {
			fmt.Fprintf(builder, " — %s", option.Description)
		}
	}
}
