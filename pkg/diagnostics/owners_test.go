package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestInspectOwnersCorrelatesExactDurableStateWithoutContent(t *testing.T) {
	workspace := t.TempDir()
	const (
		taskID        = "task-owner-safe"
		interactionID = "interaction_01234567abcdef"
		privateMarker = "PRIVATE_OWNER_MARKER_must_not_escape"
	)
	taskRegistry := tasks.NewRegistry(tasks.WorkspaceStorePath(workspace))
	if err := taskRegistry.Create(tasks.Record{
		TaskID: taskID, Task: privateMarker, Label: privateMarker,
		Status: tasks.StatusRunning, ParentTaskID: "parent-safe",
	}); err != nil {
		t.Fatal(err)
	}

	outboxStore, err := outbox.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	identity := outbox.Identity{
		SourceID: "interaction-prompt", Channel: "telegram", ChatID: "private-chat",
		SessionKey: "private-session", Kind: outbox.KindMessage,
	}
	intent, err := outbox.NewMessageIntent(workspace, identity, bus.OutboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "private-chat", SenderID: "private-sender",
		},
		SessionKey: identity.SessionKey,
		Content:    privateMarker,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	intent, err = outboxStore.Create(intent)
	if err != nil {
		t.Fatal(err)
	}

	interactionRegistry := interactions.NewRegistry(interactions.WorkspaceStorePath(workspace))
	record, err := interactionRegistry.Create(interactions.CreateRequest{
		ID:   interactionID,
		Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: "main", SessionKey: "private-session", Channel: "telegram",
			ChatID: "private-chat", SenderID: "private-sender",
		},
		Origin: interactions.Origin{
			TurnID: "turn-owner-safe", ToolCallID: "call-owner-safe",
			ToolName: "request_user_input", TaskID: taskID,
		},
		Questions: []interactions.Question{{ID: "choice", Question: privateMarker}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = interactionRegistry.BindPromptDelivery(record.ID, record.Revision, intent.ID); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		tasks.WorkspaceStorePath(workspace),
		interactions.WorkspaceStorePath(workspace),
		filepath.Join(outbox.Path(workspace), intent.ID+".json"),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}

	report, err := InspectOwners(OwnerOptions{
		Query: QueryResult{
			GeneratedAt: time.Now().UTC(),
			Selector:    Selector{TaskID: taskID},
		},
		Workspaces: []OwnerWorkspaceSpec{{ID: "profile-main", Alias: "main", Workspace: workspace}},
		CodingRoot: filepath.Join(t.TempDir(), "coding"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 1 || len(report.Interactions) != 1 || len(report.Deliveries) != 1 ||
		report.Tasks[0].TaskID != taskID || report.Interactions[0].InteractionID != interactionID ||
		report.Deliveries[0].DeliveryID != intent.ID {
		t.Fatalf("owner report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		privateMarker, workspace, "private-chat", "private-session", "private-sender",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("owner report leaked private value %q: %s", private, encoded)
		}
	}
	for _, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before[path], after) {
			t.Fatalf("owner inspection changed %s", path)
		}
	}
}

func TestInspectOwnersExplicitTraceRootsRemainTraceOnly(t *testing.T) {
	report, err := InspectOwners(OwnerOptions{
		TraceRootsExplicit: true,
		Workspaces:         []OwnerWorkspaceSpec{{ID: "invalid workspace", Workspace: "private"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "trace_only" || report.Availability != "unavailable_by_design" ||
		len(report.Stores) != 0 {
		t.Fatalf("trace-only owner report = %#v", report)
	}
}

func TestInspectOwnersRejectsUnsafeOrDuplicateWorkspaceMetadata(t *testing.T) {
	workspace := t.TempDir()
	for name, workspaces := range map[string][]OwnerWorkspaceSpec{
		"unsafe alias": {{ID: "profile-main", Alias: "/private/alias", Workspace: workspace}},
		"duplicate ID": {
			{ID: "profile-main", Alias: "main", Workspace: workspace},
			{ID: "profile-main", Alias: "other", Workspace: t.TempDir()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectOwners(OwnerOptions{Workspaces: workspaces}); err == nil {
				t.Fatal("unsafe owner workspace metadata was accepted")
			}
		})
	}
}

func TestOwnerProjectionDropsFreeFormIdentifierFields(t *testing.T) {
	inspection := projectTaskInspection("profile-main", tasks.Record{
		TaskID: "task-safe", GenerationID: "private generation value", ParentTaskID: "/private/path",
		LastCompletionID: "private completion value",
	})
	if inspection.GenerationID != "" || inspection.ParentTaskID != "" || inspection.LastCompletionID != "" {
		t.Fatalf("unsafe owner tokens survived = %#v", inspection)
	}
}
