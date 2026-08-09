// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type RuntimeToolFactory func(cfg *config.Config) (toolshared.Tool, error)

// RuntimeToolDecoratorFactory wraps one already configured agent tool. Unlike
// RuntimeToolFactory, it receives the agent-specific local implementation, so
// routing can preserve each agent's workspace and filesystem policy.
type RuntimeToolDecoratorFactory func(
	cfg *config.Config,
	agentID string,
	local toolshared.Tool,
) (toolshared.Tool, error)

func (al *AgentLoop) RegisterTool(tool toolshared.Tool) {
	if al == nil || al.hasCodingToolProfile() {
		return
	}
	registry := al.GetRegistry()
	registerToolOnRegistry(registry, tool)
}

func (al *AgentLoop) RegisterRuntimeTool(name string, factory RuntimeToolFactory) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	if al.hasCodingToolProfile() {
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

	al.mu.Lock()
	if al.runtimeTools == nil {
		al.runtimeTools = make(map[string]RuntimeToolFactory)
	}
	al.runtimeTools[name] = factory
	registry := al.registry
	al.mu.Unlock()

	registerToolOnRegistry(registry, tool)
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
	if al.hasCodingToolProfile() {
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
	al.runtimeToolDecorators[name] = factory
	registry := al.registry
	cfg := al.cfg
	al.mu.Unlock()

	return decorateRuntimeToolOnRegistry(cfg, registry, name, factory)
}

func (al *AgentLoop) registerRuntimeToolsForRegistry(cfg *config.Config, registry *AgentRegistry) error {
	factories := al.runtimeToolFactories()
	for _, name := range sortedRuntimeToolNames(factories) {
		tool, err := factories[name](cfg)
		if err != nil {
			return fmt.Errorf("register runtime tool %s: %w", name, err)
		}
		registerToolOnRegistry(registry, tool)
	}
	decorators := al.runtimeToolDecoratorFactories()
	for _, name := range sortedRuntimeToolDecoratorNames(decorators) {
		if err := decorateRuntimeToolOnRegistry(cfg, registry, name, decorators[name]); err != nil {
			return fmt.Errorf("decorate runtime tool %s: %w", name, err)
		}
	}
	return nil
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
	for _, agentID := range registry.ListAgentIDs() {
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
		registerToolIfAllowed(agent, decorated)
	}
	return nil
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

func registerToolOnRegistry(registry *AgentRegistry, tool toolshared.Tool) {
	if registry == nil || tool == nil {
		return
	}
	for _, agentID := range registry.ListAgentIDs() {
		if scoped, ok := tool.(tools.AgentScopedTool); ok &&
			!scoped.ToolEnabledForAgent(agentID) {
			continue
		}
		if agent, ok := registry.GetAgent(agentID); ok {
			registerToolIfAllowed(agent, tool)
		}
	}
}

func agentWithoutInheritedNodeFileTools(agent *AgentInstance) *AgentInstance {
	if agent == nil {
		return nil
	}
	cloned := *agent
	if agent.Tools != nil {
		cloned.Tools = agent.Tools.Clone()
		removeInheritedNodeFileTools(cloned.Tools)
	}
	return &cloned
}

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
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
	al.mediaStore = s

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
