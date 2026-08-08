package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeLayoutStatePathOwnership(t *testing.T) {
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
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: " MAIN "},
		workspace,
		stateRoot,
		instructionRoots,
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	instructionRoots[0] = "changed-after-construction"
	if layout.Owner() != (RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: "main"}) {
		t.Fatalf("Owner() = %#v", layout.Owner())
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
	wants := RuntimeStatePaths{
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

func TestNewRuntimeLayoutCanonicalizesPersonalOwnerID(t *testing.T) {
	root := t.TempDir()
	tests := map[string]string{
		" MAIN ":        "main",
		"Main":          "main",
		"Support Agent": "support-agent",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			layout, err := NewRuntimeLayout(
				RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: input},
				filepath.Join(root, "workspace"),
				filepath.Join(root, "state", want),
				[]string{filepath.Join(root, "workspace")},
			)
			if err != nil {
				t.Fatalf("NewRuntimeLayout() error = %v", err)
			}
			if got := layout.Owner().ID; got != want {
				t.Fatalf("Owner().ID = %q, want %q", got, want)
			}
		})
	}
}

func TestNewRuntimeLayoutStoresCanonicalResolvedPaths(t *testing.T) {
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

	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: " thread-1 "},
		" "+relativeProject+" ",
		" "+relativeState+" ",
		[]string{" " + relativeProject + " "},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
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
	if layout.Owner().ID != "thread-1" {
		t.Fatalf("Owner().ID = %q", layout.Owner().ID)
	}
}

func TestNewRuntimeLayoutRejectsIncompleteContract(t *testing.T) {
	root := t.TempDir()
	validOwner := RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: "main"}
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "state")
	tests := []struct {
		name             string
		owner            RuntimeOwner
		executionRoot    string
		stateRoot        string
		instructionRoots []string
		wantError        string
	}{
		{
			name: "owner ID", owner: RuntimeOwner{Kind: RuntimeOwnerPersonalAgent}, executionRoot: workspace,
			stateRoot: stateRoot, instructionRoots: []string{workspace}, wantError: "owner ID",
		},
		{
			name: "owner kind", owner: RuntimeOwner{Kind: "unknown", ID: "main"}, executionRoot: workspace,
			stateRoot: stateRoot, instructionRoots: []string{workspace}, wantError: "owner kind",
		},
		{
			name: "execution root", owner: validOwner, stateRoot: stateRoot,
			instructionRoots: []string{workspace}, wantError: "execution root",
		},
		{
			name: "state root", owner: validOwner, executionRoot: workspace,
			instructionRoots: []string{workspace}, wantError: "state root",
		},
		{
			name: "instruction roots", owner: validOwner, executionRoot: workspace,
			stateRoot: stateRoot, wantError: "instruction root",
		},
		{
			name: "empty instruction root", owner: validOwner, executionRoot: workspace,
			stateRoot: stateRoot, instructionRoots: []string{" "}, wantError: "instruction root 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRuntimeLayout(
				test.owner,
				test.executionRoot,
				test.stateRoot,
				test.instructionRoots,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("NewRuntimeLayout() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestRuntimeLayoutRejectsStateInsideExecutionRoot(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, ownerKind := range []RuntimeOwnerKind{RuntimeOwnerPersonalAgent, RuntimeOwnerCodingThread} {
		for _, stateRoot := range []string{project, filepath.Join(project, ".mintclaw")} {
			_, err := NewRuntimeLayout(
				RuntimeOwner{Kind: ownerKind, ID: "owner-1"},
				project,
				stateRoot,
				[]string{project},
			)
			if err == nil || !strings.Contains(err.Error(), "outside the execution root") {
				t.Fatalf("NewRuntimeLayout() error = %v for owner %q, state root %q", err, ownerKind, stateRoot)
			}
		}
	}
}

func TestRuntimeLayoutRejectsStateThroughSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedProject := filepath.Join(root, "linked-project")
	if err := os.Symlink(project, linkedProject); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		project,
		filepath.Join(linkedProject, "state", "thread-1"),
		[]string{project},
	)
	if err == nil || !strings.Contains(err.Error(), "outside the execution root") {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
}

func TestRuntimeLayoutRejectsDanglingSymlinkAncestor(t *testing.T) {
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

	_, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		project,
		filepath.Join(danglingLink, "thread-1"),
		[]string{project},
	)
	if err == nil || !strings.Contains(err.Error(), "resolve state root") {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	if _, statErr := os.Stat(danglingTarget); !os.IsNotExist(statErr) {
		t.Fatalf("validation created dangling target unexpectedly: %v", statErr)
	}
}

func TestRuntimeLayoutAllowsExternalStateRoot(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "mintclaw-state", "threads", "thread-1")
	_, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		project,
		stateRoot,
		[]string{project},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	if _, err := os.Stat(project); !os.IsNotExist(err) {
		t.Fatalf("Validate() created or observed project unexpectedly: %v", err)
	}
	if _, err := os.Stat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("Validate() created state root unexpectedly: %v", err)
	}
}
