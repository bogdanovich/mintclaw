package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

// RuntimeProfile is the immutable set of layouts admitted before registry construction.
type RuntimeProfile struct {
	agentLayouts map[string]RuntimeLayout
	storeFactory RuntimeStoreFactory
}

// RuntimeStoreFactory opens the canonical and derived stores owned by a
// resolved runtime layout. Implementations must not fall back to legacy paths.
type RuntimeStoreFactory interface {
	NewSessionStore(layout RuntimeLayout) (session.SessionStore, error)
	NewSeahorseEngine(config seahorse.Config, complete seahorse.CompleteFn) (*seahorse.Engine, error)
}

type defaultRuntimeStoreFactory struct{}

func (defaultRuntimeStoreFactory) NewSessionStore(layout RuntimeLayout) (session.SessionStore, error) {
	return initRuntimeSessionStore(layout.StatePaths().SessionsRoot)
}

func (defaultRuntimeStoreFactory) NewSeahorseEngine(
	config seahorse.Config,
	complete seahorse.CompleteFn,
) (*seahorse.Engine, error) {
	return seahorse.NewEngine(config, complete)
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
	return NewRuntimeProfileWithStoreFactory(defaultRuntimeStoreFactory{}, bindings...)
}

// NewRuntimeProfileWithStoreFactory creates a profile with construction-time
// store injection. It is primarily useful for alternate frontends and fault
// injection; normal callers should use NewRuntimeProfile.
func NewRuntimeProfileWithStoreFactory(
	storeFactory RuntimeStoreFactory,
	bindings ...RuntimeProfileBinding,
) (RuntimeProfile, error) {
	if runtimeDependencyIsNil(storeFactory) {
		return RuntimeProfile{}, fmt.Errorf("runtime profile: store factory is required")
	}
	profile := RuntimeProfile{
		agentLayouts: make(map[string]RuntimeLayout, len(bindings)),
		storeFactory: storeFactory,
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
	for leftIndex := 0; leftIndex < len(bindings); leftIndex++ {
		left := bindings[leftIndex]
		for rightIndex := leftIndex + 1; rightIndex < len(bindings); rightIndex++ {
			right := bindings[rightIndex]
			if left.Layout.Owner() == right.Layout.Owner() {
				continue
			}
			leftInsideRight, err := runtimeLayoutPathWithin(
				left.Layout.StateRoot(),
				right.Layout.StateRoot(),
			)
			if err != nil {
				return RuntimeProfile{}, fmt.Errorf("runtime profile: compare owner state roots: %w", err)
			}
			rightInsideLeft, err := runtimeLayoutPathWithin(
				right.Layout.StateRoot(),
				left.Layout.StateRoot(),
			)
			if err != nil {
				return RuntimeProfile{}, fmt.Errorf("runtime profile: compare owner state roots: %w", err)
			}
			if leftInsideRight || rightInsideLeft {
				return RuntimeProfile{}, fmt.Errorf(
					"runtime profile: state roots for distinct owners %q and %q must not overlap",
					left.Layout.Owner().ID,
					right.Layout.Owner().ID,
				)
			}
		}
	}
	return profile, nil
}

func runtimeDependencyIsNil(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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

func (p RuntimeProfile) preflightStatePaths(agentIDs []string) error {
	refreshedBindings := make([]RuntimeProfileBinding, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		layout, ok := p.AgentLayout(agentID)
		if !ok {
			return fmt.Errorf("runtime profile: no layout for agent %q", routing.NormalizeAgentID(agentID))
		}
		refreshedLayout, err := NewRuntimeLayout(
			layout.Owner(),
			layout.ExecutionRoot(),
			layout.StateRoot(),
			layout.InstructionRoots(),
		)
		if err != nil {
			return fmt.Errorf(
				"runtime profile: refresh layout for agent %q: %w",
				routing.NormalizeAgentID(agentID),
				err,
			)
		}
		refreshedBindings = append(refreshedBindings, RuntimeProfileBinding{
			AgentID: agentID,
			Layout:  refreshedLayout,
		})
	}
	refreshedProfile, err := NewRuntimeProfileWithStoreFactory(p.storeFactory, refreshedBindings...)
	if err != nil {
		return fmt.Errorf("runtime profile: refresh physical root isolation: %w", err)
	}

	for _, agentID := range agentIDs {
		layout, ok := refreshedProfile.AgentLayout(agentID)
		if !ok {
			return fmt.Errorf("runtime profile: no layout for agent %q", routing.NormalizeAgentID(agentID))
		}
		paths := layout.StatePaths()
		for _, target := range []struct {
			name string
			path string
		}{
			{name: "sessions", path: paths.SessionsRoot},
			{name: "context", path: paths.ContextRoot},
			{name: "memory", path: paths.MemoryRoot},
		} {
			if err := preflightRuntimeDirectory(target.path); err != nil {
				return fmt.Errorf(
					"runtime profile: preflight %s state for agent %q: %w",
					target.name,
					routing.NormalizeAgentID(agentID),
					err,
				)
			}
		}
		if err := preflightRuntimeFile(filepath.Join(paths.ContextRoot, "seahorse.db")); err != nil {
			return fmt.Errorf(
				"runtime profile: preflight Seahorse state for agent %q: %w",
				routing.NormalizeAgentID(agentID),
				err,
			)
		}
	}
	return nil
}

func preflightRuntimeFile(path string) error {
	entry, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect file %q: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q must not be a symbolic link", path)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", path)
	}
	return nil
}

func preflightRuntimeDirectory(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		entry, err := os.Lstat(current)
		if err == nil {
			if entry.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path %q must not be a symbolic link", current)
			}
			info, statErr := os.Stat(current)
			if statErr != nil {
				return fmt.Errorf("resolve path %q: %w", current, statErr)
			}
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", current)
			}
			return probeRuntimeDirectory(current)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect path %q: %w", current, err)
		}
		if filepath.Dir(current) == current {
			return fmt.Errorf("path %q has no existing directory ancestor", path)
		}
	}
}

func probeRuntimeDirectory(directory string) error {
	probe, err := os.MkdirTemp(directory, ".mintclaw-preflight-")
	if err != nil {
		return fmt.Errorf("verify directory %q is creatable: %w", directory, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("remove directory creatability probe %q: %w", probe, err)
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
