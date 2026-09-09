package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

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
		reflect.DeepEqual(left.Tool, right.Tool) && reflect.DeepEqual(left.Plan, right.Plan) &&
		reflect.DeepEqual(left.Compaction, right.Compaction) && reflect.DeepEqual(left.Turn, right.Turn)
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
	if item.Compaction != nil {
		payloads++
	}
	if item.Turn != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("presentation item %q has %d typed payloads", item.ID, payloads)
	}
	switch item.Kind {
	case frontend.PresentationUserMessage,
		frontend.PresentationAssistantMessage,
		frontend.PresentationFinalAnswer,
		frontend.PresentationReasoning,
		frontend.PresentationToolMessage,
		frontend.PresentationWarning,
		frontend.PresentationError:
		if item.Message == nil {
			return fmt.Errorf("presentation item %q kind %q requires a message payload", item.ID, item.Kind)
		}
		if !presentationMessageMatchesKind(item.Kind, *item.Message) {
			return fmt.Errorf("presentation item %q kind %q contradicts its message payload", item.ID, item.Kind)
		}
	case frontend.PresentationToolCall:
		if item.Tool == nil {
			return fmt.Errorf("presentation item %q kind %q requires a tool payload", item.ID, item.Kind)
		}
	case frontend.PresentationPlanUpdate:
		if item.Plan == nil {
			return fmt.Errorf("presentation item %q kind %q requires a plan payload", item.ID, item.Kind)
		}
	case frontend.PresentationCompaction:
		if item.Compaction == nil {
			return fmt.Errorf("presentation item %q kind %q requires a compaction payload", item.ID, item.Kind)
		}
		if !compactionLifecycleMatches(item.Lifecycle, item.Compaction.Status) {
			return fmt.Errorf("presentation item %q compaction lifecycle contradicts its status", item.ID)
		}
	case frontend.PresentationTurnSeparator:
		if item.Turn == nil {
			return fmt.Errorf("presentation item %q kind %q requires a turn payload", item.ID, item.Kind)
		}
		if !turnLifecycleMatches(item.Lifecycle, item.Turn.Outcome) {
			return fmt.Errorf("presentation item %q turn lifecycle contradicts its outcome", item.ID)
		}
	default:
		return fmt.Errorf("presentation item %q has unsupported kind %q", item.ID, item.Kind)
	}
	return nil
}

func presentationMessageMatchesKind(kind frontend.PresentationKind, message frontend.TranscriptEntry) bool {
	switch kind {
	case frontend.PresentationUserMessage:
		return message.Kind == frontend.EntryUser && message.Phase == ""
	case frontend.PresentationAssistantMessage:
		return message.Kind == frontend.EntryAssistant && message.Phase != frontend.AssistantPhaseFinal
	case frontend.PresentationFinalAnswer:
		return message.Kind == frontend.EntryAssistant && message.Phase == frontend.AssistantPhaseFinal
	case frontend.PresentationReasoning:
		return message.Kind == frontend.EntryReasoning && message.Phase == ""
	case frontend.PresentationToolMessage:
		return message.Kind == frontend.EntryTool && message.Phase == ""
	case frontend.PresentationWarning:
		return message.Kind == frontend.EntryWarning && message.Phase == ""
	case frontend.PresentationError:
		return message.Kind == frontend.EntryError && message.Phase == ""
	default:
		return false
	}
}

func compactionLifecycleMatches(
	lifecycle frontend.PresentationLifecycle,
	status frontend.CompactionStatus,
) bool {
	switch status {
	case frontend.CompactionRunning, frontend.CompactionProgress:
		return lifecycle == frontend.PresentationActive
	case frontend.CompactionCompleted, frontend.CompactionNoProgress:
		return lifecycle == frontend.PresentationCompleted
	case frontend.CompactionInterrupted:
		return lifecycle == frontend.PresentationInterrupted
	case frontend.CompactionFailed:
		return lifecycle == frontend.PresentationFailed
	default:
		return false
	}
}

func turnLifecycleMatches(lifecycle frontend.PresentationLifecycle, outcome frontend.TurnOutcome) bool {
	switch outcome {
	case frontend.TurnOutcomeCompleted:
		return lifecycle == frontend.PresentationCompleted
	case frontend.TurnOutcomeFailed:
		return lifecycle == frontend.PresentationFailed
	case frontend.TurnOutcomeInterrupted:
		return lifecycle == frontend.PresentationInterrupted
	default:
		return false
	}
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
	items := make([]frontend.PresentationItem, 0, len(entries)*2)
	var currentTurnID string
	var turnStartedAt time.Time
	turnHadConcreteWork := false
	for index, source := range entries {
		entry := source
		if strings.TrimSpace(entry.ID) == "" {
			entry.ID = fmt.Sprintf("hydrated-entry-%d", index)
		}
		if entry.RootTurnStart {
			currentTurnID = "history-turn:" + entry.ID
			turnStartedAt = entry.OccurredAt
			turnHadConcreteWork = false
		} else if currentTurnID == "" {
			currentTurnID = "history-open:" + entry.TurnID
			turnStartedAt = entry.OccurredAt
		}
		entry.TurnID = currentTurnID
		entry.Complete = true
		turnHadConcreteWork = turnHadConcreteWork || entry.ConcreteWork || entry.Kind == frontend.EntryTool
		if entry.EvidenceOnly {
			continue
		}
		if entry.Kind == frontend.EntryAssistant && entry.Phase == frontend.AssistantPhaseFinal &&
			turnHadConcreteWork {
			completedAt := entry.OccurredAt
			startedAt := turnStartedAt
			duration := time.Duration(0)
			if !startedAt.IsZero() && !completedAt.IsZero() && !completedAt.Before(startedAt) {
				duration = completedAt.Sub(startedAt)
			} else {
				startedAt = completedAt
			}
			sequence := uint64(len(items) + 1)
			boundary := frontend.PresentationItem{
				ID:        "hydrated-turn-boundary:" + entry.ID,
				TurnID:    currentTurnID,
				Sequence:  sequence,
				Revision:  1,
				Kind:      frontend.PresentationTurnSeparator,
				Lifecycle: frontend.PresentationCompleted,
				StartedAt: startedAt,
				Duration:  duration,
				Turn:      &frontend.TurnBoundaryState{Outcome: frontend.TurnOutcomeCompleted},
			}
			if !completedAt.IsZero() {
				boundary.CompletedAt = &completedAt
			}
			items = append(items, boundary)
		}
		kind := hydratedPresentationKind(entry)
		sequence := uint64(len(items) + 1)
		completedAt := entry.OccurredAt
		item := frontend.PresentationItem{
			ID: entry.ID, TurnID: currentTurnID, Sequence: sequence, Revision: 1,
			Kind: kind, Lifecycle: frontend.PresentationCompleted, CreatedAt: entry.OccurredAt,
			StartedAt: entry.OccurredAt, Message: &entry,
		}
		if !completedAt.IsZero() {
			item.CompletedAt = &completedAt
		}
		items = append(items, item)
	}
	return newSemanticCellStore(items)
}

func hydratedPresentationKind(entry frontend.TranscriptEntry) frontend.PresentationKind {
	switch entry.Kind {
	case frontend.EntryUser:
		return frontend.PresentationUserMessage
	case frontend.EntryAssistant:
		if entry.Phase == frontend.AssistantPhaseFinal {
			return frontend.PresentationFinalAnswer
		}
		return frontend.PresentationAssistantMessage
	case frontend.EntryReasoning:
		return frontend.PresentationReasoning
	case frontend.EntryTool:
		return frontend.PresentationToolMessage
	case frontend.EntryWarning:
		return frontend.PresentationWarning
	case frontend.EntryError:
		return frontend.PresentationError
	default:
		return frontend.PresentationError
	}
}

func (m *Model) reconcileStaticCells(state frontend.ThreadSnapshot) {
	if state.Workspace != nil {
		entry := workspaceChangesEntry(*state.Workspace)
		m.staticCell(
			"tui:workspace",
			cellStyleAccent,
			entry.label,
			entry.text,
			entry.truncated,
		)
	} else {
		delete(m.staticCells, "tui:workspace")
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
	liveSpecs := groupedLiveCellSpecs(m.cells.ordered)
	if cell := m.staticCells["tui:workspace"]; cell != nil {
		liveSpecs = insertWorkspaceBeforeTurnCompletion(
			liveSpecs,
			semanticCellRenderSpec{cell: cell, mode: cellRenderCompact},
		)
	}
	specs = append(specs, liveSpecs...)
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

// insertWorkspaceBeforeTurnCompletion keeps the terminal separator and final
// response at the end of the turn. A failed or interrupted turn can end with
// the separator alone. Historical hydrated turns are unaffected because the
// current workspace describes only the live snapshot.
func insertWorkspaceBeforeTurnCompletion(
	specs []semanticCellRenderSpec,
	workspace semanticCellRenderSpec,
) []semanticCellRenderSpec {
	index := len(specs)
	if index > 0 {
		terminal, ok := specs[index-1].cell.(*presentationCell)
		switch {
		case ok && terminal.item.Kind == frontend.PresentationFinalAnswer:
			index--
			if index > 0 {
				boundary, boundaryOK := specs[index-1].cell.(*presentationCell)
				if boundaryOK && boundary.item.Kind == frontend.PresentationTurnSeparator &&
					boundary.item.TurnID == terminal.item.TurnID {
					index--
				}
			}
		case ok && terminal.item.Kind == frontend.PresentationTurnSeparator:
			index--
		}
	}
	specs = append(specs, semanticCellRenderSpec{})
	copy(specs[index+1:], specs[index:])
	specs[index] = workspace
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

// transcriptOverlayLines renders the currently hydrated transcript window
// without terminal styling. Stable line keys preserve selection when earlier
// history is prepended. Semantic cells keep compact previews and full evidence
// from diverging.
func (m *Model) transcriptOverlayLines() []transcriptOverlayLine {
	// Projection payloads are bounded to 64 KiB by default. Render well above
	// that bound so logical line identity never depends on terminal width; the
	// overlay applies its own grapheme-aware visual wrapping below.
	const logicalRenderWidth = 1 << 20
	context := cellRenderContext{Width: logicalRenderWidth, Theme: m.theme, ColorLevel: cellColorNone}
	visualWidth := max(1, m.width-2)
	lines := make([]transcriptOverlayLine, 0, len(m.cells.ordered)*2)
	if m.transcript.loading {
		lines = appendTranscriptOverlayLogicalLine(
			lines,
			"notice:loading",
			"[earlier transcript loading]",
			visualWidth,
		)
	} else if !m.transcript.disabled && (m.transcript.hasOlder || m.snapshot.HasOlderEntries) {
		lines = appendTranscriptOverlayLogicalLine(
			lines,
			"notice:older",
			"[earlier transcript omitted; press Page Up at the top to load more]",
			visualWidth,
		)
	}

	liveMessageIDs := make(map[string]struct{}, len(m.cells.ordered))
	for _, cell := range m.cells.ordered {
		if cell.item.Message != nil {
			liveMessageIDs[cell.item.Message.ID] = struct{}{}
		}
	}
	emittedCells := 0
	appendCell := func(cell semanticCell) {
		if cell == nil {
			return
		}
		document := cell.Render(context, cellRenderPlain)
		text := strings.Trim(renderCellDocument(document, context, cellRenderPlain), "\n")
		if text == "" {
			return
		}
		identity := cell.Identity().ID
		if emittedCells > 0 {
			lines = appendTranscriptOverlayLogicalLine(lines, "gap:"+identity, "", visualWidth)
		}
		for index, logical := range strings.Split(sanitizeTerminalText(text), "\n") {
			lines = appendTranscriptOverlayLogicalLine(
				lines,
				fmt.Sprintf("cell:%s:%d", identity, index),
				logical,
				visualWidth,
			)
		}
		emittedCells++
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
		lines = appendTranscriptOverlayLogicalLine(lines, "gap:notice:newer", "", visualWidth)
		lines = appendTranscriptOverlayLogicalLine(
			lines,
			"notice:newer",
			"[newer hydrated transcript omitted; press End to reload latest]",
			visualWidth,
		)
	}
	if len(lines) == 0 {
		lines = appendTranscriptOverlayLogicalLine(lines, "notice:empty", "[transcript is empty]", visualWidth)
	}
	return lines
}

func appendTranscriptOverlayLogicalLine(
	lines []transcriptOverlayLine,
	key string,
	logicalText string,
	width int,
) []transcriptOverlayLine {
	logicalText = sanitizeTerminalText(logicalText)
	width = max(1, width)
	if logicalText == "" {
		return append(lines, transcriptOverlayLine{key: key, logicalText: logicalText})
	}
	start := 0
	lineStart := 0
	lineWidth := 0
	var visual strings.Builder
	flush := func(end int) {
		lines = append(lines, transcriptOverlayLine{
			key: key, text: visual.String(), logicalText: logicalText, start: lineStart, end: end,
		})
		visual.Reset()
		lineStart = end
		lineWidth = 0
	}
	for start < len(logicalText) {
		cluster, clusterWidth := ansi.FirstGraphemeCluster(logicalText[start:], ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		end := start + len(cluster)
		if lineWidth > 0 && lineWidth+clusterWidth > width {
			flush(start)
		}
		if clusterWidth > width {
			visual.WriteRune('�')
			lineWidth++
		} else {
			visual.WriteString(cluster)
			lineWidth += clusterWidth
		}
		start = end
	}
	if visual.Len() > 0 || lineStart == len(logicalText) {
		flush(len(logicalText))
	}
	return lines
}
