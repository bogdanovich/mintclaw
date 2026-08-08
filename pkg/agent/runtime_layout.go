package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeOwnerKind identifies the domain that owns a runtime layout.
type RuntimeOwnerKind string

const (
	// RuntimeOwnerPersonalAgent identifies an always-on personal agent.
	RuntimeOwnerPersonalAgent RuntimeOwnerKind = "personal_agent"
	// RuntimeOwnerCodingThread identifies a project-aware coding thread.
	RuntimeOwnerCodingThread RuntimeOwnerKind = "coding_thread"
)

// RuntimeOwner is the stable identity used for runtime state ownership.
type RuntimeOwner struct {
	Kind RuntimeOwnerKind
	ID   string
}

// RuntimeLayout separates the roots historically represented by AgentInstance.Workspace.
//
// Every runtime keeps StateRoot outside ExecutionRoot so construction cannot
// place MintClaw-owned state in a personal workspace or source checkout.
type RuntimeLayout struct {
	owner            RuntimeOwner
	executionRoot    string
	stateRoot        string
	instructionRoots []string
}

// RuntimeStatePaths names the MintClaw-owned paths below a runtime state root.
type RuntimeStatePaths struct {
	SessionsRoot       string
	ContextRoot        string
	MemoryRoot         string
	OperationalRoot    string
	RuntimeStateFile   string
	TaskRegistryFile   string
	InteractionFile    string
	InteractionKeyFile string
	DiagnosticsRoot    string
	MediaRoot          string
}

// NewRuntimeLayout validates and returns a side-effect-free runtime layout.
func NewRuntimeLayout(
	owner RuntimeOwner,
	executionRoot string,
	stateRoot string,
	instructionRoots []string,
) (RuntimeLayout, error) {
	layout := RuntimeLayout{
		owner:            owner,
		executionRoot:    executionRoot,
		stateRoot:        stateRoot,
		instructionRoots: append([]string(nil), instructionRoots...),
	}
	if err := layout.Validate(); err != nil {
		return RuntimeLayout{}, err
	}
	return layout, nil
}

// Owner returns the stable owner of this runtime.
func (l RuntimeLayout) Owner() RuntimeOwner {
	return l.owner
}

// ExecutionRoot returns the cwd/project authority for tools and subprocesses.
func (l RuntimeLayout) ExecutionRoot() string {
	return l.executionRoot
}

// StateRoot returns the external root for MintClaw-owned runtime state.
func (l RuntimeLayout) StateRoot() string {
	return l.stateRoot
}

// InstructionRoots returns an ordered copy of the instruction search roots.
func (l RuntimeLayout) InstructionRoots() []string {
	return append([]string(nil), l.instructionRoots...)
}

// StatePaths returns the path ownership contract for state-producing runtime services.
func (l RuntimeLayout) StatePaths() RuntimeStatePaths {
	operationalRoot := runtimeLayoutJoin(l.stateRoot, "runtime")
	return RuntimeStatePaths{
		SessionsRoot:       runtimeLayoutJoin(l.stateRoot, "sessions"),
		ContextRoot:        runtimeLayoutJoin(l.stateRoot, "context"),
		MemoryRoot:         runtimeLayoutJoin(l.stateRoot, "memory"),
		OperationalRoot:    operationalRoot,
		RuntimeStateFile:   runtimeLayoutJoin(operationalRoot, "state.json"),
		TaskRegistryFile:   runtimeLayoutJoin(operationalRoot, "task_registry.json"),
		InteractionFile:    runtimeLayoutJoin(operationalRoot, "interaction_registry.json"),
		InteractionKeyFile: runtimeLayoutJoin(operationalRoot, "interaction_hmac.key"),
		DiagnosticsRoot:    runtimeLayoutJoin(l.stateRoot, "diagnostics"),
		MediaRoot:          runtimeLayoutJoin(l.stateRoot, "media"),
	}
}

// Validate checks the root and owner invariants without creating filesystem state.
func (l RuntimeLayout) Validate() error {
	if strings.TrimSpace(l.owner.ID) == "" {
		return fmt.Errorf("runtime layout: owner ID is required")
	}
	switch l.owner.Kind {
	case RuntimeOwnerPersonalAgent, RuntimeOwnerCodingThread:
	default:
		return fmt.Errorf("runtime layout: unsupported owner kind %q", l.owner.Kind)
	}
	if strings.TrimSpace(l.executionRoot) == "" {
		return fmt.Errorf("runtime layout: execution root is required")
	}
	if strings.TrimSpace(l.stateRoot) == "" {
		return fmt.Errorf("runtime layout: state root is required")
	}
	if len(l.instructionRoots) == 0 {
		return fmt.Errorf("runtime layout: at least one instruction root is required")
	}
	for index, root := range l.instructionRoots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("runtime layout: instruction root %d is empty", index)
		}
	}
	if runtimeLayoutPathWithin(l.stateRoot, l.executionRoot) {
		return fmt.Errorf("runtime layout: state root must be outside the execution root")
	}
	return nil
}

func runtimeLayoutJoin(root string, elements ...string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elements...)...)
}

func runtimeLayoutPathWithin(candidate, root string) bool {
	candidate = resolveRuntimeLayoutPath(candidate)
	root = resolveRuntimeLayoutPath(root)
	if candidate == "" || root == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

// resolveRuntimeLayoutPath resolves symlinks through the nearest existing
// ancestor so a not-yet-created state directory cannot hide under a linked
// source-checkout path.
func resolveRuntimeLayoutPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	cleaned := filepath.Clean(path)
	for current := cleaned; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			relative, relErr := filepath.Rel(current, cleaned)
			if relErr != nil || relative == "." {
				return filepath.Clean(resolved)
			}
			return filepath.Clean(filepath.Join(resolved, relative))
		}
		if !os.IsNotExist(err) {
			return cleaned
		}
		if filepath.Dir(current) == current {
			return cleaned
		}
	}
}
