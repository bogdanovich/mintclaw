package tasks

import (
	"path/filepath"
	"testing"
)

func TestTerminalStatusForObjectiveOutcome(t *testing.T) {
	tests := []struct {
		name    string
		outcome *ObjectiveOutcome
		want    Status
	}{
		{name: "legacy", want: StatusSucceeded},
		{name: "succeeded", outcome: &ObjectiveOutcome{Status: "succeeded"}, want: StatusSucceeded},
		{name: "partial", outcome: &ObjectiveOutcome{Status: "partial"}, want: StatusFailed},
		{name: "blocked", outcome: &ObjectiveOutcome{Status: "blocked"}, want: StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TerminalStatusForObjectiveOutcome(tt.outcome); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompleteInteractionTaskResultPreservesBlockedStatus(t *testing.T) {
	registry := NewRegistry("")
	if err := registry.Upsert(Record{
		TaskID: "blocked-browser-task", Status: StatusRunning, DeliveryStatus: DeliveryPending,
		InteractionID: "approval-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.CompleteInteractionTaskResult(
		"blocked-browser-task",
		"approval-1",
		"Task could not be completed",
		&ObjectiveOutcome{Status: "blocked", MissingItems: []string{"browser verification"}},
		DeliveryDelivered,
	); err != nil {
		t.Fatal(err)
	}
	record, ok := registry.Get("blocked-browser-task")
	if !ok || record.Status != StatusFailed || record.Error != "Task could not be completed" ||
		record.Completion == nil || record.Completion.ObjectiveOutcome == nil ||
		record.Completion.ObjectiveOutcome.Status != "blocked" {
		t.Fatalf("record = %#v", record)
	}
}

func TestRegistryPersistsHistoryDisabledPolicy(t *testing.T) {
	store := filepath.Join(t.TempDir(), "task_registry.json")
	registry := NewRegistry(store)
	if err := registry.Upsert(Record{
		TaskID: "stateless-task", Status: StatusRunning, DeliveryStatus: DeliveryPending,
		HistoryPolicyKnown: true, HistoryDisabled: true,
		PendingObservation: "terminal result", ObservationMarker: "marker-1",
	}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewRegistry(store)
	record, ok := reloaded.Get("stateless-task")
	if !ok || !record.HistoryPolicyKnown || !record.HistoryDisabled ||
		record.PendingObservation != "terminal result" || record.ObservationMarker != "marker-1" {
		t.Fatalf("reloaded record = %#v", record)
	}
}
