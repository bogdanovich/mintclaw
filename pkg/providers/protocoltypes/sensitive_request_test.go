package protocoltypes

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSensitiveRequestContextIsScopedAndPreservesLifetime(t *testing.T) {
	type unrelatedKey struct{}
	parent, cancel := context.WithDeadline(context.WithValue(t.Context(), unrelatedKey{}, "retained"),
		time.Now().Add(time.Minute))
	defer cancel()
	marked := WithSensitiveRequest(parent)
	if IsSensitiveRequest(context.Background()) || IsSensitiveRequest(parent) || !IsSensitiveRequest(marked) ||
		marked.Value(unrelatedKey{}) != "retained" || WithSensitiveRequest(marked) != marked {
		t.Fatal("sensitive marker was not scoped and idempotent")
	}
	want, _ := parent.Deadline()
	got, ok := marked.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatal("sensitive marker changed the deadline")
	}
	child, childCancel := context.WithCancel(marked)
	defer childCancel()
	if !IsSensitiveRequest(child) {
		t.Fatal("sensitive marker was not inherited")
	}
	cancel()
	if !errors.Is(child.Err(), context.Canceled) || !errors.Is(marked.Err(), context.Canceled) {
		t.Fatal("sensitive marker detached cancellation")
	}
}
