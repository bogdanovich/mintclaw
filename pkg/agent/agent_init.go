// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/audio/tts"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/state"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	hardwaretools "github.com/bogdanovich/mintclaw/pkg/tools/hardware"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
)

func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)
	return newAgentLoopWithRegistry(context.Background(), cfg, msgBus, provider, registry, opts...)
}

func newAgentLoopWithRegistry(
	ctx context.Context,
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	registry *AgentRegistry,
	opts ...AgentLoopOption,
) *AgentLoop {
	// Set up shared fallback chain with rate limiting.
	cooldown := providers.NewCooldownTracker()
	rl := providers.NewRateLimiterRegistry()
	// Register rate limiters for all agents' candidates so that RPM limits
	// configured in ModelConfig are enforced before each LLM call.
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			rl.RegisterCandidates(agent.Candidates)
			rl.RegisterCandidates(agent.LightCandidates)
		}
	}
	fallbackChain := providers.NewFallbackChain(cooldown, rl)

	// Determine worker pool size from config (default: 1 = sequential)
	workerPoolSize := cfg.Agents.Defaults.MaxParallelTurns
	if workerPoolSize <= 0 {
		workerPoolSize = 1
	}

	al := &AgentLoop{
		bus:               msgBus,
		cfg:               cfg,
		registry:          registry,
		fallback:          fallbackChain,
		cmdRegistry:       commands.NewRegistry(commands.BuiltinDefinitions()),
		steering:          newSteeringQueue(parseSteeringMode(cfg.Agents.Defaults.SteeringMode)),
		workerSem:         make(chan struct{}, workerPoolSize),
		turns:             newTurnRuntime(registry, msgBus),
		ownsRuntimeEvents: true,
		interactions:      newInteractionCoordinator(config.GetHome()),
		startupResult:     make(chan error, 1),
	}
	al.compactionRunner = newBackgroundCompactionRunner(
		func() ContextManager {
			return al.contextManager
		},
	)
	for _, opt := range opts {
		if opt != nil {
			opt(al)
		}
	}
	al.interactions.configure(al.GetConfig, al.codingProfile, al.observeInteractionEvent)
	al.tasks = newTaskCoordinator(al.GetConfig, al.codingProfile, &al.interactions)
	if defaultAgent := registry.GetDefaultAgent(); defaultAgent != nil && al.state == nil {
		if !al.isolatedToolBootstrap {
			manager, err := state.NewManagerChecked(defaultAgent.Workspace)
			if err != nil {
				al.runtimeInitErr = fmt.Errorf("load runtime state: %w", err)
			} else {
				al.state = manager
			}
		}
	}
	if al.runtimeEvents == nil {
		al.runtimeEvents = runtimeevents.NewBus()
		al.ownsRuntimeEvents = true
	}
	al.traceCapture = newTraceCaptureManager(cfg, al.runtimeEvents)
	al.refreshRuntimeEventLogger(cfg)
	al.providerFactory = providers.CreateProviderFromConfig
	al.modelExecution = &modelExecutionManager{
		configProvider: al.GetConfig,
		state:          al.state,
		providerFactory: func() modelProviderFactory {
			return al.providerFactory
		},
	}
	al.hooks = NewHookManager(al.runtimeEvents.Channel())
	configureHookManagerFromConfig(al.hooks, cfg)
	al.contextManager, al.contextManagerInitErr = al.resolveContextManager(ctx)

	// Register shared tools to all agents (now that al is created)
	if !al.isolatedToolBootstrap {
		registerSharedTools(al, cfg, msgBus, registry, provider)
	}
	al.turns.replaceRunner(newTurnRunner(al, cfg))

	return al
}

// NewAgentLoopChecked constructs an AgentLoop and returns startup failures.
func NewAgentLoopChecked(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) (*AgentLoop, error) {
	registry, err := newAgentRegistry(cfg, provider)
	if err != nil {
		return nil, err
	}
	al := newAgentLoopWithRegistry(context.Background(), cfg, msgBus, provider, registry, opts...)
	if al.runtimeInitErr != nil {
		err := al.runtimeInitErr
		al.Close()
		return nil, err
	}
	if al.contextManagerInitErr != nil {
		al.Close()
		return nil, al.contextManagerInitErr
	}
	return al, nil
}

// NewCodingAgentLoop applies a resolved coding-thread profile while bounding
// construction and startup repair with ctx.
func NewCodingAgentLoop(
	ctx context.Context,
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	profile CodingRuntimeProfile,
	opts ...AgentLoopOption,
) (*AgentLoop, error) {
	if ctx == nil {
		return nil, fmt.Errorf("coding runtime construction context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	contextManagerName := contextManagerConfigName(cfg)
	if contextManagerName != "none" && contextManagerName != "seahorse" {
		return nil, fmt.Errorf(
			"coding profile context manager %q has no thread-scoped storage contract",
			contextManagerName,
		)
	}
	registry, err := newAgentRegistryWithCodingRuntimeProfile(cfg, provider, profile)
	if err != nil {
		return nil, err
	}
	opts = append([]AgentLoopOption{
		withCodingRuntimeProfile(profile),
		WithIsolatedToolBootstrap(),
	}, opts...)
	al := newAgentLoopWithRegistry(ctx, cfg, msgBus, provider, registry, opts...)
	if al.runtimeInitErr != nil {
		err := al.runtimeInitErr
		al.Close()
		return nil, err
	}
	if al.contextManagerInitErr != nil {
		err := al.contextManagerInitErr
		al.Close()
		return nil, err
	}
	if err := al.repairCodingToolLifecycles(ctx); err != nil {
		al.Close()
		return nil, fmt.Errorf("repair coding tool lifecycle: %w", err)
	}
	if err := al.prepareCodingContext(ctx); err != nil {
		al.Close()
		return nil, fmt.Errorf("prepare coding context: %w", err)
	}
	al.sealCodingTools()
	return al, nil
}

func (al *AgentLoop) sealCodingTools() {
	for _, agentID := range al.registry.ListAgentIDs() {
		if instance, ok := al.registry.GetAgent(agentID); ok && instance != nil {
			instance.Tools.Seal()
			instance.admitTrustedToolRegistry()
		}
	}
}

func registerSharedTools(
	al *AgentLoop,
	cfg *config.Config,
	msgBus interfaces.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) {
	allowReadPaths := buildAllowReadPatterns(cfg)
	var ttsProvider tts.TTSProvider
	if cfg.Tools.IsToolEnabled("send_tts") {
		ttsProvider = tts.DetectTTS(cfg)
		if ttsProvider == nil {
			logger.WarnCF("voice-tts", "send_tts enabled but no TTS provider configured", nil)
		}
	}
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		interactionRegistry := al.interactionRegistryForWorkspace(agent.Workspace)
		taskRegistry := al.taskRegistryForWorkspace(agent.Workspace)
		if cfg.Tools.IsToolEnabled("request_user_input") {
			requestTool, err := tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{
				DefaultTimeout: cfg.Tools.RequestUserInput.DefaultTimeout(),
				MaxTimeout:     cfg.Tools.RequestUserInput.MaxTimeout(),
			})
			if err != nil {
				logger.ErrorCF("agent", "Failed to initialize request_user_input tool", map[string]any{
					"error": err.Error(),
				})
			} else {
				registerToolIfAllowed(agent, requestTool)
			}
		}
		if cfg.Tools.IsToolEnabled("memory") {
			workspace := agent.Workspace
			memoryRoot := workspace
			if layout, ok := codingLayoutForAgent(al.codingProfile, agent.ID); ok {
				memoryRoot = layout.StateRoot()
			}
			registerToolIfAllowed(
				agent,
				tools.NewMemoryTool(
					memoryRoot,
					func() { registry.invalidateWorkspaceContextCaches(workspace) },
					al.runtimeEvents,
				),
			)
		}
		if al.state != nil {
			registerToolIfAllowed(agent, tools.NewGetGoalTool(al.state))
			registerToolIfAllowed(agent, tools.NewCreateGoalTool(al.state))
			registerToolIfAllowed(agent, tools.NewUpdateGoalTool(al.state))
		}

		if cfg.Tools.IsToolEnabled("web") {
			searchTool, err := integrationtools.NewWebSearchTool(integrationtools.WebSearchToolOptionsFromConfig(cfg))
			if err != nil {
				logger.ErrorCF(
					"agent",
					"Failed to create web search tool",
					map[string]any{"error": err.Error()},
				)
			} else if searchTool != nil {
				registerToolIfAllowed(agent, searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := integrationtools.NewWebFetchToolWithProxy(
				50000,
				cfg.Tools.Web.Proxy,
				cfg.Tools.Web.Format,
				cfg.Tools.Web.FetchLimitBytes,
				cfg.Tools.Web.PrivateHostWhitelist)
			if err != nil {
				logger.ErrorCF(
					"agent",
					"Failed to create web fetch tool",
					map[string]any{"error": err.Error()},
				)
			} else {
				registerToolIfAllowed(agent, fetchTool)
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			registerToolIfAllowed(agent, hardwaretools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			registerToolIfAllowed(agent, hardwaretools.NewSPITool())
		}
		if cfg.Tools.IsToolEnabled("serial") {
			registerToolIfAllowed(agent, hardwaretools.NewSerialTool())
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := integrationtools.NewMessageTool()
			if cfg.Tools.Message.MediaEnabled {
				messageTool.ConfigureLocalMedia(
					agent.Workspace,
					cfg.Agents.Defaults.RestrictToWorkspace,
					cfg.Agents.Defaults.GetMaxMediaSize(),
					allowReadPaths,
				)
			}
			registerToolIfAllowed(agent, messageTool)
		}
		if cfg.Tools.IsToolEnabled("reaction") {
			reactionTool := integrationtools.NewReactionTool()
			reactionTool.SetReactionCallback(
				func(ctx context.Context, channel, chatID, messageID string) error {
					if al.channelManager == nil {
						return fmt.Errorf("channel manager not configured")
					}
					ch, ok := al.channelManager.GetChannel(channel)
					if !ok {
						return fmt.Errorf("channel %s not found", channel)
					}
					rc, ok := ch.(channels.ReactionCapable)
					if !ok {
						return fmt.Errorf("channel %s does not support reactions", channel)
					}
					_, err := rc.ReactToMessage(ctx, chatID, messageID)
					return err
				},
			)
			registerToolIfAllowed(agent, reactionTool)
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := fstools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			registerToolIfAllowed(agent, sendFileTool)
		}

		if ttsProvider != nil {
			registerToolIfAllowed(agent, integrationtools.NewSendTTSTool(ttsProvider, nil))
		}

		if cfg.Tools.IsToolEnabled("load_image") {
			loadImageTool := fstools.NewLoadImageTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			registerToolIfAllowed(agent, loadImageTool)
		}

		if cfg.Tools.IsToolEnabled("image_generate") {
			imageGenerateTool := tools.NewImageGenerateTool(
				agent.Workspace,
				cfg.Tools.ImageGenerate.EffectiveModel(),
				nil,
				tools.WithImageGenerationOutputDir(cfg.Tools.ImageGenerate.OutputDir),
			)
			registerToolIfAllowed(agent, imageGenerateTool)
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				registerToolIfAllowed(agent, integrationtools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				registerToolIfAllowed(
					agent,
					integrationtools.NewInstallSkillTool(registryMgr, agent.Workspace),
				)
			}
		}

		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		subagentEnabled := cfg.Tools.IsToolEnabled("subagent")
		if spawnEnabled && subagentEnabled {
			subagentManager := tools.NewSubagentManagerWithRegistry(
				agent.Model,
				taskRegistry,
			)
			subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)
			subagentManager.SetSpawner(NewSubTurnSpawner(al))
			spawnTool := tools.NewSpawnTool(subagentManager)
			currentAgentID := agentID
			spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
				return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
			})
			spawnTool.SetObjectiveChecklistRequirement(func(targetAgentID string) bool {
				if targetAgentID == "" {
					targetAgentID = currentAgentID
				}
				target, ok := registry.GetAgent(targetAgentID)
				return ok && target.Tools != nil && target.Tools.HasRegistered("browser_act")
			})

			registerToolIfAllowed(agent, spawnTool)
			registerToolIfAllowed(agent, tools.NewSubagentTool(subagentManager))
		} else if spawnEnabled {
			logger.WarnCF("agent", "spawn tool requires subagent to be enabled", nil)
		}

		if cfg.Tools.IsToolEnabled("task_status") {
			registerToolIfAllowed(agent, tools.NewTaskStatusTool(taskRegistry, interactionRegistry))
		}

		// Register delegate tool for multi-agent setups.
		// Auto-enabled when multiple agents exist. Delegation uses the SubTurn
		// mechanism directly (not SubagentManager) and is independent of the
		// subagent tool.
		if len(registry.ListAgentIDs()) > 1 {
			delegateTool := tools.NewDelegateTool()
			delegateTool.SetSpawner(NewSubTurnSpawner(al))
			delegateTool.SetTaskRegistry(taskRegistry)
			currentAgentID := agentID
			delegateTool.SetSelfAgentID(currentAgentID)
			delegateTool.SetAllowlistChecker(func(targetAgentID string) bool {
				return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
			})
			delegateTool.SetObjectiveChecklistRequirement(func(targetAgentID string) bool {
				target, ok := registry.GetAgent(targetAgentID)
				return ok && target.Tools != nil && target.Tools.HasRegistered("browser_act")
			})
			registerToolIfAllowed(agent, delegateTool)
		}
		warnOnUnknownAgentToolDeclarations(agentID, agent.Workspace, agent.ToolPolicy, agent.Tools)
	}
}

func codingLayoutForAgent(profile *CodingRuntimeProfile, agentID string) (CodingRuntimeLayout, bool) {
	if profile == nil {
		return CodingRuntimeLayout{}, false
	}
	return profile.AgentLayout(agentID)
}
