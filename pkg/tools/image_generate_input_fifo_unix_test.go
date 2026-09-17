//go:build !windows

package tools

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestImageGenerateToolRejectsFIFOWithoutBlocking(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	provider := &fakeImageGenerationProvider{id: "test-provider", editing: true}
	tool := NewImageGenerateTool(
		workspace,
		"model",
		media.NewFileMediaStore(),
		WithImageGenerationProvider(provider),
	)

	resultChannel := make(chan *toolshared.ToolResult, 1)
	go func() {
		resultChannel <- tool.Execute(t.Context(), map[string]any{
			"action": "edit", "prompt": "edit", "input_images": []any{path},
		})
	}()

	select {
	case result := <-resultChannel:
		if !result.IsError || !strings.Contains(result.ContentForLLM(), "not a regular file") {
			t.Fatalf("Execute result = %q, want regular-file error", result.ContentForLLM())
		}
	case <-time.After(time.Second):
		t.Fatal("Execute blocked while opening FIFO")
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}
