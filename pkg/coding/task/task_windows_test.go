package task

import "testing"

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
