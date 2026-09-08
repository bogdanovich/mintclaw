package worker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestSnapshotFromFrontendProjectsCanonicalItemsWithoutFrontendLifecycle(t *testing.T) {
	binding := testBinding(t)
	completedAt := time.Now().UTC()
	exitCode := 0
	source := frontend.ThreadSnapshot{
		ThreadID:     binding.ThreadID,
		ActiveTurnID: "turn-1",
		Activity:     frontend.ActivityRunning,
		LastTurn: &frontend.LastTurnOutcome{
			TurnID:  "turn-0",
			Outcome: frontend.TurnOutcomeCompleted,
		},
		ContextUsage: frontend.ContextUsage{UsedTokens: 25, LimitTokens: 100},
		Status:       "working\ncarefully",
		Items: []frontend.PresentationItem{
			{
				ID:          "message:turn-1:user-1",
				TurnID:      "turn-1",
				Sequence:    1,
				Revision:    1,
				Kind:        frontend.PresentationUserMessage,
				Lifecycle:   frontend.PresentationCompleted,
				CreatedAt:   completedAt.Add(-2 * time.Second),
				StartedAt:   completedAt.Add(-time.Second),
				CompletedAt: &completedAt,
				Duration:    time.Second,
				Message: &frontend.TranscriptEntry{
					ID:       "user-1",
					TurnID:   "turn-1",
					Kind:     frontend.EntryUser,
					Text:     "inspect\nthis repository",
					Complete: true,
				},
			},
			{
				ID:          "tool:turn-1:call-1",
				TurnID:      "turn-1",
				Sequence:    2,
				Revision:    3,
				Kind:        frontend.PresentationToolCall,
				Lifecycle:   frontend.PresentationCompleted,
				CreatedAt:   completedAt.Add(-time.Second),
				StartedAt:   completedAt.Add(-time.Second),
				CompletedAt: &completedAt,
				Duration:    750 * time.Millisecond,
				Tool: &frontend.ToolState{
					TurnID:    "turn-1",
					CallID:    "call-1",
					Name:      "read_file",
					Arguments: "{\"path\":\"README.md\"}",
					Output:    "contents",
					Status:    frontend.ToolSucceeded,
					Duration:  750 * time.Millisecond,
					Command: &frontend.CommandState{
						Action: "run", Command: "printf contents", CWD: "/repo", Source: frontend.CommandSourceAgent,
						Output: "contents", Duration: 700 * time.Millisecond,
						Transcript: []frontend.CommandTranscriptEntry{
							{Sequence: 1, Stream: "stdout", Text: "contents"},
						},
						Status: frontend.CommandSucceeded, ExitCode: &exitCode, OwnsProcess: true,
					},
				},
			},
			{
				ID:        "plan:turn-1:call-2",
				TurnID:    "turn-1",
				Sequence:  3,
				Revision:  1,
				Kind:      frontend.PresentationPlanUpdate,
				Lifecycle: frontend.PresentationCompleted,
				CreatedAt: completedAt,
				StartedAt: completedAt,
				Plan: &frontend.PlanState{
					CallID:      "call-2",
					Explanation: "Inspect first",
					Steps: []frontend.PlanStepState{
						{Step: "Inspect", Status: frontend.PlanStepCompleted},
						{Step: "Report", Status: frontend.PlanStepInProgress},
					},
				},
			},
		},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 3 || snapshot.Items[0].Message == nil ||
		snapshot.Items[1].Tool == nil || snapshot.Items[2].Plan == nil {
		t.Fatalf("SnapshotFromFrontend() items = %#v", snapshot.Items)
	}
	if snapshot.Items[1].Tool.Duration != int64(750*time.Millisecond) ||
		snapshot.Items[1].Tool.Command == nil || snapshot.Items[1].Tool.Command.ExitCode == nil {
		t.Fatalf("projected tool = %#v", snapshot.Items[1].Tool)
	}
	command := snapshot.Items[1].Tool.Command
	if command.Command != "printf contents" || command.CWD != "/repo" || command.Source != CommandSourceAgent ||
		command.Duration != int64(700*time.Millisecond) || !command.OwnsProcess ||
		len(command.Transcript) != 1 || command.Transcript[0].Text != "contents" {
		t.Fatalf("projected command = %#v", command)
	}

	raw, err := json.Marshal(snapshot.Items)
	if err != nil {
		t.Fatal(err)
	}
	var wireItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wireItems); err != nil {
		t.Fatal(err)
	}
	for index, item := range wireItems {
		for _, omitted := range []string{"kind", "lifecycle", "created_at", "started_at", "completed_at", "duration"} {
			if _, exists := item[omitted]; exists {
				t.Fatalf("wire item %d contains duplicated frontend field %q: %s", index, omitted, raw)
			}
		}
	}
	var wireTool map[string]json.RawMessage
	if err := json.Unmarshal(wireItems[1]["tool"], &wireTool); err != nil {
		t.Fatal(err)
	}
	if _, exists := wireTool["duration_ns"]; !exists {
		t.Fatalf("wire tool omits nested duration_ns: %s", wireItems[1]["tool"])
	}
	if _, exists := wireTool["duration"]; exists {
		t.Fatalf("wire tool contains ambiguous duration: %s", wireItems[1]["tool"])
	}
	var wireCommand map[string]json.RawMessage
	if err := json.Unmarshal(wireTool["command"], &wireCommand); err != nil {
		t.Fatal(err)
	}
	if _, exists := wireCommand["duration_ns"]; !exists {
		t.Fatalf("wire command omits duration_ns: %s", wireTool["command"])
	}
	if _, exists := wireCommand["duration"]; exists {
		t.Fatalf("wire command contains ambiguous duration: %s", wireTool["command"])
	}
}

func TestSnapshotFromFrontendProjectsBoundedTypedExploration(t *testing.T) {
	binding := testBinding(t)
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityRunning,
		Items: []frontend.PresentationItem{{
			ID: "tool:turn-1:call-search", TurnID: "turn-1", Sequence: 1, Revision: 1,
			Tool: &frontend.ToolState{
				CallID: "call-search", Name: "search_files", Status: frontend.ToolRunning,
				Exploration: &frontend.ExplorationState{
					Operation: frontend.ExplorationSearch,
					Path:      strings.Repeat("p", MaxExplorationValue+20),
					Pattern:   "needle\nvalue",
					Workspace: "build",
				},
			},
		}},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Tool == nil ||
		snapshot.Items[0].Tool.Exploration == nil {
		t.Fatalf("projected exploration items = %#v", snapshot.Items)
	}
	exploration := snapshot.Items[0].Tool.Exploration
	if exploration.Operation != ExplorationSearch || len(exploration.Path) > MaxExplorationValue ||
		exploration.Pattern != "needle value" || exploration.Workspace != "build" ||
		!exploration.Truncated {
		t.Fatalf("projected exploration = %#v", exploration)
	}
}

func TestSnapshotFromFrontendRetainsNewestCountBoundedSuffix(t *testing.T) {
	binding := testBinding(t)
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityIdle,
		Items:    make([]frontend.PresentationItem, 140),
	}
	for index := range source.Items {
		sequence := uint64(index + 1)
		source.Items[index] = frontend.PresentationItem{
			ID:       fmt.Sprintf("message:turn-1:%03d", sequence),
			TurnID:   "turn-1",
			Sequence: sequence,
			Revision: 1,
			Message: &frontend.TranscriptEntry{
				Kind:     frontend.EntryAssistant,
				Phase:    frontend.AssistantPhaseFinal,
				Text:     "done",
				Complete: true,
			},
		}
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if len(snapshot.Items) != MaxSnapshotItems || !snapshot.ItemsTruncated {
		t.Fatalf("bounded snapshot items = %d, truncated = %t", len(snapshot.Items), snapshot.ItemsTruncated)
	}
	if snapshot.Items[0].Sequence != 13 || snapshot.Items[len(snapshot.Items)-1].Sequence != 140 {
		t.Fatalf("bounded snapshot sequence range = %d..%d", snapshot.Items[0].Sequence,
			snapshot.Items[len(snapshot.Items)-1].Sequence)
	}
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
}

func TestSnapshotFromFrontendRetainsNewestByteBoundedSuffix(t *testing.T) {
	binding := testBinding(t)
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityIdle,
		Items:    make([]frontend.PresentationItem, 40),
	}
	for index := range source.Items {
		sequence := uint64(index + 1)
		source.Items[index] = frontend.PresentationItem{
			ID:       fmt.Sprintf("message:turn-1:%03d", sequence),
			TurnID:   "turn-1",
			Sequence: sequence,
			Revision: 1,
			Message: &frontend.TranscriptEntry{
				Kind:     frontend.EntryAssistant,
				Phase:    frontend.AssistantPhaseCommentary,
				Text:     strings.Repeat("x", MaxEventTextBytes),
				Complete: true,
			},
		}
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if len(snapshot.Items) == 0 || len(snapshot.Items) >= len(source.Items) || !snapshot.ItemsTruncated {
		t.Fatalf("byte-bounded snapshot items = %d, truncated = %t", len(snapshot.Items), snapshot.ItemsTruncated)
	}
	if snapshot.Items[len(snapshot.Items)-1].Sequence != 40 {
		t.Fatalf("newest retained sequence = %d, want 40", snapshot.Items[len(snapshot.Items)-1].Sequence)
	}
	raw, err := json.Marshal(snapshot.Items)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxSnapshotItemsBytes {
		t.Fatalf("encoded items use %d bytes, limit %d", len(raw), MaxSnapshotItemsBytes)
	}
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
}

func TestSnapshotFromFrontendBoundsUnsafeIdentitiesDeterministically(t *testing.T) {
	binding := testBinding(t)
	unsafeTurnID := "turn\n" + strings.Repeat("x", MaxItemIdentityBytes)
	source := frontend.ThreadSnapshot{
		ThreadID:     binding.ThreadID,
		ActiveTurnID: unsafeTurnID,
		Activity:     frontend.ActivityIdle,
		LastTurn: &frontend.LastTurnOutcome{
			TurnID:  unsafeTurnID,
			Outcome: frontend.TurnOutcomeFailed,
		},
		Items: []frontend.PresentationItem{{
			ID:       "item\nforged",
			TurnID:   unsafeTurnID,
			Sequence: 1,
			Revision: 1,
			Message: &frontend.TranscriptEntry{
				Kind:     frontend.EntryError,
				Text:     "failed",
				Complete: true,
			},
		}},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if !strings.HasPrefix(snapshot.ActiveTurnID, "wire:") ||
		snapshot.LastTurn == nil || snapshot.LastTurn.TurnID != snapshot.ActiveTurnID ||
		snapshot.Items[0].TurnID != snapshot.ActiveTurnID ||
		!strings.HasPrefix(snapshot.Items[0].ID, "wire:") {
		t.Fatalf("bounded identities = %#v", snapshot)
	}
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
}

func TestSnapshotFromFrontendNormalizesMaximumFrontendToolToEncodableWireItem(t *testing.T) {
	binding := testBinding(t)
	audits := make([]frontend.WriteAudit, 128)
	for index := range audits {
		audits[index] = frontend.WriteAudit{
			Kind:    "file\nchange",
			Target:  strings.Repeat("t", MaxEventTextBytes),
			Action:  "write",
			Success: true,
			Tool:    strings.Repeat("w", MaxEventTextBytes),
		}
	}
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityIdle,
		Items: []frontend.PresentationItem{{
			ID:       "tool:turn-1:call-1",
			TurnID:   "turn-1",
			Sequence: 1,
			Revision: 1,
			Tool: &frontend.ToolState{
				TurnID:     "turn-1",
				CallID:     "call-1",
				Name:       strings.Repeat("n", MaxEventTextBytes),
				Arguments:  strings.Repeat("a", MaxEventTextBytes),
				Output:     "output\x1b[31m",
				Status:     frontend.ToolSucceeded,
				WriteAudit: audits,
				Command: &frontend.CommandState{
					Action: strings.Repeat("r", MaxEventTextBytes), Command: strings.Repeat("m", MaxEventTextBytes),
					CWD: strings.Repeat("d", MaxEventTextBytes), Input: strings.Repeat("i", MaxEventTextBytes),
					Stdout: strings.Repeat("o", MaxEventTextBytes), Stderr: strings.Repeat("e", MaxEventTextBytes),
					Output: strings.Repeat("c", MaxEventTextBytes), Status: frontend.CommandUnknown,
					SessionID: strings.Repeat("s", MaxEventTextBytes),
					Transcript: []frontend.CommandTranscriptEntry{
						{Sequence: 1, Stream: "stdout", Text: strings.Repeat("t", MaxEventTextBytes)},
						{Sequence: 2, Stream: "stderr", Text: "omitted"},
					},
				},
			},
		}},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if len(snapshot.Items) != 1 || snapshot.Items[0].Tool == nil {
		t.Fatalf("normalized snapshot items = %#v", snapshot.Items)
	}
	tool := snapshot.Items[0].Tool
	if !tool.Truncated || len(tool.WriteAudit) != MaxEventWriteAudits ||
		len(tool.Name) > MaxAttachmentMeta || len(tool.WriteAudit[0].Target) > MaxAuditTargetBytes ||
		len(tool.WriteAudit[0].Tool) > MaxAttachmentMeta || tool.Command == nil || !tool.Command.Truncated {
		t.Fatalf("normalized tool = %#v", tool)
	}
	if strings.ContainsRune(tool.Output, '\x1b') || strings.ContainsRune(tool.WriteAudit[0].Kind, '\n') {
		t.Fatalf("normalized tool retains structural control: %#v", tool)
	}
	if tool.Command.Status != CommandUnknown || len(tool.Command.Action) > MaxAttachmentMeta ||
		len(tool.Command.SessionID) > MaxAttachmentMeta || len(tool.Command.Transcript) != 1 ||
		len(tool.Command.Transcript[0].Text) > MaxEventTextBytes {
		t.Fatalf("normalized command lifecycle = %#v", tool.Command)
	}
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	payload := mustPayload(t, ItemUpdatedPayload{
		ControlIdentity: binding.ControlIdentity(),
		Item:            snapshot.Items[0],
	})
	if _, err := Encode(Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordEvent,
		Event:         EventItemUpdated,
		Payload:       payload,
	}); err != nil {
		t.Fatalf("Encode(maximum projected item) error = %v", err)
	}
}
