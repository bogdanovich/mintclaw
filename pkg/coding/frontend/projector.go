package frontend

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	defaultEntryLimit = 256
	defaultToolLimit  = 128
	defaultDeltaLimit = 512
	defaultTextBytes  = 64 << 10
)

type ProjectionLimits struct {
	Entries   int
	Tools     int
	Deltas    int
	TextBytes int
}

func (l ProjectionLimits) normalized() ProjectionLimits {
	if l.Entries <= 0 {
		l.Entries = defaultEntryLimit
	}
	if l.Tools <= 0 {
		l.Tools = defaultToolLimit
	}
	if l.Deltas <= 0 {
		l.Deltas = defaultDeltaLimit
	}
	if l.TextBytes <= 0 {
		l.TextBytes = defaultTextBytes
	}
	return l
}

// Projector owns a bounded UI projection. Canonical transcript and tool audit
// persistence remain outside this type.
type Projector struct {
	mu          sync.RWMutex
	limits      ProjectionLimits
	state       ThreadSnapshot
	deltas      []Delta
	nextWatcher uint64
	watchers    map[uint64]chan Delta
}

func NewProjector(threadID string, limits ProjectionLimits) (*Projector, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("coding frontend thread ID is required")
	}
	return &Projector{
		limits: limits.normalized(),
		state: ThreadSnapshot{
			ProtocolVersion: ProtocolVersion,
			ThreadID:        threadID,
			Activity:        ActivityIdle,
		},
		watchers: make(map[uint64]chan Delta),
	}, nil
}

func (p *Projector) Snapshot(ctx context.Context) (ThreadSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return ThreadSnapshot{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneSnapshot(p.state), nil
}

func (p *Projector) ChangesSince(ctx context.Context, revision Revision) ([]Delta, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if revision == p.state.Revision {
		return []Delta{}, nil
	}
	if revision > p.state.Revision || len(p.deltas) == 0 {
		return nil, ErrRevisionUnavailable
	}
	start := -1
	for i := range p.deltas {
		if p.deltas[i].PreviousRevision == revision {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, ErrRevisionUnavailable
	}
	result := make([]Delta, len(p.deltas)-start)
	for i := range result {
		result[i] = cloneDelta(p.deltas[start+i])
	}
	return result, nil
}

// Watch atomically queues retained changes after revision and then publishes
// live deltas. Delivery is bounded and non-blocking: a slow consumer detects a
// revision gap and resynchronizes from Snapshot instead of blocking a turn.
func (p *Projector) Watch(ctx context.Context, revision Revision) (<-chan Delta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if revision > p.state.Revision {
		p.mu.Unlock()
		return nil, ErrRevisionUnavailable
	}
	retained, ok := p.changesSinceLocked(revision)
	if !ok {
		p.mu.Unlock()
		return nil, ErrRevisionUnavailable
	}
	p.nextWatcher++
	id := p.nextWatcher
	channel := make(chan Delta, p.limits.Deltas+1)
	for i := range retained {
		channel <- cloneDelta(retained[i])
	}
	p.watchers[id] = channel
	p.mu.Unlock()

	go func() {
		<-ctx.Done()
		p.mu.Lock()
		if current, exists := p.watchers[id]; exists {
			delete(p.watchers, id)
			close(current)
		}
		p.mu.Unlock()
	}()
	return channel, nil
}

func (p *Projector) Open(resumed bool) Delta {
	kind := DeltaThreadOpened
	status := "new coding thread"
	if resumed {
		kind = DeltaThreadResumed
		status = "coding thread resumed"
	}
	return p.mutate(kind, func(state *ThreadSnapshot, _ *Delta) {
		state.Activity = ActivityIdle
		state.Status = status
	})
}

func (p *Projector) ThreadMetadataUpdated(metadata ThreadMetadata) Delta {
	return p.mutate(DeltaThreadMetadata, func(state *ThreadSnapshot, delta *Delta) {
		metadata.Title, _ = boundText(metadata.Title, p.limits.TextBytes)
		metadata.Preview, _ = boundText(metadata.Preview, p.limits.TextBytes)
		metadata.ProjectRoot, _ = boundText(metadata.ProjectRoot, p.limits.TextBytes)
		metadata.CWD, _ = boundText(metadata.CWD, p.limits.TextBytes)
		metadata.Model, _ = boundText(metadata.Model, p.limits.TextBytes)
		metadata.Provider, _ = boundText(metadata.Provider, p.limits.TextBytes)
		state.Metadata = metadata
		delta.Metadata = &metadata
		delta.EntityID = state.ThreadID
	})
}

func (p *Projector) TurnStarted(turnID, userMessage string) Delta {
	return p.mutate(DeltaTurnStarted, func(state *ThreadSnapshot, delta *Delta) {
		delta.TurnID = normalizeTurnID(turnID)
		delta.EntityID = delta.TurnID
		state.Activity = ActivityRunning
		state.Status = "running"
		if strings.TrimSpace(userMessage) == "" {
			return
		}
		entry := p.boundedEntry(TranscriptEntry{
			ID:       entryID(turnID, "user"),
			TurnID:   normalizeTurnID(turnID),
			Kind:     EntryUser,
			Text:     userMessage,
			Complete: true,
		})
		p.upsertEntry(state, delta, entry)
		delta.Entry = &entry
		delta.EntityID = entry.ID
	})
}

func (p *Projector) AssistantAccumulated(turnID, content string, complete bool) Delta {
	return p.upsertStreamEntry(DeltaAssistant, turnID, EntryAssistant, content, complete)
}

func (p *Projector) ReasoningAccumulated(turnID, content string, complete bool) Delta {
	return p.upsertStreamEntry(DeltaReasoning, turnID, EntryReasoning, content, complete)
}

func (p *Projector) upsertStreamEntry(
	kind DeltaKind,
	turnID string,
	entryKind EntryKind,
	content string,
	complete bool,
) Delta {
	return p.mutate(kind, func(state *ThreadSnapshot, delta *Delta) {
		entry := p.boundedEntry(TranscriptEntry{
			ID:       entryID(turnID, string(entryKind)),
			TurnID:   normalizeTurnID(turnID),
			Kind:     entryKind,
			Text:     content,
			Complete: complete,
		})
		p.upsertEntry(state, delta, entry)
		delta.Entry = &entry
		delta.TurnID = entry.TurnID
		delta.EntityID = entry.ID
	})
}

func (p *Projector) Warning(turnID, id, content string) Delta {
	return p.notice(EntryWarning, turnID, id, content)
}

func (p *Projector) Error(turnID, id, content string) Delta {
	return p.notice(EntryError, turnID, id, content)
}

func (p *Projector) notice(kind EntryKind, turnID, id, content string) Delta {
	return p.mutate(DeltaNotice, func(state *ThreadSnapshot, delta *Delta) {
		turnID = normalizeTurnID(turnID)
		id = strings.TrimSpace(id)
		if id == "" {
			id = fmt.Sprintf("%s:%s:%d", turnID, kind, state.Revision+1)
		}
		entry := p.boundedEntry(TranscriptEntry{
			ID: id, TurnID: turnID, Kind: kind, Text: content, Complete: true,
		})
		p.upsertEntry(state, delta, entry)
		delta.Entry = &entry
		delta.TurnID = turnID
		delta.EntityID = id
	})
}

func (p *Projector) ToolStarted(turnID, callID, name, arguments string) Delta {
	return p.mutate(DeltaToolStarted, func(state *ThreadSnapshot, delta *Delta) {
		tool := p.boundedTool(ToolState{
			TurnID: normalizeTurnID(turnID), CallID: callID, Name: name, Arguments: arguments, Status: ToolRunning,
		})
		p.upsertTool(state, delta, tool)
		delta.Tool = &tool
		delta.TurnID = tool.TurnID
		delta.EntityID = toolEntityID(tool.TurnID, tool.CallID)
	})
}

func (p *Projector) ToolOutput(turnID, callID, output string) Delta {
	return p.mutate(DeltaToolOutput, func(state *ThreadSnapshot, delta *Delta) {
		turnID = normalizeTurnID(turnID)
		tool := toolByID(state.Tools, turnID, callID)
		if tool.CallID == "" {
			tool = ToolState{TurnID: turnID, CallID: callID, Status: ToolUnknown}
		}
		if tool.TurnID == "" {
			tool.TurnID = normalizeTurnID(turnID)
		}
		bounded, truncated := boundText(output, p.limits.TextBytes)
		tool.Output = bounded
		tool.OutputTruncated = truncated
		p.upsertTool(state, delta, tool)
		delta.Tool = &tool
		delta.TurnID = tool.TurnID
		delta.EntityID = toolEntityID(tool.TurnID, tool.CallID)
	})
}

func (p *Projector) ToolCompleted(
	turnID, callID, name, output string,
	duration time.Duration,
	failed bool,
	writeAudit []WriteAudit,
) Delta {
	return p.mutate(DeltaToolCompleted, func(state *ThreadSnapshot, delta *Delta) {
		turnID = normalizeTurnID(turnID)
		tool := toolByID(state.Tools, turnID, callID)
		if tool.CallID == "" {
			tool = ToolState{TurnID: turnID, CallID: callID, Name: name}
		}
		if tool.TurnID == "" {
			tool.TurnID = normalizeTurnID(turnID)
		}
		if tool.Name == "" {
			tool.Name = name
		}
		tool.Status = ToolSucceeded
		if failed {
			tool.Status = ToolFailed
		}
		tool.Duration = duration
		tool.Output, tool.OutputTruncated = boundText(output, p.limits.TextBytes)
		tool.WriteAudit = p.boundedWriteAudit(writeAudit)
		p.upsertTool(state, delta, tool)
		delta.Tool = &tool
		delta.TurnID = tool.TurnID
		delta.EntityID = toolEntityID(tool.TurnID, tool.CallID)
	})
}

// ToolSuspended records that durable continuation ownership moved outside the
// running turn. The tool has not succeeded or failed and may be resumed after
// the pending human interaction is resolved.
func (p *Projector) ToolSuspended(turnID, callID, name string, duration time.Duration) Delta {
	return p.mutate(DeltaToolSuspended, func(state *ThreadSnapshot, delta *Delta) {
		turnID = normalizeTurnID(turnID)
		tool := toolByID(state.Tools, turnID, callID)
		if tool.CallID == "" {
			tool = ToolState{TurnID: turnID, CallID: callID, Name: name}
		}
		if tool.Name == "" {
			tool.Name = name
		}
		tool.Status = ToolSuspended
		tool.Duration = duration
		p.upsertTool(state, delta, tool)
		delta.Tool = &tool
		delta.TurnID = tool.TurnID
		delta.EntityID = toolEntityID(tool.TurnID, tool.CallID)
	})
}

func (p *Projector) ContextUsage(used, limit int) Delta {
	return p.mutate(DeltaContextUsage, func(state *ThreadSnapshot, delta *Delta) {
		state.ContextUsage = ContextUsage{UsedTokens: max(0, used), LimitTokens: max(0, limit)}
		usage := state.ContextUsage
		delta.ContextUsage = &usage
	})
}

func (p *Projector) WorkspaceUpdated(snapshot codingworkspace.Snapshot) Delta {
	return p.mutate(DeltaWorkspaceUpdated, func(state *ThreadSnapshot, delta *Delta) {
		workspace := cloneWorkspaceSnapshot(snapshot)
		state.Workspace = &workspace
		delta.Workspace = &workspace
	})
}

func (p *Projector) CompactionStarted() Delta {
	return p.activity(DeltaCompactionStarted, ActivityCompacting, "compacting context")
}

func (p *Projector) CompactionCompleted(status string) Delta {
	return p.activity(DeltaCompactionComplete, ActivityRunning, status)
}

// BackgroundCompactionCompleted records session-scoped maintenance without
// reopening or relabeling a turn that has already reached a terminal state.
func (p *Projector) BackgroundCompactionCompleted() Delta {
	return p.mutate(DeltaCompactionComplete, func(*ThreadSnapshot, *Delta) {})
}

func (p *Projector) CompactionFailed(status string) Delta {
	return p.activity(DeltaCompactionFailed, ActivityFailed, status)
}

func (p *Projector) TurnCompleted(turnID, status string) Delta {
	return p.finishTurn(DeltaTurnCompleted, TurnOutcomeCompleted, turnID, status, ActivityIdle, "")
}

func (p *Projector) TurnSuspended(turnID, status string) Delta {
	return p.finishTurn(
		DeltaTurnSuspended,
		TurnOutcomeSuspended,
		turnID,
		status,
		ActivityWaitingInput,
		"",
	)
}

func (p *Projector) TurnFailed(turnID, status string) Delta {
	return p.finishTurn(DeltaTurnFailed, TurnOutcomeFailed, turnID, status, ActivityFailed, ToolFailed)
}

func (p *Projector) TurnInterrupted(turnID, status string) Delta {
	return p.finishTurn(DeltaTurnInterrupted, TurnOutcomeInterrupted, turnID, status, ActivityIdle, ToolInterrupted)
}

// finishTurn is the single transition for typed terminal turn outcomes. Only
// abnormal outcomes pass a tool status, because they can bypass ToolExecEnd.
func (p *Projector) finishTurn(
	kind DeltaKind,
	outcome TurnOutcome,
	turnID, status string,
	activity Activity,
	toolStatus ToolStatus,
) Delta {
	turnID = normalizeTurnID(turnID)
	return p.mutate(kind, func(state *ThreadSnapshot, delta *Delta) {
		state.Activity = activity
		state.Status, _ = boundText(status, p.limits.TextBytes)
		delta.TurnID = turnID
		delta.EntityID = turnID
		lastTurn := LastTurnOutcome{TurnID: turnID, Outcome: outcome}
		state.LastTurn = &lastTurn
		delta.LastTurn = &lastTurn
		if toolStatus == "" {
			return
		}
		for i := range state.Tools {
			if state.Tools[i].TurnID == turnID && state.Tools[i].Status == ToolRunning {
				state.Tools[i].Status = toolStatus
				delta.RequiresSnapshot = true
			}
		}
	})
}

func (p *Projector) InterruptRequested() Delta {
	return p.activity(DeltaInterruptRequested, ActivityInterrupting, "interrupt requested")
}

func (p *Projector) activity(kind DeltaKind, activity Activity, status string) Delta {
	return p.mutate(kind, func(state *ThreadSnapshot, _ *Delta) {
		state.Activity = activity
		state.Status, _ = boundText(status, p.limits.TextBytes)
	})
}

func (p *Projector) mutate(kind DeltaKind, apply func(*ThreadSnapshot, *Delta)) Delta {
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := p.state.Revision
	delta := Delta{
		ProtocolVersion:  ProtocolVersion,
		ThreadID:         p.state.ThreadID,
		PreviousRevision: previous,
		Revision:         previous + 1,
		Kind:             kind,
	}
	apply(&p.state, &delta)
	p.state.Revision = delta.Revision
	delta.Activity = p.state.Activity
	delta.Status = p.state.Status
	p.deltas = append(p.deltas, cloneDelta(delta))
	if overflow := len(p.deltas) - p.limits.Deltas; overflow > 0 {
		p.deltas = slices.Clone(p.deltas[overflow:])
	}
	for _, watcher := range p.watchers {
		select {
		case watcher <- cloneDelta(delta):
		default:
			select {
			case <-watcher:
			default:
			}
			select {
			case watcher <- cloneDelta(delta):
			default:
			}
		}
	}
	return cloneDelta(delta)
}

func (p *Projector) changesSinceLocked(revision Revision) ([]Delta, bool) {
	if revision == p.state.Revision {
		return []Delta{}, true
	}
	if len(p.deltas) == 0 {
		return nil, false
	}
	for i := range p.deltas {
		if p.deltas[i].PreviousRevision == revision {
			return p.deltas[i:], true
		}
	}
	return nil, false
}

func (p *Projector) boundedEntry(entry TranscriptEntry) TranscriptEntry {
	entry.Text, entry.Truncated = boundText(entry.Text, p.limits.TextBytes)
	return entry
}

func (p *Projector) boundedTool(tool ToolState) ToolState {
	tool.Arguments, _ = boundText(tool.Arguments, p.limits.TextBytes)
	tool.Output, tool.OutputTruncated = boundText(tool.Output, p.limits.TextBytes)
	tool.WriteAudit = p.boundedWriteAudit(tool.WriteAudit)
	return tool
}

func (p *Projector) boundedWriteAudit(audit []WriteAudit) []WriteAudit {
	if len(audit) == 0 {
		return nil
	}
	result := slices.Clone(audit)
	if len(result) > p.limits.Tools {
		result = result[:p.limits.Tools]
	}
	for i := range result {
		result[i].Kind, _ = boundText(result[i].Kind, p.limits.TextBytes)
		result[i].Target, _ = boundText(result[i].Target, p.limits.TextBytes)
		result[i].Action, _ = boundText(result[i].Action, p.limits.TextBytes)
	}
	return result
}

func (p *Projector) upsertEntry(state *ThreadSnapshot, delta *Delta, entry TranscriptEntry) {
	previousLength := len(state.Entries)
	state.Entries = replaceEntry(state.Entries, entry)
	if len(state.Entries) == previousLength {
		return
	}
	if overflow := len(state.Entries) - p.limits.Entries; overflow > 0 {
		state.HasOlderEntries = true
		state.Entries = slices.Clone(state.Entries[overflow:])
		delta.RequiresSnapshot = true
	}
}

func (p *Projector) upsertTool(state *ThreadSnapshot, delta *Delta, tool ToolState) {
	for i := range state.Tools {
		if state.Tools[i].TurnID == tool.TurnID && state.Tools[i].CallID == tool.CallID {
			state.Tools[i] = tool
			return
		}
	}
	state.Tools = append(state.Tools, tool)
	if overflow := len(state.Tools) - p.limits.Tools; overflow > 0 {
		state.Tools = slices.Clone(state.Tools[overflow:])
		delta.RequiresSnapshot = true
	}
}

func entryID(turnID, suffix string) string {
	return normalizeTurnID(turnID) + ":" + suffix
}

func normalizeTurnID(turnID string) string {
	if turnID = strings.TrimSpace(turnID); turnID != "" {
		return turnID
	}
	return "current"
}

func toolByID(tools []ToolState, turnID, callID string) ToolState {
	for i := range tools {
		if tools[i].TurnID == turnID && tools[i].CallID == callID {
			return tools[i]
		}
	}
	return ToolState{}
}

func toolEntityID(turnID, callID string) string {
	return normalizeTurnID(turnID) + ":tool:" + strings.TrimSpace(callID)
}

func boundText(value string, maximum int) (string, bool) {
	if maximum <= 0 || len(value) <= maximum {
		return value, false
	}
	const marker = "\n… output omitted from frontend projection …"
	if maximum <= len(marker) {
		return marker[:validUTF8PrefixEnd(marker, maximum)], true
	}
	limit := max(0, maximum-len(marker))
	end := validUTF8PrefixEnd(value, min(limit, len(value)))
	return value[:end] + marker, true
}

func validUTF8PrefixEnd(value string, end int) int {
	end = min(max(0, end), len(value))
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return end
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func cloneSnapshot(snapshot ThreadSnapshot) ThreadSnapshot {
	snapshot.Entries = slices.Clone(snapshot.Entries)
	snapshot.Tools = cloneTools(snapshot.Tools)
	if snapshot.LastTurn != nil {
		lastTurn := *snapshot.LastTurn
		snapshot.LastTurn = &lastTurn
	}
	if snapshot.Workspace != nil {
		workspace := cloneWorkspaceSnapshot(*snapshot.Workspace)
		snapshot.Workspace = &workspace
	}
	return snapshot
}

func cloneDelta(delta Delta) Delta {
	if delta.Entry != nil {
		entry := *delta.Entry
		delta.Entry = &entry
	}
	if delta.Tool != nil {
		tool := *delta.Tool
		tool.WriteAudit = slices.Clone(tool.WriteAudit)
		delta.Tool = &tool
	}
	if delta.Metadata != nil {
		metadata := *delta.Metadata
		delta.Metadata = &metadata
	}
	if delta.LastTurn != nil {
		lastTurn := *delta.LastTurn
		delta.LastTurn = &lastTurn
	}
	if delta.ContextUsage != nil {
		usage := *delta.ContextUsage
		delta.ContextUsage = &usage
	}
	if delta.Workspace != nil {
		workspace := cloneWorkspaceSnapshot(*delta.Workspace)
		delta.Workspace = &workspace
	}
	return delta
}

func cloneTools(tools []ToolState) []ToolState {
	tools = slices.Clone(tools)
	for i := range tools {
		tools[i].WriteAudit = slices.Clone(tools[i].WriteAudit)
	}
	return tools
}

func cloneWorkspaceSnapshot(snapshot codingworkspace.Snapshot) codingworkspace.Snapshot {
	snapshot.ChangedPaths = slices.Clone(snapshot.ChangedPaths)
	return snapshot
}
