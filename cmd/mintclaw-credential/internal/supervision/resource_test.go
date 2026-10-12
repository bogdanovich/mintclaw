package supervision

import (
	"context"
	"errors"
	"testing"
)

func TestUnavailableResourceWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(ServeResources(ctx), errUnavailable) {
		t.Fatal("expired resource service admitted")
	}
	if worker, err := AllocateResources(ctx); worker != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("expired allocation admitted")
	}
	for _, worker := range []*ResourceWorker{nil, {}} {
		if listener, control, err := worker.EndpointFiles(context.Background()); listener != nil || control != nil ||
			!errors.Is(err, errUnavailable) {
			t.Fatal("unavailable resource endpoints admitted")
		}
		if !errors.Is(worker.Start(context.Background()), errUnavailable) ||
			!errors.Is(worker.Retire(context.Background()), errUnavailable) || worker.Done() != nil {
			t.Fatal("unavailable resource worker admitted")
		}
		worker.Close()
	}
}
