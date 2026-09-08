package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

// semanticCellStore is a disposable renderer index over the authoritative
// bounded frontend snapshot. It owns immutable cells, never conversation or
// runtime state, and can be rebuilt from ThreadSnapshot.Items at any time.
type semanticCellStore struct {
	ordered   []*presentationCell
	committed []*presentationCell
	active    []*presentationCell
	byID      map[string]*presentationCell
}

func newSemanticCellStore(items []frontend.PresentationItem) (semanticCellStore, error) {
	return reconcileSemanticCellStore(semanticCellStore{}, items)
}

func reconcileSemanticCellStore(
	previous semanticCellStore,
	items []frontend.PresentationItem,
) (semanticCellStore, error) {
	next := semanticCellStore{
		ordered: make([]*presentationCell, 0, len(items)),
		byID:    make(map[string]*presentationCell, len(items)),
	}
	var priorSequence uint64
	for index, item := range items {
		if err := validateSemanticCellItem(item); err != nil {
			return semanticCellStore{}, fmt.Errorf("presentation item %d: %w", index, err)
		}
		if index != 0 && item.Sequence <= priorSequence {
			return semanticCellStore{}, fmt.Errorf(
				"presentation item %q sequence %d is not after %d",
				item.ID,
				item.Sequence,
				priorSequence,
			)
		}
		priorSequence = item.Sequence
		if _, duplicate := next.byID[item.ID]; duplicate {
			return semanticCellStore{}, fmt.Errorf("duplicate presentation item ID %q", item.ID)
		}

		cell := reconcileSemanticCell(previous.byID[item.ID], item)
		next.ordered = append(next.ordered, cell)
		next.byID[item.ID] = cell
		if presentationCellCommitted(item.Lifecycle) {
			next.committed = append(next.committed, cell)
		} else {
			next.active = append(next.active, cell)
		}
	}
	return next, nil
}

func reconcileSemanticCell(
	current *presentationCell,
	item frontend.PresentationItem,
) *presentationCell {
	if current == nil {
		return newPresentationCell(item)
	}
	if semanticCellItemEqual(current.item, item) {
		return current
	}
	return newPresentationCell(item)
}

func semanticCellItemEqual(left, right frontend.PresentationItem) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.Sequence == right.Sequence &&
		left.Revision == right.Revision && left.Kind == right.Kind && left.Lifecycle == right.Lifecycle &&
		left.Duration == right.Duration && reflect.DeepEqual(left.Message, right.Message) &&
		reflect.DeepEqual(left.Tool, right.Tool) && reflect.DeepEqual(left.Plan, right.Plan)
}

func validateSemanticCellItem(item frontend.PresentationItem) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("presentation item ID is required")
	}
	if item.Sequence == 0 {
		return fmt.Errorf("presentation item %q has zero sequence", item.ID)
	}
	if item.Revision == 0 {
		return fmt.Errorf("presentation item %q has zero revision", item.ID)
	}
	if !knownPresentationLifecycle(item.Lifecycle) {
		return fmt.Errorf("presentation item %q has unknown lifecycle %q", item.ID, item.Lifecycle)
	}
	payloads := 0
	if item.Message != nil {
		payloads++
	}
	if item.Tool != nil {
		payloads++
	}
	if item.Plan != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("presentation item %q has %d typed payloads", item.ID, payloads)
	}
	switch item.Kind {
	case frontend.PresentationUserMessage,
		frontend.PresentationAssistantMessage,
		frontend.PresentationReasoning,
		frontend.PresentationToolMessage,
		frontend.PresentationWarning,
		frontend.PresentationError:
		if item.Message == nil {
			return fmt.Errorf("presentation item %q kind %q requires a message payload", item.ID, item.Kind)
		}
	case frontend.PresentationToolCall:
		if item.Tool == nil {
			return fmt.Errorf("presentation item %q kind %q requires a tool payload", item.ID, item.Kind)
		}
	case frontend.PresentationPlanUpdate:
		if item.Plan == nil {
			return fmt.Errorf("presentation item %q kind %q requires a plan payload", item.ID, item.Kind)
		}
	default:
		return fmt.Errorf("presentation item %q has unsupported kind %q", item.ID, item.Kind)
	}
	return nil
}

func knownPresentationLifecycle(lifecycle frontend.PresentationLifecycle) bool {
	switch lifecycle {
	case frontend.PresentationActive,
		frontend.PresentationCompleted,
		frontend.PresentationFailed,
		frontend.PresentationInterrupted,
		frontend.PresentationSuspended,
		frontend.PresentationUnknown:
		return true
	default:
		return false
	}
}

func presentationCellCommitted(lifecycle frontend.PresentationLifecycle) bool {
	switch lifecycle {
	case frontend.PresentationCompleted, frontend.PresentationFailed, frontend.PresentationInterrupted:
		return true
	default:
		return false
	}
}

func (store *semanticCellStore) cells() []semanticCell {
	cells := make([]semanticCell, len(store.ordered))
	for index, cell := range store.ordered {
		cells[index] = cell
	}
	return cells
}

// flushActiveForShutdown freezes the renderer-owned mutable index after the
// Bubble Tea program stops. It does not rewrite authoritative item lifecycle:
// the final snapshot remains truthful about whether runtime work was active,
// suspended, or unknown when the local frontend disappeared.
func (store *semanticCellStore) flushActiveForShutdown() int {
	if store == nil || len(store.active) == 0 {
		return 0
	}
	flushed := len(store.active)
	store.committed = append([]*presentationCell(nil), store.ordered...)
	store.active = nil
	return flushed
}

func cloneCellPresentationItem(item frontend.PresentationItem) frontend.PresentationItem {
	return frontend.ThreadSnapshot{Items: []frontend.PresentationItem{item}}.Clone().Items[0]
}

func newHydratedSemanticCellStore(entries []frontend.TranscriptEntry) (semanticCellStore, error) {
	items := make([]frontend.PresentationItem, 0, len(entries))
	for index, source := range entries {
		entry := source
		if strings.TrimSpace(entry.ID) == "" {
			entry.ID = fmt.Sprintf("hydrated-entry-%d", index)
		}
		entry.Complete = true
		kind := frontend.PresentationError
		switch entry.Kind {
		case frontend.EntryUser:
			kind = frontend.PresentationUserMessage
		case frontend.EntryAssistant:
			kind = frontend.PresentationAssistantMessage
		case frontend.EntryReasoning:
			kind = frontend.PresentationReasoning
		case frontend.EntryTool:
			kind = frontend.PresentationToolMessage
		case frontend.EntryWarning:
			kind = frontend.PresentationWarning
		case frontend.EntryError:
			kind = frontend.PresentationError
		}
		items = append(items, frontend.PresentationItem{
			ID: entry.ID, TurnID: entry.TurnID, Sequence: uint64(index + 1), Revision: 1,
			Kind: kind, Lifecycle: frontend.PresentationCompleted, Message: &entry,
		})
	}
	return newSemanticCellStore(items)
}

func (m *Model) reconcileStaticCells(state frontend.ThreadSnapshot) {
	if entry, ok := verifiedWritesEntry(state.ChangedFiles); ok {
		m.staticCell(
			"tui:compat:verified-writes",
			cellStyleAccent,
			entry.label,
			entry.text,
			entry.truncated,
		)
	} else {
		delete(m.staticCells, "tui:compat:verified-writes")
	}
	if state.Workspace != nil {
		entry := workspaceChangesEntry(*state.Workspace)
		m.staticCell(
			"tui:compat:workspace",
			cellStyleAccent,
			entry.label,
			entry.text,
			entry.truncated,
		)
	} else {
		delete(m.staticCells, "tui:compat:workspace")
	}
}

func (m *Model) staticCell(
	id string,
	role cellStyleRole,
	label, text string,
	truncated bool,
) *staticSemanticCell {
	if current := m.staticCells[id]; current != nil && current.matches(role, label, text, truncated) {
		return current
	}
	revision := uint64(1)
	if current := m.staticCells[id]; current != nil {
		revision = current.identity.Revision + 1
	}
	cell := newStaticSemanticCell(id, revision, role, label, text, truncated)
	m.staticCells[id] = cell
	return cell
}

func (m *Model) visibleSemanticCellSpecs(state frontend.ThreadSnapshot) []semanticCellRenderSpec {
	specs := make([]semanticCellRenderSpec, 0, len(m.hydratedCells.ordered)+len(m.cells.ordered)+5)
	if m.transcript.loading {
		specs = append(specs, semanticCellRenderSpec{cell: m.staticCell(
			"tui:notice:loading", cellStyleMuted, "Loading earlier transcript…", "", false,
		)})
	} else if !m.transcript.disabled && (m.transcript.hasOlder || state.HasOlderEntries) {
		specs = append(specs, semanticCellRenderSpec{cell: m.staticCell(
			"tui:notice:older", cellStyleMuted, "↑ More transcript available (Page Up)", "", false,
		)})
	}

	liveMessageIDs := make(map[string]struct{}, len(m.cells.ordered))
	for _, cell := range m.cells.ordered {
		if cell.item.Message != nil {
			liveMessageIDs[cell.item.Message.ID] = struct{}{}
		}
	}
	for _, cell := range m.hydratedCells.ordered {
		if cell.item.Message != nil {
			if _, duplicate := liveMessageIDs[cell.item.Message.ID]; duplicate {
				continue
			}
		}
		specs = append(specs, semanticCellRenderSpec{cell: cell, mode: cellRenderCompact})
	}
	for _, cell := range m.cells.ordered {
		if redundantNativePlanTool(cell) {
			continue
		}
		mode := cellRenderCompact
		selected := false
		if cell.item.Tool != nil {
			viewID := toolViewID(*cell.item.Tool)
			selected = viewID == m.selectedToolID
			if viewID == m.expandedToolID {
				mode = cellRenderFull
			}
		}
		specs = append(specs, semanticCellRenderSpec{cell: cell, mode: mode, selected: selected})
	}
	for _, id := range []string{"tui:compat:verified-writes", "tui:compat:workspace"} {
		if cell := m.staticCells[id]; cell != nil {
			specs = append(specs, semanticCellRenderSpec{cell: cell, mode: cellRenderCompact})
		}
	}
	if m.transcript.hasNewer {
		specs = append(specs, semanticCellRenderSpec{cell: m.staticCell(
			"tui:notice:newer",
			cellStyleMuted,
			"↓ Newer hydrated transcript omitted; press Alt+End to reload latest",
			"",
			false,
		)})
	}
	return specs
}

func redundantNativePlanTool(cell *presentationCell) bool {
	if cell == nil || cell.item.Tool == nil {
		return false
	}
	return redundantNativePlanToolState(*cell.item.Tool)
}

func redundantNativePlanToolState(tool frontend.ToolState) bool {
	return tool.PlanObserved && tool.Status == frontend.ToolSucceeded && tool.Command == nil &&
		len(tool.WriteAudit) == 0
}

func navigableToolStates(tools []frontend.ToolState) []frontend.ToolState {
	visible := make([]frontend.ToolState, 0, len(tools))
	for _, tool := range tools {
		if !redundantNativePlanToolState(tool) {
			visible = append(visible, tool)
		}
	}
	return visible
}

func (m *Model) selectedToolCellID() string {
	for _, cell := range m.cells.ordered {
		if cell.item.Tool != nil && toolViewID(*cell.item.Tool) == m.selectedToolID {
			return cell.item.ID
		}
	}
	return ""
}

// fullTranscriptPanelLines renders the currently hydrated transcript window
// without terminal styling. It intentionally reuses the semantic cells so a
// command's compact preview and complete evidence cannot diverge.
func (m *Model) fullTranscriptPanelLines() []string {
	context := cellRenderContext{Width: max(1, m.width), Theme: m.theme, ColorLevel: cellColorNone}
	lines := []string{"Full transcript · copy-safe plain text · Ctrl+T or Esc closes"}
	if m.transcript.loading {
		lines = append(lines, "[earlier transcript loading]")
	} else if !m.transcript.disabled && (m.transcript.hasOlder || m.snapshot.HasOlderEntries) {
		lines = append(lines, "[earlier transcript omitted; close this panel and press Page Up to load more]")
	}

	liveMessageIDs := make(map[string]struct{}, len(m.cells.ordered))
	for _, cell := range m.cells.ordered {
		if cell.item.Message != nil {
			liveMessageIDs[cell.item.Message.ID] = struct{}{}
		}
	}
	appendCell := func(cell semanticCell) {
		if cell == nil {
			return
		}
		document := cell.Render(context, cellRenderPlain)
		text := strings.Trim(renderCellDocument(document, context, cellRenderPlain), "\n")
		if text == "" {
			return
		}
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(sanitizeTerminalText(text), "\n")...)
	}
	for _, cell := range m.hydratedCells.ordered {
		if cell.item.Message != nil {
			if _, duplicate := liveMessageIDs[cell.item.Message.ID]; duplicate {
				continue
			}
		}
		appendCell(cell)
	}
	for _, cell := range m.cells.ordered {
		if redundantNativePlanTool(cell) {
			continue
		}
		appendCell(cell)
	}
	if m.transcript.hasNewer {
		lines = append(lines, "", "[newer hydrated transcript omitted; close this panel and press Alt+End]")
	}
	return lines
}
