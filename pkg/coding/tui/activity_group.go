package tui

import (
	"strconv"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type activityGroupKind uint8

const (
	activityGroupExploration activityGroupKind = iota + 1
	activityGroupCommands
)

// activityGroupCell is a disposable presentation projection over contiguous
// authoritative tool cells. The underlying cells remain individually ordered
// in the frontend snapshot and in the copy-safe full transcript.
type activityGroupCell struct {
	kind        activityGroupKind
	members     []*presentationCell
	identity    cellIdentity
	renderCache map[cellRenderCacheKey]cellDocument
}

func newActivityGroupCell(kind activityGroupKind, members []*presentationCell) *activityGroupCell {
	cloned := append([]*presentationCell(nil), members...)
	return &activityGroupCell{
		kind: kind, members: cloned, identity: activityGroupIdentity(kind, cloned),
	}
}

func (cell *activityGroupCell) Identity() cellIdentity {
	if cell == nil {
		return cellIdentity{}
	}
	return cell.identity
}

func (cell *activityGroupCell) Render(context cellRenderContext, mode cellRenderMode) cellDocument {
	if cell == nil {
		return cellDocument{}
	}
	context.Width = max(1, context.Width)
	key := cellRenderCacheKey{Context: context, Mode: mode}
	if document, ok := cell.renderCache[key]; ok {
		return document
	}
	var document cellDocument
	switch cell.kind {
	case activityGroupExploration:
		document = cell.explorationDocument()
	case activityGroupCommands:
		document = cell.commandDocument()
	}
	document = wrapCellDocument(document, context.Width)
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	if cell.renderCache == nil {
		cell.renderCache = make(map[cellRenderCacheKey]cellDocument)
	}
	cell.renderCache[key] = document
	return document
}

func (cell *activityGroupCell) explorationDocument() cellDocument {
	active := cell.identity.Lifecycle == frontend.PresentationActive
	title := "• Explored"
	role := cellStyleSuccess
	if active {
		title = "• Exploring"
		role = cellStyleAccent
	}
	lines := []cellLine{styledCellLine(title, role)}
	details, truncated := groupedExplorationDetails(cell.members)
	for index, detail := range details {
		prefix := "    "
		if index == 0 {
			prefix = "  └ "
		}
		lines = append(lines, logicalCellLines(prefix+detail, cellStyleMuted)...)
	}
	if truncated {
		lines = append(lines, styledCellLine("    [… exploration labels bounded …]", cellStyleMuted))
	}
	lines = append(lines, styledCellLine("  ctrl+t to view full transcript", cellStyleMuted))
	return cellDocument{Lines: lines, Truncated: truncated, TruncationVisible: truncated}
}

func (cell *activityGroupCell) commandDocument() cellDocument {
	return cellDocument{Lines: []cellLine{
		styledCellLine("• Ran "+strconv.Itoa(len(cell.members))+" commands", cellStyleSuccess),
		styledCellLine("  ctrl+t to view full transcript", cellStyleMuted),
	}}
}

func groupedExplorationDetails(members []*presentationCell) ([]string, bool) {
	type detailCount struct {
		text  string
		count int
	}
	ordered := make([]detailCount, 0, len(members))
	truncated := false
	for _, member := range members {
		if member == nil || member.item.Tool == nil || member.item.Tool.Exploration == nil {
			continue
		}
		exploration := *member.item.Tool.Exploration
		detail := explorationDetail(exploration)
		truncated = truncated || exploration.Truncated
		if len(ordered) != 0 && ordered[len(ordered)-1].text == detail {
			ordered[len(ordered)-1].count++
			continue
		}
		ordered = append(ordered, detailCount{text: detail, count: 1})
	}
	result := make([]string, 0, len(ordered))
	for _, detail := range ordered {
		text := detail.text
		if detail.count > 1 {
			text += " ×" + strconv.Itoa(detail.count)
		}
		result = append(result, text)
	}
	return result, truncated
}

func activityGroupIdentity(kind activityGroupKind, members []*presentationCell) cellIdentity {
	if len(members) == 0 || members[0] == nil {
		return cellIdentity{}
	}
	first := members[0].Identity()
	lifecycle := frontend.PresentationCompleted
	for _, member := range members {
		if member != nil && member.item.Lifecycle == frontend.PresentationActive {
			lifecycle = frontend.PresentationActive
			break
		}
	}
	prefix := "exploration"
	if kind == activityGroupCommands {
		prefix = "commands"
	}
	return cellIdentity{
		ID:        "tui:group:" + prefix + ":" + first.ID,
		Kind:      frontend.PresentationToolCall,
		Sequence:  first.Sequence,
		Revision:  activityGroupRevision(kind, members),
		Lifecycle: lifecycle,
	}
}

func activityGroupRevision(kind activityGroupKind, members []*presentationCell) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	value := offset ^ uint64(kind)
	for _, member := range members {
		if member == nil {
			value *= prime
			continue
		}
		identity := member.Identity()
		for _, character := range identity.ID {
			value ^= uint64(character)
			value *= prime
		}
		value ^= identity.Revision
		value *= prime
	}
	if value == 0 {
		return 1
	}
	return value
}

func groupedLiveCellSpecs(
	cells []*presentationCell,
) []semanticCellRenderSpec {
	specs := make([]semanticCellRenderSpec, 0, len(cells))
	for index := 0; index < len(cells); {
		cell := cells[index]
		if redundantNativePlanTool(cell) {
			index++
			continue
		}
		if groupableExplorationCell(cell) {
			end := contiguousActivityGroupEnd(
				cells,
				index,
				groupableExplorationCell,
			)
			if end-index > 1 {
				specs = append(specs, semanticCellRenderSpec{
					cell: newActivityGroupCell(activityGroupExploration, cells[index:end]),
				})
				index = end
				continue
			}
		}
		if groupableSuccessfulCommandCell(cell) {
			end := contiguousActivityGroupEnd(
				cells,
				index,
				groupableSuccessfulCommandCell,
			)
			if end-index > 1 {
				specs = append(specs, semanticCellRenderSpec{
					cell: newActivityGroupCell(activityGroupCommands, cells[index:end]),
				})
				index = end
				continue
			}
		}
		specs = append(specs, semanticCellRenderSpec{cell: cell, mode: cellRenderCompact})
		index++
	}
	return specs
}

type activityGroupPredicate func(*presentationCell) bool

func contiguousActivityGroupEnd(
	cells []*presentationCell,
	start int,
	predicate activityGroupPredicate,
) int {
	turnID := cells[start].item.TurnID
	end := start
	for end < len(cells) && cells[end] != nil && cells[end].item.TurnID == turnID &&
		predicate(cells[end]) {
		end++
	}
	return end
}

func groupableExplorationCell(cell *presentationCell) bool {
	if cell == nil || cell.item.Tool == nil || cell.item.Tool.Exploration == nil ||
		cell.item.Tool.Command != nil || len(cell.item.Tool.WriteAudit) != 0 {
		return false
	}
	switch cell.item.Tool.Status {
	case frontend.ToolRunning, frontend.ToolSucceeded:
		return cell.item.Lifecycle == frontend.PresentationActive ||
			cell.item.Lifecycle == frontend.PresentationCompleted
	default:
		return false
	}
}

func groupableSuccessfulCommandCell(cell *presentationCell) bool {
	if cell == nil || cell.item.Tool == nil || cell.item.Tool.Command == nil ||
		len(cell.item.Tool.WriteAudit) != 0 {
		return false
	}
	tool := cell.item.Tool
	command := tool.Command
	action := strings.TrimSpace(command.Action)
	return cell.item.Lifecycle == frontend.PresentationCompleted && tool.Status == frontend.ToolSucceeded &&
		command.Status == frontend.CommandSucceeded && command.OwnsProcess && !command.Background &&
		!command.Orphan && command.Source != frontend.CommandSourceUserShell && (action == "" || action == "run")
}
