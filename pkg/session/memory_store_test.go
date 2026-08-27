package session

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestMemoryStoreDetachesCanonicalDeliverableAtBoundaries(t *testing.T) {
	store := NewMemoryStore()
	const key = "detached-deliverable"
	original := &taskresult.Deliverable{
		Text:     "original",
		Metadata: map[string]string{"producer": "tool"},
	}
	store.AddFullMessage(key, providers.Message{
		Role: "assistant", Content: "done", Deliverable: original,
	})
	original.Text = "mutated caller"
	original.Metadata["producer"] = "mutated caller"

	history := store.GetHistory(key)
	if len(history) != 1 || history[0].Deliverable == nil || history[0].Deliverable.Text != "original" ||
		history[0].Deliverable.Metadata["producer"] != "tool" {
		t.Fatalf("ingress retained caller aliases: %#v", history)
	}
	history[0].Deliverable.Text = "mutated get"
	page, err := store.ReadTurnHistoryPage(t.Context(), key, memory.HistoryPageRequest{Before: -1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page.Messages[0].Deliverable.Text = "mutated page"

	var callbackDeliverable *taskresult.Deliverable
	changed, err := store.MutateTurnHistory(
		t.Context(), key,
		func(current []providers.Message) ([]providers.Message, bool, error) {
			callbackDeliverable = current[0].Deliverable
			current[0].Deliverable.Text = "stored mutation"
			return current, true, nil
		},
	)
	if err != nil || !changed {
		t.Fatalf("MutateTurnHistory() = (%t, %v)", changed, err)
	}
	callbackDeliverable.Text = "mutated after callback"

	stored := store.GetHistory(key)
	if stored[0].Deliverable.Text != "stored mutation" || stored[0].Deliverable.Metadata["producer"] != "tool" {
		t.Fatalf("session boundary leaked deliverable alias: %#v", stored)
	}
}

func TestMemoryStoreCanceledWritesDoNotMutate(t *testing.T) {
	store := NewMemoryStore()
	store.SetHistory("turn", []providers.Message{{Role: "user", Content: "current"}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.AppendTurnMessage(
		ctx,
		"turn",
		providers.Message{Role: "user", Content: "canceled"},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("AppendTurnMessage() error = %v, want %v", err, context.Canceled)
	}
	if err := store.ReplaceTurnHistory(
		ctx,
		"turn",
		[]providers.Message{{Role: "user", Content: "replacement"}},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplaceTurnHistory() error = %v, want %v", err, context.Canceled)
	}
	history := store.GetHistory("turn")
	if len(history) != 1 || history[0].Content != "current" {
		t.Fatalf("canceled writes mutated history: %+v", history)
	}
}

func TestMemoryStoreReplacementAndClear(t *testing.T) {
	store := NewMemoryStore()
	store.SetSummary("turn", "retained summary")
	if err := store.ReplaceTurnHistory(
		t.Context(),
		"turn",
		[]providers.Message{{Role: "user", Content: "replacement"}},
	); err != nil {
		t.Fatal(err)
	}
	if summary := store.GetSummary("turn"); summary != "retained summary" {
		t.Fatalf("summary = %q, want retained summary", summary)
	}
	if err := store.ClearSession(t.Context(), "turn"); err != nil {
		t.Fatal(err)
	}
	if history := store.GetHistory("turn"); len(history) != 0 {
		t.Fatalf("history = %+v, want empty", history)
	}
	if summary := store.GetSummary("turn"); summary != "" {
		t.Fatalf("summary = %q, want empty", summary)
	}
}

func TestMemoryStoreRevisionAndListing(t *testing.T) {
	store := NewMemoryStore()
	store.SetHistory("second", []providers.Message{{Role: "user", Content: "first"}})
	first, err := store.GetHistoryRevision(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	store.SetHistory("second", []providers.Message{{Role: "user", Content: "second"}})
	store.SetSummary("first", "summary")
	second, err := store.GetHistoryRevision(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision+1 || second.Count != first.Count {
		t.Fatalf("revision = %+v, want revision %d and count %d", second, first.Revision+1, first.Count)
	}
	if keys := store.ListSessions(); !slices.Equal(keys, []string{"first", "second"}) {
		t.Fatalf("ListSessions() = %v", keys)
	}
}

func TestMemoryStoreRevisionHonorsCanceledContext(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := store.GetHistoryRevision(ctx, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetHistoryRevision() error = %v, want %v", err, context.Canceled)
	}
}
