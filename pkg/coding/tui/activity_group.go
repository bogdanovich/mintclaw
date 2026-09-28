package tui

import (
	"strconv"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type activityGroupKind uint8

const (
	activityGroupExploration activityGroupKind = iota + 1
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
	}
	document = wrapCellDocument(document, context.Width)
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	cell.renderCache = storeCellRenderDocument(cell.renderCache, key, document)
	return document
}

func (cell *activityGroupCell) explorationDocument() cellDocument {
	active := cell.identity.Lifecycle == frontend.PresentationActive
	title := "• Explored"
	role := cellStyleMuted
	if active {
		title = "• Exploring"
		role = cellStyleAccent
	}
	details, truncated := groupedExplorationDetails(cell.members)
	failed := 0
	for _, detail := range details {
		if detail.status == frontend.ToolFailed || detail.status == frontend.ToolInterrupted {
			failed += detail.count
		}
	}
	// Match Codex's exploration header: keep the activity label neutral and
	// color only the aggregate failure suffix.
	header := statusTitleCellLine(title, role)
	if failed > 0 {
		appendCellSpan(&header.Spans, " · "+strconv.Itoa(failed)+" failed", cellStyleFailure)
	}
	lines := []cellLine{header}
	for index, detail := range details {
		prefix := "    "
		if index == 0 {
			prefix = "  └ "
		}
		lines = append(
			lines,
			explorationDetailWithStatusCellLine(prefix, detail.exploration, detail.status, detail.count),
		)
		if reason := boundedSingleLine(detail.reason, 240); reason != "" &&
			(detail.status == frontend.ToolFailed || detail.status == frontend.ToolInterrupted) {
			lines = append(lines, styledCellLine("      "+reason, cellStyleFailure))
		}
	}
	if truncated {
		lines = append(lines, styledCellLine("    [… exploration labels bounded …]", cellStyleMuted))
	}
	lines = append(lines, styledCellLine("  ctrl+t to view full transcript", cellStyleMuted))
	return cellDocument{Lines: lines, Truncated: truncated, TruncationVisible: truncated}
}

type groupedExplorationDetail struct {
	exploration frontend.ExplorationState
	status      frontend.ToolStatus
	reason      string
	count       int
}

func groupedExplorationDetails(members []*presentationCell) ([]groupedExplorationDetail, bool) {
	type detailCount struct {
		exploration frontend.ExplorationState
		text        string
		status      frontend.ToolStatus
		reason      string
		count       int
	}
	ordered := make([]detailCount, 0, len(members))
	truncated := false
	for _, member := range members {
		if member == nil || member.item.Tool == nil || member.item.Tool.Exploration == nil {
			continue
		}
		exploration := *member.item.Tool.Exploration
		detail := explorationDetail(exploration)
		status := member.item.Tool.Status
		reason := member.item.Tool.Output
		truncated = truncated || exploration.Truncated
		if len(ordered) != 0 && ordered[len(ordered)-1].text == detail &&
			sameGroupedExplorationOutcome(
				ordered[len(ordered)-1].status,
				ordered[len(ordered)-1].reason,
				status,
				reason,
			) {
			ordered[len(ordered)-1].count++
			if status == frontend.ToolRunning {
				ordered[len(ordered)-1].status = status
			}
			continue
		}
		ordered = append(ordered, detailCount{
			exploration: exploration,
			text:        detail,
			status:      status,
			reason:      reason,
			count:       1,
		})
	}
	result := make([]groupedExplorationDetail, 0, len(ordered))
	for _, detail := range ordered {
		result = append(result, groupedExplorationDetail{
			exploration: detail.exploration,
			status:      detail.status,
			reason:      detail.reason,
			count:       detail.count,
		})
	}
	return result, truncated
}

func sameGroupedExplorationOutcome(
	leftStatus frontend.ToolStatus,
	leftReason string,
	rightStatus frontend.ToolStatus,
	rightReason string,
) bool {
	leftOrdinary := leftStatus == frontend.ToolRunning || leftStatus == frontend.ToolSucceeded
	rightOrdinary := rightStatus == frontend.ToolRunning || rightStatus == frontend.ToolSucceeded
	if leftOrdinary || rightOrdinary {
		return leftOrdinary && rightOrdinary
	}
	return leftStatus == rightStatus && leftReason == rightReason
}

func activityGroupIdentity(kind activityGroupKind, members []*presentationCell) cellIdentity {
	if len(members) == 0 || members[0] == nil {
		return cellIdentity{}
	}
	first := members[0].Identity()
	lifecycle := frontend.PresentationCompleted
	for _, member := range members {
		if member == nil {
			continue
		}
		switch member.item.Lifecycle {
		case frontend.PresentationActive:
			lifecycle = frontend.PresentationActive
		case frontend.PresentationFailed, frontend.PresentationInterrupted:
			if lifecycle != frontend.PresentationActive {
				lifecycle = frontend.PresentationFailed
			}
		}
	}
	return cellIdentity{
		ID:        "tui:group:exploration:" + first.ID,
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
	case frontend.ToolRunning, frontend.ToolSucceeded, frontend.ToolFailed, frontend.ToolInterrupted:
		return cell.item.Lifecycle == frontend.PresentationActive ||
			cell.item.Lifecycle == frontend.PresentationCompleted ||
			cell.item.Lifecycle == frontend.PresentationFailed ||
			cell.item.Lifecycle == frontend.PresentationInterrupted
	default:
		return false
	}
}
