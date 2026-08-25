package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const personalInstructionsFile = "AGENTS.md"

type workspaceMarkdown struct {
	Path    string
	Content string
}

// personalContext contains the prose admitted by the personal runtime profile.
// Machine-interpreted agent settings live exclusively in config.AgentConfig.
type personalContext struct {
	Instructions *workspaceMarkdown
	Soul         *workspaceMarkdown
	User         *workspaceMarkdown
}

func (cb *ContextBuilder) loadPersonalContext() personalContext {
	return loadPersonalContext(cb.workspace)
}

func loadPersonalContext(workspace string) personalContext {
	return personalContext{
		Instructions: loadWorkspaceMarkdown(filepath.Join(workspace, personalInstructionsFile)),
		Soul:         loadWorkspaceMarkdown(filepath.Join(workspace, "SOUL.md")),
		User:         loadWorkspaceMarkdown(filepath.Join(workspace, "USER.md")),
	}
}

func loadWorkspaceMarkdown(path string) *workspaceMarkdown {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return &workspaceMarkdown{Path: path, Content: string(content)}
}

func personalContextPaths(workspace string) []string {
	return []string{
		filepath.Join(workspace, personalInstructionsFile),
		filepath.Join(workspace, "SOUL.md"),
		filepath.Join(workspace, "USER.md"),
	}
}

func relativeWorkspacePath(workspace, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	relativePath, err := filepath.Rel(workspace, path)
	if err == nil && relativePath != "." && !strings.HasPrefix(relativePath, "..") {
		return filepath.ToSlash(relativePath)
	}
	return filepath.Clean(path)
}

func uniquePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if slices.Contains(result, cleaned) {
			continue
		}
		result = append(result, cleaned)
	}
	return result
}
