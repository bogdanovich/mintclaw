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
	defaultTextBytes  = 64 << 10
)

type ProjectionLimits struct {
	Entries   int
	Tools     int
	TextBytes int
}

func (l ProjectionLimits) normalized() ProjectionLimits {
	if l.Entries <= 0 {
		l.Entries = defaultEntryLimit
	}
	if l.Tools <= 0 {
		l.Tools = defaultToolLimit
	}
	if l.TextBytes <= 0 {
		l.TextBytes = defaultTextBytes
	}
	return l
}

// Projector owns a bounded UI projection. Canonical transcript and tool audit
// persistence remain outside this type.
type Projector struct {
	mu                         sync.RWMutex
	limits                     ProjectionLimits
	state                      ThreadSnapshot
	entryGenerations           map[string]uint64
	nextEntryGeneration        uint64
	nextNotice                 uint64
	activeTurnID               string
	foregroundCompactionTurnID string
	foregroundCompactionActive bool
	nextSubscriber             uint64
	subscribers                map[uint64]chan ThreadSnapshot
}

func NewProjector(threadID string, limits ProjectionLimits) (*Projector, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("coding frontend thread ID is required")
	}
	return &Projector{
		limits: limits.normalized(),
		state: ThreadSnapshot{
			ThreadID: threadID,
			Activity: ActivityIdle,
		},
		entryGenerations: make(map[string]uint64),
		subscribers:      make(map[uint64]chan ThreadSnapshot),
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

// Subscribe atomically captures the current view and registers for later
// views. Delivery is bounded and non-blocking: a slow subscriber receives the
// newest view instead of blocking a turn or replaying intermediate mutations.
func (p *Projector) Subscribe(
	ctx context.Context,
) (ThreadSnapshot, <-chan ThreadSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ThreadSnapshot{}, nil, err
	}
	p.mu.Lock()
	p.nextSubscriber++
	id := p.nextSubscriber
	channel := make(chan ThreadSnapshot, 1)
	p.subscribers[id] = channel
	current := cloneSnapshot(p.state)
	p.mu.Unlock()

	go func() {
		<-ctx.Done()
		p.mu.Lock()
		if current, exists := p.subscribers[id]; exists {
			delete(p.subscribers, id)
			close(current)
		}
		p.mu.Unlock()
	}()
	return current, channel, nil
}

func (p *Projector) Open(resumed bool) {
	status := "new coding thread"
	if resumed {
		status = "coding thread resumed"
	}
	p.mutate(func(state *ThreadSnapshot) {
		state.Activity = ActivityIdle
		state.Status = status
	})
}

func (p *Projector) ThreadMetadataUpdated(metadata ThreadMetadata) {
	p.mutate(func(state *ThreadSnapshot) {
		metadata.Title, _ = boundText(metadata.Title, p.limits.TextBytes)
		metadata.Preview, _ = boundText(metadata.Preview, p.limits.TextBytes)
		metadata.ProjectRoot, _ = boundText(metadata.ProjectRoot, p.limits.TextBytes)
		metadata.CWD, _ = boundText(metadata.CWD, p.limits.TextBytes)
		metadata.Model, _ = boundText(metadata.Model, p.limits.TextBytes)
		metadata.Provider, _ = boundText(metadata.Provider, p.limits.TextBytes)
		state.Metadata = metadata
	})
}

func (p *Projector) TurnStarted(turnID, userMessage string) {
	p.mutate(func(state *ThreadSnapshot) {
		turnID = normalizeTurnID(turnID)
		p.activeTurnID = turnID
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
		p.upsertEntry(state, entry)
	})
}

func (p *Projector) AssistantAccumulated(turnID, content string, complete bool) {
	p.upsertStreamEntry(turnID, EntryAssistant, content, complete)
}

func (p *Projector) ReasoningAccumulated(turnID, content string, complete bool) {
	p.upsertStreamEntry(turnID, EntryReasoning, content, complete)
}

type streamOwnedEntry struct {
	entry      TranscriptEntry
	generation uint64
}

type (
	streamBaseline     map[string]streamOwnedEntry
	streamOwnedEntries map[string]streamOwnedEntry
)

func (p *Projector) captureStreamBaseline(turnID string) streamBaseline {
	turnID = normalizeTurnID(turnID)
	p.mu.RLock()
	defer p.mu.RUnlock()
	baseline := make(streamBaseline)
	for _, entry := range p.state.Entries {
		if entry.TurnID == turnID && (entry.Kind == EntryAssistant || entry.Kind == EntryReasoning) {
			baseline[entry.ID] = streamOwnedEntry{entry: entry, generation: p.entryGenerations[entry.ID]}
		}
	}
	return baseline
}

// discardOwnedStream restores answer/reasoning entries changed by a canceled
// provider attempt when that attempt is still their latest writer. Canonical
// turn lifecycle and entries from later writers remain untouched.
func (p *Projector) discardOwnedStream(
	turnID string,
	baseline streamBaseline,
	owned streamOwnedEntries,
) {
	turnID = normalizeTurnID(turnID)
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := make([]TranscriptEntry, 0, len(p.state.Entries))
	visibleChange := false
	for _, entry := range p.state.Entries {
		streamEntry, streamOwned := owned[entry.ID]
		ownsCurrent := streamOwned && entry.TurnID == turnID && entry == streamEntry.entry &&
			p.entryGenerations[entry.ID] == streamEntry.generation
		if ownsCurrent {
			if previous, ok := baseline[entry.ID]; ok {
				filtered = append(filtered, previous.entry)
				p.entryGenerations[entry.ID] = previous.generation
				visibleChange = visibleChange || previous.entry != entry
			} else {
				delete(p.entryGenerations, entry.ID)
				visibleChange = true
			}
			continue
		}
		filtered = append(filtered, entry)
	}
	if !visibleChange {
		return
	}
	p.mutateLocked(func(state *ThreadSnapshot) {
		state.Entries = filtered
	})
}

func (p *Projector) upsertStreamEntry(
	turnID string,
	entryKind EntryKind,
	content string,
	complete bool,
) streamOwnedEntry {
	entry := p.boundedEntry(TranscriptEntry{
		ID:       entryID(turnID, string(entryKind)),
		TurnID:   normalizeTurnID(turnID),
		Kind:     entryKind,
		Text:     content,
		Complete: complete,
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextEntryGeneration++
	owned := streamOwnedEntry{entry: entry, generation: p.nextEntryGeneration}
	p.entryGenerations[entry.ID] = owned.generation
	for i := range p.state.Entries {
		if p.state.Entries[i].ID == entry.ID && p.state.Entries[i] == entry {
			return owned
		}
	}
	p.mutateLocked(func(state *ThreadSnapshot) {
		p.upsertEntry(state, entry)
	})
	return owned
}

func (p *Projector) Warning(turnID, id, content string) {
	p.notice(EntryWarning, turnID, id, content)
}

func (p *Projector) Error(turnID, id, content string) {
	p.notice(EntryError, turnID, id, content)
}

func (p *Projector) notice(kind EntryKind, turnID, id, content string) {
	p.mutate(func(state *ThreadSnapshot) {
		turnID = normalizeTurnID(turnID)
		id = strings.TrimSpace(id)
		if id == "" {
			p.nextNotice++
			id = fmt.Sprintf("%s:%s:%d", turnID, kind, p.nextNotice)
		}
		entry := p.boundedEntry(TranscriptEntry{
			ID: id, TurnID: turnID, Kind: kind, Text: content, Complete: true,
		})
		p.upsertEntry(state, entry)
	})
}

func (p *Projector) ToolStarted(turnID, callID, name, arguments string) {
	p.mutate(func(state *ThreadSnapshot) {
		tool := p.boundedTool(ToolState{
			TurnID: normalizeTurnID(turnID), CallID: callID, Name: name, Arguments: arguments, Status: ToolRunning,
		})
		p.upsertTool(state, tool)
	})
}

func (p *Projector) ToolOutput(turnID, callID, output string) {
	p.mutate(func(state *ThreadSnapshot) {
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
		p.upsertTool(state, tool)
	})
}

// ToolCommandOutput projects bounded process state owned by the command tool.
// It never derives command output or lifecycle state from model-facing prose.
func (p *Projector) ToolCommandOutput(turnID, callID string, command CommandState) {
	p.mutate(func(state *ThreadSnapshot) {
		turnID = normalizeTurnID(turnID)
		tool := toolByID(state.Tools, turnID, callID)
		if tool.CallID == "" {
			tool = ToolState{TurnID: turnID, CallID: callID, Status: ToolUnknown}
		}
		command = p.boundedCommand(command)
		tool.Command = &command
		tool.Output, tool.OutputTruncated = commandDisplayOutput(command, p.limits.TextBytes)
		p.upsertTool(state, tool)
	})
}

func (p *Projector) ToolCompleted(
	turnID, callID, name, output string,
	duration time.Duration,
	failed bool,
	writeAudit []WriteAudit,
) {
	p.mutate(func(state *ThreadSnapshot) {
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
		if tool.Command != nil {
			switch tool.Command.Status {
			case CommandCanceled:
				tool.Status = ToolInterrupted
			case CommandFailed, CommandTimedOut:
				tool.Status = ToolFailed
			}
		}
		tool.Duration = duration
		if output != "" || tool.Command == nil {
			tool.Output, tool.OutputTruncated = boundText(output, p.limits.TextBytes)
		}
		tool.WriteAudit = p.boundedWriteAudit(writeAudit)
		p.upsertTool(state, tool)
	})
}

// FilesChanged promotes only successful, verified file write audits into the
// bounded changed-file projection.
func (p *Projector) FilesChanged(turnID, callID string, audit []WriteAudit) {
	p.mutate(func(state *ThreadSnapshot) {
		turnID = normalizeTurnID(turnID)
		changed := make([]ChangedFile, 0, min(len(audit), p.limits.Tools))
		for _, entry := range audit {
			if !entry.Success || entry.Kind != "file" || strings.TrimSpace(entry.Target) == "" {
				continue
			}
			changed = replaceChangedFile(changed, p.boundedChangedFile(ChangedFile{
				Path: entry.Target, Action: entry.Action, Tool: entry.Tool, TurnID: turnID, CallID: callID,
			}))
			if overflow := len(changed) - p.limits.Tools; overflow > 0 {
				changed = slices.Clone(changed[overflow:])
			}
		}
		for _, file := range changed {
			state.ChangedFiles = replaceChangedFile(state.ChangedFiles, file)
		}
		if overflow := len(state.ChangedFiles) - p.limits.Tools; overflow > 0 {
			state.ChangedFiles = slices.Clone(state.ChangedFiles[overflow:])
		}
	})
}

// ToolSuspended records that durable continuation ownership moved outside the
// running turn. The tool has not succeeded or failed and may be resumed after
// the pending human interaction is resolved.
func (p *Projector) ToolSuspended(turnID, callID, name string, duration time.Duration) {
	p.mutate(func(state *ThreadSnapshot) {
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
		p.upsertTool(state, tool)
	})
}

func (p *Projector) ContextUsage(used, limit int) {
	usage := ContextUsage{UsedTokens: max(0, used), LimitTokens: max(0, limit)}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.ContextUsage == usage {
		return
	}
	p.mutateLocked(func(state *ThreadSnapshot) {
		state.ContextUsage = usage
	})
}

func (p *Projector) WorkspaceUpdated(snapshot codingworkspace.Snapshot) {
	p.mutate(func(state *ThreadSnapshot) {
		workspace := cloneWorkspaceSnapshot(snapshot)
		state.Workspace = &workspace
	})
}

func (p *Projector) CompactionStarted(turnID, reason string, background bool) {
	p.CompactionUpdate(
		CompactionState{
			TurnID: normalizeOptionalTurnID(turnID), Reason: reason, Status: CompactionRunning, Background: background,
		},
	)
}

func (p *Projector) CompactionCompleted(
	turnID, reason string,
	tokensSaved int,
	noProgress, background bool,
) {
	status := CompactionCompleted
	if noProgress {
		status = CompactionNoProgress
	}
	p.CompactionUpdate(
		CompactionState{
			TurnID: normalizeOptionalTurnID(turnID), Reason: reason, Status: status,
			TokensSaved: max(0, tokensSaved), Background: background,
		},
	)
}

func (p *Projector) CompactionFailed(turnID, reason string, background bool) {
	p.CompactionUpdate(
		CompactionState{
			TurnID: normalizeOptionalTurnID(turnID), Reason: reason, Status: CompactionFailed, Background: background,
		},
	)
}

// CompactionUpdate projects one correlated compaction lifecycle observation.
func (p *Projector) CompactionUpdate(compaction CompactionState) {
	p.compaction(compaction)
}

func (p *Projector) compaction(compaction CompactionState) {
	p.mutate(func(state *ThreadSnapshot) {
		compaction.Reason, _ = boundText(compaction.Reason, p.limits.TextBytes)
		state.LastCompaction = &compaction
		if compaction.Background {
			return
		}
		switch compaction.Status {
		case CompactionRunning:
			standalone := compaction.TurnID == "" && p.activeTurnID == "" && state.Activity == ActivityIdle
			inTurn := state.Activity == ActivityRunning && p.activeTurnID == compaction.TurnID
			if !standalone && !inTurn {
				return
			}
			p.foregroundCompactionTurnID = compaction.TurnID
			p.foregroundCompactionActive = true
			state.Activity = ActivityCompacting
			state.Status = "compacting context"
		case CompactionProgress:
			return
		case CompactionNoProgress:
			activity, ok := p.releaseCompactionActivity(state.Activity, compaction.TurnID)
			if !ok {
				return
			}
			state.Activity = activity
			state.Status = "context already compact"
		case CompactionCompleted:
			activity, ok := p.releaseCompactionActivity(state.Activity, compaction.TurnID)
			if !ok {
				return
			}
			state.Activity = activity
			state.Status = fmt.Sprintf("context compacted; %d tokens saved", compaction.TokensSaved)
		case CompactionFailed:
			activity, ok := p.releaseCompactionActivity(state.Activity, compaction.TurnID)
			if !ok {
				return
			}
			state.Activity = activity
			state.Status = "context compaction failed"
		case CompactionInterrupted:
			activity, ok := p.releaseCompactionActivity(state.Activity, compaction.TurnID)
			if !ok {
				return
			}
			state.Activity = activity
			state.Status = "context compaction interrupted"
		}
	})
}

func (p *Projector) releaseCompactionActivity(activity Activity, turnID string) (Activity, bool) {
	if !p.foregroundCompactionActive || p.foregroundCompactionTurnID != turnID ||
		activity != ActivityCompacting {
		return activity, false
	}
	var next Activity
	if turnID == "" && p.activeTurnID == "" {
		next = ActivityIdle
	} else if p.activeTurnID == turnID {
		next = ActivityRunning
	} else {
		return activity, false
	}
	p.foregroundCompactionActive = false
	p.foregroundCompactionTurnID = ""
	return next, true
}

func (p *Projector) TurnCompleted(turnID, status string) {
	p.finishTurn(TurnOutcomeCompleted, turnID, status, ActivityIdle, "")
}

func (p *Projector) TurnSuspended(turnID, status string) {
	p.finishTurn(
		TurnOutcomeSuspended,
		turnID,
		status,
		ActivityWaitingInput,
		"",
	)
}

func (p *Projector) TurnFailed(turnID, status string) {
	p.finishTurn(TurnOutcomeFailed, turnID, status, ActivityFailed, ToolFailed)
}

func (p *Projector) TurnInterrupted(turnID, status string) {
	p.finishTurn(TurnOutcomeInterrupted, turnID, status, ActivityIdle, ToolInterrupted)
}

// finishTurn is the single transition for typed terminal turn outcomes. Only
// abnormal outcomes pass a tool status, because they can bypass ToolExecEnd.
func (p *Projector) finishTurn(
	outcome TurnOutcome,
	turnID, status string,
	activity Activity,
	toolStatus ToolStatus,
) {
	turnID = normalizeTurnID(turnID)
	p.mutate(func(state *ThreadSnapshot) {
		if p.activeTurnID == turnID {
			p.activeTurnID = ""
		}
		if p.foregroundCompactionActive && p.foregroundCompactionTurnID == turnID {
			p.foregroundCompactionTurnID = ""
			p.foregroundCompactionActive = false
		}
		state.Activity = activity
		state.Status, _ = boundText(status, p.limits.TextBytes)
		lastTurn := LastTurnOutcome{TurnID: turnID, Outcome: outcome}
		state.LastTurn = &lastTurn
		if toolStatus == "" {
			return
		}
		for i := range state.Tools {
			if state.Tools[i].TurnID == turnID && state.Tools[i].Status == ToolRunning {
				state.Tools[i].Status = toolStatus
			}
		}
	})
}

func (p *Projector) InterruptRequested() {
	p.activity(ActivityInterrupting, "interrupt requested")
}

func (p *Projector) activity(activity Activity, status string) {
	p.mutate(func(state *ThreadSnapshot) {
		state.Activity = activity
		state.Status, _ = boundText(status, p.limits.TextBytes)
	})
}

func (p *Projector) mutate(apply func(*ThreadSnapshot)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutateLocked(apply)
}

func (p *Projector) mutateLocked(apply func(*ThreadSnapshot)) {
	apply(&p.state)
	current := cloneSnapshot(p.state)
	for _, subscriber := range p.subscribers {
		select {
		case subscriber <- cloneSnapshot(current):
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- cloneSnapshot(current):
			default:
			}
		}
	}
}

func (p *Projector) boundedEntry(entry TranscriptEntry) TranscriptEntry {
	entry.Text, entry.Truncated = boundText(entry.Text, p.limits.TextBytes)
	return entry
}

func (p *Projector) boundedTool(tool ToolState) ToolState {
	tool.Arguments, _ = boundText(tool.Arguments, p.limits.TextBytes)
	tool.Output, tool.OutputTruncated = boundText(tool.Output, p.limits.TextBytes)
	tool.WriteAudit = p.boundedWriteAudit(tool.WriteAudit)
	if tool.Command != nil {
		command := p.boundedCommand(*tool.Command)
		tool.Command = &command
	}
	return tool
}

func (p *Projector) boundedCommand(command CommandState) CommandState {
	if command.ExitCode != nil {
		exitCode := *command.ExitCode
		command.ExitCode = &exitCode
	}
	stdout, stdoutTruncated := boundText(command.Stdout, p.limits.TextBytes)
	stderr, stderrTruncated := boundText(command.Stderr, p.limits.TextBytes)
	output, outputTruncated := boundText(command.Output, p.limits.TextBytes)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Output = output
	command.SessionID, _ = boundText(command.SessionID, p.limits.TextBytes)
	command.Truncated = command.Truncated || stdoutTruncated || stderrTruncated || outputTruncated
	return command
}

func (p *Projector) boundedChangedFile(file ChangedFile) ChangedFile {
	file.Path, _ = boundText(file.Path, p.limits.TextBytes)
	file.Action, _ = boundText(file.Action, p.limits.TextBytes)
	file.Tool, _ = boundText(file.Tool, p.limits.TextBytes)
	file.TurnID, _ = boundText(file.TurnID, p.limits.TextBytes)
	file.CallID, _ = boundText(file.CallID, p.limits.TextBytes)
	return file
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
		result[i].Tool, _ = boundText(result[i].Tool, p.limits.TextBytes)
	}
	return result
}

func replaceChangedFile(files []ChangedFile, replacement ChangedFile) []ChangedFile {
	result := make([]ChangedFile, 0, len(files)+1)
	for _, file := range files {
		if file.Path != replacement.Path {
			result = append(result, file)
		}
	}
	return append(result, replacement)
}

func commandDisplayOutput(command CommandState, maximum int) (string, bool) {
	if command.Output != "" {
		output, truncated := boundText(command.Output, maximum)
		return output, command.Truncated || truncated
	}
	output := command.Stdout
	if command.Stderr != "" {
		if output != "" {
			output += "\nSTDERR:\n"
		}
		output += command.Stderr
	}
	output, truncated := boundText(output, maximum)
	return output, command.Truncated || truncated
}

func (p *Projector) upsertEntry(state *ThreadSnapshot, entry TranscriptEntry) {
	previousLength := len(state.Entries)
	state.Entries = replaceEntry(state.Entries, entry)
	if len(state.Entries) == previousLength {
		return
	}
	if overflow := len(state.Entries) - p.limits.Entries; overflow > 0 {
		state.HasOlderEntries = true
		for _, removed := range state.Entries[:overflow] {
			delete(p.entryGenerations, removed.ID)
		}
		state.Entries = slices.Clone(state.Entries[overflow:])
	}
}

func replaceEntry(entries []TranscriptEntry, replacement TranscriptEntry) []TranscriptEntry {
	entries = slices.Clone(entries)
	for i := range entries {
		if entries[i].ID == replacement.ID {
			entries[i] = replacement
			return entries
		}
	}
	insertAt := len(entries)
	if replacement.Kind == EntryUser {
		for i := range entries {
			candidate := entries[i]
			if candidate.TurnID == replacement.TurnID &&
				(candidate.Kind == EntryAssistant || candidate.Kind == EntryReasoning) {
				insertAt = i
				break
			}
		}
	}
	entries = append(entries, TranscriptEntry{})
	copy(entries[insertAt+1:], entries[insertAt:])
	entries[insertAt] = replacement
	return entries
}

func (p *Projector) upsertTool(state *ThreadSnapshot, tool ToolState) {
	for i := range state.Tools {
		if state.Tools[i].TurnID == tool.TurnID && state.Tools[i].CallID == tool.CallID {
			state.Tools[i] = tool
			return
		}
	}
	state.Tools = append(state.Tools, tool)
	if overflow := len(state.Tools) - p.limits.Tools; overflow > 0 {
		state.Tools = slices.Clone(state.Tools[overflow:])
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

func normalizeOptionalTurnID(turnID string) string {
	return strings.TrimSpace(turnID)
}

func toolByID(tools []ToolState, turnID, callID string) ToolState {
	for i := range tools {
		if tools[i].TurnID == turnID && tools[i].CallID == callID {
			return tools[i]
		}
	}
	return ToolState{}
}

func boundText(value string, maximum int) (string, bool) {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
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
	snapshot.ChangedFiles = slices.Clone(snapshot.ChangedFiles)
	if snapshot.LastTurn != nil {
		lastTurn := *snapshot.LastTurn
		snapshot.LastTurn = &lastTurn
	}
	if snapshot.LastCompaction != nil {
		compaction := *snapshot.LastCompaction
		snapshot.LastCompaction = &compaction
	}
	if snapshot.Workspace != nil {
		workspace := cloneWorkspaceSnapshot(*snapshot.Workspace)
		snapshot.Workspace = &workspace
	}
	return snapshot
}

// Clone returns an independent copy suitable for handing to another in-process
// consumer.
func (snapshot ThreadSnapshot) Clone() ThreadSnapshot {
	return cloneSnapshot(snapshot)
}

func cloneTools(tools []ToolState) []ToolState {
	tools = slices.Clone(tools)
	for i := range tools {
		tools[i] = cloneTool(tools[i])
	}
	return tools
}

func cloneTool(tool ToolState) ToolState {
	tool.WriteAudit = slices.Clone(tool.WriteAudit)
	if tool.Command != nil {
		command := *tool.Command
		if command.ExitCode != nil {
			exitCode := *command.ExitCode
			command.ExitCode = &exitCode
		}
		tool.Command = &command
	}
	return tool
}

func cloneWorkspaceSnapshot(snapshot codingworkspace.Snapshot) codingworkspace.Snapshot {
	snapshot.ChangedPaths = slices.Clone(snapshot.ChangedPaths)
	return snapshot
}
