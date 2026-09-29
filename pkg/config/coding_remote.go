package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	remotecontract "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

const (
	MaxCodingRemoteCapabilities = 64
	MaxCodingRemoteGrants       = 64
	MaxCodingRemoteGrantItems   = 64
	MaxCodingRemoteSocketBytes  = 100
)

// CodingConfig contains local coding-frontend configuration. Remote access is
// absent and disabled by default.
type CodingConfig struct {
	Remote CodingRemoteClient `json:"remote,omitempty"`
}

// CodingRemoteClient selects one same-host gateway socket and one exact grant
// before the coding AgentLoop is constructed. Neither value is model input.
type CodingRemoteClient struct {
	Enabled    bool   `json:"enabled,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	Grant      string `json:"grant,omitempty"`
}

// GatewayCodingRemoteListener controls the owner-only local broker endpoint.
type GatewayCodingRemoteListener struct {
	Enabled    bool   `json:"enabled,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
}

// CodingRemoteCapabilityKind identifies one closed server-side adapter.
type CodingRemoteCapabilityKind string

const (
	CodingRemoteCapabilityWorkspace CodingRemoteCapabilityKind = "remote_workspace"
	CodingRemoteCapabilityNode      CodingRemoteCapabilityKind = "node_command"
	CodingRemoteCapabilityBrowser   CodingRemoteCapabilityKind = "browser_profile"
)

// CodingRemoteCapability binds one model-safe alias to an existing remote
// workspace adapter or a bounded exact node-command set.
type CodingRemoteCapability struct {
	Revision        string                     `json:"revision"`
	Kind            CodingRemoteCapabilityKind `json:"kind"`
	RemoteWorkspace string                     `json:"remote_workspace,omitempty"`
	Target          string                     `json:"target,omitempty"`
	BrowserProfile  string                     `json:"browser_profile,omitempty"`
	Operations      []string                   `json:"operations"`
}

// CodingRemoteTaskGrant binds one existing remote coding scope to the exact
// task profiles available to a local coding client.
type CodingRemoteTaskGrant struct {
	Scope    string                `json:"scope"`
	Profiles []codingscope.Profile `json:"profiles"`
}

// CodingRemoteClientGrant is the gateway-side authority for one same-user
// local coding profile. Agent selects the existing target-policy identity.
type CodingRemoteClientGrant struct {
	Revision      string                  `json:"revision"`
	Agent         string                  `json:"agent"`
	LocalProfiles []codingscope.Profile   `json:"local_profiles"`
	Capabilities  []string                `json:"capabilities,omitempty"`
	Tasks         []CodingRemoteTaskGrant `json:"tasks,omitempty"`
}

// ValidateCodingRemote validates only static, operator-owned authority. Live
// catalog, pairing, connection, and node-policy checks remain runtime gates.
func (c *Config) ValidateCodingRemote() error {
	if c == nil {
		return fmt.Errorf("coding remote config requires a root config")
	}
	listener := c.Gateway.CodingRemote
	client := c.Coding.Remote
	if (listener.Enabled || client.Enabled) && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("coding remote IPC is unsupported on %s", runtime.GOOS)
	}
	if listener.Enabled {
		if !c.Nodes.Enabled {
			return fmt.Errorf("gateway.coding_remote requires nodes.enabled")
		}
		if err := validateCodingRemoteSocketPath("gateway.coding_remote.socket_path", listener.SocketPath); err != nil {
			return err
		}
	}
	if client.Enabled {
		if err := validateCodingRemoteSocketPath("coding.remote.socket_path", client.SocketPath); err != nil {
			return err
		}
		if !remotecontract.ValidAlias(client.Grant) {
			return fmt.Errorf("coding.remote.grant is invalid")
		}
		if _, exists := c.Execution.CodingRemoteGrants[client.Grant]; !exists {
			return fmt.Errorf("coding.remote.grant %q is not configured", client.Grant)
		}
	}
	if listener.Enabled && client.Enabled && listener.SocketPath != client.SocketPath {
		return fmt.Errorf("gateway and coding remote socket paths must match when both are enabled")
	}
	if len(c.Execution.CodingRemoteCapabilities) > MaxCodingRemoteCapabilities {
		return fmt.Errorf(
			"execution.coding_remote_capabilities exceeds the %d capability limit",
			MaxCodingRemoteCapabilities,
		)
	}
	for alias, capability := range c.Execution.CodingRemoteCapabilities {
		if err := c.validateCodingRemoteCapability(alias, capability); err != nil {
			return err
		}
	}
	if len(c.Execution.CodingRemoteGrants) > MaxCodingRemoteGrants {
		return fmt.Errorf("execution.coding_remote_grants exceeds the %d grant limit", MaxCodingRemoteGrants)
	}
	for alias, grant := range c.Execution.CodingRemoteGrants {
		if err := c.validateCodingRemoteGrant(alias, grant); err != nil {
			return err
		}
	}
	return nil
}

func validateCodingRemoteSocketPath(label, path string) error {
	if path == "" || path != strings.TrimSpace(path) || len(path) > MaxCodingRemoteSocketBytes ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s must be a clean absolute path within %d bytes", label, MaxCodingRemoteSocketBytes)
	}
	return nil
}

func (c *Config) validateCodingRemoteCapability(alias string, capability CodingRemoteCapability) error {
	if !remotecontract.ValidAlias(alias) {
		return fmt.Errorf("coding remote capability %q has an invalid alias", alias)
	}
	if !codingtask.ValidRevision(capability.Revision) {
		return fmt.Errorf("coding remote capability %q has an invalid revision", alias)
	}
	if len(capability.Operations) == 0 || len(capability.Operations) > remotecontract.MaxOperationsPerCapability {
		return fmt.Errorf("coding remote capability %q requires a bounded non-empty operation set", alias)
	}
	seen := make(map[string]struct{}, len(capability.Operations))
	for _, operation := range capability.Operations {
		if _, duplicate := seen[operation]; duplicate {
			return fmt.Errorf("coding remote capability %q contains duplicate operation %q", alias, operation)
		}
		seen[operation] = struct{}{}
	}
	switch capability.Kind {
	case CodingRemoteCapabilityWorkspace:
		if capability.Target != "" || capability.BrowserProfile != "" ||
			!remotecontract.ValidAlias(capability.RemoteWorkspace) {
			return fmt.Errorf("coding remote capability %q has an invalid remote workspace binding", alias)
		}
		workspace, exists := c.Execution.RemoteWorkspaces[capability.RemoteWorkspace]
		if !exists {
			return fmt.Errorf(
				"coding remote capability %q references unknown remote workspace %q",
				alias,
				capability.RemoteWorkspace,
			)
		}
		hasWorkspaceExec := slices.Contains(capability.Operations, "workspace_exec")
		for _, operation := range capability.Operations {
			requiredTool, supported := codingRemoteWorkspaceOperationTool(operation)
			if !supported || !slices.Contains(workspace.Tools, requiredTool) {
				return fmt.Errorf(
					"coding remote capability %q operation %q is not granted by remote workspace %q",
					alias,
					operation,
					capability.RemoteWorkspace,
				)
			}
			if codingRemoteWorkspaceJobOperation(operation) &&
				(!hasWorkspaceExec || !slices.Contains(workspace.Tools, "workspace_exec")) {
				return fmt.Errorf(
					"coding remote capability %q job operation %q requires workspace_exec in the same capability",
					alias,
					operation,
				)
			}
		}
	case CodingRemoteCapabilityNode:
		if capability.RemoteWorkspace != "" || capability.BrowserProfile != "" ||
			!validExecutionTargetName(capability.Target) {
			return fmt.Errorf("coding remote capability %q has an invalid node target binding", alias)
		}
		if _, exists := c.Execution.Targets[capability.Target]; !exists {
			return fmt.Errorf("coding remote capability %q references unknown target %q", alias, capability.Target)
		}
		for _, operation := range capability.Operations {
			if !codingRemoteNodeCommandSupported(operation) {
				return fmt.Errorf("coding remote capability %q contains invalid node command %q", alias, operation)
			}
		}
	case CodingRemoteCapabilityBrowser:
		if capability.RemoteWorkspace != "" || !remotecontract.ValidAlias(capability.Target) ||
			!remotecontract.ValidAlias(capability.BrowserProfile) {
			return fmt.Errorf("coding remote capability %q has an invalid browser profile binding", alias)
		}
		target, exists := c.Tools.Browser.Targets[capability.Target]
		if !c.Tools.Browser.Enabled || !exists || !target.Enabled ||
			target.EffectivePlacement() != BrowserPlacementNode {
			return fmt.Errorf("coding remote capability %q references an unavailable node browser target", alias)
		}
		profile, exists := target.Profiles[capability.BrowserProfile]
		if !exists || !profile.Enabled || profile.Mode == BrowserProfileAttachedUser {
			return fmt.Errorf("coding remote capability %q references an unavailable browser profile", alias)
		}
		for _, operation := range capability.Operations {
			if !codingRemoteBrowserOperationSupported(operation) {
				return fmt.Errorf("coding remote capability %q contains invalid browser operation %q", alias, operation)
			}
			if codingRemoteBrowserOperationRequiresApprovalBypass(operation) &&
				profile.ApprovalMode != BrowserApprovalNone {
				return fmt.Errorf(
					"coding remote capability %q browser operation %q requires approval_mode none",
					alias,
					operation,
				)
			}
		}
	default:
		return fmt.Errorf("coding remote capability %q has unsupported kind %q", alias, capability.Kind)
	}
	return nil
}

func codingRemoteBrowserOperationSupported(operation string) bool {
	switch operation {
	case "browser_open", "browser_status", "browser_close",
		"browser_context_list", "browser_context_open", "browser_context_select", "browser_context_close",
		"browser_observe", "browser_diagnostics", "browser_capture", "browser_act":
		return true
	default:
		return false
	}
}

func codingRemoteBrowserOperationRequiresApprovalBypass(operation string) bool {
	switch operation {
	case "browser_context_open", "browser_context_select", "browser_context_close", "browser_act":
		return true
	default:
		return false
	}
}

func codingRemoteWorkspaceOperationTool(operation string) (string, bool) {
	switch operation {
	case "read_file", "search_files", "write_file", "apply_patch", "workspace_exec":
		return operation, true
	case "job_status", "job_logs", "job_artifacts", "job_cancel":
		return "jobs", true
	default:
		return "", false
	}
}

func codingRemoteWorkspaceJobOperation(operation string) bool {
	switch operation {
	case "job_status", "job_logs", "job_artifacts", "job_cancel":
		return true
	default:
		return false
	}
}

// codingRemoteNodeCommandSupported is deliberately closed. Service status,
// logs, and actions have typed, profile-projected contracts; arbitrary node
// descriptors could expose paths, identities, or generic execution authority.
func codingRemoteNodeCommandSupported(command string) bool {
	switch command {
	case "service.status.v1", "service.logs.v1", "service.action.v1":
		return true
	default:
		return false
	}
}

func (c *Config) validateCodingRemoteGrant(alias string, grant CodingRemoteClientGrant) error {
	if !remotecontract.ValidAlias(alias) {
		return fmt.Errorf("coding remote grant %q has an invalid alias", alias)
	}
	if !codingtask.ValidRevision(grant.Revision) {
		return fmt.Errorf("coding remote grant %q has an invalid revision", alias)
	}
	policy, found := c.codingRemoteAgentPolicy(grant.Agent)
	if !found {
		return fmt.Errorf("coding remote grant %q references unknown agent %q", alias, grant.Agent)
	}
	if len(grant.LocalProfiles) == 0 || len(grant.LocalProfiles) > 2 {
		return fmt.Errorf("coding remote grant %q requires a bounded local profile set", alias)
	}
	seenProfiles := make(map[codingscope.Profile]struct{}, len(grant.LocalProfiles))
	for _, profile := range grant.LocalProfiles {
		if !remotecontract.LocalProfileAllowed(profile) {
			return fmt.Errorf("coding remote grant %q contains unsupported local profile %q", alias, profile)
		}
		if _, duplicate := seenProfiles[profile]; duplicate {
			return fmt.Errorf("coding remote grant %q contains duplicate local profile %q", alias, profile)
		}
		seenProfiles[profile] = struct{}{}
	}
	if len(grant.Capabilities) > MaxCodingRemoteGrantItems || len(grant.Tasks) > MaxCodingRemoteGrantItems ||
		len(grant.Capabilities)+len(grant.Tasks) == 0 {
		return fmt.Errorf("coding remote grant %q requires a bounded non-empty authority set", alias)
	}
	seenCapabilities := make(map[string]struct{}, len(grant.Capabilities))
	for _, capabilityAlias := range grant.Capabilities {
		capability, exists := c.Execution.CodingRemoteCapabilities[capabilityAlias]
		if !exists {
			return fmt.Errorf("coding remote grant %q references unknown capability %q", alias, capabilityAlias)
		}
		if _, duplicate := seenCapabilities[capabilityAlias]; duplicate {
			return fmt.Errorf("coding remote grant %q contains duplicate capability %q", alias, capabilityAlias)
		}
		seenCapabilities[capabilityAlias] = struct{}{}
		target := capability.Target
		switch capability.Kind {
		case CodingRemoteCapabilityWorkspace:
			target = c.Execution.RemoteWorkspaces[capability.RemoteWorkspace].Target
		case CodingRemoteCapabilityBrowser:
			browserTarget := c.Tools.Browser.Targets[capability.Target]
			target = browserTarget.NodeTarget
			profile := browserTarget.Profiles[capability.BrowserProfile]
			if !slices.Contains(c.Tools.Browser.Agents, grant.Agent) ||
				!slices.Contains(profile.AllowedAgents, grant.Agent) {
				return fmt.Errorf(
					"coding remote grant %q browser capability %q is outside agent browser policy",
					alias,
					capabilityAlias,
				)
			}
		}
		if !targetAllowedByPolicy(target, policy) {
			return fmt.Errorf(
				"coding remote grant %q capability %q is outside agent target policy",
				alias,
				capabilityAlias,
			)
		}
	}
	seenTasks := make(map[string]struct{}, len(grant.Tasks))
	for _, taskGrant := range grant.Tasks {
		if !remotecontract.ValidAlias(taskGrant.Scope) {
			return fmt.Errorf("coding remote grant %q contains an invalid task scope", alias)
		}
		remoteScope, exists := c.Execution.RemoteCodingScopes[taskGrant.Scope]
		if !exists {
			return fmt.Errorf("coding remote grant %q references unknown task scope %q", alias, taskGrant.Scope)
		}
		if _, duplicate := seenTasks[taskGrant.Scope]; duplicate {
			return fmt.Errorf("coding remote grant %q contains duplicate task scope %q", alias, taskGrant.Scope)
		}
		seenTasks[taskGrant.Scope] = struct{}{}
		if !targetAllowedByPolicy(remoteScope.Target, policy) {
			return fmt.Errorf(
				"coding remote grant %q task scope %q is outside agent target policy",
				alias,
				taskGrant.Scope,
			)
		}
		if len(taskGrant.Profiles) == 0 || len(taskGrant.Profiles) > len(remoteScope.Profiles) {
			return fmt.Errorf(
				"coding remote grant %q task scope %q requires a bounded profile set",
				alias,
				taskGrant.Scope,
			)
		}
		seenTaskProfiles := make(map[codingscope.Profile]struct{}, len(taskGrant.Profiles))
		for _, profile := range taskGrant.Profiles {
			if !profile.AdmittedInV5() || !slices.Contains(remoteScope.Profiles, profile) {
				return fmt.Errorf(
					"coding remote grant %q task scope %q contains unavailable profile %q",
					alias,
					taskGrant.Scope,
					profile,
				)
			}
			if _, duplicate := seenTaskProfiles[profile]; duplicate {
				return fmt.Errorf(
					"coding remote grant %q task scope %q contains duplicate profile %q",
					alias,
					taskGrant.Scope,
					profile,
				)
			}
			seenTaskProfiles[profile] = struct{}{}
		}
	}
	return nil
}

func (c *Config) codingRemoteAgentPolicy(agentID string) (*TargetPolicy, bool) {
	if !validExecutionTargetName(agentID) {
		return nil, false
	}
	for _, agent := range c.Agents.List {
		if agent.ID != agentID {
			continue
		}
		if agent.TargetPolicy != nil {
			return agent.TargetPolicy, true
		}
		return c.Agents.Defaults.TargetPolicy, true
	}
	return nil, false
}

func targetAllowedByPolicy(target string, policy *TargetPolicy) bool {
	return policy != nil && slices.Contains(policy.AllowedTargets, target)
}
