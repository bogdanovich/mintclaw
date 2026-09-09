package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestMCPCellsRenderDistinctTruthfulLifecycleStates(t *testing.T) {
	tests := []struct {
		name        string
		outcome     frontend.MCPOutcome
		lifecycle   frontend.PresentationLifecycle
		toolStatus  frontend.ToolStatus
		errorText   string
		loopCode    string
		want        string
		wantOutcome string
	}{
		{
			name: "running", outcome: frontend.MCPOutcomeRunning,
			lifecycle: frontend.PresentationActive, toolStatus: frontend.ToolRunning,
			want: "Calling github.search_repositories",
		},
		{
			name: "succeeded", outcome: frontend.MCPOutcomeSucceeded,
			lifecycle: frontend.PresentationCompleted, toolStatus: frontend.ToolSucceeded,
			want: "Called github.search_repositories", wantOutcome: "3 repositories",
		},
		{
			name: "failed", outcome: frontend.MCPOutcomeFailed,
			lifecycle: frontend.PresentationFailed, toolStatus: frontend.ToolFailed,
			errorText: "permission denied", want: "MCP call failed",
		},
		{
			name: "canceled", outcome: frontend.MCPOutcomeCanceled,
			lifecycle: frontend.PresentationInterrupted, toolStatus: frontend.ToolInterrupted,
			errorText: "canceled by user", want: "MCP call canceled",
		},
		{
			name: "timed out", outcome: frontend.MCPOutcomeTimedOut,
			lifecycle: frontend.PresentationFailed, toolStatus: frontend.ToolFailed,
			errorText: "deadline exceeded", want: "MCP call timed out",
		},
		{
			name: "uncertain", outcome: frontend.MCPOutcomeUncertain,
			lifecycle: frontend.PresentationFailed, toolStatus: frontend.ToolFailed,
			errorText: "inspect external state before retrying", want: "MCP outcome uncertain",
		},
		{
			name: "successful no progress halt", outcome: frontend.MCPOutcomeSucceeded,
			lifecycle: frontend.PresentationCompleted, toolStatus: frontend.ToolSucceeded,
			loopCode: "identical_call_emergency_halt", want: "turn halted: no progress (4/4)",
			wantOutcome: "same result",
		},
		{
			name: "repeated failure halt", outcome: frontend.MCPOutcomeFailed,
			lifecycle: frontend.PresentationFailed, toolStatus: frontend.ToolFailed,
			loopCode: "same_tool_failure_halt", want: "turn halted: repeated failures (4/4)",
			errorText: "permission denied",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := semanticToolItem("mcp-"+test.name, 1, 1, test.lifecycle, test.toolStatus)
			item.Tool.Name = "opaque-provider-alias"
			item.Tool.Arguments = "fields: query"
			item.Tool.Duration = 1200 * time.Millisecond
			item.Tool.MCP = &frontend.MCPState{
				Server: "github", Tool: "search_repositories", Purpose: "Search repositories",
				Outcome: test.outcome, Result: test.wantOutcome, Error: test.errorText,
				LoopHaltCode: test.loopCode,
			}
			if test.loopCode != "" {
				item.Tool.MCP.LoopHaltCount = 4
				item.Tool.MCP.LoopHaltThreshold = 4
			}
			rendered := newPresentationCell(item).Render(cellRenderContext{Width: 80}, cellRenderFull).plainText()
			searchable := strings.ReplaceAll(rendered, "\n", " ")
			for _, want := range []string{test.want, "purpose: Search repositories", "input: fields: query"} {
				if !strings.Contains(searchable, want) {
					t.Fatalf("MCP cell omits %q: %q", want, rendered)
				}
			}
			if test.errorText != "" && !strings.Contains(rendered, "Error: "+test.errorText) {
				t.Fatalf("MCP error is not actionable: %q", rendered)
			}
		})
	}
}

func TestMCPCompactPreviewIsBoundedAndFullEvidenceIsExpandable(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "search")
	projector.ToolStarted("turn-1", "call-1", "opaque-provider-alias", "fields: query")
	projector.ToolMCPObserved("turn-1", "call-1", frontend.MCPState{
		Server: "github", Tool: "search_repositories", Purpose: "Search repositories",
		Outcome: frontend.MCPOutcomeSucceeded,
		Result: strings.Join([]string{
			"line-1", "line-2", "line-3", "line-4", "line-5", "line-6", "line-7",
		}, "\n"),
	})
	projector.ToolCompleted("turn-1", "call-1", "opaque-provider-alias", "", time.Second, false, nil)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 24)
	compact := model.document.text()
	if !strings.Contains(compact, "result omitted; Ctrl+T opens full transcript") ||
		strings.Contains(compact, "line-7") {
		t.Fatalf("compact MCP evidence is not bounded: %q", compact)
	}
	plain := strings.Join(transcriptOverlayLogicalLines(model.transcriptOverlayLines()), "\n")
	if !strings.Contains(plain, "line-7") || strings.Contains(plain, "\x1b") {
		t.Fatalf("copy-safe transcript omitted or styled MCP evidence: %q", plain)
	}
}

func TestMCPCellSanitizesTerminalControlsAtTinyWidths(t *testing.T) {
	item := semanticToolItem("mcp", 1, 1, frontend.PresentationFailed, frontend.ToolFailed)
	item.Tool.MCP = &frontend.MCPState{
		Server: "git\x1b]0;forged\a", Tool: "search\rforged", Purpose: "purpose\x1b[2J",
		Outcome: frontend.MCPOutcomeFailed, Error: "failure\x1b[31m\rforged\a",
	}
	for _, width := range []int{1, 8, 80} {
		for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
			rendered := renderCellDocument(
				newPresentationCell(item).Render(cellRenderContext{Width: width}, mode),
				cellRenderContext{Width: width},
				mode,
			)
			plain := ansi.Strip(rendered)
			if strings.ContainsAny(plain, "\x1b\a\r") {
				t.Fatalf("width %d mode %d retained controls: %q", width, mode, plain)
			}
			for _, line := range strings.Split(rendered, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("width %d mode %d overflowed: %q", width, mode, line)
				}
			}
		}
	}
}

func TestUnknownToolRetainsCompactGenericFallback(t *testing.T) {
	item := semanticToolItem("unknown", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	item.Tool.Name = "custom_native_tool"
	text := newPresentationCell(item).Render(cellRenderContext{Width: 80}, cellRenderCompact).plainText()
	if text != "• Tool custom_native_tool [succeeded]" {
		t.Fatalf("generic fallback = %q", text)
	}
}
