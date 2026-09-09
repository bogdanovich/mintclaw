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

type reservedTurnBoundary struct {
	TurnID   string
	Sequence uint64
}

func (p *Projector) upsertEntry(state *ThreadSnapshot, entry TranscriptEntry) (PresentationItem, bool) {
	entry = p.boundedEntry(entry)
	message := entry
	return p.upsertPresentationItem(state, PresentationItem{
		ID:        messagePresentationID(entry),
		TurnID:    entry.TurnID,
		Kind:      presentationKindForEntry(entry),
		Lifecycle: presentationLifecycleForEntry(entry),
		Message:   &message,
	})
}

func (p *Projector) upsertCommittedEntry(
	state *ThreadSnapshot,
	entry TranscriptEntry,
) (PresentationItem, bool) {
	item, changed := p.upsertEntry(state, entry)
	if changed && len(p.activeStreamOwners) != 0 {
		p.captureRollbackCommittedMessages(state.Items, item.ID)
		p.rebuildStreamMessageProjection(state, item.ID)
	}
	return item, changed
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

func (p *Projector) upsertPlan(state *ThreadSnapshot, turnID string, plan PlanState) PresentationItem {
	plan = clonePlan(plan)
	item, _ := p.upsertPresentationItem(state, PresentationItem{
		ID:        planPresentationID(turnID, plan.CallID),
		TurnID:    turnID,
		Kind:      PresentationPlanUpdate,
		Lifecycle: PresentationCompleted,
		Plan:      &plan,
	})
	return item
}

func (p *Projector) upsertCompaction(state *ThreadSnapshot, compaction CompactionState) PresentationItem {
	turnID := compaction.TurnID
	if turnID == "" {
		turnID = presentationTurnID("compaction:" + compaction.AttemptID)
	}
	copy := compaction
	item, _ := p.upsertPresentationItem(state, PresentationItem{
		ID:         compactionPresentationID(compaction.AttemptID),
		TurnID:     turnID,
		Kind:       PresentationCompaction,
		Lifecycle:  presentationLifecycleForCompaction(compaction.Status),
		Duration:   max(time.Duration(0), compaction.Duration),
		Compaction: &copy,
	})
	return item
}

func (p *Projector) finishTurnPresentation(
	state *ThreadSnapshot,
	turnID string,
	outcome TurnOutcome,
) {
	if presentationItemIndex(state.Items, turnBoundaryPresentationID(turnID)) >= 0 {
		p.clearTurnBoundaryReservations(turnID)
		return
	}
	if outcome == TurnOutcomeSuspended {
		p.clearTurnBoundaryReservations(turnID)
		return
	}
	hadConcreteWork := p.turnHadConcreteWork[turnID]
	if !hadConcreteWork {
		for _, item := range state.Items {
			if item.TurnID == turnID && (item.Tool != nil || item.Compaction != nil) {
				hadConcreteWork = true
				break
			}
		}
	}
	if !hadConcreteWork {
		p.clearTurnBoundaryReservations(turnID)
		return
	}

	completedAt := p.presentationNow()
	startedAt := p.turnStartedAt[turnID]
	if startedAt.IsZero() || startedAt.After(completedAt) {
		startedAt = completedAt
	}
	duration := max(time.Duration(0), completedAt.Sub(startedAt))
	completedAtCopy := completedAt
	sequence := uint64(0)
	for _, item := range state.Items {
		if item.TurnID != turnID || item.Message == nil || item.Message.Kind != EntryAssistant ||
			item.Message.Phase != AssistantPhaseFinal {
			continue
		}
		if reserved, ok := p.reservedTurnBoundaries[item.ID]; ok {
			sequence = reserved.Sequence
		}
	}
	p.insertTurnBoundary(state, PresentationItem{
		ID:          turnBoundaryPresentationID(turnID),
		TurnID:      turnID,
		Kind:        PresentationTurnSeparator,
		Lifecycle:   presentationLifecycleForTurn(outcome),
		Duration:    duration,
		Turn:        &TurnBoundaryState{Outcome: outcome},
		CreatedAt:   startedAt,
		StartedAt:   startedAt,
		CompletedAt: &completedAtCopy,
	}, sequence)
	p.clearTurnBoundaryReservations(turnID)
}

func (p *Projector) insertTurnBoundary(
	state *ThreadSnapshot,
	boundary PresentationItem,
	reservedSequence uint64,
) {
	if index := presentationItemIndex(state.Items, boundary.ID); index >= 0 {
		p.upsertPresentationItem(state, boundary)
		return
	}
	if reservedSequence == 0 {
		reservedSequence = p.allocateSequence()
	}
	boundary.Sequence = reservedSequence
	boundary.Revision = 1
	state.Items = append(state.Items, clonePresentationItem(boundary))
	slices.SortFunc(state.Items, func(left, right PresentationItem) int {
		return intCompare(left.Sequence, right.Sequence)
	})
	p.enforcePresentationBounds(state, boundary.ID)
	p.pruneTurnOrderingState(state)
	p.syncCompatibilityProjection(state)
}

func (p *Projector) clearTurnBoundaryReservations(turnID string) {
	for id, reserved := range p.reservedTurnBoundaries {
		if reserved.TurnID == turnID {
			delete(p.reservedTurnBoundaries, id)
		}
	}
}

func (p *Projector) upsertPresentationItem(
	state *ThreadSnapshot,
	replacement PresentationItem,
) (PresentationItem, bool) {
	if replacement.Message != nil && len(p.activeStreamOwners) != 0 {
		p.captureRollbackCommittedMessages(state.Items, "")
	}
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
	if item.Message != nil && item.Message.Kind == EntryAssistant {
		p.reservedTurnBoundaries[item.ID] = reservedTurnBoundary{
			TurnID:   item.TurnID,
			Sequence: p.allocateSequence(),
		}
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
	for turnID := range p.turnStartedAt {
		if _, visible := represented[turnID]; !visible && turnID != p.activeTurnID {
			delete(p.turnStartedAt, turnID)
			delete(p.turnHadConcreteWork, turnID)
			p.clearTurnBoundaryReservations(turnID)
		}
	}
	for id, turnID := range p.deferredAssistantItems {
		if presentationItemIndex(state.Items, id) < 0 {
			delete(p.deferredAssistantItems, id)
			continue
		}
		if _, visible := represented[turnID]; !visible && turnID != p.activeTurnID {
			delete(p.deferredAssistantItems, id)
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
	for observationPresentationPayloadCount(state.Items) > p.limits.Observations {
		index := oldestObservationPresentationPayload(state.Items, protectedID)
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

func oldestObservationPresentationPayload(items []PresentationItem, protectedID string) int {
	fallback := -1
	for index, item := range items {
		if item.Plan == nil && item.Compaction == nil && item.Turn == nil {
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

func presentationKindForEntry(entry TranscriptEntry) PresentationKind {
	switch entry.Kind {
	case EntryUser:
		return PresentationUserMessage
	case EntryAssistant:
		if entry.Phase == AssistantPhaseFinal {
			return PresentationFinalAnswer
		}
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

func presentationLifecycleForCompaction(status CompactionStatus) PresentationLifecycle {
	switch status {
	case CompactionRunning, CompactionProgress:
		return PresentationActive
	case CompactionCompleted, CompactionNoProgress:
		return PresentationCompleted
	case CompactionInterrupted:
		return PresentationInterrupted
	case CompactionFailed:
		return PresentationFailed
	default:
		return PresentationUnknown
	}
}

func presentationLifecycleForTurn(outcome TurnOutcome) PresentationLifecycle {
	switch outcome {
	case TurnOutcomeCompleted:
		return PresentationCompleted
	case TurnOutcomeFailed:
		return PresentationFailed
	case TurnOutcomeInterrupted:
		return PresentationInterrupted
	case TurnOutcomeSuspended:
		return PresentationSuspended
	default:
		return PresentationUnknown
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
		reflect.DeepEqual(left.Message, right.Message) && reflect.DeepEqual(left.Tool, right.Tool) &&
		reflect.DeepEqual(left.Plan, right.Plan) && reflect.DeepEqual(left.Compaction, right.Compaction) &&
		reflect.DeepEqual(left.Turn, right.Turn)
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

func observationPresentationPayloadCount(items []PresentationItem) int {
	count := 0
	for _, item := range items {
		if item.Plan != nil || item.Compaction != nil || item.Turn != nil {
			count++
		}
	}
	return count
}

func latestPresentationPlan(items []PresentationItem) *PlanState {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].Plan == nil {
			continue
		}
		plan := clonePlan(*items[index].Plan)
		return &plan
	}
	return nil
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

func planPresentationID(turnID, callID string) string {
	return encodedPresentationID("plan", turnID, callID)
}

func compactionPresentationID(attemptID string) string {
	return encodedPresentationID("compaction", attemptID)
}

func turnBoundaryPresentationID(turnID string) string {
	return encodedPresentationID("turn", turnID)
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
	if item.Plan != nil {
		plan := clonePlan(*item.Plan)
		item.Plan = &plan
	}
	if item.Compaction != nil {
		compaction := *item.Compaction
		item.Compaction = &compaction
	}
	if item.Turn != nil {
		turn := *item.Turn
		item.Turn = &turn
	}
	return item
}
