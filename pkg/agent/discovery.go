package agent

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// AgentDescriptor is the structured discovery payload injected into each
// agent's system prompt so the LLM can choose a peer by identity.
type AgentDescriptor struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListAgents returns structured descriptors for every agent in the current
// MintClaw instance. The current workspace, when provided, is used only to
// order the matching agent first for prompt readability.
func (r *AgentRegistry) ListAgents(workspace string) []AgentDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	selfWorkspace := cleanWorkspacePath(workspace)
	descriptors := make([]AgentDescriptor, 0, len(ids))
	for _, id := range ids {
		agent := r.agents[id]
		if agent == nil {
			continue
		}
		descriptors = append(descriptors, r.buildAgentDescriptorLocked(agent))
	}

	if selfWorkspace == "" {
		return descriptors
	}

	slices.SortStableFunc(descriptors, func(a, b AgentDescriptor) int {
		leftSelf := cleanWorkspacePath(
			r.workspaceForAgentIDLocked(a.ID),
		) == selfWorkspace
		rightSelf := cleanWorkspacePath(
			r.workspaceForAgentIDLocked(b.ID),
		) == selfWorkspace
		if leftSelf != rightSelf {
			if leftSelf {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})

	return descriptors
}

// ListSpawnableAgents returns descriptors only when the current agent can call
// spawn, and only for peers it is allowed to spawn. Restricted peers are
// intentionally omitted from discovery.
func (r *AgentRegistry) ListSpawnableAgents(agentID string) []AgentDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	parentID := routing.NormalizeAgentID(agentID)
	parent, ok := r.agents[parentID]
	if !ok || parent == nil {
		return nil
	}
	if !agentHasSpawnTool(parent) {
		return nil
	}

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		if id == parentID {
			continue
		}
		if !agentAllowsSubagent(parent, id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	descriptors := make([]AgentDescriptor, 0, len(ids))
	for _, id := range ids {
		agent := r.agents[id]
		if agent == nil {
			continue
		}
		descriptors = append(descriptors, r.buildAgentDescriptorLocked(agent))
	}
	return descriptors
}

// GetAgentDescriptor returns the structured discovery payload for one agent.
func (r *AgentRegistry) GetAgentDescriptor(agentID string) (*AgentDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := routing.NormalizeAgentID(agentID)
	agent, ok := r.agents[id]
	if !ok || agent == nil {
		return nil, false
	}

	descriptor := r.buildAgentDescriptorLocked(agent)
	return &descriptor, true
}

func (r *AgentRegistry) buildAgentDescriptorLocked(agent *AgentInstance) AgentDescriptor {
	definition := loadAgentDefinition(agent.Workspace)
	name, description := descriptorIdentity(agent.ID, definition)

	return AgentDescriptor{
		ID:          agent.ID,
		Name:        name,
		Description: description,
	}
}

func descriptorIdentity(agentID string, definition AgentContextDefinition) (string, string) {
	name := agentID
	description := ""
	if definition.Agent != nil {
		if trimmed := strings.TrimSpace(definition.Agent.Frontmatter.Name); trimmed != "" {
			name = trimmed
		}
		if trimmed := strings.TrimSpace(definition.Agent.Frontmatter.Description); trimmed != "" {
			description = trimmed
		}
	}

	if description == "" && definition.Agent != nil {
		description = firstNonEmptyLine(definition.Agent.Body)
	}

	return name, description
}

func firstNonEmptyLine(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (r *AgentRegistry) workspaceForAgentIDLocked(agentID string) string {
	agent, ok := r.agents[routing.NormalizeAgentID(agentID)]
	if !ok || agent == nil {
		return ""
	}
	return agent.Workspace
}

func (r *AgentRegistry) defaultAgentIDLocked() string {
	if configured := configuredDefaultAgent(r.cfg); configured != nil {
		id := routing.NormalizeAgentID(configured.ID)
		if _, ok := r.agents[id]; ok {
			return id
		}
	}
	if _, ok := r.agents[routing.DefaultAgentID]; ok {
		return routing.DefaultAgentID
	}
	for id := range r.agents {
		return id
	}
	return ""
}

func configuredDefaultAgent(cfg *config.Config) *config.AgentConfig {
	if cfg == nil || len(cfg.Agents.List) == 0 {
		return nil
	}
	for index := range cfg.Agents.List {
		if routing.NormalizeAgentID(cfg.Agents.List[index].ID) == routing.DefaultAgentID {
			return &cfg.Agents.List[index]
		}
	}
	for index := range cfg.Agents.List {
		if cfg.Agents.List[index].Default {
			return &cfg.Agents.List[index]
		}
	}
	return &cfg.Agents.List[0]
}

func cleanWorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func formatAgentDiscoverySection(agents []AgentDescriptor) string {
	if len(agents) == 0 {
		return ""
	}

	payload := struct {
		Agents []AgentDescriptor `json:"agents"`
	}{
		Agents: agents,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}

	var header strings.Builder
	header.WriteString("# Agent Discovery\n\n")
	header.WriteString("This registry lists the peer agents this agent is permitted to spawn.\n")
	header.WriteString(
		"Choose a peer based on its description. Use only agent IDs listed here when calling spawn.\n\n",
	)
	header.WriteString("```json\n")
	header.Write(encoded)
	header.WriteString("\n```")

	return header.String()
}
