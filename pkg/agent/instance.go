package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/isolation"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID                        string
	Name                      string
	Model                     string
	Fallbacks                 []string
	Workspace                 string
	CodingLayout              CodingRuntimeLayout
	MaxIterations             int
	MaxTokens                 int
	Temperature               float64
	ThinkingLevel             ThinkingLevel
	ThinkingLevelConfigured   bool
	ContextWindow             int
	SummarizeMessageThreshold int
	SummarizeTokenPercent     int
	Provider                  providers.LLMProvider
	Sessions                  session.SessionStore
	ContextBuilder            *ContextBuilder
	Tools                     *tools.ToolRegistry
	trustedToolRegistry       *tools.ToolRegistry
	Definition                AgentContextDefinition
	Subagents                 *config.SubagentsConfig
	SkillsFilter              []string
	MCPServerPolicy           *PatternPolicy
	ToolPolicy                *PatternPolicy
	Candidates                []providers.FallbackCandidate
	MaxParallelTurns          int

	// Router is non-nil when model routing is configured and the light model
	// was successfully resolved. It scores each incoming message and decides
	// whether to route to LightCandidates or stay with Candidates.
	Router *routing.Router
	// LightCandidates holds the resolved provider candidates for the light model.
	// Pre-computed at agent creation to avoid repeated model_list lookups at runtime.
	LightCandidates []providers.FallbackCandidate
	// LightProvider is the concrete provider instance for the configured light model.
	// It is only used when routing selects the light tier for a turn.
	LightProvider providers.LLMProvider
	// CandidateProviders maps "provider/model" keys to per-candidate LLMProvider
	// instances. This allows each fallback model to use its own api_base and api_key
	// from model_list, instead of inheriting the primary model's provider config.
	CandidateProviders map[string]providers.LLMProvider
	ToolLoopDetection  loopguard.Config
	ownedProviders     []providers.StatefulProvider
	ownedToolClosers   []interface{ Close() error }
	closeState         *agentInstanceCloseState
	ownerRegistry      *AgentRegistry
}

type agentInstanceCloseState struct {
	once sync.Once
	err  error
}

func (a *AgentInstance) admitTrustedToolRegistry() {
	if a != nil {
		a.trustedToolRegistry = a.Tools
	}
}

func (a *AgentInstance) isAdmittedTrustedToolRegistry(registry *tools.ToolRegistry) bool {
	return a != nil && a.trustedToolRegistry != nil && registry == a.trustedToolRegistry
}

type providerOwnership struct {
	injected providers.LLMProvider
	owned    []providers.StatefulProvider
}

func newProviderOwnership(injected providers.LLMProvider) *providerOwnership {
	return &providerOwnership{injected: injected}
}

func (o *providerOwnership) trackCreated(provider providers.LLMProvider) {
	stateful, ok := provider.(providers.StatefulProvider)
	if !ok || providersShareIdentity(provider, o.injected) {
		return
	}
	for _, existing := range o.owned {
		if providersShareIdentity(existing, provider) {
			return
		}
	}
	o.owned = append(o.owned, stateful)
}

func providersShareIdentity(first, second providers.LLMProvider) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	firstValue := reflect.ValueOf(first)
	secondValue := reflect.ValueOf(second)
	if firstValue.Type() != secondValue.Type() {
		return false
	}
	if firstValue.Type().Comparable() {
		return firstValue.Interface() == secondValue.Interface()
	}
	switch firstValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
		return firstValue.Pointer() == secondValue.Pointer()
	default:
		return false
	}
}

type agentToolInitConfig struct {
	restrict      bool
	readRestrict  bool
	allowRead     []*regexp.Regexp
	allowWrite    []*regexp.Regexp
	toolPolicy    *PatternPolicy
	toolsRegistry *tools.ToolRegistry
	execScratch   string
}

type runtimeInstanceDependencies struct {
	storeFactory CodingRuntimeStoreFactory
}

type agentIdentityConfig struct {
	agentID      string
	agentName    string
	subagents    *config.SubagentsConfig
	skillsFilter []string
}

type agentRuntimeConfig struct {
	maxIterations             int
	maxTokens                 int
	contextWindow             int
	temperature               float64
	thinkingLevel             ThinkingLevel
	thinkingLevelConfigured   bool
	summarizeMessageThreshold int
	summarizeTokenPercent     int
}

type agentRoutingConfig struct {
	candidates         []providers.FallbackCandidate
	candidateProviders map[string]providers.LLMProvider
	router             *routing.Router
	lightCandidates    []providers.FallbackCandidate
	lightProvider      providers.LLMProvider
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	instance, _ := newAgentInstance(agentCfg, defaults, cfg, provider, nil, nil)
	return instance
}

// newCodingAgentInstance constructs an isolated coding agent from a layout
// resolved before registry construction. MintClaw-owned session state is
// opened under StateRoot; ExecutionRoot is never created by construction.
func newCodingAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	layout CodingRuntimeLayout,
	storeFactory CodingRuntimeStoreFactory,
) (*AgentInstance, error) {
	return newAgentInstance(agentCfg, defaults, cfg, provider, &layout, &runtimeInstanceDependencies{
		storeFactory: storeFactory,
	})
}

func newAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	layout *CodingRuntimeLayout,
	runtimeDeps *runtimeInstanceDependencies,
) (*AgentInstance, error) {
	if cfg != nil {
		// Keep the subprocess isolation runtime aligned with the latest loaded config
		// before any tools or providers start spawning child processes.
		isolation.Configure(cfg)
	}

	workspace := resolveAgentWorkspace(agentCfg, defaults)
	if layout != nil {
		if err := layout.Validate(); err != nil {
			return nil, fmt.Errorf("construct agent: invalid coding layout: %w", err)
		}
		workspace = layout.ExecutionRoot()
	} else {
		_ = os.MkdirAll(workspace, 0o755)
	}

	codingRuntime := layout != nil
	definition := AgentContextDefinition{}
	if !codingRuntime {
		definition = loadAgentDefinition(workspace)
	}
	frontmatterModel := ""
	if definition.Agent != nil {
		frontmatterModel = definition.Agent.Frontmatter.Model
		if frontmatterModel != "" {
			if err := requireExactModelName(frontmatterModel); err != nil {
				return nil, fmt.Errorf("construct agent: workspace model: %w", err)
			}
		}
	}
	model := resolveAgentModel(agentCfg, defaults, definition)
	if frontmatterModel != "" {
		if cfg == nil {
			return nil, fmt.Errorf("construct agent: workspace model %q requires configuration", model)
		}
		configured := false
		for _, modelCfg := range cfg.ModelList {
			if modelCfg != nil && modelCfg.Enabled && modelCfg.ModelName == model {
				configured = true
				break
			}
		}
		if !configured {
			return nil, fmt.Errorf("construct agent: workspace model %q is not configured", model)
		}
	}
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)
	agentToolPolicy := resolveAgentToolPolicy(definition)
	agentMCPServerPolicy := resolveAgentMCPServerPolicy(definition)
	if codingRuntime {
		// Repository frontmatter cannot mutate the admitted coding catalog.
		agentToolPolicy = nil
		agentMCPServerPolicy = &PatternPolicy{Allow: []string{}, form: patternPolicyFormList}
	}

	sessionsDir := filepath.Join(workspace, "sessions")
	if layout != nil {
		sessionsDir = layout.StatePaths().SessionsRoot
	}
	var sessions session.SessionStore
	var contextBuilder *ContextBuilder
	if layout != nil {
		var err error
		factory := CodingRuntimeStoreFactory(defaultCodingRuntimeStoreFactory{})
		if runtimeDeps != nil && runtimeDeps.storeFactory != nil {
			factory = runtimeDeps.storeFactory
		}
		sessions, err = factory.NewSessionStore(*layout)
		if err != nil {
			return nil, fmt.Errorf("construct agent: %w", err)
		}
		if runtimeDependencyIsNil(sessions) {
			return nil, fmt.Errorf("construct agent: coding store factory returned a nil session store")
		}
		contextBuilder, err = newCodingContextBuilder(*layout)
		if err != nil {
			_ = sessions.Close()
			return nil, fmt.Errorf("construct agent: %w", err)
		}
	} else {
		var err error
		sessions, err = initRuntimeSessionStore(sessionsDir)
		if err != nil {
			return nil, fmt.Errorf("construct agent: %w", err)
		}
		contextBuilder = NewContextBuilder(workspace)
	}
	if !codingRuntime {
		contextBuilder = contextBuilder.
			WithSplitOnMarker(cfg.Agents.Defaults.SplitOnMarker).
			WithPromptMemoryConfig(defaults.PromptMemory)
	}

	identity := buildAgentIdentityConfig(defaults, agentCfg, definition)
	if codingRuntime {
		identity.agentName = "MintClaw coding agent"
		identity.subagents = nil
		identity.skillsFilter = nil
		contextBuilder.WithCodingPromptModel(model)
	}
	providerOwnership := newProviderOwnership(provider)
	var selectedModel *resolvedModelSelection
	provider, selectedModel = resolvePrimaryProviderForAgent(
		cfg,
		workspace,
		identity.agentID,
		model,
		provider,
		providerOwnership,
	)
	warnOnUnknownAgentMCPServerDeclarations(identity.agentID, workspace, cfg, definition)

	toolInit := newAgentToolInitConfig(defaults, cfg, agentToolPolicy)
	if layout != nil {
		toolInit.execScratch = filepath.Join(layout.StatePaths().OperationalRoot, "tmp")
	}
	if codingRuntime {
		workingDirectory := workspace
		if contextBuilder.codingInstructions != nil {
			workingDirectory = contextBuilder.codingInstructions.workingDirectory()
		}
		if err := initCodingAgentTools(workspace, workingDirectory, cfg, toolInit); err != nil {
			_ = sessions.Close()
			return nil, fmt.Errorf("construct agent: %w", err)
		}
	} else {
		initCoreAgentTools(workspace, cfg, toolInit)
	}
	var selectedModelConfig *config.ModelConfig
	if selectedModel != nil {
		selectedModelConfig = selectedModel.modelConfig
	}
	runtimeCfg := buildAgentRuntimeConfig(defaults, selectedModelConfig)
	routingCfg := buildAgentRoutingConfig(
		cfg,
		defaults,
		workspace,
		selectedModel,
		fallbacks,
		identity.agentID,
		providerOwnership,
	)

	instance := &AgentInstance{
		ID:                        identity.agentID,
		Name:                      identity.agentName,
		Model:                     model,
		Fallbacks:                 fallbacks,
		Workspace:                 workspace,
		MaxIterations:             runtimeCfg.maxIterations,
		MaxTokens:                 runtimeCfg.maxTokens,
		Temperature:               runtimeCfg.temperature,
		ThinkingLevel:             runtimeCfg.thinkingLevel,
		ThinkingLevelConfigured:   runtimeCfg.thinkingLevelConfigured,
		ContextWindow:             runtimeCfg.contextWindow,
		SummarizeMessageThreshold: runtimeCfg.summarizeMessageThreshold,
		SummarizeTokenPercent:     runtimeCfg.summarizeTokenPercent,
		Provider:                  provider,
		Sessions:                  sessions,
		ContextBuilder:            contextBuilder,
		Tools:                     toolInit.toolsRegistry,
		Definition:                definition,
		Subagents:                 identity.subagents,
		SkillsFilter:              identity.skillsFilter,
		MCPServerPolicy:           agentMCPServerPolicy,
		ToolPolicy:                agentToolPolicy,
		MaxParallelTurns:          resolveAgentMaxParallelTurns(agentCfg),
		Candidates:                routingCfg.candidates,
		Router:                    routingCfg.router,
		LightCandidates:           routingCfg.lightCandidates,
		LightProvider:             routingCfg.lightProvider,
		CandidateProviders:        routingCfg.candidateProviders,
		ToolLoopDetection:         loopGuardConfigFromConfig(cfg.Tools.LoopDetection),
		ownedProviders:            providerOwnership.owned,
		closeState:                &agentInstanceCloseState{},
	}
	if layout != nil {
		if execTool, ok := toolInit.toolsRegistry.Get("exec"); ok {
			if closer, ok := execTool.(interface{ Close() error }); ok {
				instance.ownedToolClosers = append(instance.ownedToolClosers, closer)
			}
		}
	}
	if layout != nil {
		instance.CodingLayout = *layout
	}
	return instance, nil
}

func resolveAgentMaxParallelTurns(agentCfg *config.AgentConfig) int {
	if agentCfg == nil || agentCfg.MaxParallelTurns <= 0 {
		return 0
	}
	return agentCfg.MaxParallelTurns
}

func loopGuardConfigFromConfig(cfg config.ToolLoopDetectionConfig) loopguard.Config {
	return loopguard.Config{
		Enabled:             cfg.Enabled,
		WarningsEnabled:     cfg.WarningsEnabled,
		HardStopsEnabled:    cfg.HardStopsEnabled,
		ExactFailureWarn:    cfg.ExactFailureWarn,
		ExactFailureBlock:   cfg.ExactFailureBlock,
		SameToolFailureWarn: cfg.SameToolFailureWarn,
		SameToolFailureHalt: cfg.SameToolFailureHalt,
		NoProgressWarn:      cfg.NoProgressWarn,
		NoProgressBlock:     cfg.NoProgressBlock,
		IdenticalCallWarn:   cfg.IdenticalCallWarn,
		IdenticalCallHalt:   cfg.IdenticalCallHalt,
		MaxSignatures:       cfg.MaxSignatures,
	}.Normalized()
}

func newAgentToolInitConfig(
	defaults *config.AgentDefaults,
	cfg *config.Config,
	toolPolicy *PatternPolicy,
) agentToolInitConfig {
	restrict := defaults.RestrictToWorkspace
	return agentToolInitConfig{
		restrict:      restrict,
		readRestrict:  restrict && !defaults.AllowReadOutsideWorkspace,
		allowRead:     buildAllowReadPatterns(cfg),
		allowWrite:    compilePatterns(cfg.Tools.AllowWritePaths),
		toolPolicy:    toolPolicy,
		toolsRegistry: tools.NewToolRegistry(),
	}
}

func initCoreAgentTools(workspace string, cfg *config.Config, initCfg agentToolInitConfig) {
	registerTool := func(tool toolshared.Tool) {
		registerToolWithPolicies(initCfg.toolsRegistry, tool, initCfg.toolPolicy)
	}

	if cfg.Tools.IsToolEnabled("read_file") {
		maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
		switch cfg.Tools.ReadFile.EffectiveMode() {
		case config.ReadFileModeLines:
			registerTool(
				fstools.NewReadFileLinesTool(
					workspace,
					initCfg.readRestrict,
					maxReadFileSize,
					initCfg.allowRead,
				),
			)
		default:
			registerTool(
				fstools.NewReadFileBytesTool(
					workspace,
					initCfg.readRestrict,
					maxReadFileSize,
					initCfg.allowRead,
				),
			)
		}
	}
	if cfg.Tools.IsToolEnabled("append_file") {
		registerTool(fstools.NewAppendFileTool(workspace, initCfg.restrict, initCfg.allowWrite))
	}
	// Build write_file's copy from the registered editors so it steers the agent
	// to append_file only when that tool is actually available.
	if cfg.Tools.IsToolEnabled("write_file") {
		writeTool := fstools.NewWriteFileTool(workspace, initCfg.restrict, initCfg.allowWrite)
		var altTools []string
		if initCfg.toolsRegistry.HasRegistered("append_file") {
			altTools = append(altTools, "append_file")
		}
		writeTool.SetAlternativeTools(altTools)
		registerTool(writeTool)
	}
	if cfg.Tools.IsToolEnabled("list_dir") {
		registerTool(
			fstools.NewListDirTool(workspace, initCfg.readRestrict, initCfg.allowRead),
		)
	}
	if cfg.Tools.IsToolEnabled("search_files") {
		registerTool(
			fstools.NewSearchFilesTool(
				workspace,
				initCfg.readRestrict,
				cfg.Tools.ReadFile.MaxReadFileSize,
				initCfg.allowRead,
			),
		)
	}
	if cfg.Tools.IsToolEnabled("exec") {
		var execTool *tools.ExecTool
		var err error
		if initCfg.execScratch != "" {
			execTool, err = tools.NewExecToolWithRuntimeConfig(
				workspace,
				initCfg.execScratch,
				initCfg.restrict,
				cfg,
				initCfg.allowRead,
			)
		} else {
			execTool, err = tools.NewExecToolWithConfig(workspace, initCfg.restrict, cfg, initCfg.allowRead)
		}
		if err != nil {
			logger.ErrorCF("agent", "Failed to initialize exec tool; continuing without exec",
				map[string]any{"error": err.Error()})
		} else {
			registerTool(execTool)
		}
	}
	if cfg.Tools.IsToolEnabled("apply_patch") {
		registerTool(fstools.NewApplyPatchTool(workspace, initCfg.restrict, initCfg.allowWrite))
	}
}

func initCodingAgentTools(
	workspace string,
	workingDirectory string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
) error {
	registerTool := func(tool toolshared.Tool) {
		initCfg.toolsRegistry.Register(tool)
	}
	maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
	registerTool(fstools.NewReadFileBytesTool(workspace, false, maxReadFileSize, nil))
	registerTool(fstools.NewAppendFileTool(workspace, false, nil))
	writeTool := fstools.NewWriteFileTool(workspace, false, nil)
	writeTool.SetAlternativeTools([]string{"append_file"})
	registerTool(writeTool)
	registerTool(fstools.NewListDirTool(workspace, false, nil))
	registerTool(fstools.NewSearchFilesTool(workspace, false, maxReadFileSize, nil))

	execCfg := *cfg
	execCfg.Tools = cfg.Tools
	execCfg.Tools.Exec = config.ExecConfig{TimeoutSeconds: cfg.Tools.Exec.TimeoutSeconds}
	execTool, err := tools.NewCodingExecToolWithRuntimeConfig(workingDirectory, initCfg.execScratch, &execCfg)
	if err != nil {
		return fmt.Errorf("initialize coding exec tool: %w", err)
	}
	registerTool(execTool)
	registerTool(fstools.NewApplyPatchTool(workspace, false, nil))
	registerTool(tools.NewUpdatePlanTool())
	return nil
}

func buildAgentIdentityConfig(
	defaults *config.AgentDefaults,
	agentCfg *config.AgentConfig,
	definition AgentContextDefinition,
) agentIdentityConfig {
	identity := agentIdentityConfig{
		agentID: routing.DefaultAgentID,
	}
	if agentCfg == nil {
		return identity
	}
	identity.agentID = routing.NormalizeAgentID(agentCfg.ID)
	identity.agentName = agentCfg.Name
	if definition.Agent != nil && strings.TrimSpace(definition.Agent.Frontmatter.Name) != "" {
		identity.agentName = strings.TrimSpace(definition.Agent.Frontmatter.Name)
	}
	var defaultsSubagents *config.SubagentsConfig
	if defaults != nil {
		defaultsSubagents = defaults.Subagents
	}
	identity.subagents = mergeSubagentsConfig(defaultsSubagents, agentCfg.Subagents)
	identity.skillsFilter = resolveAgentSkillsFilter(agentCfg, definition)
	return identity
}

func buildAgentRuntimeConfig(
	defaults *config.AgentDefaults,
	selectedModel *config.ModelConfig,
) agentRuntimeConfig {
	maxIterations := defaults.MaxToolIterations
	if maxIterations == 0 {
		maxIterations = 20
	}

	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	contextWindow := defaults.ContextWindow
	if contextWindow == 0 {
		contextWindow = modelContextWindow(selectedModel)
	}
	if contextWindow == 0 {
		contextWindow = maxTokens * 4
	}

	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}

	var thinkingLevelStr string
	if selectedModel != nil {
		thinkingLevelStr = selectedModel.ThinkingLevel
	}

	summarizeMessageThreshold := defaults.SummarizeMessageThreshold
	if summarizeMessageThreshold == 0 {
		summarizeMessageThreshold = 20
	}

	summarizeTokenPercent := defaults.SummarizeTokenPercent
	if summarizeTokenPercent == 0 {
		summarizeTokenPercent = 75
	}

	return agentRuntimeConfig{
		maxIterations:             maxIterations,
		maxTokens:                 maxTokens,
		contextWindow:             contextWindow,
		temperature:               temperature,
		thinkingLevel:             parseThinkingLevel(thinkingLevelStr),
		thinkingLevelConfigured:   isConfiguredThinkingLevel(thinkingLevelStr),
		summarizeMessageThreshold: summarizeMessageThreshold,
		summarizeTokenPercent:     summarizeTokenPercent,
	}
}

func modelContextWindow(modelConfig *config.ModelConfig) int {
	if modelConfig == nil {
		return 0
	}
	if modelConfig.ContextWindow > 0 {
		return modelConfig.ContextWindow
	}
	protocol, modelID := providers.ExtractProtocol(modelConfig)
	if protocol != "openai" {
		return 0
	}
	authMethod := strings.ToLower(strings.TrimSpace(modelConfig.AuthMethod))
	if authMethod != "oauth" && authMethod != "token" {
		return 0
	}
	metadata, ok := providers.BundledCodexModel(modelID)
	if !ok {
		return 0
	}
	return metadata.ContextWindow
}

func buildAgentRoutingConfig(
	cfg *config.Config,
	defaults *config.AgentDefaults,
	workspace string,
	selectedModel *resolvedModelSelection,
	fallbacks []string,
	agentID string,
	providerOwnership *providerOwnership,
) agentRoutingConfig {
	routingCfg := agentRoutingConfig{
		candidates:         resolveModelCandidatesFromSelection(cfg, selectedModel, fallbacks),
		candidateProviders: make(map[string]providers.LLMProvider),
	}
	populateCandidateProvidersFromNamesTracked(
		cfg,
		workspace,
		fallbacks,
		routingCfg.candidateProviders,
		providerOwnership,
	)

	rc := defaults.Routing
	if rc == nil || !rc.Enabled || rc.LightModel == "" {
		return routingCfg
	}

	lightSelection, err := resolveModelSelection(cfg, rc.LightModel, workspace)
	if err != nil {
		logger.WarnCF(
			"agent",
			"Routing light model config invalid; routing disabled",
			map[string]any{
				"light_model": rc.LightModel,
				"agent_id":    agentID,
				"error":       err.Error(),
			},
		)
		return routingCfg
	}
	resolved := resolveModelCandidatesFromSelection(cfg, &lightSelection, nil)

	lightProvider, _, err := providers.CreateProviderFromConfig(lightSelection.modelConfig)
	if err != nil {
		logger.WarnCF("agent", "Routing light model provider init failed; routing disabled",
			map[string]any{"light_model": rc.LightModel, "agent_id": agentID, "error": err.Error()})
		return routingCfg
	}

	routingCfg.router = routing.New(routing.RouterConfig{
		LightModel: rc.LightModel,
		Threshold:  rc.Threshold,
	})
	routingCfg.lightCandidates = resolved
	routingCfg.lightProvider = lightProvider
	providerOwnership.trackCreated(lightProvider)
	populateCandidateProvidersFromNamesTracked(
		cfg,
		workspace,
		[]string{rc.LightModel},
		routingCfg.candidateProviders,
		providerOwnership,
	)
	return routingCfg
}

func resolveModelCandidatesFromSelection(
	cfg *config.Config,
	selection *resolvedModelSelection,
	fallbacks []string,
) []providers.FallbackCandidate {
	candidates := make([]providers.FallbackCandidate, 0, 1+len(fallbacks))
	seen := make(map[string]bool, 1+len(fallbacks))
	if selection != nil {
		primary, ok := candidateFromModelSelection(*selection)
		if ok {
			candidates = append(candidates, primary)
			seen[primary.StableKey()] = true
		}
	}
	for _, fallback := range resolveModelCandidates(cfg, "", fallbacks) {
		if seen[fallback.StableKey()] {
			continue
		}
		candidates = append(candidates, fallback)
		seen[fallback.StableKey()] = true
	}
	return candidates
}

// populateCandidateProvidersFromNames resolves each exact configured model_name
// and creates its dedicated LLMProvider. Duplicate names retain model-list
// load-balancing behavior.
func populateCandidateProvidersFromNames(
	cfg *config.Config,
	workspace string,
	names []string,
	out map[string]providers.LLMProvider,
) {
	populateCandidateProvidersFromNamesTracked(cfg, workspace, names, out, nil)
}

func populateCandidateProvidersFromNamesTracked(
	cfg *config.Config,
	workspace string,
	names []string,
	out map[string]providers.LLMProvider,
	providerOwnership *providerOwnership,
) {
	if cfg == nil || len(names) == 0 {
		return
	}
	for _, name := range names {
		mc, err := resolvedModelConfig(cfg, strings.TrimSpace(name), workspace)
		if err != nil {
			logger.WarnCF("agent",
				"fallback provider: no model_list entry found; will inherit primary provider credentials",
				map[string]any{"name": name, "error": err.Error()})
			continue
		}
		protocol, modelID := providers.ExtractProtocol(mc)
		key := providers.ModelKey(protocol, modelID)
		if _, exists := out[key]; exists {
			continue
		}
		p, _, err := providers.CreateProviderFromConfig(mc)
		if err != nil {
			logger.WarnCF("agent", "fallback provider: failed to create provider",
				map[string]any{"model": mc.Model, "error": err.Error()})
			continue
		}
		out[key] = p
		if providerOwnership != nil {
			providerOwnership.trackCreated(p)
		}
	}
}

// resolvePrimaryProviderForAgent resolves a dedicated provider for the active
// primary model when the model points at a model_list entry. This keeps the
// agent's single-candidate path aligned with the selected model's own
// provider/api_base/api_key instead of inheriting the process default provider.
func resolvePrimaryProviderForAgent(
	cfg *config.Config,
	workspace string,
	agentID string,
	model string,
	fallback providers.LLMProvider,
	providerOwnership *providerOwnership,
) (providers.LLMProvider, *resolvedModelSelection) {
	model = strings.TrimSpace(model)
	if cfg == nil || model == "" {
		return fallback, nil
	}

	selection, err := resolveModelSelection(cfg, model, workspace)
	if err != nil || selection.modelConfig == nil {
		return fallback, nil
	}

	resolvedProvider, _, err := providers.CreateProviderFromConfig(selection.modelConfig)
	if err != nil {
		logger.WarnCF("agent", "Primary model provider init failed; using injected provider",
			map[string]any{
				"agent_id": agentID,
				"model":    model,
				"error":    err.Error(),
			})
		return fallback, &selection
	}
	if resolvedProvider == nil {
		return fallback, &selection
	}
	providerOwnership.trackCreated(resolvedProvider)
	return resolvedProvider, &selection
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return fileutil.ExpandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	// Use the configured default workspace (respects MINTCLAW_HOME)
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" ||
		routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return fileutil.ExpandHome(defaults.Workspace)
	}
	// For named agents without explicit workspace, use default workspace with agent ID suffix
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(fileutil.ExpandHome(defaults.Workspace), "..", "workspace-"+id)
}

// resolveAgentModel resolves the primary model for an agent.
func resolveAgentModel(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	definition AgentContextDefinition,
) string {
	if definition.Agent != nil && strings.TrimSpace(definition.Agent.Frontmatter.Model) != "" {
		return strings.TrimSpace(definition.Agent.Frontmatter.Model)
	}
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	return defaults.GetModelName()
}

// resolveAgentFallbacks resolves the fallback models for an agent.
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	return defaults.ModelFallbacks
}

func resolveAgentSkillsFilter(
	agentCfg *config.AgentConfig,
	definition AgentContextDefinition,
) []string {
	if definition.Agent != nil && definition.Agent.Frontmatter.Skills != nil {
		return append([]string(nil), definition.Agent.Frontmatter.Skills...)
	}
	if agentCfg == nil || agentCfg.Skills == nil {
		return nil
	}
	return append([]string(nil), agentCfg.Skills...)
}

func (a *AgentInstance) AllowsMCPServer(serverName string) bool {
	if a == nil {
		return true
	}
	return toolAllowedByPolicy(a.MCPServerPolicy, normalizeMCPServerName(serverName))
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			logger.WarnCF("agent", "invalid path pattern in compilePatterns", map[string]any{
				"pattern": p,
				"error":   err.Error(),
			})
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

func buildAllowReadPatterns(cfg *config.Config) []*regexp.Regexp {
	var configured []string
	if cfg != nil {
		configured = cfg.Tools.AllowReadPaths
	}

	compiled := compilePatterns(configured)
	mediaDirPattern := regexp.MustCompile(mediaTempDirPattern())
	for _, pattern := range compiled {
		if pattern.String() == mediaDirPattern.String() {
			return compiled
		}
	}

	return append(compiled, mediaDirPattern)
}

func mediaTempDirPattern() string {
	sep := regexp.QuoteMeta(string(os.PathSeparator))
	return "^" + regexp.QuoteMeta(filepath.Clean(media.TempDir())) + "(?:" + sep + "|$)"
}

// Close releases resources held by the agent's session store.
func (a *AgentInstance) Close() error {
	if a == nil {
		return nil
	}
	if a.closeState == nil {
		a.closeState = &agentInstanceCloseState{}
	}
	a.closeState.once.Do(func() {
		var closeErrors []error
		for _, closer := range a.ownedToolClosers {
			if closer != nil {
				closeErrors = append(closeErrors, closer.Close())
			}
		}
		a.ownedToolClosers = nil
		if a.Sessions != nil {
			closeErrors = append(closeErrors, a.Sessions.Close())
			a.Sessions = nil
		}
		for _, provider := range a.ownedProviders {
			provider.Close()
		}
		a.ownedProviders = nil
		a.closeState.err = errors.Join(closeErrors...)
	})
	return a.closeState.err
}

func initRuntimeSessionStore(dir string) (session.SessionStore, error) {
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("initialize runtime session store: %w", err)
	}
	return session.NewJSONLBackend(store), nil
}
