package agent

import (
	"context"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func (p *Pipeline) emitPendingCodingWorkspaceSnapshot(ts *turnState, stage string) {
	if p == nil || ts == nil || ts.agent == nil || ts.agent.ContextBuilder == nil {
		return
	}
	snapshot, changed := ts.agent.ContextBuilder.pendingCodingWorkspaceUpdate(context.Background())
	if !changed {
		return
	}
	p.emitEvent(
		runtimeevents.KindAgentWorkspaceSnapshot,
		ts.eventMeta("runTurn", stage),
		WorkspaceSnapshotPayload{Snapshot: snapshot},
	)
}

func (p *Pipeline) refreshCodingWorkspaceAfterTool(
	ts *turnState,
	toolName string,
	result *toolshared.ToolResult,
) {
	if p == nil || ts == nil || ts.agent == nil || ts.agent.ContextBuilder == nil || result == nil {
		return
	}
	if toolName != "exec" && len(result.WriteAudit) == 0 {
		return
	}
	snapshot, changed := ts.agent.ContextBuilder.RefreshCodingWorkspace(context.Background())
	if !changed {
		return
	}
	p.emitEvent(
		runtimeevents.KindAgentWorkspaceSnapshot,
		ts.eventMeta("runTurn", "turn.workspace.refresh"),
		WorkspaceSnapshotPayload{Snapshot: snapshot},
	)
}
