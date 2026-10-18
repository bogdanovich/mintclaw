package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestInspectOwnersBoundsTaskRelatedInteractionExpansion(t *testing.T) {
	workspace := t.TempDir()
	const taskID = "task-owner-bounded"
	taskRegistry := tasks.NewRegistry(tasks.WorkspaceStorePath(workspace))
	if err := taskRegistry.Create(tasks.Record{TaskID: taskID, Status: tasks.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(workspace))
	for index := 0; index < HardOwnerIDs+5; index++ {
		record, err := registry.Create(interactions.CreateRequest{
			ID:   fmt.Sprintf("interaction_%016x", index),
			Kind: interactions.KindQuestion,
			Route: interactions.Route{
				AgentID: "main", SessionKey: fmt.Sprintf("private-session-%d", index), Channel: "telegram",
				ChatID: "private-chat", SenderID: "private-sender",
			},
			Origin: interactions.Origin{
				TurnID: fmt.Sprintf("turn-%d", index), ToolCallID: fmt.Sprintf("call-%d", index),
				ToolName: "request_user_input", TaskID: taskID,
			},
			Questions: []interactions.Question{{ID: "choice", Question: "private"}},
			ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = registry.Cancel(record.ID, record.Revision, "test_fixture"); err != nil {
			t.Fatal(err)
		}
	}

	report, err := InspectOwners(OwnerOptions{
		Query:      QueryResult{Selector: Selector{TaskID: taskID}},
		Workspaces: []OwnerWorkspaceSpec{{ID: "profile-main", Workspace: workspace}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.IdentityTruncated || len(report.Interactions) != HardOwnerIDs {
		t.Fatalf("bounded related interactions = %d truncated=%v", len(report.Interactions), report.IdentityTruncated)
	}
	interactionLookups := 0
	for _, outcome := range report.Lookups {
		if outcome.Kind == "interaction" {
			interactionLookups++
			if outcome.Status != "found" {
				t.Fatalf("interaction lookup = %#v", outcome)
			}
		}
	}
	if interactionLookups != HardOwnerIDs {
		t.Fatalf("interaction lookup count = %d", interactionLookups)
	}
}

func TestInspectOwnersAccountsForMissingAndUnavailableSelectedOwners(t *testing.T) {
	workspace := t.TempDir()
	codingRoot := filepath.Join(t.TempDir(), "missing-coding")
	report, err := InspectOwners(OwnerOptions{
		Query: QueryResult{Selector: Selector{
			TaskID: "task-missing", InteractionID: "interaction-missing",
			DeliveryID: "delivery-missing", InvocationID: "invocation-missing",
			ThreadID: "00000000-0000-4000-8000-000000000001",
		}},
		Workspaces: []OwnerWorkspaceSpec{{ID: "profile-main", Workspace: workspace}},
		CodingRoot: codingRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Lookups) != 5 {
		t.Fatalf("missing lookup outcomes = %#v", report.Lookups)
	}
	for _, outcome := range report.Lookups {
		if outcome.Status != "not_found" {
			t.Fatalf("missing owner outcome = %#v", outcome)
		}
	}
	for _, health := range report.Stores {
		if health.Kind == "coding_threads" && health.Status != "not_found" {
			t.Fatalf("missing coding store health = %#v", health)
		}
	}

	taskPath := tasks.WorkspaceStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = InspectOwners(OwnerOptions{
		Query:      QueryResult{Selector: Selector{TaskID: "task-unavailable"}},
		Workspaces: []OwnerWorkspaceSpec{{ID: "profile-main", Workspace: workspace}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Lookups) != 1 || report.Lookups[0].Status != "unavailable" ||
		report.Lookups[0].ErrorCode != "store_unavailable" {
		t.Fatalf("unavailable owner outcome = %#v", report.Lookups)
	}
}

func TestRetiredDeliveryInspectionKeepsTerminalTruthWithoutPayload(t *testing.T) {
	root := t.TempDir()
	spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	input, err := spool.Prepare(t.Context(), bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "private-route",
			SenderID: "owner",
		},
		Content: "diagnostic input",
	}))
	if err != nil {
		t.Fatal(err)
	}
	native := bus.NewMessageBus()
	native.SetInboundSpool(spool)
	t.Cleanup(native.Close)
	coordinator, err := outbox.OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	admission, err := coordinator.AdmitNativeFinalMessage(
		root,
		outbox.Identity{
			SourceID: input.SpoolID,
			Kind:     outbox.KindMessage,
			Channel:  "telegram",
			ChatID:   "private-route",
		},
		bus.OutboundMessage{Content: "PRIVATE_RETIRED_BODY"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{}); err != nil {
		t.Fatal(err)
	}
	proof, err := native.AckInbound(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := coordinator.SealNativeFinalMessage(admission.Intent.ID, proof)
	if err != nil || !sealed {
		t.Fatalf("native diagnostic fixture seal=%v,%v", sealed, err)
	}
	if err = coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = outbox.RetirePayloadsOffline(t.Context(), root, outbox.RetirementPolicy{
		DeliveryIDs: []string{admission.Intent.ID}, MinAge: time.Nanosecond, Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	report := OwnerReport{}
	ids := ownerIDs{deliveries: map[string]struct{}{admission.Intent.ID: {}}}
	inspectDeliveryOwners(OwnerWorkspaceSpec{ID: "main", Workspace: root}, &ids, &report)
	if len(report.Deliveries) != 1 || !report.Deliveries[0].PayloadRetired ||
		report.Deliveries[0].Status != outbox.StatusDelivered || report.Deliveries[0].PayloadRetiredAt.IsZero() {
		t.Fatalf("retired inspection = %#v", report)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("PRIVATE_RETIRED_BODY")) || bytes.Contains(data, []byte("private-route")) {
		t.Fatal("retired owner report exposed payload or route")
	}
}
