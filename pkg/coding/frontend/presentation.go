package frontend

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPresentationIdentityBytes = 1024

func (p *Projector) upsertEntry(state *ThreadSnapshot, entry TranscriptEntry) (PresentationItem, bool) {
	entry = p.boundedEntry(entry)
	message := entry
	return p.upsertPresentationItem(state, PresentationItem{
		ID:        messagePresentationID(entry),
		TurnID:    entry.TurnID,
		Kind:      presentationKindForEntry(entry.Kind),
		Lifecycle: presentationLifecycleForEntry(entry),
		Message:   &message,
	})
}

func (p *Projector) upsertTool(state *ThreadSnapshot, tool ToolState) PresentationItem {
	tool = p.boundedTool(tool)
	tool = cloneTool(tool)
	item, _ := p.upsertPresentationItem(state, PresentationItem{
		ID:        toolPresentationID(tool.TurnID, tool.CallID),
		TurnID:    tool.TurnID,
		Kind:      PresentationToolCall,
		Lifecycle: presentationLifecycleForTool(tool.Status),
		Duration:  tool.Duration,
		Tool:      &tool,
	})
	return item
}

func (p *Projector) upsertPresentationItem(
	state *ThreadSnapshot,
	replacement PresentationItem,
) (PresentationItem, bool) {
	index := presentationItemIndex(state.Items, replacement.ID)
	inserted := index < 0
	if index >= 0 {
		current := state.Items[index]
		if current.Message != nil && presentationLifecycleTerminal(current.Lifecycle) &&
			replacement.Lifecycle == PresentationActive {
			return clonePresentationItem(current), false
		}
		replacement.Sequence = current.Sequence
		replacement.CreatedAt = current.CreatedAt
		replacement.StartedAt = current.StartedAt
		replacement = p.withPresentationTiming(replacement, &current)
		if presentationVisibleEqual(current, replacement) {
			return clonePresentationItem(current), false
		}
		replacement.Revision = current.Revision + 1
		state.Items[index] = clonePresentationItem(replacement)
	} else {
		replacement.Sequence = p.sequenceForNewItem(state, replacement)
		replacement.Revision = 1
		replacement = p.withPresentationTiming(replacement, nil)
		state.Items = append(state.Items, clonePresentationItem(replacement))
		slices.SortFunc(state.Items, func(left, right PresentationItem) int {
			return intCompare(left.Sequence, right.Sequence)
		})
	}
	protectedID := ""
	if inserted {
		protectedID = replacement.ID
	}
	p.enforcePresentationBounds(state, protectedID)
	p.pruneTurnOrderingState(state)
	p.syncCompatibilityProjection(state)
	return clonePresentationItem(replacement), true
}

func (p *Projector) withPresentationTiming(
	item PresentationItem,
	current *PresentationItem,
) PresentationItem {
	now := p.presentationNow()
	if current == nil {
		item.CreatedAt = now
		item.StartedAt = now
	} else {
		item.CreatedAt = current.CreatedAt
		item.StartedAt = current.StartedAt
	}

	if item.Lifecycle == PresentationActive || item.Lifecycle == PresentationUnknown {
		item.CompletedAt = nil
		if current != nil && current.Lifecycle == PresentationSuspended && item.Lifecycle == PresentationActive {
			item.Duration = 0
		}
		return item
	}

	if current != nil && current.Lifecycle == item.Lifecycle && current.CompletedAt != nil {
		completedAt := *current.CompletedAt
		item.CompletedAt = &completedAt
		if item.Duration == 0 {
			item.Duration = current.Duration
		}
		return item
	}
	completedAt := now
	item.CompletedAt = &completedAt
	if item.Duration == 0 && !item.StartedAt.IsZero() {
		item.Duration = max(time.Duration(0), now.Sub(item.StartedAt))
	}
	return item
}

func (p *Projector) sequenceForNewItem(state *ThreadSnapshot, item PresentationItem) uint64 {
	if item.Message != nil && item.Message.Kind == EntryUser {
		if sequence, exists := p.reservedUserSequences[item.TurnID]; exists {
			delete(p.reservedUserSequences, item.TurnID)
			return sequence
		}
		return p.allocateSequence()
	}
	if _, started := p.startedTurns[item.TurnID]; !started &&
		!turnHasUserMessage(state.Items, item.TurnID) {
		if _, reserved := p.reservedUserSequences[item.TurnID]; !reserved {
			p.reservedUserSequences[item.TurnID] = p.allocateSequence()
		}
	}
	return p.allocateSequence()
}

func (p *Projector) allocateSequence() uint64 {
	p.nextSequence++
	return p.nextSequence
}

func (p *Projector) markTurnStarted(turnID string) {
	if _, exists := p.startedTurns[turnID]; exists {
		return
	}
	p.nextTurnOrder++
	p.startedTurns[turnID] = p.nextTurnOrder
}

func (p *Projector) pruneTurnOrderingState(state *ThreadSnapshot) {
	represented := make(map[string]struct{}, len(state.Items))
	for _, item := range state.Items {
		represented[item.TurnID] = struct{}{}
	}
	for turnID := range p.reservedUserSequences {
		if _, visible := represented[turnID]; !visible {
			delete(p.reservedUserSequences, turnID)
		}
	}
	for turnID := range p.startedTurns {
		if _, visible := represented[turnID]; !visible && turnID != p.activeTurnID {
			delete(p.startedTurns, turnID)
		}
	}
}

func (p *Projector) enforcePresentationBounds(state *ThreadSnapshot, protectedID string) {
	for presentationPayloadCount(state.Items, true) > p.limits.Entries {
		state.HasOlderEntries = true
		index := oldestPresentationPayload(state.Items, true, protectedID)
		state.Items = slices.Delete(state.Items, index, index+1)
	}
	for presentationPayloadCount(state.Items, false) > p.limits.Tools {
		index := oldestPresentationPayload(state.Items, false, protectedID)
		state.Items = slices.Delete(state.Items, index, index+1)
	}
}

func oldestPresentationPayload(items []PresentationItem, messages bool, protectedID string) int {
	fallback := -1
	for index, item := range items {
		matches := (messages && item.Message != nil) || (!messages && item.Tool != nil)
		if !matches {
			continue
		}
		if fallback < 0 {
			fallback = index
		}
		if item.ID != protectedID {
			return index
		}
	}
	return fallback
}

func (p *Projector) syncCompatibilityProjection(state *ThreadSnapshot) {
	entries := make([]TranscriptEntry, 0, min(len(state.Items), p.limits.Entries))
	tools := make([]ToolState, 0, min(len(state.Items), p.limits.Tools))
	for _, item := range state.Items {
		if item.Message != nil {
			entries = append(entries, *item.Message)
		}
		if item.Tool != nil {
			tools = append(tools, cloneTool(*item.Tool))
		}
	}
	state.Entries = entries
	state.Tools = tools
}

func (p *Projector) presentationNow() time.Time {
	return p.now().UTC().Round(0)
}

func presentationKindForEntry(kind EntryKind) PresentationKind {
	switch kind {
	case EntryUser:
		return PresentationUserMessage
	case EntryAssistant:
		return PresentationAssistantMessage
	case EntryReasoning:
		return PresentationReasoning
	case EntryTool:
		return PresentationToolMessage
	case EntryWarning:
		return PresentationWarning
	case EntryError:
		return PresentationError
	default:
		return PresentationError
	}
}

func presentationLifecycleForEntry(entry TranscriptEntry) PresentationLifecycle {
	if entry.Kind == EntryError {
		return PresentationFailed
	}
	if entry.Complete {
		return PresentationCompleted
	}
	return PresentationActive
}

func presentationLifecycleForTool(status ToolStatus) PresentationLifecycle {
	switch status {
	case ToolRunning:
		return PresentationActive
	case ToolSucceeded:
		return PresentationCompleted
	case ToolFailed:
		return PresentationFailed
	case ToolInterrupted:
		return PresentationInterrupted
	case ToolSuspended:
		return PresentationSuspended
	default:
		return PresentationUnknown
	}
}

func presentationLifecycleTerminal(lifecycle PresentationLifecycle) bool {
	return lifecycle == PresentationCompleted || lifecycle == PresentationFailed ||
		lifecycle == PresentationInterrupted
}

func terminalToolStatus(status ToolStatus) bool {
	return status == ToolSucceeded || status == ToolFailed || status == ToolInterrupted
}

func presentationVisibleEqual(left, right PresentationItem) bool {
	return left.Kind == right.Kind && left.Lifecycle == right.Lifecycle && left.Duration == right.Duration &&
		reflect.DeepEqual(left.Message, right.Message) && reflect.DeepEqual(left.Tool, right.Tool)
}

func presentationItemIndex(items []PresentationItem, id string) int {
	for index := range items {
		if items[index].ID == id {
			return index
		}
	}
	return -1
}

func toolFromPresentationItems(items []PresentationItem, turnID, callID string) ToolState {
	index := presentationItemIndex(items, toolPresentationID(turnID, callID))
	if index < 0 || items[index].Tool == nil {
		return ToolState{}
	}
	return cloneTool(*items[index].Tool)
}

func presentationPayloadCount(items []PresentationItem, messages bool) int {
	count := 0
	for _, item := range items {
		if (messages && item.Message != nil) || (!messages && item.Tool != nil) {
			count++
		}
	}
	return count
}

func turnHasUserMessage(items []PresentationItem, turnID string) bool {
	for _, item := range items {
		if item.TurnID == turnID && item.Message != nil && item.Message.Kind == EntryUser {
			return true
		}
	}
	return false
}

func messagePresentationID(entry TranscriptEntry) string {
	return encodedPresentationID("message", entry.TurnID, entry.ID)
}

func toolPresentationID(turnID, callID string) string {
	return encodedPresentationID("tool", turnID, callID)
}

func encodedPresentationID(kind string, parts ...string) string {
	var result strings.Builder
	result.WriteString(kind)
	for _, part := range parts {
		result.WriteByte(':')
		result.WriteString(strconv.Itoa(len(part)))
		result.WriteByte(':')
		result.WriteString(part)
	}
	return boundPresentationIdentity(result.String())
}

func presentationTurnID(turnID string) string {
	return boundPresentationIdentity(normalizeTurnID(turnID))
}

// boundPresentationIdentity keeps identity-bearing snapshot fields small and
// valid UTF-8. Escaping short raw values that share the internal prefix keeps
// the raw, digest, and invalid-byte domains disjoint.
func boundPresentationIdentity(identity string) string {
	if !utf8.ValidString(identity) {
		digest := sha256.Sum256([]byte(identity))
		return "~b:" + hex.EncodeToString(digest[:])
	}
	if len(identity) > maxPresentationIdentityBytes {
		digest := sha256.Sum256([]byte(identity))
		return "~h:" + hex.EncodeToString(digest[:])
	}
	if strings.HasPrefix(identity, "~") {
		return "~r:" + identity
	}
	return identity
}

func intCompare(left, right uint64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func clonePresentationItems(items []PresentationItem) []PresentationItem {
	items = slices.Clone(items)
	for index := range items {
		items[index] = clonePresentationItem(items[index])
	}
	return items
}

func clonePresentationItem(item PresentationItem) PresentationItem {
	if item.CompletedAt != nil {
		completedAt := *item.CompletedAt
		item.CompletedAt = &completedAt
	}
	if item.Message != nil {
		message := *item.Message
		item.Message = &message
	}
	if item.Tool != nil {
		tool := cloneTool(*item.Tool)
		item.Tool = &tool
	}
	return item
}
