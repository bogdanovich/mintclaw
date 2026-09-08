package skills

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const managedSkillMarkdown = "---\nname: test-skill\ndescription: Managed skill\n---\n\n# Managed Skill\n"

func TestWorkspaceSkillInventoryInspectsLocalAndThirdPartySkills(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	targetDir := writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	writeManagedFile(t, targetDir, "references/guide.md", "guide", 0o644)
	inventory := NewWorkspaceSkillInventory(workspace)

	local, err := inventory.Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect(local) error = %v", err)
	}
	if !local.Valid || local.OriginKind != ManagedSkillOriginLocal || local.Origin != nil ||
		local.Description != "Managed skill" || local.RelativePath != "skills/test-skill" ||
		local.FileCount != 2 || local.TotalBytes <= 0 || !strings.HasPrefix(local.Revision, "sha256:") {
		t.Fatalf("local inspection = %#v", local)
	}

	if err := WriteInstalledSkillOrigin(
		targetDir,
		originTestRegistry{},
		"owner/repo/skills/test-skill",
		"main",
	); err != nil {
		t.Fatalf("WriteInstalledSkillOrigin() error = %v", err)
	}
	installed, err := inventory.Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect(installed) error = %v", err)
	}
	if !installed.Valid || installed.OriginKind != ManagedSkillOriginThirdParty || installed.Origin == nil ||
		installed.Origin.Registry != "github" || installed.Revision == local.Revision || installed.FileCount != 3 {
		t.Fatalf("installed inspection = %#v", installed)
	}
}

func TestWorkspaceSkillInventoryRevisionIsOrderIndependent(t *testing.T) {
	firstWorkspace := canonicalInventoryTempDir(t)
	firstDir := writeManagedSkillFixture(t, firstWorkspace, "test-skill", managedSkillMarkdown)
	writeManagedFile(t, firstDir, "scripts/z.sh", "z", 0o755)
	writeManagedFile(t, firstDir, "references/a.md", "a", 0o644)

	secondWorkspace := canonicalInventoryTempDir(t)
	secondDir := writeManagedSkillFixture(t, secondWorkspace, "test-skill", managedSkillMarkdown)
	writeManagedFile(t, secondDir, "references/a.md", "a", 0o644)
	writeManagedFile(t, secondDir, "scripts/z.sh", "z", 0o755)

	first, err := NewWorkspaceSkillInventory(firstWorkspace).Inspect("test-skill")
	if err != nil || !first.Valid {
		t.Fatalf("first inspection = (%#v, %v)", first, err)
	}
	second, err := NewWorkspaceSkillInventory(secondWorkspace).Inspect("test-skill")
	if err != nil || !second.Valid {
		t.Fatalf("second inspection = (%#v, %v)", second, err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("order-dependent revisions: %q != %q", first.Revision, second.Revision)
	}
}

func TestWorkspaceSkillInventoryRevisionTracksContentAndMode(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	targetDir := writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	scriptPath := writeManagedFile(t, targetDir, "scripts/check.sh", "first", 0o644)
	inventory := NewWorkspaceSkillInventory(workspace)

	initial := mustInspectManagedSkill(t, inventory, "test-skill")
	if err := os.WriteFile(scriptPath, []byte("second"), 0o644); err != nil {
		t.Fatalf("replace script: %v", err)
	}
	contentChanged := mustInspectManagedSkill(t, inventory, "test-skill")
	if contentChanged.Revision == initial.Revision {
		t.Fatal("content change did not change managed skill revision")
	}

	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatalf("chmod script: %v", err)
	}
	modeChanged := mustInspectManagedSkill(t, inventory, "test-skill")
	if modeChanged.Revision == contentChanged.Revision {
		t.Fatal("mode change did not change managed skill revision")
	}

	if err := os.Chmod(scriptPath, 0o755|os.ModeSetuid); err != nil {
		t.Skipf("special permission bits unavailable: %v", err)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat special-mode script: %v", err)
	}
	if info.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem did not retain the setuid mode bit")
	}
	specialModeChanged := mustInspectManagedSkill(t, inventory, "test-skill")
	if specialModeChanged.Revision == modeChanged.Revision {
		t.Fatal("special permission bit change did not change managed skill revision")
	}
}

func TestWorkspaceSkillInventoryRejectsSymlinks(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	targetDir := writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(targetDir, "linked.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	managed, err := NewWorkspaceSkillInventory(workspace).Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if managed.Valid || !strings.Contains(managed.ValidationErr, "symlink") {
		t.Fatalf("symlink inspection = %#v", managed)
	}
}

func TestWorkspaceSkillInventoryRejectsSymlinkRoot(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	realRoot := t.TempDir()
	if err := os.Symlink(realRoot, filepath.Join(workspace, "skills")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	inventory := NewWorkspaceSkillInventory(workspace)
	if err := inventory.ValidateRoot(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("ValidateRoot() error = %v", err)
	}
	if _, err := inventory.List(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("List() error = %v", err)
	}
}

func TestWorkspaceSkillInventoryRejectsSymlinkWorkspaceAncestor(t *testing.T) {
	safeParent := canonicalInventoryTempDir(t)
	realParent := canonicalInventoryTempDir(t)
	realWorkspace := filepath.Join(realParent, "workspace")
	if err := os.Mkdir(realWorkspace, 0o755); err != nil {
		t.Fatalf("create real workspace: %v", err)
	}
	linkedParent := filepath.Join(safeParent, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	inventory := NewWorkspaceSkillInventory(filepath.Join(linkedParent, "workspace"))
	if err := inventory.ValidateRoot(); err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("ValidateRoot() error = %v", err)
	}
}

func TestWorkspaceSkillInventoryExposesMalformedOrigin(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	targetDir := writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	writeManagedFile(t, targetDir, OriginMetadataFilename, `{"version":1}`, 0o600)

	managed, err := NewWorkspaceSkillInventory(workspace).Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if managed.Valid || managed.OriginKind != ManagedSkillOriginMalformed || managed.ValidationErr == "" {
		t.Fatalf("malformed-origin inspection = %#v", managed)
	}
}

func TestWorkspaceSkillInventoryEnforcesBounds(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	targetDir := writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	writeManagedFile(t, targetDir, "extra.txt", "extra", 0o600)
	limits := DefaultManagedSkillLimits()
	limits.MaxFiles = 1

	managed, err := NewWorkspaceSkillInventoryWithLimits(workspace, limits).Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if managed.Valid || !strings.Contains(managed.ValidationErr, "exceeds 1 files") {
		t.Fatalf("bounded inspection = %#v", managed)
	}
}

func TestWorkspaceSkillInventoryListsInvalidCanonicalEntries(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	badDir := filepath.Join(workspace, "skills", "bad_name")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("create invalid skill directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "skills", ".internal"), 0o755); err != nil {
		t.Fatalf("create internal directory: %v", err)
	}

	managed, err := NewWorkspaceSkillInventory(workspace).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(managed) != 2 || managed[0].Name != "bad_name" || managed[0].Valid ||
		managed[1].Name != "test-skill" || !managed[1].Valid {
		t.Fatalf("managed list = %#v", managed)
	}
}

func TestWorkspaceSkillInventoryRejectsMetadataNameMismatch(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	writeManagedSkillFixture(
		t,
		workspace,
		"test-skill",
		"---\nname: different-skill\ndescription: Mismatch\n---\n# Mismatch\n",
	)

	managed, err := NewWorkspaceSkillInventory(workspace).Inspect("test-skill")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if managed.Valid || !strings.Contains(managed.ValidationErr, "does not match") {
		t.Fatalf("mismatched inspection = %#v", managed)
	}
}

func TestWorkspaceSkillInventoryDistinguishesAbsentSkill(t *testing.T) {
	_, err := NewWorkspaceSkillInventory(canonicalInventoryTempDir(t)).Inspect("missing-skill")
	if !errors.Is(err, ErrManagedSkillNotFound) {
		t.Fatalf("Inspect() error = %v, want ErrManagedSkillNotFound", err)
	}
}

func TestWorkspaceSkillInventorySupportsConcurrentIndependentReads(t *testing.T) {
	workspace := canonicalInventoryTempDir(t)
	writeManagedSkillFixture(t, workspace, "test-skill", managedSkillMarkdown)
	inventory := NewWorkspaceSkillInventory(workspace)
	want := mustInspectManagedSkill(t, inventory, "test-skill").Revision

	const readers = 24
	errorsCh := make(chan error, readers)
	var wait sync.WaitGroup
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			managed, err := inventory.Inspect("test-skill")
			if err != nil {
				errorsCh <- err
				return
			}
			if !managed.Valid || managed.Revision != want {
				errorsCh <- errors.New("concurrent inspection returned inconsistent revision")
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
}

func writeManagedSkillFixture(t *testing.T, workspace, name, markdown string) string {
	t.Helper()
	targetDir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("create managed skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(markdown), 0o644); err != nil {
		t.Fatalf("write managed skill: %v", err)
	}
	return targetDir
}

func writeManagedFile(t *testing.T, root, relative, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create managed file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write managed file: %v", err)
	}
	return path
}

func mustInspectManagedSkill(
	t *testing.T,
	inventory *WorkspaceSkillInventory,
	name string,
) ManagedSkill {
	t.Helper()
	managed, err := inventory.Inspect(name)
	if err != nil || !managed.Valid {
		t.Fatalf("Inspect(%q) = (%#v, %v)", name, managed, err)
	}
	return managed
}

func canonicalInventoryTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return resolved
}
