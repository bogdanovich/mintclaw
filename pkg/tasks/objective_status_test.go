package tasks

import (
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestTerminalStatusForObjectiveOutcome(t *testing.T) {
	tests := []struct {
		name    string
		outcome *taskresult.Outcome
		want    Status
	}{
		{name: "no outcome", want: StatusSucceeded},
		{name: "succeeded", outcome: &taskresult.Outcome{Status: "succeeded"}, want: StatusSucceeded},
		{name: "partial", outcome: &taskresult.Outcome{Status: "partial"}, want: StatusFailed},
		{name: "blocked", outcome: &taskresult.Outcome{Status: "blocked"}, want: StatusFailed},
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
		&taskresult.Outcome{Status: "blocked", MissingItems: []string{"browser verification"}},
		DeliveryDelivered,
	); err != nil {
		t.Fatal(err)
	}
	record, ok := registry.Get("blocked-browser-task")
	if !ok || record.Status != StatusFailed || record.Error != "Task could not be completed" ||
		record.Deliverable == nil || record.Deliverable.ObjectiveOutcome == nil ||
		record.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("record = %#v", record)
	}
}

func TestRegistryPersistsHistoryDisabledPolicy(t *testing.T) {
	store := filepath.Join(t.TempDir(), "task_registry.json")
	registry := NewRegistry(store)
	if err := registry.Upsert(Record{
		TaskID: "stateless-task", Status: StatusRunning, DeliveryStatus: DeliveryPending,
		HistoryPolicyKnown: true, HistoryDisabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewRegistry(store)
	record, ok := reloaded.Get("stateless-task")
	if !ok || !record.HistoryPolicyKnown || !record.HistoryDisabled {
		t.Fatalf("reloaded record = %#v", record)
	}
}
