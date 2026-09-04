package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestSemanticCellStoreReconcilesStableActiveAndCommittedCells(t *testing.T) {
	active := semanticMessageItem("assistant-1", 1, 1, frontend.PresentationActive, "work")
	store, err := newSemanticCellStore([]frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ordered) != 1 || len(store.active) != 1 || len(store.committed) != 0 {
		t.Fatalf("initial semantic store = %+v", store)
	}
	first := store.ordered[0]

	active.Revision = 2
	active.Message.Text = "working"
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] == first || first.item.Message.Text != "work" ||
		store.ordered[0].item.Message.Text != "working" {
		t.Fatalf("active cell replacement mutated prior state: first=%+v next=%+v", first.item, store.ordered[0].item)
	}

	active.Revision = 3
	active.Lifecycle = frontend.PresentationCompleted
	active.Message.Complete = true
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.active) != 0 || len(store.committed) != 1 || store.committed[0] != store.ordered[0] {
		t.Fatalf("completed semantic store = %+v", store)
	}
	committed := store.ordered[0]

	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] != committed {
		t.Fatal("unchanged ID/revision did not reuse the immutable semantic cell")
	}

	next := semanticToolItem("tool-1", 2, 1, frontend.PresentationActive, frontend.ToolRunning)
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active, next})
	if err != nil {
		t.Fatal(err)
	}
	identities := []cellIdentity{store.ordered[0].Identity(), store.ordered[1].Identity()}
	if len(store.committed) != 1 || len(store.active) != 1 || identities[0].Sequence != 1 ||
		identities[1].Sequence != 2 {
		t.Fatalf("ordered semantic store = %+v identities=%+v", store, identities)
	}
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{next})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ordered) != 1 || store.ordered[0].Identity().ID != "tool-1" {
		t.Fatalf("bounded snapshot did not prune absent cells: %+v", store)
	}
}

func TestSemanticCellStoreRejectsAmbiguousOrStaleSnapshotsAtomically(t *testing.T) {
	item := semanticMessageItem("assistant-1", 1, 2, frontend.PresentationActive, "working")
	store, err := newSemanticCellStore([]frontend.PresentationItem{item})
	if err != nil {
		t.Fatal(err)
	}
	original := store.ordered[0]

	tests := []struct {
		name  string
		items []frontend.PresentationItem
		stale bool
	}{
		{
			name: "stale revision",
			items: []frontend.PresentationItem{
				semanticMessageItem("assistant-1", 1, 1, frontend.PresentationActive, "older"),
			},
			stale: true,
		},
		{
			name: "duplicate ID",
			items: []frontend.PresentationItem{
				item,
				semanticToolItem("assistant-1", 2, 1, frontend.PresentationActive, frontend.ToolRunning),
			},
		},
		{
			name: "changed stable sequence",
			items: []frontend.PresentationItem{
				semanticMessageItem("assistant-1", 2, 3, frontend.PresentationActive, "moved"),
			},
		},
		{
			name: "changed stable kind",
			items: []frontend.PresentationItem{
				semanticToolItem("assistant-1", 1, 3, frontend.PresentationActive, frontend.ToolRunning),
			},
		},
		{
			name: "changed stable turn",
			items: func() []frontend.PresentationItem {
				changed := semanticMessageItem("assistant-1", 1, 3, frontend.PresentationActive, "moved")
				changed.TurnID = "turn-2"
				return []frontend.PresentationItem{changed}
			}(),
		},
		{
			name: "unordered sequence",
			items: []frontend.PresentationItem{
				semanticToolItem("tool-2", 2, 1, frontend.PresentationCompleted, frontend.ToolSucceeded),
				semanticToolItem("tool-1", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded),
			},
		},
		{
			name: "ambiguous payload",
			items: []frontend.PresentationItem{{
				ID: "ambiguous", Sequence: 1, Revision: 1, Kind: frontend.PresentationToolCall,
				Lifecycle: frontend.PresentationCompleted,
				Message:   &frontend.TranscriptEntry{Text: "claim"},
				Tool:      &frontend.ToolState{Name: "exec", Status: frontend.ToolSucceeded},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, reconcileErr := reconcileSemanticCellStore(store, tt.items)
			if reconcileErr == nil {
				t.Fatalf("reconcile succeeded with store %+v", next)
			}
			if tt.stale && !errors.Is(reconcileErr, errStaleSemanticCellRevision) {
				t.Fatalf("error = %v, want stale revision", reconcileErr)
			}
			if store.ordered[0] != original || store.ordered[0].item.Message.Text != "working" {
				t.Fatalf("failed reconcile mutated original store: %+v", store)
			}
		})
	}
}

func TestSemanticCellOwnsTypedPayloadAndSanitizesEveryRenderMode(t *testing.T) {
	item := semanticToolItem("tool-1", 1, 1, frontend.PresentationFailed, frontend.ToolFailed)
	item.Tool.Name = "exec\x1b[31m"
	item.Tool.Arguments = "SECRET-ARGUMENT\x07"
	item.Tool.Command = &frontend.CommandState{
		Status: frontend.CommandFailed, Stderr: "failure\x1b[0m", Truncated: true,
	}
	cell := newPresentationCell(item)
	item.Tool.Command.Stderr = "mutated"

	for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
		document := cell.Render(cellRenderContext{
			Width: 16, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
		}, mode)
		text := document.plainText()
		if strings.Contains(text, "\x1b") || strings.Contains(text, "\x07") || strings.Contains(text, "mutated") ||
			strings.Contains(text, "SECRET") {
			t.Fatalf("mode %d leaked control or aliased content: %q", mode, text)
		}
		for _, line := range document.Lines {
			if width := visibleCellWidth(line.plainText()); width > 16 {
				t.Fatalf("mode %d line width %d > 16: %q", mode, width, line.plainText())
			}
		}
		if mode == cellRenderCompact && strings.Contains(text, "failure") {
			t.Fatalf("compact mode exposed full command evidence: %q", text)
		}
		if mode != cellRenderCompact && !strings.Contains(text, "failure") {
			t.Fatalf("mode %d omitted full command evidence: %q", mode, text)
		}
		if mode == cellRenderPlain {
			for _, line := range document.Lines {
				for _, span := range line.Spans {
					if span.Role != cellStyleDefault {
						t.Fatalf("plain mode retained role %d in %+v", span.Role, document)
					}
				}
			}
		}
	}
}

func TestModelOwnsSemanticCellsWhileKeepingCompatibilityViewport(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	before := renderedModelTranscript(model, 80)
	if len(model.cells.ordered) != 1 || model.cells.ordered[0].Identity().Kind != frontend.PresentationUserMessage {
		t.Fatalf("initial model semantic cells = %+v", model.cells)
	}

	projector.PlanUpdated("turn-1", "call-1", frontend.PlanState{Steps: []frontend.PlanStepState{{
		Step: "Inspect", Status: frontend.PlanStepInProgress,
	}}})
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(model.cells.ordered) != 2 || model.cells.ordered[1].Identity().Kind != frontend.PresentationPlanUpdate {
		t.Fatalf("updated model semantic cells = %+v", model.cells)
	}
	if after := renderedModelTranscript(model, 80); after != before {
		t.Fatalf(
			"semantic cell admission changed shipped compatibility viewport:\nbefore: %q\n after: %q",
			before,
			after,
		)
	}
}

func TestCellRenderContextValidation(t *testing.T) {
	valid := []cellRenderContext{
		{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorTrueColor},
		{Width: 80, Theme: cellThemeLight, ColorLevel: cellColorANSI256},
		{Width: 120, Theme: cellThemeDark, ColorLevel: cellColorANSI16},
		{Width: 40, Theme: cellThemeLight, ColorLevel: cellColorNone},
	}
	for _, context := range valid {
		if err := validateCellRenderContext(context); err != nil {
			t.Fatalf("valid context %+v: %v", context, err)
		}
	}
	for _, context := range []cellRenderContext{
		{Width: 0, Theme: cellThemeDark, ColorLevel: cellColorNone},
		{Width: 40, Theme: cellThemeUnknown, ColorLevel: cellColorNone},
		{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorLevel(99)},
	} {
		if err := validateCellRenderContext(context); err == nil {
			t.Fatalf("invalid context accepted: %+v", context)
		}
	}
}

func TestSemanticCellSanitizerPreservesOrdinaryPunctuation(t *testing.T) {
	const value = "fields: command, yield-time-ms"
	if sanitized := sanitizeTerminalText(value); sanitized != value {
		t.Fatalf("sanitized punctuation = %q (%x), want %q (%x)", sanitized, []byte(sanitized), value, []byte(value))
	}
	document := wrapCellDocument(cellDocument{Lines: logicalCellLines("  "+value, cellStyleMuted)}, 120)
	if wrapped := document.plainText(); wrapped != "  "+value {
		t.Fatalf(
			"wrapped punctuation = %q (%x), want %q (%x)",
			wrapped,
			[]byte(wrapped),
			"  "+value,
			[]byte("  "+value),
		)
	}
}

func TestSemanticCellWrappingBoundsIndentAndTruncationAtNarrowWidths(t *testing.T) {
	document := cellDocument{
		Lines:     logicalCellLines("    evidence", cellStyleDefault),
		Truncated: true,
	}
	for width := 1; width <= 12; width++ {
		wrapped := wrapCellDocument(document, width)
		if !wrapped.Truncated {
			t.Fatalf("width %d lost truncation state", width)
		}
		for _, line := range wrapped.Lines {
			if visible := visibleCellWidth(line.plainText()); visible > width {
				t.Fatalf("width %d produced line width %d: %q", width, visible, line.plainText())
			}
		}
	}
}

func TestSemanticCellLayoutDoesNotInventLinesForEmptyCells(t *testing.T) {
	empty := newPresentationCell(semanticMessageItem(
		"empty",
		1,
		1,
		frontend.PresentationCompleted,
		"",
	))
	visible := newPresentationCell(semanticMessageItem(
		"visible",
		2,
		1,
		frontend.PresentationCompleted,
		"done",
	))
	rendered, layout := renderSemanticCells(
		[]semanticCell{empty, visible},
		cellRenderContext{Width: 80},
		cellRenderCompact,
	)
	if rendered != "• done" {
		t.Fatalf("rendered semantic cells = %q", rendered)
	}
	want := cellLayout{Blocks: []cellLayoutBlock{
		{ID: "empty", Start: 0, End: 0},
		{ID: "visible", Start: 0, End: 1},
	}}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout = %+v, want %+v", layout, want)
	}
}

func TestSemanticCellRolesPreservePlanAndVerifiedWriteMeaning(t *testing.T) {
	plan := newPresentationCell(frontend.PresentationItem{
		ID: "plan-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
		Plan: &frontend.PlanState{Steps: []frontend.PlanStepState{
			{Step: "done", Status: frontend.PlanStepCompleted},
			{Step: "current", Status: frontend.PlanStepInProgress},
			{Step: "later", Status: frontend.PlanStepPending},
		}},
	}).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	roles := []cellStyleRole{
		plan.Lines[1].Spans[0].Role,
		plan.Lines[2].Spans[0].Role,
		plan.Lines[3].Spans[0].Role,
	}
	if !reflect.DeepEqual(roles, []cellStyleRole{cellStyleSuccess, cellStyleAccent, cellStyleMuted}) {
		t.Fatalf("plan roles = %v", roles)
	}

	writes := newPresentationCell(frontend.PresentationItem{
		ID:        "writes-1",
		TurnID:    "turn-1",
		Sequence:  2,
		Revision:  1,
		Kind:      frontend.PresentationToolCall,
		Lifecycle: frontend.PresentationCompleted,
		Tool: &frontend.ToolState{
			Name:   "apply_patch",
			Status: frontend.ToolSucceeded,
			WriteAudit: []frontend.WriteAudit{
				{Target: "created.go", Action: "create", Success: true},
				{Target: "deleted.go", Action: "delete", Success: true},
				{Target: "failed.go", Action: "update", Success: false},
			},
		},
	}).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	roles = []cellStyleRole{
		writes.Lines[1].Spans[0].Role,
		writes.Lines[2].Spans[0].Role,
		writes.Lines[3].Spans[0].Role,
	}
	if !reflect.DeepEqual(roles, []cellStyleRole{cellStyleInsertion, cellStyleDeletion, cellStyleFailure}) {
		t.Fatalf("write roles = %v", roles)
	}
}

func semanticMessageItem(
	id string,
	sequence uint64,
	revision uint64,
	lifecycle frontend.PresentationLifecycle,
	text string,
) frontend.PresentationItem {
	return frontend.PresentationItem{
		ID: id, TurnID: "turn-1", Sequence: sequence, Revision: revision,
		Kind: frontend.PresentationAssistantMessage, Lifecycle: lifecycle,
		Message: &frontend.TranscriptEntry{
			ID: id, TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: text,
			Complete: presentationCellCommitted(lifecycle),
		},
	}
}

func semanticToolItem(
	id string,
	sequence uint64,
	revision uint64,
	lifecycle frontend.PresentationLifecycle,
	status frontend.ToolStatus,
) frontend.PresentationItem {
	return frontend.PresentationItem{
		ID: id, TurnID: "turn-1", Sequence: sequence, Revision: revision,
		Kind: frontend.PresentationToolCall, Lifecycle: lifecycle,
		Tool: &frontend.ToolState{
			TurnID: "turn-1", CallID: id, Name: "exec", Status: status,
		},
	}
}

func visibleCellWidth(value string) int {
	// All renderer output is ANSI-free by contract; rune-aware ANSI width also
	// handles CJK, emoji, and combining characters used by later fixtures.
	return ansi.StringWidth(value)
}
