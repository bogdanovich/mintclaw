package frontend

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Reducer is frontend-local state. It can recover only through SnapshotSource,
// which keeps runtime internals out of terminal models and future IPC clients.
type Reducer struct {
	state ThreadSnapshot
}

func NewReducer(snapshot ThreadSnapshot) (*Reducer, error) {
	reducer := &Reducer{}
	if err := reducer.ApplySnapshot(snapshot); err != nil {
		return nil, err
	}
	return reducer, nil
}

func (r *Reducer) State() ThreadSnapshot {
	if r == nil {
		return ThreadSnapshot{}
	}
	return cloneSnapshot(r.state)
}

func (r *Reducer) ApplySnapshot(snapshot ThreadSnapshot) error {
	if snapshot.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported coding frontend protocol %q", snapshot.ProtocolVersion)
	}
	if snapshot.ThreadID == "" {
		return fmt.Errorf("coding frontend snapshot has no thread ID")
	}
	if r.state.ThreadID != "" && snapshot.ThreadID != r.state.ThreadID {
		return ErrThreadMismatch
	}
	if r.state.ThreadID != "" && snapshot.Revision < r.state.Revision {
		return ErrRevisionGap
	}
	r.state = cloneSnapshot(snapshot)
	return nil
}

func (r *Reducer) Apply(delta Delta) error {
	if r == nil {
		return fmt.Errorf("coding frontend reducer is nil")
	}
	if delta.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported coding frontend protocol %q", delta.ProtocolVersion)
	}
	if delta.ThreadID != r.state.ThreadID {
		return ErrThreadMismatch
	}
	if delta.RequiresSnapshot || delta.PreviousRevision != r.state.Revision || delta.Revision != r.state.Revision+1 {
		return ErrRevisionGap
	}
	r.state.Revision = delta.Revision
	r.state.Activity = delta.Activity
	r.state.Status = delta.Status
	if delta.ContextUsage != nil {
		r.state.ContextUsage = *delta.ContextUsage
	}
	if delta.Workspace != nil {
		workspace := cloneWorkspaceSnapshot(*delta.Workspace)
		r.state.Workspace = &workspace
	}
	if delta.Entry != nil {
		r.state.Entries = replaceEntry(r.state.Entries, *delta.Entry)
	}
	if delta.Tool != nil {
		r.state.Tools = replaceTool(r.state.Tools, *delta.Tool)
	}
	return nil
}

// ApplyOrResync applies a live delta. A gap or explicit bounded-window reset is
// recovered from an authoritative snapshot without consulting runtime state.
func (r *Reducer) ApplyOrResync(ctx context.Context, source SnapshotSource, delta Delta) error {
	if err := r.Apply(delta); err == nil {
		return nil
	} else if !errors.Is(err, ErrRevisionGap) {
		return err
	}
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		return err
	}
	return r.ApplySnapshot(snapshot)
}

// CatchUp applies the retained bounded delta window and falls back to a fresh
// snapshot when the requested revision has already fallen out of that window.
func (r *Reducer) CatchUp(ctx context.Context, source SnapshotSource) error {
	deltas, err := source.ChangesSince(ctx, r.state.Revision)
	if errors.Is(err, ErrRevisionUnavailable) {
		snapshot, snapshotErr := source.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		return r.ApplySnapshot(snapshot)
	}
	if err != nil {
		return err
	}
	for _, delta := range deltas {
		if err = r.Apply(delta); err != nil {
			if !errors.Is(err, ErrRevisionGap) {
				return err
			}
			snapshot, snapshotErr := source.Snapshot(ctx)
			if snapshotErr != nil {
				return snapshotErr
			}
			return r.ApplySnapshot(snapshot)
		}
	}
	return nil
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

func replaceTool(tools []ToolState, replacement ToolState) []ToolState {
	tools = slices.Clone(tools)
	for i := range tools {
		if tools[i].CallID == replacement.CallID {
			tools[i] = replacement
			return tools
		}
	}
	return append(tools, replacement)
}
