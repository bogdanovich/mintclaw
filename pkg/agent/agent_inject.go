// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/state"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type RuntimeToolFactory func(cfg *config.Config) (toolshared.Tool, error)

// RuntimeAgentToolFactory builds one agent-scoped runtime tool so its schema
// and authority can be projected without exposing another agent's grants.
type RuntimeAgentToolFactory func(cfg *config.Config, agentID string) (toolshared.Tool, error)

// RuntimeToolDecoratorFactory wraps one already configured agent tool. Unlike
// RuntimeToolFactory, it receives the agent-specific local implementation, so
// routing can preserve each agent's workspace and filesystem policy.
type RuntimeToolDecoratorFactory func(
	cfg *config.Config,
	agentID string,
	local toolshared.Tool,
) (toolshared.Tool, error)

type agentRuntimeToolMutationKind uint8

const (
	agentRuntimeToolPut agentRuntimeToolMutationKind = iota
	agentRuntimeToolRemove
	agentRuntimeToolReplace
)

type agentRuntimeToolMutation struct {
	agent    *AgentInstance
	kind     agentRuntimeToolMutationKind
	source   string
	toolName string
	tool     toolshared.Tool
	hidden   bool
}

func applyAgentRuntimeToolMutations(mutations ...agentRuntimeToolMutation) error {
	composerMutations := make(map[*runtimeToolComposer][]agentRuntimeToolMutation)
	composerLabels := make(map[*runtimeToolComposer]string)
	direct := make([]agentRuntimeToolMutation, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.agent == nil || mutation.agent.Tools == nil {
			continue
		}
		switch mutation.kind {
		case agentRuntimeToolPut:
			if mutation.tool == nil || strings.TrimSpace(mutation.source) == "" {
				return fmt.Errorf("agent %s has an invalid runtime tool registration", mutation.agent.ID)
			}
		case agentRuntimeToolRemove:
			if strings.TrimSpace(mutation.source) == "" || strings.TrimSpace(mutation.toolName) == "" {
				return fmt.Errorf("agent %s has an invalid runtime tool removal", mutation.agent.ID)
			}
		case agentRuntimeToolReplace:
			if mutation.tool == nil || strings.TrimSpace(mutation.toolName) == "" ||
				mutation.tool.Name() != mutation.toolName {
				return fmt.Errorf("agent %s has an invalid runtime tool replacement", mutation.agent.ID)
			}
		default:
			return fmt.Errorf("agent %s has an unknown runtime tool mutation", mutation.agent.ID)
		}
		if mutation.agent.toolComposer == nil {
			direct = append(direct, mutation)
			continue
		}
		composer := mutation.agent.toolComposer
		composerMutations[composer] = append(composerMutations[composer], mutation)
		composerLabels[composer] = "agent " + mutation.agent.ID
	}
	composerUpdates := make([]runtimeToolComposerUpdate, 0, len(composerMutations))
	for composer, grouped := range composerMutations {
		mutationsForComposer := append([]agentRuntimeToolMutation(nil), grouped...)
		composerUpdates = append(composerUpdates, runtimeToolComposerUpdate{
			composer: composer,
			label:    composerLabels[composer],
			mutate: func(draft *runtimeToolComposerDraft) error {
				for _, mutation := range mutationsForComposer {
					var err error
					switch mutation.kind {
					case agentRuntimeToolPut:
						err = draft.put(mutation.source, runtimeToolCandidate{
							tool: mutation.tool, hidden: mutation.hidden,
						})
					case agentRuntimeToolRemove:
						err = draft.remove(mutation.source)
					case agentRuntimeToolReplace:
						err = draft.replace(mutation.toolName, mutation.tool)
					default:
						err = fmt.Errorf("unknown runtime tool mutation")
					}
					if err != nil {
						return err
					}
				}
				return nil
			},
		})
	}
	if err := applyRuntimeToolComposerUpdates(composerUpdates...); err != nil {
		return err
	}
	for _, mutation := range direct {
		switch mutation.kind {
		case agentRuntimeToolPut:
			if !agentAllowsTool(mutation.agent, mutation.tool.Name()) {
				continue
			}
			if mutation.hidden {
				mutation.agent.Tools.RegisterHidden(mutation.tool)
			} else {
				mutation.agent.Tools.Register(mutation.tool)
			}
		case agentRuntimeToolRemove:
			mutation.agent.Tools.Unregister(mutation.toolName)
		case agentRuntimeToolReplace:
			mutation.agent.Tools.Register(mutation.tool)
		}
	}
	return nil
}

func (al *AgentLoop) RegisterTool(tool toolshared.Tool) {
	if al == nil || al.usesCodingProfile() {
		return
	}
	registry := al.GetRegistry()
	if err := registerToolOnRegistry(registry, tool); err != nil {
		logger.ErrorCF("agent", "Failed to register injected runtime tool", map[string]any{
			"tool":  tool.Name(),
			"error": err.Error(),
		})
	}
}

func (al *AgentLoop) RegisterRuntimeTool(name string, factory RuntimeToolFactory) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	if al.usesCodingProfile() {
		return fmt.Errorf("coding runtime profiles do not admit runtime tools")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("runtime tool name is required")
	}
	if factory == nil {
		return fmt.Errorf("runtime tool factory is required for %s", name)
	}
	cfg := al.GetConfig()
	tool, err := factory(cfg)
	if err != nil {
		return err
	}
	if tool != nil && tool.Name() != name {
		return fmt.Errorf("runtime tool factory returned %q for %q", tool.Name(), name)
	}

	al.mu.Lock()
	if al.runtimeTools == nil {
		al.runtimeTools = make(map[string]RuntimeToolFactory)
	}
	previous, hadPrevious := al.runtimeTools[name]
	al.runtimeTools[name] = factory
	registry := al.registry
	al.mu.Unlock()

	if err = registerRuntimeFactoryToolOnRegistry(registry, tool); err != nil {
		al.mu.Lock()
		if hadPrevious {
			al.runtimeTools[name] = previous
		} else {
			delete(al.runtimeTools, name)
		}
		al.mu.Unlock()
		return err
	}
	return nil
}

func (al *AgentLoop) RegisterRuntimeAgentTool(name string, factory RuntimeAgentToolFactory) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	if al.usesCodingProfile() {
		return fmt.Errorf("coding runtime profiles do not admit runtime tools")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("runtime agent tool name is required")
	}
	if factory == nil {
		return fmt.Errorf("runtime agent tool factory is required for %s", name)
	}

	al.mu.Lock()
	if al.runtimeAgentTools == nil {
		al.runtimeAgentTools = make(map[string]RuntimeAgentToolFactory)
	}
	previous, hadPrevious := al.runtimeAgentTools[name]
	al.runtimeAgentTools[name] = factory
	registry := al.registry
	cfg := al.cfg
	al.mu.Unlock()

	if err := registerRuntimeAgentToolOnRegistry(cfg, registry, name, factory, false); err != nil {
		al.mu.Lock()
		if hadPrevious {
			al.runtimeAgentTools[name] = previous
		} else {
			delete(al.runtimeAgentTools, name)
		}
		al.mu.Unlock()
		return err
	}
	return nil
}

// RefreshRuntimeTools rebuilds selected generation-bound tools on the active
// registry without changing their retained factories.
func (al *AgentLoop) RefreshRuntimeTools(names ...string) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	al.mu.RLock()
	cfg := al.cfg
	registry := al.registry
	al.mu.RUnlock()
	return al.refreshRuntimeToolsOnRegistry(cfg, registry, names...)
}

// RefreshRuntimeTools rebuilds selected generation-bound tools on the
// unpublished registry after the owning runtime has reconciled.
func (prepared *PreparedConfigReload) RefreshRuntimeTools(names ...string) error {
	if prepared == nil {
		return fmt.Errorf("prepared config reload is nil")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.committed || prepared.registry == nil {
		return fmt.Errorf("prepared config reload is no longer available")
	}
	err := prepared.loop.refreshRuntimeToolsOnRegistry(
		prepared.config,
		prepared.registry,
		names...,
	)
	if err != nil {
		prepared.err = errors.Join(prepared.err, fmt.Errorf("refresh prepared runtime tools: %w", err))
	}
	return err
}

func (al *AgentLoop) refreshRuntimeToolsOnRegistry(
	cfg *config.Config,
	registry *AgentRegistry,
	names ...string,
) error {
	if registry == nil {
		return nil
	}
	factories := al.runtimeToolFactories()
	agentFactories := al.runtimeAgentToolFactories()
	mutations := make([]agentRuntimeToolMutation, 0, len(names)*len(registry.ListAgentIDs()))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("runtime tool name is required")
		}
		if factory, ok := factories[name]; ok {
			tool, err := factory(cfg)
			if err != nil {
				return fmt.Errorf("refresh runtime tool %s: %w", name, err)
			}
			if tool != nil && tool.Name() != name {
				return fmt.Errorf("refresh runtime tool %s: factory returned %q", name, tool.Name())
			}
			mutations = append(mutations, runtimeToolReplacementMutations(registry, name, tool)...)
			continue
		}
		if factory, ok := agentFactories[name]; ok {
			toolMutations, err := buildRuntimeAgentToolMutations(cfg, registry, name, factory, true)
			if err != nil {
				return fmt.Errorf("refresh runtime agent tool %s: %w", name, err)
			}
			mutations = append(mutations, toolMutations...)
			continue
		}
		return fmt.Errorf("runtime tool %s is not registered", name)
	}
	if err := applyAgentRuntimeToolMutations(mutations...); err != nil {
		return fmt.Errorf("publish refreshed runtime tools: %w", err)
	}
	return nil
}

// RegisterRuntimeToolDecorator installs a per-agent wrapper around an existing
// tool and remembers the factory for registry rebuilds after configuration
// reload. It never creates a tool that the agent did not already have. A nil
// result leaves the local tool unchanged.
func (al *AgentLoop) RegisterRuntimeToolDecorator(name string, factory RuntimeToolDecoratorFactory) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	if al.usesCodingProfile() {
		return fmt.Errorf("coding runtime profiles do not admit runtime tool decorators")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("runtime tool decorator name is required")
	}
	if factory == nil {
		return fmt.Errorf("runtime tool decorator factory is required for %s", name)
	}

	al.mu.Lock()
	if al.runtimeToolDecorators == nil {
		al.runtimeToolDecorators = make(map[string]RuntimeToolDecoratorFactory)
	}
	previous, registered := al.runtimeToolDecorators[name]
	al.runtimeToolDecorators[name] = factory
	registry := al.registry
	cfg := al.cfg
	al.mu.Unlock()

	// Registry rebuilds apply retained decorators before service setup runs
	// again. Re-registration refreshes the factory for the next rebuild, but
	// must not wrap the already decorated current registry a second time.
	if registered {
		return nil
	}
	if err := decorateRuntimeToolOnRegistry(cfg, registry, name, factory); err != nil {
		al.mu.Lock()
		if registered {
			al.runtimeToolDecorators[name] = previous
		} else {
			delete(al.runtimeToolDecorators, name)
		}
		al.mu.Unlock()
		return err
	}
	return nil
}

func (al *AgentLoop) registerRuntimeToolsForRegistry(cfg *config.Config, registry *AgentRegistry) error {
	factories := al.runtimeToolFactories()
	for _, name := range sortedRuntimeToolNames(factories) {
		tool, err := factories[name](cfg)
		if err != nil {
			return fmt.Errorf("register runtime tool %s: %w", name, err)
		}
		if tool != nil && tool.Name() != name {
			return fmt.Errorf("register runtime tool %s: factory returned %q", name, tool.Name())
		}
		if err := registerRuntimeFactoryToolOnRegistry(registry, tool); err != nil {
			return fmt.Errorf("register runtime tool %s: %w", name, err)
		}
	}
	agentFactories := al.runtimeAgentToolFactories()
	for _, name := range sortedRuntimeAgentToolNames(agentFactories) {
		if err := registerRuntimeAgentToolOnRegistry(cfg, registry, name, agentFactories[name], false); err != nil {
			return fmt.Errorf("register runtime agent tool %s: %w", name, err)
		}
	}
	decorators := al.runtimeToolDecoratorFactories()
	for _, name := range sortedRuntimeToolDecoratorNames(decorators) {
		if err := decorateRuntimeToolOnRegistry(cfg, registry, name, decorators[name]); err != nil {
			return fmt.Errorf("decorate runtime tool %s: %w", name, err)
		}
	}
	return nil
}

func (al *AgentLoop) runtimeAgentToolFactories() map[string]RuntimeAgentToolFactory {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if len(al.runtimeAgentTools) == 0 {
		return nil
	}
	factories := make(map[string]RuntimeAgentToolFactory, len(al.runtimeAgentTools))
	for name, factory := range al.runtimeAgentTools {
		factories[name] = factory
	}
	return factories
}

func sortedRuntimeAgentToolNames(factories map[string]RuntimeAgentToolFactory) []string {
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func registerRuntimeAgentToolOnRegistry(
	cfg *config.Config,
	registry *AgentRegistry,
	name string,
	factory RuntimeAgentToolFactory,
	removeMissing bool,
) error {
	mutations, err := buildRuntimeAgentToolMutations(cfg, registry, name, factory, removeMissing)
	if err != nil {
		return err
	}
	return applyAgentRuntimeToolMutations(mutations...)
}

func buildRuntimeAgentToolMutations(
	cfg *config.Config,
	registry *AgentRegistry,
	name string,
	factory RuntimeAgentToolFactory,
	removeMissing bool,
) ([]agentRuntimeToolMutation, error) {
	if registry == nil {
		return nil, nil
	}
	mutations := make([]agentRuntimeToolMutation, 0, len(registry.ListAgentIDs()))
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		instance, ok := registry.GetAgent(agentID)
		if !ok || instance == nil {
			continue
		}
		tool, err := factory(cfg, agentID)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", agentID, err)
		}
		if tool == nil {
			if removeMissing {
				mutations = append(mutations, agentRuntimeToolMutation{
					agent: instance, kind: agentRuntimeToolRemove,
					source: runtimeAgentFactoryToolContributorName(name), toolName: name,
				})
			}
			continue
		}
		if tool.Name() != name {
			return nil, fmt.Errorf("agent %s: factory returned invalid %s tool", agentID, name)
		}
		mutations = append(mutations, agentRuntimeToolMutation{
			agent: instance, kind: agentRuntimeToolPut,
			source: runtimeAgentFactoryToolContributorName(name), toolName: name, tool: tool,
		})
	}
	return mutations, nil
}

func (al *AgentLoop) runtimeToolDecoratorFactories() map[string]RuntimeToolDecoratorFactory {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if len(al.runtimeToolDecorators) == 0 {
		return nil
	}
	factories := make(map[string]RuntimeToolDecoratorFactory, len(al.runtimeToolDecorators))
	for name, factory := range al.runtimeToolDecorators {
		factories[name] = factory
	}
	return factories
}

func sortedRuntimeToolDecoratorNames(factories map[string]RuntimeToolDecoratorFactory) []string {
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decorateRuntimeToolOnRegistry(
	cfg *config.Config,
	registry *AgentRegistry,
	name string,
	factory RuntimeToolDecoratorFactory,
) error {
	if registry == nil {
		return nil
	}
	mutations := make([]agentRuntimeToolMutation, 0, len(registry.ListAgentIDs()))
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}
		local, ok := agent.Tools.Get(name)
		if !ok || local == nil {
			continue
		}
		decorated, err := factory(cfg, agentID, local)
		if err != nil {
			return fmt.Errorf("agent %s: %w", agentID, err)
		}
		if decorated == nil || sameRuntimeToolInstance(local, decorated) {
			continue
		}
		if decorated.Name() != name {
			return fmt.Errorf("agent %s: decorator returned invalid %s tool", agentID, name)
		}
		if !agentAllowsTool(agent, decorated.Name()) {
			continue
		}
		mutations = append(mutations, agentRuntimeToolMutation{
			agent: agent, kind: agentRuntimeToolReplace, toolName: name, tool: decorated,
		})
	}
	return applyAgentRuntimeToolMutations(mutations...)
}

func sameRuntimeToolInstance(left, right toolshared.Tool) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	return leftValue.IsValid() && rightValue.IsValid() &&
		leftValue.Type() == rightValue.Type() && leftValue.Kind() == reflect.Pointer &&
		leftValue.Pointer() == rightValue.Pointer()
}

func (al *AgentLoop) runtimeToolFactories() map[string]RuntimeToolFactory {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if len(al.runtimeTools) == 0 {
		return nil
	}
	factories := make(map[string]RuntimeToolFactory, len(al.runtimeTools))
	for name, factory := range al.runtimeTools {
		factories[name] = factory
	}
	return factories
}

func sortedRuntimeToolNames(factories map[string]RuntimeToolFactory) []string {
	if len(factories) == 0 {
		return nil
	}
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func registerToolOnRegistry(registry *AgentRegistry, tool toolshared.Tool) error {
	return registerOwnedToolOnRegistry(registry, tool, runtimeInjectedToolContributorName)
}

func registerRuntimeFactoryToolOnRegistry(registry *AgentRegistry, tool toolshared.Tool) error {
	return registerOwnedToolOnRegistry(registry, tool, runtimeFactoryToolContributorName)
}

func registerOwnedToolOnRegistry(
	registry *AgentRegistry,
	tool toolshared.Tool,
	contributorName func(string) string,
) error {
	if registry == nil || tool == nil {
		return nil
	}
	mutations := make([]agentRuntimeToolMutation, 0, len(registry.ListAgentIDs()))
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		if scoped, ok := tool.(tools.AgentScopedTool); ok &&
			!scoped.ToolEnabledForAgent(agentID) {
			continue
		}
		if agent, ok := registry.GetAgent(agentID); ok {
			mutations = append(mutations, agentRuntimeToolMutation{
				agent: agent, kind: agentRuntimeToolPut,
				source: contributorName(tool.Name()), toolName: tool.Name(), tool: tool,
			})
		}
	}
	return applyAgentRuntimeToolMutations(mutations...)
}

func runtimeToolReplacementMutations(
	registry *AgentRegistry,
	name string,
	tool toolshared.Tool,
) []agentRuntimeToolMutation {
	if registry == nil {
		return nil
	}
	mutations := make([]agentRuntimeToolMutation, 0, len(registry.ListAgentIDs()))
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		remove := tool == nil
		if !remove {
			if scopedTool, scoped := tool.(tools.AgentScopedTool); scoped &&
				!scopedTool.ToolEnabledForAgent(agentID) {
				remove = true
			}
		}
		if remove {
			mutations = append(mutations, agentRuntimeToolMutation{
				agent: agent, kind: agentRuntimeToolRemove,
				source: runtimeFactoryToolContributorName(name), toolName: name,
			})
			continue
		}
		mutations = append(mutations, agentRuntimeToolMutation{
			agent: agent, kind: agentRuntimeToolPut,
			source: runtimeFactoryToolContributorName(name), toolName: name, tool: tool,
		})
	}
	return mutations
}

func agentWithoutInheritedNodeFileTools(agent *AgentInstance) *AgentInstance {
	if agent == nil {
		return nil
	}
	cloned := *agent
	if agent.Tools != nil {
		cloned.Tools = agent.Tools.Clone()
		cloned.toolComposer = nil
		removeInheritedNodeFileTools(cloned.Tools)
	}
	return &cloned
}

func (al *AgentLoop) SetChannelManager(cm interfaces.ChannelManager) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.channelManager = cm
	if al.turns.currentRunner() != nil {
		al.turns.replaceRunner(newTurnRunner(al, al.cfg))
	}
}

// SetBrowserCapabilityClient replaces the browser availability boundary for
// future turns while already-admitted turns retain their runner generation.
func (al *AgentLoop) SetBrowserCapabilityClient(client runtimecap.BrowserClient) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.runtimeBrowserClient = client
	if al.turns.currentRunner() != nil {
		al.turns.replaceRunner(newTurnRunner(al, al.cfg))
	}
}

func (al *AgentLoop) GetRegistry() *AgentRegistry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.registry
}

func (al *AgentLoop) GetConfig() *config.Config {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cfg
}

func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	al.mu.Lock()
	al.mediaStore = s
	if al.turns.currentRunner() != nil {
		al.turns.replaceRunner(newTurnRunner(al, al.cfg))
	}
	al.mu.Unlock()

	// Propagate store to all registered tools that can emit media.
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.SetMediaStore(s)
		}
	}
	registry.ForEachTool("send_tts", func(t toolshared.Tool) {
		if st, ok := t.(*integrationtools.SendTTSTool); ok {
			st.SetMediaStore(s)
		}
	})
}

func (al *AgentLoop) SetTranscriber(t asr.Transcriber) {
	al.transcriber = t
}

func (al *AgentLoop) SetReloadFunc(fn func() error) {
	al.reloadFunc = fn
}

// StateManager returns the single current state owner injected or constructed
// for this runtime.
func (al *AgentLoop) StateManager() *state.Manager {
	if al == nil {
		return nil
	}
	return al.state
}

func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	registry := al.GetRegistry()
	agent := registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(registry.ListAgentIDs()),
		"ids":   registry.ListAgentIDs(),
	}

	return info
}
