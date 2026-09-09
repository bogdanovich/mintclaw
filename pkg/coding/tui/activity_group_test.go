package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestExplorationCellRendersTypedReadListAndSearchLabels(t *testing.T) {
	tests := []struct {
		name        string
		exploration frontend.ExplorationState
		status      frontend.ToolStatus
		lifecycle   frontend.PresentationLifecycle
		want        []string
	}{
		{
			name: "read", exploration: frontend.ExplorationState{
				Operation: frontend.ExplorationRead, Path: "pkg/agent/pipeline.go",
			}, status: frontend.ToolSucceeded, lifecycle: frontend.PresentationCompleted,
			want: []string{"Explored", "Read pkg/agent/pipeline.go"},
		},
		{
			name: "list", exploration: frontend.ExplorationState{
				Operation: frontend.ExplorationList, Path: "pkg/coding", Workspace: "build",
			}, status: frontend.ToolRunning, lifecycle: frontend.PresentationActive,
			want: []string{"Exploring", "List pkg/coding on build"},
		},
		{
			name: "failed search", exploration: frontend.ExplorationState{
				Operation: frontend.ExplorationSearch, Path: "pkg", Pattern: "ToolStarted",
			}, status: frontend.ToolFailed, lifecycle: frontend.PresentationFailed,
			want: []string{"Exploration failed", `Search "ToolStarted" in pkg`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := explorationTestCell("call-1", 1, test.lifecycle, test.status, test.exploration)
			rendered := cell.Render(cellRenderContext{Width: 80}, cellRenderCompact).plainText()
			for _, want := range test.want {
				if !strings.Contains(rendered, want) {
					t.Fatalf("exploration cell omits %q: %q", want, rendered)
				}
			}
		})
	}
}

func TestExplorationGroupDeduplicatesLabelsAndKeepsActiveCallsVisible(t *testing.T) {
	cells := []*presentationCell{
		explorationTestCell("read-a", 1, frontend.PresentationCompleted, frontend.ToolSucceeded,
			frontend.ExplorationState{Operation: frontend.ExplorationRead, Path: "pkg/a.go"}),
		explorationTestCell("read-a-again", 2, frontend.PresentationActive, frontend.ToolRunning,
			frontend.ExplorationState{Operation: frontend.ExplorationRead, Path: "pkg/a.go"}),
		explorationTestCell("search", 3, frontend.PresentationActive, frontend.ToolRunning,
			frontend.ExplorationState{Operation: frontend.ExplorationSearch, Path: "pkg", Pattern: "needle"}),
		explorationTestCell("failed", 4, frontend.PresentationFailed, frontend.ToolFailed,
			frontend.ExplorationState{Operation: frontend.ExplorationList, Path: "missing"}),
	}
	specs := groupedLiveCellSpecs(cells)
	if len(specs) != 2 {
		t.Fatalf("grouped exploration specs = %d, want 2", len(specs))
	}
	group, ok := specs[0].cell.(*activityGroupCell)
	if !ok || len(group.members) != 3 || group.Identity().Lifecycle != frontend.PresentationActive {
		t.Fatalf("active exploration group = %#v", specs[0].cell)
	}
	rendered := group.Render(cellRenderContext{Width: 80}, cellRenderCompact).plainText()
	for _, want := range []string{
		"Exploring", "Read pkg/a.go ×2", `Search "needle" in pkg`, "ctrl+t to view full transcript",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("exploration group omits %q: %q", want, rendered)
		}
	}
	failure := specs[1].cell.Render(cellRenderContext{Width: 80}, cellRenderCompact).plainText()
	if !strings.Contains(failure, "Exploration failed") || !strings.Contains(failure, "List missing") {
		t.Fatalf("exploration failure was hidden by group: %q", failure)
	}
}

func TestExplorationGroupDeduplicatesOnlyAdjacentLabels(t *testing.T) {
	cells := []*presentationCell{
		explorationTestCell("read-a", 1, frontend.PresentationCompleted, frontend.ToolSucceeded,
			frontend.ExplorationState{Operation: frontend.ExplorationRead, Path: "pkg/a.go"}),
		explorationTestCell("read-b", 2, frontend.PresentationCompleted, frontend.ToolSucceeded,
			frontend.ExplorationState{Operation: frontend.ExplorationRead, Path: "pkg/b.go"}),
		explorationTestCell("read-a-again", 3, frontend.PresentationCompleted, frontend.ToolSucceeded,
			frontend.ExplorationState{Operation: frontend.ExplorationRead, Path: "pkg/a.go"}),
	}
	specs := groupedLiveCellSpecs(cells)
	if len(specs) != 1 {
		t.Fatalf("exploration specs = %+v", specs)
	}
	group, ok := specs[0].cell.(*activityGroupCell)
	if !ok {
		t.Fatalf("exploration group = %T", specs[0].cell)
	}
	rendered := group.Render(cellRenderContext{Width: 80}, cellRenderCompact).plainText()
	if strings.Count(rendered, "Read pkg/a.go") != 2 || strings.Contains(rendered, "×2") {
		t.Fatalf("non-adjacent exploration was deduplicated: %q", rendered)
	}
}

func TestSuccessfulCommandGroupsFlushAtSemanticBarriers(t *testing.T) {
	first := commandTestCell("command-a", 1, frontend.ToolSucceeded, frontend.CommandSucceeded)
	second := commandTestCell("command-b", 2, frontend.ToolSucceeded, frontend.CommandSucceeded)
	failure := commandTestCell("command-failed", 3, frontend.ToolFailed, frontend.CommandFailed)
	third := commandTestCell("command-c", 4, frontend.ToolSucceeded, frontend.CommandSucceeded)
	commentary := newPresentationCell(semanticMessageItem(
		"commentary", 5, 1, frontend.PresentationCompleted, "Checking the failure before continuing.",
	))
	fourth := commandTestCell("command-d", 6, frontend.ToolSucceeded, frontend.CommandSucceeded)

	specs := groupedLiveCellSpecs(
		[]*presentationCell{first, second, failure, third, commentary, fourth},
	)
	if len(specs) != 5 {
		t.Fatalf("command grouping specs = %d, want 5", len(specs))
	}
	group, ok := specs[0].cell.(*activityGroupCell)
	if !ok || group.kind != activityGroupCommands || len(group.members) != 2 {
		t.Fatalf("successful command group = %#v", specs[0].cell)
	}
	if rendered := group.Render(cellRenderContext{Width: 80}, cellRenderCompact).
		plainText(); !strings.Contains(rendered, "Ran 2 commands") ||
		!strings.Contains(rendered, "ctrl+t") {
		t.Fatalf("command group = %q", rendered)
	}
	if rendered := specs[1].cell.Render(cellRenderContext{Width: 80}, cellRenderCompact).
		plainText(); !strings.Contains(
		rendered,
		"Command failed",
	) {
		t.Fatalf("failed command was hidden: %q", rendered)
	}
	for _, index := range []int{2, 3, 4} {
		if _, grouped := specs[index].cell.(*activityGroupCell); grouped {
			t.Fatalf("semantic barrier did not flush spec %d", index)
		}
	}
}

func TestSuccessfulCommandGroupingFlushesBeforeEveryRequiredBarrier(t *testing.T) {
	barriers := map[string]*presentationCell{
		"commentary": newPresentationCell(assistantPhaseTestItem("commentary", 2, frontend.AssistantPhaseCommentary)),
		"final":      newPresentationCell(assistantPhaseTestItem("final", 2, frontend.AssistantPhaseFinal)),
		"attention":  genericToolTestCell("request-input", 2, "request_user_input", frontend.ToolRunning),
		"patch":      patchToolTestCell("patch", 2),
		"failure":    genericToolTestCell("failed", 2, "read_file", frontend.ToolFailed),
	}
	for name, barrier := range barriers {
		t.Run(name, func(t *testing.T) {
			specs := groupedLiveCellSpecs([]*presentationCell{
				commandTestCell("before", 1, frontend.ToolSucceeded, frontend.CommandSucceeded),
				barrier,
				commandTestCell("after", 3, frontend.ToolSucceeded, frontend.CommandSucceeded),
			})
			if len(specs) != 3 {
				t.Fatalf("barrier specs = %d, want 3", len(specs))
			}
			for index, spec := range specs {
				if _, grouped := spec.cell.(*activityGroupCell); grouped {
					t.Fatalf("barrier grouped spec %d", index)
				}
			}
		})
	}
}

func TestFullTranscriptPreservesCallsHiddenByCompactGroupsInCausalOrder(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	projector.ToolStarted("turn-1", "read", "read_file", "fields: path")
	projector.ToolExploration("turn-1", "read", frontend.ExplorationState{
		Operation: frontend.ExplorationRead, Path: "pkg/a.go",
	})
	projector.ToolCompleted("turn-1", "read", "read_file", "", time.Millisecond, false, nil)
	projector.ToolStarted("turn-1", "search", "search_files", "fields: path, pattern")
	projector.ToolExploration("turn-1", "search", frontend.ExplorationState{
		Operation: frontend.ExplorationSearch, Path: "pkg", Pattern: "needle",
	})
	projector.ToolCompleted("turn-1", "search", "search_files", "", time.Millisecond, false, nil)
	for index, command := range []string{"printf first", "printf second"} {
		callID := []string{"command-a", "command-b"}[index]
		projector.ToolStarted("turn-1", callID, "exec", "fields: command")
		projector.ToolCommandOutput("turn-1", callID, frontend.CommandState{
			Action: "run", Command: command, Source: frontend.CommandSourceAgent,
			Status: frontend.CommandSucceeded, OwnsProcess: true,
			Transcript: []frontend.CommandTranscriptEntry{{
				Sequence: 1, Stream: "stdout", Text: "output-" + callID + "\n",
			}},
		})
		projector.ToolCompleted("turn-1", callID, "exec", "", time.Millisecond, false, nil)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(100, 30)
	compact := renderedModelTranscript(model, 100)
	if !strings.Contains(compact, "Explored") || !strings.Contains(compact, "Ran 2 commands") {
		t.Fatalf("compact transcript omitted groups: %q", compact)
	}
	full := strings.Join(transcriptOverlayLogicalLines(model.transcriptOverlayLines()), "\n")
	wants := []string{
		"Read pkg/a.go", `Search "needle" in pkg`, "$ printf first", "output-command-a",
		"$ printf second", "output-command-b",
	}
	last := -1
	for _, want := range wants {
		index := strings.Index(full, want)
		if index <= last {
			t.Fatalf("full transcript lost causal order at %q: %q", want, full)
		}
		last = index
	}
	if strings.Contains(full, "Ran 2 commands") {
		t.Fatalf("full transcript retained collapsed command summary: %q", full)
	}
}

func explorationTestCell(
	id string,
	sequence uint64,
	lifecycle frontend.PresentationLifecycle,
	status frontend.ToolStatus,
	exploration frontend.ExplorationState,
) *presentationCell {
	item := semanticToolItem(id, sequence, 1, lifecycle, status)
	item.Tool.Name = "native"
	item.Tool.Exploration = &exploration
	return newPresentationCell(item)
}

func commandTestCell(
	id string,
	sequence uint64,
	toolStatus frontend.ToolStatus,
	commandStatus frontend.CommandStatus,
) *presentationCell {
	lifecycle := frontend.PresentationCompleted
	if toolStatus == frontend.ToolFailed {
		lifecycle = frontend.PresentationFailed
	}
	item := semanticToolItem(id, sequence, 1, lifecycle, toolStatus)
	item.Tool.Command = &frontend.CommandState{
		Action: "run", Command: "printf " + id, Source: frontend.CommandSourceAgent,
		Status: commandStatus, OwnsProcess: true,
	}
	return newPresentationCell(item)
}

func assistantPhaseTestItem(
	id string,
	sequence uint64,
	phase frontend.AssistantPhase,
) frontend.PresentationItem {
	item := semanticMessageItem(id, sequence, 1, frontend.PresentationCompleted, string(phase))
	item.Message.Phase = phase
	return item
}

func genericToolTestCell(id string, sequence uint64, name string, status frontend.ToolStatus) *presentationCell {
	lifecycle := frontend.PresentationActive
	if status == frontend.ToolFailed {
		lifecycle = frontend.PresentationFailed
	}
	item := semanticToolItem(id, sequence, 1, lifecycle, status)
	item.Tool.Name = name
	return newPresentationCell(item)
}

func patchToolTestCell(id string, sequence uint64) *presentationCell {
	item := semanticToolItem(id, sequence, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	item.Tool.Name = "apply_patch"
	item.Tool.WriteAudit = []frontend.WriteAudit{{
		Kind: "file", Target: "file.go", Action: "update", Success: true,
	}}
	return newPresentationCell(item)
}
