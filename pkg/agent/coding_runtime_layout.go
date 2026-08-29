package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	codingthread "github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

// CodingRuntimeLayout separates coding-thread execution, instruction, and
// MintClaw-owned state roots.
//
// StateRoot remains outside ExecutionRoot so construction cannot place
// MintClaw-owned state in a source checkout.
type CodingRuntimeLayout struct {
	threadID         string
	executionRoot    string
	stateRoot        string
	instructionRoots []string
}

// CodingRuntimeStatePaths names the MintClaw-owned paths below a runtime state root.
type CodingRuntimeStatePaths struct {
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

// NewCodingRuntimeLayout validates and returns a side-effect-free coding layout.
func NewCodingRuntimeLayout(
	threadID string,
	executionRoot string,
	stateRoot string,
	instructionRoots []string,
) (CodingRuntimeLayout, error) {
	if strings.TrimSpace(threadID) == "" {
		return CodingRuntimeLayout{}, fmt.Errorf("coding runtime layout: thread ID is required")
	}
	if threadID != strings.TrimSpace(threadID) {
		return CodingRuntimeLayout{}, fmt.Errorf("coding runtime layout: thread ID must be trimmed")
	}

	resolvedExecutionRoot, err := resolveCodingRuntimeLayoutPath(executionRoot)
	if err != nil {
		return CodingRuntimeLayout{}, fmt.Errorf("coding runtime layout: resolve execution root: %w", err)
	}
	resolvedStateRoot, err := resolveCodingRuntimeLayoutPath(stateRoot)
	if err != nil {
		return CodingRuntimeLayout{}, fmt.Errorf("coding runtime layout: resolve state root: %w", err)
	}
	if len(instructionRoots) == 0 {
		return CodingRuntimeLayout{}, fmt.Errorf("coding runtime layout: at least one instruction root is required")
	}
	resolvedInstructionRoots := make([]string, len(instructionRoots))
	for index, root := range instructionRoots {
		resolved, resolveErr := resolveCodingRuntimeLayoutPath(root)
		if resolveErr != nil {
			return CodingRuntimeLayout{}, fmt.Errorf(
				"coding runtime layout: resolve instruction root %d: %w",
				index,
				resolveErr,
			)
		}
		resolvedInstructionRoots[index] = resolved
	}

	layout := CodingRuntimeLayout{
		threadID:         threadID,
		executionRoot:    resolvedExecutionRoot,
		stateRoot:        resolvedStateRoot,
		instructionRoots: resolvedInstructionRoots,
	}
	if err := layout.Validate(); err != nil {
		return CodingRuntimeLayout{}, err
	}
	return layout, nil
}

// ThreadID returns the stable coding thread that owns this runtime.
func (l CodingRuntimeLayout) ThreadID() string {
	return l.threadID
}

// SessionKey returns the canonical transcript identity for this admitted thread.
func (l CodingRuntimeLayout) SessionKey() string {
	return codingRuntimeSessionKey(l.threadID)
}

func codingRuntimeSessionKey(threadID string) string {
	return codingthread.SessionKey(threadID)
}

// ExecutionRoot returns the cwd/project authority for tools and subprocesses.
func (l CodingRuntimeLayout) ExecutionRoot() string {
	return l.executionRoot
}

// StateRoot returns the external root for MintClaw-owned runtime state.
func (l CodingRuntimeLayout) StateRoot() string {
	return l.stateRoot
}

// InstructionRoots returns an ordered copy of the instruction search roots.
func (l CodingRuntimeLayout) InstructionRoots() []string {
	return append([]string(nil), l.instructionRoots...)
}

// StatePaths returns the path ownership contract for state-producing runtime services.
func (l CodingRuntimeLayout) StatePaths() CodingRuntimeStatePaths {
	operationalRoot := runtimeLayoutJoin(l.stateRoot, "runtime")
	return CodingRuntimeStatePaths{
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

// Validate checks the coding-thread root invariants without creating filesystem state.
func (l CodingRuntimeLayout) Validate() error {
	if strings.TrimSpace(l.threadID) == "" {
		return fmt.Errorf("coding runtime layout: thread ID is required")
	}
	if l.threadID != strings.TrimSpace(l.threadID) {
		return fmt.Errorf("coding runtime layout: thread ID must be trimmed")
	}
	if strings.TrimSpace(l.executionRoot) == "" {
		return fmt.Errorf("coding runtime layout: execution root is required")
	}
	if strings.TrimSpace(l.stateRoot) == "" {
		return fmt.Errorf("coding runtime layout: state root is required")
	}
	if len(l.instructionRoots) == 0 {
		return fmt.Errorf("coding runtime layout: at least one instruction root is required")
	}
	for index, root := range l.instructionRoots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("coding runtime layout: instruction root %d is empty", index)
		}
	}
	stateInsideExecution, err := runtimeLayoutPathWithin(l.stateRoot, l.executionRoot)
	if err != nil {
		return fmt.Errorf("coding runtime layout: check state root containment: %w", err)
	}
	if stateInsideExecution {
		return fmt.Errorf("coding runtime layout: state root must be outside the execution root")
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

// resolveCodingRuntimeLayoutPath returns an absolute path resolved through its nearest
// existing ancestor. An existing but unresolvable ancestor fails closed.
func resolveCodingRuntimeLayoutPath(path string) (string, error) {
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
