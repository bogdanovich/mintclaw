package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPersonalContextUsesScopedProseFiles(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENTS.md":   "# Instructions\nUse current workspace guidance.",
		"AGENT.md":    "# Removed manifest\nDo not load this file.",
		"IDENTITY.md": "# Removed identity\nDo not load this file either.",
		"SOUL.md":     "# Soul\nStay precise.",
		"USER.md":     "# User\nWorkspace preferences.",
	})
	defer cleanupWorkspace(t, workspace)

	context := NewContextBuilder(workspace).loadPersonalContext()
	if context.Instructions == nil || !strings.Contains(context.Instructions.Content, "current workspace guidance") {
		t.Fatalf("instructions = %#v", context.Instructions)
	}
	if context.Instructions.Path != filepath.Join(workspace, "AGENTS.md") {
		t.Fatalf("instructions path = %q", context.Instructions.Path)
	}
	if context.Soul == nil || !strings.Contains(context.Soul.Content, "Stay precise") {
		t.Fatalf("soul = %#v", context.Soul)
	}
	if context.User == nil || !strings.Contains(context.User.Content, "Workspace preferences") {
		t.Fatalf("user = %#v", context.User)
	}

	bootstrap := NewContextBuilder(workspace).LoadBootstrapFiles()
	for _, required := range []string{"## AGENTS.md", "## SOUL.md", "## USER.md", "current workspace guidance"} {
		if !strings.Contains(bootstrap, required) {
			t.Fatalf("bootstrap missing %q: %q", required, bootstrap)
		}
	}
	if strings.Contains(bootstrap, "Do not load this file") {
		t.Fatalf("removed personal files reached bootstrap: %q", bootstrap)
	}
}

func TestPersonalInstructionsTreatFrontmatterAsProse(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENTS.md": "---\nmodel: untrusted-model\ntools: []\nunknownAuthority: true\n---\n\nFollow the prose.",
	})
	defer cleanupWorkspace(t, workspace)

	bootstrap := NewContextBuilder(workspace).LoadBootstrapFiles()
	for _, prose := range []string{"model: untrusted-model", "tools: []", "unknownAuthority: true", "Follow the prose."} {
		if !strings.Contains(bootstrap, prose) {
			t.Fatalf("bootstrap missing prose %q: %q", prose, bootstrap)
		}
	}
}

func TestPersonalContextCacheTracksOnlyCurrentFiles(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENTS.md":   "# Instructions\nVersion one.",
		"AGENT.md":    "# Removed manifest\nVersion one.",
		"IDENTITY.md": "# Removed identity\nVersion one.",
		"SOUL.md":     "# Soul\nVersion one.",
		"USER.md":     "# User\nVersion one.",
	})
	defer cleanupWorkspace(t, workspace)

	builder := NewContextBuilder(workspace)
	promptV1 := builder.BuildSystemPromptWithCache()
	future := time.Now().Add(2 * time.Second)

	for _, filename := range []string{"AGENT.md", "IDENTITY.md"} {
		path := filepath.Join(workspace, filename)
		if err := os.WriteFile(path, []byte("# Removed\nVersion two."), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatal(err)
		}
	}
	builder.systemPromptMutex.RLock()
	changed := builder.sourceFilesChangedLocked()
	builder.systemPromptMutex.RUnlock()
	if changed {
		t.Fatal("removed personal files invalidated the prompt cache")
	}
	if promptV2 := builder.BuildSystemPromptWithCache(); promptV2 != promptV1 {
		t.Fatal("removed personal files changed the prompt")
	}

	instructionsPath := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte("# Instructions\nVersion two."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(instructionsPath, future, future); err != nil {
		t.Fatal(err)
	}
	builder.systemPromptMutex.RLock()
	changed = builder.sourceFilesChangedLocked()
	builder.systemPromptMutex.RUnlock()
	if !changed {
		t.Fatal("AGENTS.md change did not invalidate the prompt cache")
	}
	if promptV2 := builder.BuildSystemPromptWithCache(); !strings.Contains(promptV2, "Version two") {
		t.Fatalf("updated instructions missing from prompt: %q", promptV2)
	}
}

func cleanupWorkspace(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("failed to clean up workspace %s: %v", path, err)
	}
}
