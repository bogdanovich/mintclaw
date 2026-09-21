package task

import (
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
)

func TestValidPathRejectsWindowsDeviceAliases(t *testing.T) {
	for _, path := range []string{
		`\\?\C:\repo`,
		`\\?\C:\repo\nested-worktree`,
		`\\.\C:\repo`,
		`\??\C:\repo`,
		`//?/C:/repo`,
	} {
		if validPath(path) {
			t.Fatalf("validPath(%q) accepted a Windows device alias", path)
		}
	}
}

func TestMutationRejectsWindowsDOSNormalizedPathAliases(t *testing.T) {
	sourceRoot := `C:\repo`
	identity := project.ProjectIdentity{
		Kind:            project.ProjectKindGitWorktree,
		ProjectRoot:     sourceRoot,
		InvocationCWD:   sourceRoot,
		GitWorktreeRoot: sourceRoot,
		GitDir:          sourceRoot + `\.git`,
		GitCommonDir:    sourceRoot + `\.git`,
		GitBranch:       "main",
		GitHead:         strings.Repeat("a", 40),
	}
	identity.ProjectKey = project.ProjectKey(identity.Kind, identity.ProjectRoot)
	now := time.Now().UTC().UnixNano()
	record := testRecord(identity, now)
	record.Profile = TaskModeMutate
	record.WorktreeID = WorktreeIDForThread(record.ThreadID)
	record.ExecutionRoot = `C:\worktrees\task`
	record.ExecutionRootIdentity = ExecutionRootIdentity(record.ExecutionRoot)
	record.Branch = "mintclaw/task"
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	binding, err := record.WorkerBinding()
	if err != nil {
		t.Fatal(err)
	}

	for _, alias := range []string{`C:\repo.\worktree`, `C:\repo \worktree`} {
		changed := record
		changed.ExecutionRoot = alias
		changed.ExecutionRootIdentity = ExecutionRootIdentity(alias)
		if err := changed.Validate(); err == nil {
			t.Fatalf("Record.Validate() accepted DOS-normalized alias %q", alias)
		}
		changedBinding := binding
		changedBinding.ExecutionRoot = alias
		changedBinding.ExecutionRootIdentity = ExecutionRootIdentity(alias)
		if err := changedBinding.Validate(); err == nil {
			t.Fatalf("Binding.Validate() accepted DOS-normalized alias %q", alias)
		}
	}
}
