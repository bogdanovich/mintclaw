package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// turnRunner owns the immutable service snapshot used for one runtime
// generation. AgentLoop replaces the runner when runtime wiring changes;
// already admitted turns keep the generation they started with.
type turnRunner struct {
	runtime      *turnRuntime
	pipeline     *Pipeline
	traceCapture *traceCaptureManager
	toolFeedback *toolFeedbackPublisher
	interaction  *humanInteractionRuntime
}

func newTurnRunner(al *AgentLoop, cfg *config.Config) *turnRunner {
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
	syncDelivery := &syncToolResultDelivery{deliverToUser: al.deliverToolResultToUserWithScopes}
	asyncDelivery := newAsyncToolCompletionDelivery(al, events, syncDelivery)
	interaction := &humanInteractionRuntime{al: al, coordinator: &al.interactions}

	runner := &turnRunner{
		runtime:      al.turns,
		traceCapture: al.traceCapture,
		toolFeedback: toolFeedback,
		interaction:  interaction,
	}
	runner.pipeline = &Pipeline{
		Cfg:                  cfg,
		turnPolicy:           newPipelineTurnPolicy(cfg),
		bus:                  al.bus,
		events:               events,
		activeRequests:       al.turns.activeRequests,
		turnControl:          &turnAbortController{events: events},
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
			CodingMedia:          al.codingMedia,
			TerminalTasks:        &al.tasks,
		},
		Interaction: PipelineInteractionServices{
			Reasoning:        reasoning,
			ToolFeedback:     toolFeedback,
			SyncToolDelivery: syncDelivery,
			ToolDelivery:     asyncDelivery,
			Hooks:            al.hooks,
			Fallback:         al.fallback,
			Suspension:       interaction,
		},
	}
	return runner
}
