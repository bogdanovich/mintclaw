package supervision

import (
	"context"
	"errors"
	"testing"
)

func TestUnavailableLauncherAndWorker(t *testing.T) {
	ctx := context.Background()
	expired, cancel := context.WithCancel(ctx)
	cancel()
	var launcher *Launcher
	var worker *Worker
	if value, err := OpenLauncher(expired); value != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("expired startup context admitted")
	}
	if value, err := launcher.Prepare(ctx); value != nil || !errors.Is(err, errUnavailable) {
		t.Fatal("missing launcher admitted")
	}
	if listener, control, err := worker.Endpoints(ctx); listener != nil || control != nil ||
		!errors.Is(err, errUnavailable) {
		t.Fatal("missing worker exported endpoints")
	}
	if !errors.Is(worker.Start(ctx), errUnavailable) || !errors.Is(worker.Retire(ctx), errUnavailable) ||
		!errors.Is(launcher.Close(ctx), errUnavailable) || worker.Done() != nil {
		t.Fatal("missing launch resource admitted")
	}
	if !errors.Is(BootstrapBrowser(expired), errUnavailable) {
		t.Fatal("expired bootstrap context admitted")
	}
}
