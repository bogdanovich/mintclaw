package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// turnRunner owns the immutable service snapshot used for one runtime
// generation. AgentLoop replaces the runner when runtime wiring changes;
// already admitted turns keep the generation they started with.
type turnRunner struct {
	host     *AgentLoop
	pipeline *Pipeline
}

func newTurnRunner(al *AgentLoop, cfg *config.Config) *turnRunner {
	return &turnRunner{host: al, pipeline: newPipeline(al, cfg)}
}

func (al *AgentLoop) currentTurnRunner() *turnRunner {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.turnRunner
}

func (al *AgentLoop) replaceTurnRunnerLocked(cfg *config.Config) {
	al.turnRunner = newTurnRunner(al, cfg)
}

func newPipeline(al *AgentLoop, cfg *config.Config) *Pipeline {
	events := &agentRuntimeEventEmitter{events: al.runtimeEvents}
	reasoning := &reasoningPublisherComponent{
		bus:            al.bus,
		cfg:            cfg,
		channelManager: al.channelManager,
	}
	toolFeedback := &toolFeedbackPublisher{
		bus:                 al.bus,
		cfg:                 cfg,
		channelManager:      al.channelManager,
		getFeedbackOverride: al.getToolFeedbackOverride,
	}

	return &Pipeline{
		Cfg: cfg,
		Runtime: PipelineRuntimeServices{
			Bus:            al.bus,
			Events:         events,
			ActiveRequests: al.activeRequests,
			TurnControl:    &turnAbortController{events: events},
		},
		retrySleeper:         contextRetrySleeper{},
		trustAllTools:        al.usesCodingProfile(),
		durableToolLifecycle: al.usesCodingProfile(),
		hashArguments: func(workspace string, arguments map[string]any) (string, error) {
			if layout, ok := al.codingLayoutForWorkspace(workspace); ok {
				return interactions.HashArgumentsAtPath(layout.StatePaths().InteractionKeyFile, arguments)
			}
			return interactions.HashArguments(workspace, arguments)
		},
		Context: PipelineContextServices{
			Runtime:              al.contextManager,
			BackgroundCompaction: al.compactionRunner,
			ModelExecution:       al.modelExecution,
			Steering:             al.steering,
			MediaResolver:        al.mediaStore,
			TerminalTasks:        al,
		},
		Interaction: PipelineInteractionServices{
			Reasoning:        reasoning,
			ToolFeedback:     toolFeedback,
			SyncToolDelivery: &syncToolResultDelivery{deliverToUser: al.deliverToolResultToUser},
			ToolDelivery:     al.asyncToolCompletionDelivery(),
			Hooks:            al.hooks,
			Fallback:         al.fallback,
			Suspension:       &humanInteractionRuntime{al: al},
		},
	}
}
