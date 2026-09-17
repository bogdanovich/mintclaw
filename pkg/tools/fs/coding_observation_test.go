package fstools

import (
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestNativeExplorationToolsPublishTypedStartMetadata(t *testing.T) {
	tests := []struct {
		name      string
		provider  toolshared.CodingObservationProvider
		args      map[string]any
		operation toolshared.ExplorationOperation
		path      string
		pattern   string
	}{
		{
			name: "byte read", provider: &ReadFileTool{}, args: map[string]any{"path": "pkg/a.go"},
			operation: toolshared.ExplorationRead, path: "pkg/a.go",
		},
		{
			name: "line read", provider: &ReadFileLinesTool{}, args: map[string]any{"path": "pkg/b.go"},
			operation: toolshared.ExplorationRead, path: "pkg/b.go",
		},
		{
			name: "default list", provider: &ListDirTool{}, args: map[string]any{"ignored": "secret"},
			operation: toolshared.ExplorationList, path: ".",
		},
		{
			name: "search", provider: &SearchFilesTool{},
			args:      map[string]any{"path": "pkg", "pattern": "ToolStarted", "ignored": "secret"},
			operation: toolshared.ExplorationSearch, path: "pkg", pattern: "ToolStarted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := toolshared.SanitizeToolObservation(test.provider.CodingStartObservation(test.args))
			if got == nil || got.Exploration == nil || got.Exploration.Operation != test.operation ||
				got.Exploration.Path != test.path || got.Exploration.Pattern != test.pattern ||
				got.Command != nil || got.Plan != nil {
				t.Fatalf("native exploration observation = %#v", got)
			}
		})
	}
}
