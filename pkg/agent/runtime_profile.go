package agent

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// RuntimeProfile is the immutable set of layouts admitted before registry construction.
//
// P0.2 deliberately keeps the profile limited to root and owner resolution.
// Store and tool factories are added by the dependent P0.3 and P0.4 packets.
type RuntimeProfile struct {
	agentLayouts map[string]RuntimeLayout
}

// RuntimeProfileBinding binds one configured runtime agent to its state owner.
// Personal agents use the same canonical ID for both. A coding frontend may
// bind its configured agent to a distinct coding-thread owner ID.
type RuntimeProfileBinding struct {
	AgentID string
	Layout  RuntimeLayout
}

// NewRuntimeProfile validates and indexes bindings without creating filesystem state.
func NewRuntimeProfile(bindings ...RuntimeProfileBinding) (RuntimeProfile, error) {
	profile := RuntimeProfile{
		agentLayouts: make(map[string]RuntimeLayout, len(bindings)),
	}
	var profileOwnerKind RuntimeOwnerKind
	for index, binding := range bindings {
		layout := binding.Layout
		if err := layout.Validate(); err != nil {
			return RuntimeProfile{}, fmt.Errorf("runtime profile: layout %d: %w", index, err)
		}
		agentID := routing.NormalizeAgentID(binding.AgentID)
		owner := layout.Owner()
		if owner.Kind == RuntimeOwnerPersonalAgent && owner.ID != agentID {
			return RuntimeProfile{}, fmt.Errorf(
				"runtime profile: personal owner %q does not match agent %q",
				owner.ID,
				agentID,
			)
		}
		if owner.Kind != RuntimeOwnerPersonalAgent && owner.Kind != RuntimeOwnerCodingThread {
			return RuntimeProfile{}, fmt.Errorf(
				"runtime profile: layout %d has unsupported owner kind %q",
				index,
				owner.Kind,
			)
		}
		if profileOwnerKind == "" {
			profileOwnerKind = owner.Kind
		} else if owner.Kind != profileOwnerKind {
			return RuntimeProfile{}, fmt.Errorf(
				"runtime profile: mixed owner kinds %q and %q are not supported",
				profileOwnerKind,
				owner.Kind,
			)
		}
		if _, exists := profile.agentLayouts[agentID]; exists {
			return RuntimeProfile{}, fmt.Errorf("runtime profile: duplicate agent binding %q", agentID)
		}
		profile.agentLayouts[agentID] = layout
	}
	if len(profile.agentLayouts) == 0 {
		return RuntimeProfile{}, fmt.Errorf("runtime profile: at least one agent binding is required")
	}
	for stateIndex, stateBinding := range bindings {
		for executionIndex, executionBinding := range bindings {
			if stateIndex == executionIndex {
				continue
			}
			inside, err := runtimeLayoutPathWithin(
				stateBinding.Layout.StateRoot(),
				executionBinding.Layout.ExecutionRoot(),
			)
			if err != nil {
				return RuntimeProfile{}, fmt.Errorf(
					"runtime profile: compare state root for agent %q with execution root for agent %q: %w",
					routing.NormalizeAgentID(stateBinding.AgentID),
					routing.NormalizeAgentID(executionBinding.AgentID),
					err,
				)
			}
			if inside {
				return RuntimeProfile{}, fmt.Errorf(
					"runtime profile: state root for agent %q must be outside execution root for agent %q",
					routing.NormalizeAgentID(stateBinding.AgentID),
					routing.NormalizeAgentID(executionBinding.AgentID),
				)
			}
		}
	}
	return profile, nil
}

// AgentLayout returns the layout bound to a canonical configured agent ID.
func (p RuntimeProfile) AgentLayout(agentID string) (RuntimeLayout, bool) {
	layout, ok := p.agentLayouts[routing.NormalizeAgentID(agentID)]
	return layout, ok
}

func (p RuntimeProfile) validateAgentIDs(agentIDs []string) error {
	configured := make(map[string]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		canonicalID := routing.NormalizeAgentID(agentID)
		if _, duplicate := configured[canonicalID]; duplicate {
			return fmt.Errorf("runtime profile: duplicate configured agent ID %q", canonicalID)
		}
		configured[canonicalID] = struct{}{}
		if _, ok := p.agentLayouts[canonicalID]; !ok {
			return fmt.Errorf("runtime profile: no layout for agent %q", canonicalID)
		}
	}
	if len(configured) != len(p.agentLayouts) {
		for agentID := range p.agentLayouts {
			if _, ok := configured[agentID]; !ok {
				return fmt.Errorf("runtime profile: layout for unconfigured agent %q", agentID)
			}
		}
	}
	return nil
}

func (p RuntimeProfile) hasCodingOwner() bool {
	for _, layout := range p.agentLayouts {
		if layout.Owner().Kind == RuntimeOwnerCodingThread {
			return true
		}
	}
	return false
}
