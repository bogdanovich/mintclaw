// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func (r *turnRunner) run(
	ctx context.Context,
	ts *turnState,
	execute pipelineTurnExecutionFunc,
) (result turnResult, err error) {
	runtime := r.runtime
	pipeline := r.pipeline
	ctx, releaseAdmission, err := runtime.acquireAgentTurn(ctx, ts.agentID)
	if err != nil {
		return turnResult{}, err
	}
	defer releaseAdmission()

	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	ts.setTurnCancel(turnCancel)
	ts.ctx = turnCtx

	// Tools receive the current turn through context; child execution is wired
	// explicitly through their registered runner.
	turnCtx = withTurnState(turnCtx, ts)

	runtime.registerActiveTurn(ts)
	defer runtime.clearActiveTurn(ts)
	defer ts.Finish(false)

	if runtime.takePendingStop(ts.runtimeSessionScope()) {
		_ = ts.requestHardAbort()
	}

	turnStatus := TurnEndStatusCompleted
	defer func() {
		attemptedSkills := ts.attemptedSkillsSnapshot()
		skillContextSnapshots := ts.skillContextSnapshotsSnapshot()
		llmCalls, promptTokens, completionTokens, totalTokens := ts.llmUsageTotals()
		contextUsedTokens := 0
		contextLimitTokens := 0
		if turnStatus == TurnEndStatusCompleted {
			if usage := computeContextUsage(ts.agent, ts.sessionKey); usage != nil {
				contextUsedTokens = usage.UsedTokens
				contextLimitTokens = usage.TotalTokens
			}
		}
		finalSuccessfulPath := []string(nil)
		if turnStatus == TurnEndStatusCompleted {
			if latest := ts.latestSkillContextSnapshot(); len(latest) > 0 {
				finalSuccessfulPath = latest
			} else {
				finalSuccessfulPath = append([]string(nil), attemptedSkills...)
			}
		}
		if r.traceCapture != nil && r.traceCapture.enabled() {
			pipeline.emitEvent(
				runtimeevents.KindAgentContextSnapshot,
				ts.eventMeta("runTurn", "turn.context.snapshot"),
				buildContextSnapshotPayload(pipeline.Cfg, ts),
			)
		}
		pipeline.emitEvent(
			runtimeevents.KindAgentTurnEnd,
			ts.eventMeta("runTurn", "turn.end"),
			TurnEndPayload{
				Status:    turnStatus,
				Workspace: ts.workspace,
				DeliveryExpected: turnStatus != TurnEndStatusSuspended &&
					(ts.opts.SendResponse || ts.opts.ExpectFinalDelivery),
				Iterations:            ts.currentIteration(),
				Duration:              time.Since(ts.startedAt),
				LLMCalls:              llmCalls,
				PromptTokens:          promptTokens,
				CompletionTokens:      completionTokens,
				TotalTokens:           totalTokens,
				ContextUsedTokens:     contextUsedTokens,
				ContextLimitTokens:    contextLimitTokens,
				FinalContentLen:       ts.finalContentLen(),
				FinalContentProtected: ts.finalContentProtectedSnapshot(),
				UserMessage:           ts.userMessage,
				FinalContent:          ts.finalContentSnapshot(),
				ActiveSkills:          append([]string(nil), ts.activeSkills...),
				AttemptedSkills:       attemptedSkills,
				FinalSuccessfulPath:   finalSuccessfulPath,
				SkillContextSnapshots: skillContextSnapshots,
				ToolKinds:             ts.toolKindsSnapshot(),
				ToolExecutions:        ts.toolExecutionsSnapshot(),
				InteractionID:         result.suspendedInteractionID,
			},
		)
	}()
	defer func() {
		if turnStatus == TurnEndStatusSuspended || ts.agent == nil || ts.agent.Tools == nil {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(turnCtx), time.Minute)
		defer cancelCleanup()
		cleanupCtx = toolExecutionContextForTurn(cleanupCtx, ts)
		if cleanupErr := ts.agent.Tools.CleanupTurn(cleanupCtx); cleanupErr != nil {
			logger.WarnCF("agent", "Terminal turn resource cleanup failed", map[string]any{
				"agent_id": ts.agentID,
				"turn_id":  ts.turnID,
			})
		}
	}()
	defer func() {
		acceptedSteering := ts.acceptedSteeringSnapshot()
		if len(acceptedSteering) == 0 {
			return
		}
		if turnStatus == TurnEndStatusCompleted && err == nil {
			if ts.opts.ExpectFinalDelivery &&
				strings.TrimSpace(result.finalContent) != "" &&
				ts.opts.FinalDeliveryObservation != nil {
				ts.opts.FinalDeliveryObservation.observeSteering(acceptedSteering)
				return
			}
			runtime.inbound.ackAcceptedSteeringMessages(ctx, acceptedSteering)
			return
		}
		runtime.inbound.releaseSteeringMessages(context.Background(), acceptedSteering, err)
	}()

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return pipeline.abortTurn(ts)
	}

	pipeline.emitEvent(
		runtimeevents.KindAgentTurnStart,
		ts.eventMeta("runTurn", "turn.start"),
		TurnStartPayload{
			UserMessage: ts.userMessage,
			MediaCount:  len(ts.media),
			Workspace:   ts.workspace,
		},
	)
	if ts.opts.OnTurnReady != nil {
		ts.opts.OnTurnReady()
	}

	if execute == nil {
		result, turnStatus, err = pipeline.runTurnLoop(ctx, turnCtx, ts)
	} else {
		result, turnStatus, err = execute(ctx, turnCtx, ts, pipeline)
	}
	return result, err
}

func (al *AgentLoop) resolveContextManager(ctx context.Context) (ContextManager, error) {
	name := contextManagerConfigName(al.cfg)
	if name == "none" {
		return &noneContextManager{}, nil
	}
	factory, ok := lookupContextManager(name)
	if !ok {
		err := fmt.Errorf("unknown context manager %q", name)
		return &failedContextManager{err: err}, err
	}
	cm, err := factory(ctx, al.cfg.Agents.Defaults.ContextManagerConfig, al)
	if err != nil {
		wrapped := fmt.Errorf("create context manager %q: %w", name, err)
		return &failedContextManager{err: wrapped}, wrapped
	}
	return cm, nil
}

func contextManagerConfigName(cfg *config.Config) string {
	if cfg == nil {
		return "seahorse"
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Agents.Defaults.ContextManager))
	if name == "" {
		name = "seahorse"
	}
	return name
}

func (al *AgentLoop) askSideQuestion(
	ctx context.Context,
	agent *AgentInstance,
	opts *turnSpec,
	question string,
) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("askSideQuestion: no agent available for /btw")
	}

	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("askSideQuestion: %w", fmt.Errorf("usage: /btw <question>"))
	}

	if opts != nil {
		normalizeTurnSpecInPlace(opts)
		resolved, err := resolveTurnProfileOptions(al.GetConfig(), *opts)
		if err != nil {
			return "", err
		}
		*opts = resolved
	}

	var media []string
	var channel, chatID, senderID, senderDisplayName string
	if opts != nil {
		media = opts.Dispatch.Media
		channel = opts.Dispatch.Channel()
		chatID = opts.Dispatch.ChatID()
		senderID = opts.Dispatch.SenderID()
		senderDisplayName = opts.SenderDisplayName
	}

	// Build messages with context but WITHOUT adding to session history
	var history []providers.Message
	var summary string
	if opts != nil && !opts.NoHistory {
		sideQuestionOpts := *opts
		sideQuestionOpts.Dispatch.UserMessage = question
		reserveTokens := estimateNonHistoryPromptReserveForTurnSpec(
			al.GetConfig(),
			agent,
			sideQuestionOpts,
			"",
		)
		resp, err := al.contextManager.Assemble(ctx, &AssembleRequest{
			Agent:         agent,
			SessionKey:    opts.Dispatch.SessionKey,
			Budget:        agent.ContextWindow,
			MaxTokens:     agent.MaxTokens,
			ReserveTokens: reserveTokens,
		})
		if err != nil {
			return "", fmt.Errorf("assemble side-question context: %w", err)
		}
		if resp != nil {
			history = resp.History
			summary = resp.Summary
		}
	}

	var promptReq PromptBuildRequest
	if opts == nil {
		promptReq = PromptBuildRequest{
			History:           history,
			Summary:           summary,
			CurrentMessage:    question,
			Media:             append([]string(nil), media...),
			Channel:           channel,
			ChatID:            chatID,
			SenderID:          senderID,
			SenderDisplayName: senderDisplayName,
		}
	} else {
		promptReq = promptBuildRequestForTurnSpec(
			al.GetConfig(),
			agent,
			*opts,
			history,
			summary,
			question,
			media,
		)
	}
	promptReq.SuppressToolUseRule = true
	promptReq.ToolUseFallback = false
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(promptReq)

	maxMediaSize := al.GetConfig().Agents.Defaults.GetMaxMediaSize()
	currentTurnStart := promptCurrentTurnStart(messages, question, media)
	messages = resolveMediaRefs(messages, al.mediaStore, al.codingMedia, maxMediaSize, currentTurnStart)

	execution := effectiveExecutionStateForAgent(agent)
	routeSessionKey := ""
	if opts != nil {
		execution = opts.ModelBinding.ExecutionState()
		routeSessionKey = opts.ModelBinding.RouteSessionKey
	}
	selection := al.selectCandidates(
		execution,
		question,
		messages,
		routeSessionKey,
	)
	activeCandidates, activeModel, usedLight := selection.activeCandidates, selection.model, selection.usedLight
	selectedModelName := resolvedCandidateModelName(activeCandidates, activeModel)
	if selectedModelName == "" {
		selectedModelName = sideQuestionModelName(agent, usedLight)
	}
	visionExecution, visionCleanup, _, usedVisionOverride, err := al.maybeBuildVisionExecutionState(
		agent,
		effectiveExecutionState{
			AgentID:            agent.ID,
			Model:              activeModel,
			Candidates:         append([]providers.FallbackCandidate(nil), activeCandidates...),
			CandidateProviders: cloneCandidateProviderMap(execution.CandidateProviders),
		},
		messages,
	)
	if err != nil {
		return "", err
	}
	defer func() {
		if visionCleanup != nil {
			visionCleanup()
		}
	}()
	if usedVisionOverride {
		activeCandidates = visionExecution.Candidates
		selectedModelName = resolvedCandidateModelName(
			visionExecution.Candidates,
			selectedModelName,
		)
	}

	llmOpts := map[string]any{
		"max_tokens":       agent.MaxTokens,
		"temperature":      agent.Temperature,
		"prompt_cache_key": agent.ID + ":btw",
	}

	hookModelChanged := false
	sideSuppressReasoning := false
	callProvider := func(
		ctx context.Context,
		candidate providers.FallbackCandidate,
		model string,
		forceModel bool,
		callMessages []providers.Message,
	) (*providers.LLMResponse, error) {
		baseModelName := selectedModelName
		if forceModel && strings.TrimSpace(model) != "" {
			baseModelName = model
		}
		provider, providerModel, modelCfg, cleanup, err := al.isolatedSideQuestionProvider(
			agent,
			baseModelName,
			candidate,
		)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		model = providerModel
		callOpts := llmOpts
		settings := thinkingSettingsFromModelConfig(modelCfg)
		sideSuppressReasoning = shouldSuppressReasoningFor(settings)
		if _, exists := callOpts["thinking_level"]; !exists {
			if settings.configured {
				callOpts = shallowCloneLLMOptions(llmOpts)
				applyThinkingOption(callOpts, provider, settings, false, agent.ID)
			}
		}
		return provider.Chat(ctx, callMessages, nil, model, callOpts)
	}

	turnCtx := newTurnContext(nil, nil, nil)
	if opts != nil {
		turnCtx = newTurnContext(
			opts.Dispatch.InboundContext,
			opts.Dispatch.RouteResult,
			opts.Dispatch.SessionScope,
		)
	}
	if al.hooks != nil {
		llmReq, decision := al.hooks.BeforeLLM(ctx, &LLMHookRequest{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.request",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:          cloneTurnContext(turnCtx),
			Model:            selectedModelName,
			Messages:         messages,
			Tools:            nil,
			Options:          llmOpts,
			GracefulTerminal: false,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				if llmReq.Model != selectedModelName {
					if err := requireExactModelName(llmReq.Model); err != nil {
						return "", fmt.Errorf("before_llm model: %w", err)
					}
					hookModelChanged = true
					selectedModelName = llmReq.Model
				}
				messages = llmReq.Messages
				llmOpts = llmReq.Options
				delete(llmOpts, "native_search")
			}
		case HookActionAbortTurn:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		case HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		}
	}
	if hookModelChanged {
		// Hook-selected models must not continue through the pre-hook fallback
		// candidate list, otherwise fallback execution would call the original
		// candidate model and silently ignore the hook decision.
		activeCandidates = nil
	}

	callSideLLM := func(callMessages []providers.Message) (*providers.LLMResponse, error) {
		if len(activeCandidates) > 1 && al.fallback != nil {
			fbResult, err := al.fallback.ExecuteCandidate(
				ctx,
				activeCandidates,
				func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
					return callProvider(ctx, candidate, candidate.Model, false, callMessages)
				},
			)
			if err != nil {
				return nil, err
			}
			return fbResult.Response, nil
		}

		var candidate providers.FallbackCandidate
		if len(activeCandidates) > 0 {
			candidate = activeCandidates[0]
		}
		return callProvider(ctx, candidate, selectedModelName, hookModelChanged, callMessages)
	}

	// Retry without media if vision is unsupported
	// Note: Vision retry is only applied to the initial call. If fallback chain
	// is used, vision errors from fallback providers will not trigger retry.
	var resp *providers.LLMResponse
	var callErr error
	resp, callErr = callSideLLM(messages)
	if callErr != nil && hasMediaRefs(messages) && isVisionUnsupportedError(callErr) {
		al.emitEvent(
			runtimeevents.KindAgentLLMRetry,
			HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.retry",
				turnContext: cloneTurnContext(turnCtx),
			},
			LLMRetryPayload{
				Attempt:    1,
				MaxRetries: 1,
				Reason:     "vision_unsupported",
				Error:      callErr.Error(),
				Backoff:    0,
			},
		)
		messagesWithoutMedia := stripMessageMedia(messages)
		resp, callErr = callSideLLM(messagesWithoutMedia)
	}
	if callErr != nil {
		return "", callErr
	}
	if resp == nil {
		return "", nil
	}

	// Apply after_llm hooks
	if al.hooks != nil {
		llmResp, decision := al.hooks.AfterLLM(ctx, &LLMHookResponse{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.response",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:  cloneTurnContext(turnCtx),
			Model:    selectedModelName,
			Response: resp,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				resp = llmResp.Response
			}
		case HookActionAbortTurn, HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during after_llm: %s", reason)
		}
	}
	if sideSuppressReasoning {
		resp.Reasoning = ""
		resp.ReasoningContent = ""
		resp.ReasoningDetails = nil
	}

	return sideQuestionResponseContent(resp), nil
}

func (al *AgentLoop) isolatedSideQuestionProvider(
	agent *AgentInstance,
	baseModelName string,
	candidate providers.FallbackCandidate,
) (providers.LLMProvider, string, *config.ModelConfig, func(), error) {
	if agent == nil {
		return nil, "", nil, func() {}, fmt.Errorf(
			"isolatedSideQuestionProvider: no agent available for /btw",
		)
	}

	modelCfg, err := al.sideQuestionModelConfig(agent, baseModelName, candidate)
	if err != nil {
		return nil, "", nil, func() {}, fmt.Errorf("isolatedSideQuestionProvider: %w", err)
	}

	factory := al.providerFactory
	if factory == nil {
		factory = providers.CreateProviderFromConfig
	}
	provider, modelID, err := factory(modelCfg)
	if err != nil {
		return nil, "", nil, func() {}, fmt.Errorf("isolatedSideQuestionProvider: %w", err)
	}

	cleanup := func() {
		closeProviderIfStateful(provider)
	}
	return provider, modelID, modelCfg, cleanup, nil
}

func (al *AgentLoop) sideQuestionModelConfig(
	agent *AgentInstance,
	baseModelName string,
	candidate providers.FallbackCandidate,
) (*config.ModelConfig, error) {
	if agent == nil {
		return nil, fmt.Errorf("sideQuestionModelConfig: no agent available for /btw")
	}
	if err := requireExactModelName(baseModelName); err != nil {
		return nil, fmt.Errorf("sideQuestionModelConfig: %w", err)
	}
	modelName := baseModelName
	var candidates []providers.FallbackCandidate
	if name := modelAliasFromCandidateIdentityKey(candidate.IdentityKey); name != "" {
		modelName = name
		candidates = []providers.FallbackCandidate{candidate}
	}
	modelCfg := resolveActiveModelConfig(
		al.GetConfig(),
		agent.Workspace,
		candidates,
		modelName,
	)
	if modelCfg == nil {
		return nil, fmt.Errorf("sideQuestionModelConfig: model %q is not configured", modelName)
	}
	return modelCfg, nil
}
