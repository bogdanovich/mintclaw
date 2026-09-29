// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/audio/tts"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/state"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	hardwaretools "github.com/bogdanovich/mintclaw/pkg/tools/hardware"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
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
		documentBudget: document.NewExecutionBudget(
			cfg.Tools.Document.MaxConcurrentOperations,
			time.Duration(cfg.Tools.Document.QueueTimeoutSeconds)*time.Second,
		),
		startupResult: make(chan error, 1),
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
	if al.codingProfile != nil {
		if err := registerCodingMediaTools(al, registry, al.codingMedia); err != nil {
			al.runtimeInitErr = errors.Join(al.runtimeInitErr, err)
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
		if err := registerSharedTools(al, cfg, msgBus, registry, provider); err != nil {
			al.runtimeInitErr = errors.Join(al.runtimeInitErr, err)
		}
	}
	al.turns.replaceRunner(newTurnRunner(al, cfg))
	al.bindSkillCompatibilityEnvironments(registry, cfg)

	return al
}

type codingDocumentMediaStore interface {
	media.CodingMediaStore
	document.OwnedMediaResolver
	BindOwner(string, media.MediaOwner) error
	CodingDocumentAuthorityAvailable() bool
}

func registerCodingMediaTools(
	al *AgentLoop,
	registry *AgentRegistry,
	store media.CodingMediaStore,
) error {
	if al == nil || registry == nil || store == nil {
		return nil
	}
	for _, agentID := range registry.ListAgentIDs() {
		instance, ok := registry.GetAgent(agentID)
		if !ok || instance == nil || instance.Tools == nil {
			continue
		}
		contributor := newRuntimeToolSetContributor(
			"coding.media",
			runtimeToolCandidate{tool: tools.NewCodingAttachmentTool(store)},
		).withCapabilityReport(runtimecap.Unavailable(
			runtimecap.CapabilityDocumentForm,
			runtimecap.ReasonRuntimeUnsupported,
		))
		documentStore, documentStoreOK := store.(codingDocumentMediaStore)
		documentReason := runtimecap.ReasonRuntimeUnsupported
		documentEnabled := al.cfg != nil && al.cfg.Tools.IsToolEnabled("document")
		switch {
		case !documentEnabled:
			documentReason = runtimecap.ReasonPolicyDisabled
		case !documentToolAvailable():
			documentReason = runtimecap.ReasonServiceUnavailable
		case !documentStoreOK:
			documentReason = runtimecap.ReasonRuntimeUnsupported
		case !documentStore.CodingDocumentAuthorityAvailable():
			documentReason = runtimecap.ReasonIdentityIncomplete
		default:
			documentTool := tools.NewDocumentTool(
				tools.WithDocumentReadOnlySurface(),
				tools.WithDocumentExecutionBudget(al.documentBudget),
				tools.WithDocumentLocalPathPolicy(instance.Workspace, true, nil),
			)
			documentTool.SetMediaStore(documentStore)
			contributor.candidates = append(
				contributor.candidates,
				runtimeToolCandidate{tool: documentTool},
			)
			for _, capability := range []runtimecap.CapabilityID{
				runtimecap.CapabilityDocumentInspect,
				runtimecap.CapabilityDocumentExtract,
				runtimecap.CapabilityDocumentRender,
			} {
				contributor = contributor.withCapability(capability, documentTool.Name())
			}
		}
		if len(contributor.capabilities) == 0 {
			for _, capability := range []runtimecap.CapabilityID{
				runtimecap.CapabilityDocumentInspect,
				runtimecap.CapabilityDocumentExtract,
				runtimecap.CapabilityDocumentRender,
			} {
				contributor = contributor.withCapabilityReport(runtimecap.Unavailable(capability, documentReason))
			}
		}
		if instance.toolComposer == nil {
			return fmt.Errorf("compose coding media tools for agent %s: runtime composer is unavailable", agentID)
		}
		if err := instance.toolComposer.PutContributor(contributor); err != nil {
			return fmt.Errorf("compose coding media tools for agent %s: %w", agentID, err)
		}
	}
	return nil
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
) error {
	allowReadPaths := buildAllowReadPatterns(cfg)
	documentAllowReadPaths := buildDocumentAllowReadPatterns(cfg)
	availableModels := availableChildModelNames(cfg)
	var ttsProvider tts.TTSProvider
	var documentFormJobs *document.FormJobStore
	if cfg.Tools.IsToolEnabled("document") && documentToolAvailable() {
		if registered, ok := al.interactions.protectedAnswerSink(document.FormProtectedAnswerNamespace); ok {
			if sink, typed := registered.(*document.FormProtectedAnswerSink); typed {
				documentFormJobs = sink.FormJobStore()
			} else {
				logger.ErrorCF("agent", "Protected document answer sink has an unexpected implementation", nil)
			}
		} else {
			var err error
			documentFormJobs, err = openDocumentFormJobStore(config.GetHome())
			if err != nil {
				logger.ErrorCF("agent", "Failed to initialize protected document form store", map[string]any{
					"code": "protected_store_unavailable",
				})
			} else {
				sink, sinkErr := document.NewFormProtectedAnswerSink(documentFormJobs)
				if sinkErr != nil {
					documentFormJobs.Close()
					documentFormJobs = nil
					logger.ErrorCF("agent", "Failed to initialize protected document answer sink", map[string]any{
						"code": "protected_store_unavailable",
					})
				} else if registerErr := al.interactions.registerProtectedAnswerSink(sink); registerErr != nil {
					sink.Close()
					documentFormJobs = nil
					logger.ErrorCF("agent", "Failed to register protected document answer sink", map[string]any{
						"code": "protected_store_unavailable",
					})
				}
			}
		}
	}
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
		immediateRegisterTool := registerToolIfAllowed
		immediateRegisterHiddenTool := registerHiddenToolIfAllowed
		pendingTools := make([]runtimeToolUpdate, 0, 24)
		stageTool := func(target *AgentInstance, tool toolshared.Tool) bool {
			if target == nil || target.toolComposer == nil {
				return immediateRegisterTool(target, tool)
			}
			if tool == nil {
				return false
			}
			allowed := agentAllowsTool(target, tool.Name())
			pendingTools = append(pendingTools, runtimeToolUpdate{
				source: runtimeToolContributorName(tool.Name()),
				runtimeToolCandidate: runtimeToolCandidate{
					tool: tool,
				},
			})
			return allowed
		}
		stageHiddenTool := func(target *AgentInstance, tool toolshared.Tool) bool {
			if target == nil || target.toolComposer == nil {
				return immediateRegisterHiddenTool(target, tool)
			}
			if tool == nil {
				return false
			}
			allowed := agentAllowsTool(target, tool.Name())
			pendingTools = append(pendingTools, runtimeToolUpdate{
				source: runtimeToolContributorName(tool.Name()),
				runtimeToolCandidate: runtimeToolCandidate{
					tool:   tool,
					hidden: true,
				},
			})
			return allowed
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
				stageTool(agent, requestTool)
			}
		}
		if cfg.Tools.IsToolEnabled("memory") {
			workspace := agent.Workspace
			memoryRoot := workspace
			if layout, ok := codingLayoutForAgent(al.codingProfile, agent.ID); ok {
				memoryRoot = layout.StateRoot()
			}
			stageTool(
				agent,
				tools.NewMemoryTool(
					memoryRoot,
					func() { registry.invalidateWorkspaceContextCaches(workspace) },
					al.runtimeEvents,
				),
			)
		}
		if al.state != nil {
			stageTool(agent, tools.NewGetGoalTool(al.state))
			stageTool(agent, tools.NewCreateGoalTool(al.state))
			stageTool(agent, tools.NewUpdateGoalTool(al.state))
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
				stageTool(agent, searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := integrationtools.NewWebFetchTool(
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
				stageTool(agent, fetchTool)
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			stageTool(agent, hardwaretools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			stageTool(agent, hardwaretools.NewSPITool())
		}
		if cfg.Tools.IsToolEnabled("serial") {
			stageTool(agent, hardwaretools.NewSerialTool())
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
			stageTool(agent, messageTool)
		}
		if cfg.Tools.IsToolEnabled("document") && documentToolAvailable() {
			formAuditor := newDocumentFormAuditor(cfg, agent)
			formAuditPolicy := documentFormAuditPolicy(cfg, formAuditor)
			documentTool := tools.NewDocumentTool(
				tools.WithDocumentExecutionBudget(al.documentBudget),
				tools.WithDocumentFormJobStore(documentFormJobs),
				tools.WithDocumentFormAudit(formAuditPolicy, formAuditor),
				tools.WithDocumentLocalPathPolicy(
					agent.Workspace,
					cfg.Agents.Defaults.RestrictToWorkspace,
					documentAllowReadPaths,
				),
				tools.WithDocumentStateRoot(filepath.Join(config.GetHome(), "state", "document-writes")),
				tools.WithDocumentDeliveryInspector(func(deliveryID string) (outbox.DeliveryInspection, error) {
					coordinator := al.outboundCoordinator()
					if coordinator == nil {
						return outbox.DeliveryInspection{}, fmt.Errorf(
							"durable outbound coordinator is unavailable",
						)
					}
					return coordinator.Inspect(deliveryID)
				}),
			)
			if stageHiddenTool(agent, documentTool) {
				stageDocumentToolDiscovery(agent, stageTool)
			}
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
			stageTool(agent, reactionTool)
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
			stageTool(agent, sendFileTool)
		}

		if ttsProvider != nil {
			stageTool(agent, integrationtools.NewSendTTSTool(ttsProvider, nil))
		}

		if cfg.Tools.IsToolEnabled("load_image") {
			loadImageTool := fstools.NewLoadImageTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			stageTool(agent, loadImageTool)
		}

		if cfg.Tools.IsToolEnabled("image_generate") {
			imageGenerateTool := tools.NewImageGenerateTool(
				agent.Workspace,
				cfg.Tools.ImageGenerate.EffectiveModel(),
				nil,
				tools.WithImageGenerationFallbacks(cfg.Tools.ImageGenerate.Fallbacks),
				tools.WithImageGenerationOutputDir(cfg.Tools.ImageGenerate.OutputDir),
				tools.WithImageGenerationProviderResolver(func(
					model string,
				) (providers.ImageGenerationProvider, string, error) {
					return providers.CreateImageGenerationProvider(cfg, model)
				}),
				tools.WithImageGenerationInputPolicy(
					cfg.Agents.Defaults.RestrictToWorkspace,
					cfg.Agents.Defaults.GetMaxMediaSize(),
					allowReadPaths,
				),
			)
			stageTool(agent, imageGenerateTool)
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
				stageTool(agent, integrationtools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				userHome, homeErr := os.UserHomeDir()
				if homeErr != nil {
					logger.WarnCF("agent", "User skill install scope is unavailable", map[string]any{
						"error": homeErr.Error(),
					})
				}
				gatewayCompatibility := ConfiguredSkillCompatibilityEnvironment(
					cfg,
					skills.SkillRuntimeGateway,
				)
				if agent.toolComposer != nil {
					gatewayCompatibility = newSkillCompatibilityEnvironment(
						cfg,
						skills.SkillRuntimeGateway,
						agent.MCPServerPolicy,
						func() runtimecap.Report { return al.capabilityReportForAgent(agent) },
					)
				}
				stageTool(
					agent,
					integrationtools.NewInstallSkillTool(
						registryMgr,
						skills.SkillInstallContext{UserHome: userHome, Workspace: agent.Workspace},
						map[skills.SkillRuntime]skills.SkillCompatibilityEnvironment{
							skills.SkillRuntimeCoding: ConfiguredSkillCompatibilityEnvironment(
								cfg,
								skills.SkillRuntimeCoding,
							),
							skills.SkillRuntimeGateway: gatewayCompatibility,
						},
					),
				)
			}
		}

		currentAgentID := agentID
		canTargetAgent := func(targetAgentID string) bool {
			return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
		}
		targetRequiresObjectiveChecklist := func(targetAgentID string) bool {
			if targetAgentID == "" {
				targetAgentID = currentAgentID
			}
			target, found := registry.GetAgent(targetAgentID)
			return found && target.Tools != nil && target.Tools.HasRegistered("browser_act")
		}

		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		subagentEnabled := cfg.Tools.IsToolEnabled("subagent")
		if spawnEnabled && subagentEnabled {
			subagentManager, managerErr := tools.NewSubagentManager(tools.SubagentManagerConfig{
				DefaultModel:    agent.Model,
				AvailableModels: availableModels,
				MaxTokens:       agent.MaxTokens,
				Temperature:     agent.Temperature,
				Spawner:         NewSubTurnSpawner(al),
				TaskRegistry:    taskRegistry,
			})
			if managerErr != nil {
				logger.ErrorCF("agent", "Failed to initialize subagent manager", map[string]any{
					"agent_id": agentID,
					"error":    managerErr.Error(),
				})
			} else {
				spawnTool, spawnErr := tools.NewSpawnTool(tools.SpawnToolConfig{
					Manager:                    subagentManager,
					AllowTarget:                canTargetAgent,
					RequiresObjectiveChecklist: targetRequiresObjectiveChecklist,
				})
				subagentTool, subagentErr := tools.NewSubagentTool(subagentManager)
				if spawnErr != nil {
					logger.ErrorCF("agent", "Failed to initialize subagent tools", map[string]any{
						"agent_id": agentID,
						"error":    spawnErr.Error(),
					})
				} else if subagentErr != nil {
					logger.ErrorCF("agent", "Failed to initialize subagent tools", map[string]any{
						"agent_id": agentID,
						"error":    subagentErr.Error(),
					})
				} else {
					stageTool(agent, spawnTool)
					stageTool(agent, subagentTool)
				}
			}
		} else if spawnEnabled {
			logger.WarnCF("agent", "spawn tool requires subagent to be enabled", nil)
		}

		if cfg.Tools.IsToolEnabled("task_status") {
			stageTool(agent, tools.NewTaskStatusTool(taskRegistry, interactionRegistry))
		}

		// Register delegate tool for multi-agent setups.
		// Auto-enabled when multiple agents exist. Delegation uses the SubTurn
		// mechanism directly (not SubagentManager) and is independent of the
		// subagent tool.
		if len(registry.ListAgentIDs()) > 1 {
			delegateTool, delegateErr := tools.NewDelegateTool(tools.DelegateToolConfig{
				Spawner:                    NewSubTurnSpawner(al),
				AllowTarget:                canTargetAgent,
				RequiresObjectiveChecklist: targetRequiresObjectiveChecklist,
				SelfAgentID:                currentAgentID,
				TaskRegistry:               taskRegistry,
				AvailableModels:            availableModels,
			})
			if delegateErr != nil {
				logger.ErrorCF("agent", "Failed to initialize delegate tool", map[string]any{
					"agent_id": agentID,
					"error":    delegateErr.Error(),
				})
			} else {
				stageTool(agent, delegateTool)
			}
		}
		if agent.toolComposer != nil {
			if err := agent.toolComposer.PutTools(pendingTools...); err != nil {
				return fmt.Errorf("compose shared tools for agent %s: %w", agentID, err)
			}
		}
		warnOnUnknownAgentToolDeclarations(agentID, agent.Workspace, agent.ToolPolicy, agent.Tools)
	}
	return nil
}

func documentToolAvailable() bool {
	capabilities := document.Capabilities()
	for _, operation := range []string{"inspect", "extract", "render"} {
		if capabilities.Operations[operation].State != document.CapabilitySupported {
			return false
		}
	}
	return true
}

func openDocumentFormJobStore(home string) (*document.FormJobStore, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, fmt.Errorf("MintClaw home is unavailable")
	}
	stateRoot := filepath.Join(home, "state", "document-form-jobs")
	keyRoot := filepath.Join(home, "keys", "document-form-jobs")
	for _, root := range []string{stateRoot, keyRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("create protected document form root: %w", err)
		}
		if err := os.Chmod(root, 0o700); err != nil {
			return nil, fmt.Errorf("secure protected document form root: %w", err)
		}
	}
	return document.OpenFormJobStore(document.FormJobStoreOptions{
		StateRoot: stateRoot,
		KeyRoot:   keyRoot,
	})
}

func ensureDocumentToolDiscovery(agent *AgentInstance) {
	stageDocumentToolDiscovery(agent, registerToolIfAllowed)
}

func stageDocumentToolDiscovery(
	agent *AgentInstance,
	register func(*AgentInstance, toolshared.Tool) bool,
) {
	if agent == nil || agent.Tools == nil {
		return
	}
	const (
		discoveryTTL = 5
		maxResults   = 5
	)
	if !agent.Tools.HasRegistered(tools.BM25SearchToolName) {
		register(agent, tools.NewBM25SearchTool(agent.Tools, discoveryTTL, maxResults))
	}
}

func availableChildModelNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	models := make([]string, 0, len(cfg.ModelList))
	for _, model := range cfg.ModelList {
		if model == nil || model.IsVirtual() || !model.Enabled {
			continue
		}
		models = append(models, model.ModelName)
	}
	return models
}

func codingLayoutForAgent(profile *CodingRuntimeProfile, agentID string) (CodingRuntimeLayout, bool) {
	if profile == nil {
		return CodingRuntimeLayout{}, false
	}
	return profile.AgentLayout(agentID)
}
