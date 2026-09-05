//go:build !windows

package coding

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	codingreviewer "github.com/bogdanovich/mintclaw/pkg/coding/reviewer"
)

func TestNativeReviewerFilesystemToolsDoNotBlockOnFIFO(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "blocked.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	toolset := newNativeReviewerToolset(root)
	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "read", tool: "read_file", args: map[string]any{"path": "blocked.fifo"}},
		{name: "list", tool: "list_dir", args: map[string]any{"path": "blocked.fifo"}},
		{name: "broad search", tool: "search_files", args: map[string]any{"pattern": "needle", "path": "."}},
		{
			name: "explicit search",
			tool: "search_files",
			args: map[string]any{"pattern": "needle", "path": "blocked.fifo"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := make(chan codingreviewer.ToolResult, 1)
			go func() { result <- toolset.Execute(t.Context(), test.tool, test.args) }()
			select {
			case outcome := <-result:
				if test.tool == "search_files" && !strings.Contains(outcome.Content, "special_files") {
					t.Fatalf("FIFO search result = %#v", outcome)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s blocked while opening a FIFO", test.tool)
			}
		})
	}
}
