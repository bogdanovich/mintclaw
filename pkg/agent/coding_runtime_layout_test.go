package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodingRuntimeLayoutStatePathOwnership(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "state", "agents", "main")
	wantWorkspace := filepath.Join(canonicalRoot, "workspace")
	wantStateRoot := filepath.Join(canonicalRoot, "state", "agents", "main")
	instructionRoots := []string{workspace}
	layout, err := NewCodingRuntimeLayout(
		"thread-main",
		workspace,
		stateRoot,
		instructionRoots,
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	instructionRoots[0] = "changed-after-construction"
	if layout.ThreadID() != "thread-main" {
		t.Fatalf("ThreadID() = %q", layout.ThreadID())
	}
	if layout.ExecutionRoot() != wantWorkspace || layout.StateRoot() != wantStateRoot {
		t.Fatalf("roots = execution %q, state %q", layout.ExecutionRoot(), layout.StateRoot())
	}
	gotInstructionRoots := layout.InstructionRoots()
	if !reflect.DeepEqual(gotInstructionRoots, []string{wantWorkspace}) {
		t.Fatalf("InstructionRoots() = %#v", gotInstructionRoots)
	}
	gotInstructionRoots[0] = "changed-after-read"
	if !reflect.DeepEqual(layout.InstructionRoots(), []string{wantWorkspace}) {
		t.Fatalf("InstructionRoots() exposed mutable state: %#v", layout.InstructionRoots())
	}

	paths := layout.StatePaths()
	wants := CodingRuntimeStatePaths{
		SessionsRoot:       filepath.Join(wantStateRoot, "sessions"),
		ContextRoot:        filepath.Join(wantStateRoot, "context"),
		MemoryRoot:         filepath.Join(wantStateRoot, "memory"),
		OperationalRoot:    filepath.Join(wantStateRoot, "runtime"),
		RuntimeStateFile:   filepath.Join(wantStateRoot, "runtime", "state.json"),
		TaskRegistryFile:   filepath.Join(wantStateRoot, "runtime", "task_registry.json"),
		InteractionFile:    filepath.Join(wantStateRoot, "runtime", "interaction_registry.json"),
		InteractionKeyFile: filepath.Join(wantStateRoot, "runtime", "interaction_hmac.key"),
		DiagnosticsRoot:    filepath.Join(wantStateRoot, "diagnostics"),
		MediaRoot:          filepath.Join(wantStateRoot, "media"),
	}
	if !reflect.DeepEqual(paths, wants) {
		t.Fatalf("StatePaths() = %#v, want %#v", paths, wants)
	}
}

func TestNewCodingRuntimeLayoutStoresCanonicalResolvedPaths(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	projectLink := filepath.Join(root, "project-link")
	stateLink := filepath.Join(root, "state-link")
	if err := os.Symlink(project, projectLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(stateRoot, stateLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeProject, err := filepath.Rel(workingDirectory, projectLink)
	if err != nil {
		t.Fatal(err)
	}
	relativeState, err := filepath.Rel(workingDirectory, stateLink)
	if err != nil {
		t.Fatal(err)
	}

	layout, err := NewCodingRuntimeLayout(
		"thread-1",
		" "+relativeProject+" ",
		" "+relativeState+" ",
		[]string{" " + relativeProject + " "},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	wantProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	wantState, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if layout.ExecutionRoot() != wantProject || layout.StateRoot() != wantState {
		t.Fatalf("roots = execution %q, state %q", layout.ExecutionRoot(), layout.StateRoot())
	}
	if !reflect.DeepEqual(layout.InstructionRoots(), []string{wantProject}) {
		t.Fatalf("InstructionRoots() = %#v", layout.InstructionRoots())
	}
	if layout.ThreadID() != "thread-1" {
		t.Fatalf("ThreadID() = %q", layout.ThreadID())
	}
}

func TestNewCodingRuntimeLayoutRejectsIncompleteContract(t *testing.T) {
	root := t.TempDir()
	validThreadID := "thread-main"
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "state")
	tests := []struct {
		name             string
		threadID         string
		executionRoot    string
		stateRoot        string
		instructionRoots []string
		wantError        string
	}{
		{
			name: "thread ID", executionRoot: workspace, stateRoot: stateRoot,
			instructionRoots: []string{workspace}, wantError: "thread ID",
		},
		{
			name: "trimmed thread ID", threadID: " thread-main ", executionRoot: workspace,
			stateRoot: stateRoot, instructionRoots: []string{workspace}, wantError: "must be trimmed",
		},
		{
			name: "execution root", threadID: validThreadID, stateRoot: stateRoot,
			instructionRoots: []string{workspace}, wantError: "execution root",
		},
		{
			name: "state root", threadID: validThreadID, executionRoot: workspace,
			instructionRoots: []string{workspace}, wantError: "state root",
		},
		{
			name: "instruction roots", threadID: validThreadID, executionRoot: workspace,
			stateRoot: stateRoot, wantError: "instruction root",
		},
		{
			name: "empty instruction root", threadID: validThreadID, executionRoot: workspace,
			stateRoot: stateRoot, instructionRoots: []string{" "}, wantError: "instruction root 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewCodingRuntimeLayout(
				test.threadID,
				test.executionRoot,
				test.stateRoot,
				test.instructionRoots,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("NewCodingRuntimeLayout() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestCodingRuntimeLayoutRejectsStateInsideExecutionRoot(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, stateRoot := range []string{project, filepath.Join(project, ".mintclaw")} {
		_, err := NewCodingRuntimeLayout(
			"thread-1",
			project,
			stateRoot,
			[]string{project},
		)
		if err == nil || !strings.Contains(err.Error(), "outside the execution root") {
			t.Fatalf("NewCodingRuntimeLayout() error = %v for state root %q", err, stateRoot)
		}
	}
}

func TestCodingRuntimeLayoutRejectsCaseAliasOnCaseInsensitiveFilesystem(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "Project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	caseAlias := filepath.Join(root, "pROJECT")
	projectInfo, err := os.Stat(project)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(caseAlias)
	if os.IsNotExist(err) {
		t.Skip("test filesystem is case-sensitive")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(projectInfo, aliasInfo) {
		t.Skip("case alias resolves to a different directory")
	}

	_, err = NewCodingRuntimeLayout(
		"thread-1",
		project,
		filepath.Join(caseAlias, ".mintclaw", "thread-1"),
		[]string{project},
	)
	if err == nil || !strings.Contains(err.Error(), "outside the execution root") {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
}

func TestCodingRuntimeLayoutRejectsStateThroughSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedProject := filepath.Join(root, "linked-project")
	if err := os.Symlink(project, linkedProject); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := NewCodingRuntimeLayout(
		"thread-1",
		project,
		filepath.Join(linkedProject, "state", "thread-1"),
		[]string{project},
	)
	if err == nil || !strings.Contains(err.Error(), "outside the execution root") {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
}

func TestCodingRuntimeLayoutRejectsDanglingSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	danglingTarget := filepath.Join(project, "future-state")
	danglingLink := filepath.Join(root, "dangling-state")
	if err := os.Symlink(danglingTarget, danglingLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := NewCodingRuntimeLayout(
		"thread-1",
		project,
		filepath.Join(danglingLink, "thread-1"),
		[]string{project},
	)
	if err == nil || !strings.Contains(err.Error(), "resolve state root") {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	if _, statErr := os.Stat(danglingTarget); !os.IsNotExist(statErr) {
		t.Fatalf("validation created dangling target unexpectedly: %v", statErr)
	}
}

func TestCodingRuntimeLayoutAllowsExternalStateRoot(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "mintclaw-state", "threads", "thread-1")
	_, err := NewCodingRuntimeLayout(
		"thread-1",
		project,
		stateRoot,
		[]string{project},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	if _, err := os.Stat(project); !os.IsNotExist(err) {
		t.Fatalf("Validate() created or observed project unexpectedly: %v", err)
	}
	if _, err := os.Stat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("Validate() created state root unexpectedly: %v", err)
	}
}
