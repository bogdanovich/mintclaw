package agent

import "github.com/bogdanovich/mintclaw/pkg/interactions"

// NewPipeline creates a Pipeline from an AgentLoop instance.
func NewPipeline(al *AgentLoop) *Pipeline {
	cfg := al.GetConfig()
	return NewPipelineFromDependencies(PipelineDependencies{
		Cfg: cfg,
		Runtime: PipelineRuntimeServices{
			Bus:            al.bus,
			Events:         al.runtimeEventEmitter(),
			ActiveRequests: al.activeRequestCounter(),
			TurnControl:    al.turnAbortController(),
		},
		Config: PipelineConfigServices{
			ChannelStreaming:      newConfigChannelStreamingProvider(cfg),
			NativeSearch:          newConfigNativeSearchPolicy(cfg),
			LLMRetry:              newConfigLLMRetryPolicy(cfg),
			RetrySleeper:          contextRetrySleeper{},
			MediaLimits:           newConfigMediaLimitsProvider(cfg),
			FinalTurnRender:       newConfigFinalTurnRenderPolicy(cfg),
			ModelResolution:       newConfigPipelineModelResolution(cfg),
			PromptBuilder:         newConfigPipelinePromptBuilder(cfg),
			ToolContentFilter:     newConfigToolContentFilter(cfg),
			TrustAllToolExecution: al.hasCodingToolProfile(),
			DurableToolLifecycle:  al.hasCodingToolProfile(),
			HashToolArguments: func(workspace string, arguments map[string]any) (string, error) {
				if layout, ok := al.runtimeLayoutForWorkspace(workspace); ok {
					return interactions.HashArgumentsAtPath(layout.StatePaths().InteractionKeyFile, arguments)
				}
				return interactions.HashArguments(workspace, arguments)
			},
		},
		Context: PipelineContextServices{
			Runtime:              al.contextManager,
			BackgroundCompaction: al.backgroundCompactionRunner(),
			ModelExecution:       al.modelExecutionManager(),
			Steering:             al.steering,
			MediaResolver:        al.mediaStore,
			TerminalTasks:        al,
		},
		Interaction: PipelineInteractionServices{
			Reasoning:        al.reasoningPublisher(),
			ToolFeedback:     al.toolFeedbackPublisher(),
			SyncToolDelivery: al.syncToolResultDelivery(),
			ToolDelivery:     al.asyncToolCompletionDelivery(),
			Hooks:            al.hooks,
			Fallback:         al.fallback,
			Suspension:       al.humanInteractionRuntime(),
		},
	})
}
