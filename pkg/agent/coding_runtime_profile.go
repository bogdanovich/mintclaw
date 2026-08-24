package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/state"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

// CodingRuntimeProfile is the immutable set of coding-thread layouts admitted
// before registry construction.
type CodingRuntimeProfile struct {
	agentLayouts map[string]CodingRuntimeLayout
	storeFactory CodingRuntimeStoreFactory
}

// CodingRuntimeStoreFactory opens the canonical and derived stores owned by a
// resolved coding layout. Implementations open only layout-derived paths.
type CodingRuntimeStoreFactory interface {
	NewSessionStore(layout CodingRuntimeLayout) (session.SessionStore, error)
	NewSeahorseEngine(config seahorse.Config, complete seahorse.CompleteFn) (*seahorse.Engine, error)
}

type defaultCodingRuntimeStoreFactory struct{}

func (defaultCodingRuntimeStoreFactory) NewSessionStore(layout CodingRuntimeLayout) (session.SessionStore, error) {
	return initRuntimeSessionStore(layout.StatePaths().SessionsRoot)
}

func (defaultCodingRuntimeStoreFactory) NewSeahorseEngine(
	config seahorse.Config,
	complete seahorse.CompleteFn,
) (*seahorse.Engine, error) {
	return seahorse.NewEngine(config, complete)
}

// CodingRuntimeBinding binds one configured runtime agent to a coding thread.
// The configured agent ID and thread ID are independent.
type CodingRuntimeBinding struct {
	AgentID string
	Layout  CodingRuntimeLayout
}

// NewCodingRuntimeProfile validates and indexes bindings without creating filesystem state.
func NewCodingRuntimeProfile(bindings ...CodingRuntimeBinding) (CodingRuntimeProfile, error) {
	return NewCodingRuntimeProfileWithStoreFactory(defaultCodingRuntimeStoreFactory{}, bindings...)
}

// NewCodingRuntimeProfileWithStoreFactory creates a profile with construction-time
// store injection. It is primarily useful for alternate frontends and fault
// injection; normal callers should use NewCodingRuntimeProfile.
func NewCodingRuntimeProfileWithStoreFactory(
	storeFactory CodingRuntimeStoreFactory,
	bindings ...CodingRuntimeBinding,
) (CodingRuntimeProfile, error) {
	if runtimeDependencyIsNil(storeFactory) {
		return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: store factory is required")
	}
	profile := CodingRuntimeProfile{
		agentLayouts: make(map[string]CodingRuntimeLayout, len(bindings)),
		storeFactory: storeFactory,
	}
	threadAgents := make(map[string]string, len(bindings))
	for index, binding := range bindings {
		layout := binding.Layout
		if err := layout.Validate(); err != nil {
			return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: layout %d: %w", index, err)
		}
		agentID := routing.NormalizeAgentID(binding.AgentID)
		if _, exists := profile.agentLayouts[agentID]; exists {
			return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: duplicate agent binding %q", agentID)
		}
		if existingAgent, exists := threadAgents[layout.ThreadID()]; exists {
			return CodingRuntimeProfile{}, fmt.Errorf(
				"coding runtime profile: thread %q is bound to agents %q and %q",
				layout.ThreadID(),
				existingAgent,
				agentID,
			)
		}
		profile.agentLayouts[agentID] = layout
		threadAgents[layout.ThreadID()] = agentID
	}
	if len(profile.agentLayouts) == 0 {
		return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: at least one agent binding is required")
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
				return CodingRuntimeProfile{}, fmt.Errorf(
					"coding runtime profile: compare state root for agent %q with execution root for agent %q: %w",
					routing.NormalizeAgentID(stateBinding.AgentID),
					routing.NormalizeAgentID(executionBinding.AgentID),
					err,
				)
			}
			if inside {
				return CodingRuntimeProfile{}, fmt.Errorf(
					"coding runtime profile: state root for agent %q must be outside execution root for agent %q",
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
			leftInsideRightExecution, err := runtimeLayoutPathWithin(
				left.Layout.ExecutionRoot(),
				right.Layout.ExecutionRoot(),
			)
			if err != nil {
				return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: compare execution roots: %w", err)
			}
			rightInsideLeftExecution, err := runtimeLayoutPathWithin(
				right.Layout.ExecutionRoot(),
				left.Layout.ExecutionRoot(),
			)
			if err != nil {
				return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: compare execution roots: %w", err)
			}
			if leftInsideRightExecution && rightInsideLeftExecution {
				return CodingRuntimeProfile{}, fmt.Errorf(
					"coding runtime profile: agents %q and %q cannot share an execution root",
					routing.NormalizeAgentID(left.AgentID),
					routing.NormalizeAgentID(right.AgentID),
				)
			}
			leftInsideRight, err := runtimeLayoutPathWithin(
				left.Layout.StateRoot(),
				right.Layout.StateRoot(),
			)
			if err != nil {
				return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: compare thread state roots: %w", err)
			}
			rightInsideLeft, err := runtimeLayoutPathWithin(
				right.Layout.StateRoot(),
				left.Layout.StateRoot(),
			)
			if err != nil {
				return CodingRuntimeProfile{}, fmt.Errorf("coding runtime profile: compare thread state roots: %w", err)
			}
			if leftInsideRight || rightInsideLeft {
				return CodingRuntimeProfile{}, fmt.Errorf(
					"coding runtime profile: state roots for threads %q and %q must not overlap",
					left.Layout.ThreadID(),
					right.Layout.ThreadID(),
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
func (p CodingRuntimeProfile) AgentLayout(agentID string) (CodingRuntimeLayout, bool) {
	layout, ok := p.agentLayouts[routing.NormalizeAgentID(agentID)]
	return layout, ok
}

func (al *AgentLoop) codingLayoutForWorkspace(workspace string) (CodingRuntimeLayout, bool) {
	if al == nil {
		return CodingRuntimeLayout{}, false
	}
	return codingLayoutForWorkspace(al.codingProfile, workspace)
}

func codingLayoutForWorkspace(profile *CodingRuntimeProfile, workspace string) (CodingRuntimeLayout, bool) {
	if profile == nil {
		return CodingRuntimeLayout{}, false
	}
	want := normalizeRuntimeWorkspace(workspace)
	agentIDs := make([]string, 0, len(profile.agentLayouts))
	for agentID := range profile.agentLayouts {
		agentIDs = append(agentIDs, agentID)
	}
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		layout := profile.agentLayouts[agentID]
		if normalizeRuntimeWorkspace(layout.ExecutionRoot()) == want {
			return layout, true
		}
	}
	return CodingRuntimeLayout{}, false
}

func (al *AgentLoop) usesCodingProfile() bool {
	return al != nil && al.codingProfile != nil
}

func (al *AgentLoop) codingRuntimeTargetForSession(
	sessionKey string,
) (*AgentInstance, CodingRuntimeLayout, error) {
	if !al.usesCodingProfile() {
		return nil, CodingRuntimeLayout{}, fmt.Errorf("runtime does not use the coding profile")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	for agentID, layout := range al.codingProfile.agentLayouts {
		if "coding:"+layout.ThreadID() != sessionKey {
			continue
		}
		agent, ok := al.GetRegistry().GetAgent(agentID)
		if !ok || agent == nil {
			return nil, CodingRuntimeLayout{}, fmt.Errorf(
				"coding runtime thread %q has no agent",
				layout.ThreadID(),
			)
		}
		return agent, layout, nil
	}
	return nil, CodingRuntimeLayout{}, fmt.Errorf("coding runtime session %q has no admitted thread", sessionKey)
}

func (p CodingRuntimeProfile) validateAgentIDs(agentIDs []string) error {
	configured := make(map[string]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		canonicalID := routing.NormalizeAgentID(agentID)
		if _, duplicate := configured[canonicalID]; duplicate {
			return fmt.Errorf("coding runtime profile: duplicate configured agent ID %q", canonicalID)
		}
		configured[canonicalID] = struct{}{}
		if _, ok := p.agentLayouts[canonicalID]; !ok {
			return fmt.Errorf("coding runtime profile: no layout for agent %q", canonicalID)
		}
	}
	if len(configured) != len(p.agentLayouts) {
		for agentID := range p.agentLayouts {
			if _, ok := configured[agentID]; !ok {
				return fmt.Errorf("coding runtime profile: layout for unconfigured agent %q", agentID)
			}
		}
	}
	return nil
}

func (p CodingRuntimeProfile) preflightStatePaths(agentIDs []string) error {
	refreshedBindings := make([]CodingRuntimeBinding, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		layout, ok := p.AgentLayout(agentID)
		if !ok {
			return fmt.Errorf("coding runtime profile: no layout for agent %q", routing.NormalizeAgentID(agentID))
		}
		refreshedLayout, err := NewCodingRuntimeLayout(
			layout.ThreadID(),
			layout.ExecutionRoot(),
			layout.StateRoot(),
			layout.InstructionRoots(),
		)
		if err != nil {
			return fmt.Errorf(
				"coding runtime profile: refresh layout for agent %q: %w",
				routing.NormalizeAgentID(agentID),
				err,
			)
		}
		refreshedBindings = append(refreshedBindings, CodingRuntimeBinding{
			AgentID: agentID,
			Layout:  refreshedLayout,
		})
	}
	refreshedProfile, err := NewCodingRuntimeProfileWithStoreFactory(p.storeFactory, refreshedBindings...)
	if err != nil {
		return fmt.Errorf("coding runtime profile: refresh physical root isolation: %w", err)
	}

	for _, agentID := range agentIDs {
		layout, ok := refreshedProfile.AgentLayout(agentID)
		if !ok {
			return fmt.Errorf("coding runtime profile: no layout for agent %q", routing.NormalizeAgentID(agentID))
		}
		paths := layout.StatePaths()
		for _, target := range []struct {
			name string
			path string
		}{
			{name: "sessions", path: paths.SessionsRoot},
			{name: "context", path: paths.ContextRoot},
			{name: "memory", path: paths.MemoryRoot},
			{name: "operational", path: paths.OperationalRoot},
			{name: "tool scratch", path: filepath.Join(paths.OperationalRoot, "tmp")},
		} {
			if err := preflightRuntimeDirectory(target.path); err != nil {
				return fmt.Errorf(
					"coding runtime profile: preflight %s state for agent %q: %w",
					target.name,
					routing.NormalizeAgentID(agentID),
					err,
				)
			}
		}
		if err := preflightRuntimeFile(filepath.Join(paths.ContextRoot, "seahorse.db")); err != nil {
			return fmt.Errorf(
				"coding runtime profile: preflight Seahorse state for agent %q: %w",
				routing.NormalizeAgentID(agentID),
				err,
			)
		}
		if err := preflightRuntimeOperationalFiles(paths); err != nil {
			return fmt.Errorf(
				"coding runtime profile: preflight operational state for agent %q: %w",
				routing.NormalizeAgentID(agentID),
				err,
			)
		}
	}
	return nil
}

func preflightRuntimeOperationalFiles(paths CodingRuntimeStatePaths) error {
	for _, target := range []struct {
		name string
		path string
	}{
		{name: "runtime state", path: paths.RuntimeStateFile},
		{name: "task registry", path: paths.TaskRegistryFile},
		{name: "interaction registry", path: paths.InteractionFile},
		{name: "interaction key", path: paths.InteractionKeyFile},
	} {
		if err := preflightRuntimeFile(target.path); err != nil {
			return fmt.Errorf("%s: %w", target.name, err)
		}
	}
	if _, statErr := os.Lstat(paths.RuntimeStateFile); statErr == nil {
		if _, loadErr := state.NewManagerAtChecked(paths.RuntimeStateFile); loadErr != nil {
			return fmt.Errorf("runtime state: %w", loadErr)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("runtime state: inspect file: %w", statErr)
	}
	if _, statErr := os.Lstat(paths.TaskRegistryFile); statErr == nil {
		if loadErr := taskregistry.ValidateSnapshot(paths.TaskRegistryFile); loadErr != nil {
			return fmt.Errorf("task registry: %w", loadErr)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("task registry: inspect file: %w", statErr)
	}
	if _, statErr := os.Lstat(paths.InteractionFile); statErr == nil {
		if loadErr := interactions.ValidateSnapshot(paths.InteractionFile); loadErr != nil {
			return fmt.Errorf("interaction registry: %w", loadErr)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("interaction registry: inspect file: %w", statErr)
	}
	if err := interactions.ValidateArgumentHashKey(paths.InteractionKeyFile); err != nil {
		return fmt.Errorf("interaction key: %w", err)
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
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close file %q: %w", path, err)
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
