package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/routing"
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
	ownerID := strings.TrimSpace(owner.ID)
	if ownerID == "" {
		return RuntimeLayout{}, fmt.Errorf("runtime layout: owner ID is required")
	}
	switch owner.Kind {
	case RuntimeOwnerPersonalAgent:
		owner.ID = routing.NormalizeAgentID(ownerID)
	case RuntimeOwnerCodingThread:
		owner.ID = ownerID
	default:
		return RuntimeLayout{}, fmt.Errorf("runtime layout: unsupported owner kind %q", owner.Kind)
	}

	resolvedExecutionRoot, err := resolveRuntimeLayoutPath(executionRoot)
	if err != nil {
		return RuntimeLayout{}, fmt.Errorf("runtime layout: resolve execution root: %w", err)
	}
	resolvedStateRoot, err := resolveRuntimeLayoutPath(stateRoot)
	if err != nil {
		return RuntimeLayout{}, fmt.Errorf("runtime layout: resolve state root: %w", err)
	}
	if len(instructionRoots) == 0 {
		return RuntimeLayout{}, fmt.Errorf("runtime layout: at least one instruction root is required")
	}
	resolvedInstructionRoots := make([]string, len(instructionRoots))
	for index, root := range instructionRoots {
		resolved, resolveErr := resolveRuntimeLayoutPath(root)
		if resolveErr != nil {
			return RuntimeLayout{}, fmt.Errorf(
				"runtime layout: resolve instruction root %d: %w",
				index,
				resolveErr,
			)
		}
		resolvedInstructionRoots[index] = resolved
	}

	layout := RuntimeLayout{
		owner:            owner,
		executionRoot:    resolvedExecutionRoot,
		stateRoot:        resolvedStateRoot,
		instructionRoots: resolvedInstructionRoots,
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
	case RuntimeOwnerPersonalAgent:
		if l.owner.ID != routing.NormalizeAgentID(l.owner.ID) {
			return fmt.Errorf("runtime layout: personal owner ID must be canonical")
		}
	case RuntimeOwnerCodingThread:
		if l.owner.ID != strings.TrimSpace(l.owner.ID) {
			return fmt.Errorf("runtime layout: coding owner ID must be trimmed")
		}
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
	stateInsideExecution, err := runtimeLayoutPathWithin(l.stateRoot, l.executionRoot)
	if err != nil {
		return fmt.Errorf("runtime layout: check state root containment: %w", err)
	}
	if stateInsideExecution {
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

func runtimeLayoutPathWithin(candidate, root string) (bool, error) {
	if candidate == "" || root == "" {
		return false, nil
	}
	relative, err := filepath.Rel(root, candidate)
	if err == nil && (relative == "." || filepath.IsLocal(relative)) {
		return true, nil
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect execution root %q: %w", root, err)
	}
	for current := candidate; ; current = filepath.Dir(current) {
		candidateInfo, statErr := os.Stat(current)
		if statErr == nil {
			if os.SameFile(candidateInfo, rootInfo) {
				return true, nil
			}
		} else if !os.IsNotExist(statErr) {
			return false, fmt.Errorf("inspect state root ancestor %q: %w", current, statErr)
		}
		if filepath.Dir(current) == current {
			return false, nil
		}
	}
}

// resolveRuntimeLayoutPath returns an absolute path resolved through its nearest
// existing ancestor. An existing but unresolvable ancestor fails closed.
func resolveRuntimeLayoutPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	cleaned := filepath.Clean(absolute)
	for current := cleaned; ; current = filepath.Dir(current) {
		_, lstatErr := os.Lstat(current)
		if lstatErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve existing ancestor %q: %w", current, resolveErr)
			}
			relative, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", fmt.Errorf("resolve path relative to ancestor %q: %w", current, relErr)
			}
			if relative == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, relative)), nil
		}
		if !os.IsNotExist(lstatErr) {
			return "", fmt.Errorf("inspect path ancestor %q: %w", current, lstatErr)
		}
		if filepath.Dir(current) == current {
			return "", fmt.Errorf("path has no resolvable ancestor: %q", cleaned)
		}
	}
}
