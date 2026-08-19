package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type blockingRuntime struct {
	mu             sync.Mutex
	runStarted     chan string
	runRelease     chan struct{}
	compactStarted chan struct{}
	compactRelease chan struct{}
	interrupts     int
	hardCancels    int
	closes         int
	closeErr       error
	stopOnce       sync.Once
}

type pagedRuntime struct {
	*blockingRuntime
	page frontend.TranscriptPage
}

type workspaceRefreshRuntime struct {
	*blockingRuntime
	refreshes  int
	refreshErr error
}

type cancelCauseRuntime struct {
	*blockingRuntime
	joinedError error
}

func (r *cancelCauseRuntime) RunTurn(ctx context.Context, prompt string, ready func()) error {
	r.runStarted <- prompt
	ready()
	<-ctx.Done()
	return errors.Join(context.Cause(ctx), r.joinedError)
}

func (r *workspaceRefreshRuntime) RefreshWorkspace(context.Context) error {
	r.refreshes++
	return r.refreshErr
}

func (r *pagedRuntime) TranscriptPage(
	context.Context,
	frontend.TranscriptPageRequest,
) (frontend.TranscriptPage, error) {
	return r.page, nil
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{
		runStarted:     make(chan string, 1),
		runRelease:     make(chan struct{}),
		compactStarted: make(chan struct{}, 1),
		compactRelease: make(chan struct{}),
	}
}

func (r *blockingRuntime) RunTurn(ctx context.Context, prompt string, ready func()) error {
	r.runStarted <- prompt
	ready()
	select {
	case <-r.runRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *blockingRuntime) Interrupt(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interrupts++
	return nil
}

func (r *blockingRuntime) HardCancel(context.Context) error {
	r.mu.Lock()
	r.hardCancels++
	r.mu.Unlock()
	r.stopOnce.Do(func() { close(r.runRelease) })
	return nil
}

func (r *blockingRuntime) Compact(ctx context.Context) error {
	r.compactStarted <- struct{}{}
	select {
	case <-r.compactRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *blockingRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return r.closeErr
}

func newTestController(t *testing.T, runtime Runtime) *Controller {
	t.Helper()
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(projector, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestSubmitRunsOutsideCoordinatorAndRejectsSecondPrompt(t *testing.T) {
	runtime := newBlockingRuntime()
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Submit(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Interrupt(ctx); err != nil {
		t.Fatalf("immediate Interrupt() error = %v", err)
	}
	if err := controller.Submit(ctx, "second"); !errors.Is(err, ErrTurnActive) {
		t.Fatalf("second Submit() error = %v, want %v", err, ErrTurnActive)
	}
	if err := controller.HardCancel(ctx); err != nil {
		t.Fatalf("immediate HardCancel() error = %v", err)
	}
	select {
	case prompt := <-runtime.runStarted:
		if prompt != "first" {
			t.Fatalf("prompt = %q, want first", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.interrupts != 1 || runtime.hardCancels != 1 || runtime.closes != 1 {
		t.Fatalf(
			"controls = interrupt:%d hard:%d close:%d, want 1 each",
			runtime.interrupts,
			runtime.hardCancels,
			runtime.closes,
		)
	}
}

func TestHardCancelCauseIsNotProjectedAsTurnFailure(t *testing.T) {
	runtime := &cancelCauseRuntime{blockingRuntime: newBlockingRuntime()}
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Submit(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := controller.HardCancel(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		if entry.ID == "controller:turn-error" {
			t.Fatalf("intentional hard cancel was projected as a turn failure: %#v", entry)
		}
	}
}

func TestHardCancelDoesNotHideJoinedTurnFailure(t *testing.T) {
	runtime := &cancelCauseRuntime{
		blockingRuntime: newBlockingRuntime(),
		joinedError:     errors.New("persist turn metadata"),
	}
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Submit(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := controller.HardCancel(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		if entry.ID == "controller:turn-error" {
			return
		}
	}
	t.Fatal("hard cancel hid an independent joined turn failure")
}

func TestCompactionIsAsynchronousAndSerialized(t *testing.T) {
	runtime := newBlockingRuntime()
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.compactStarted:
	case <-time.After(time.Second):
		t.Fatal("compaction did not start")
	}
	if err := controller.Submit(ctx, "held"); !errors.Is(err, ErrCompactionActive) {
		t.Fatalf("Submit() during compaction error = %v, want %v", err, ErrCompactionActive)
	}
	if err := controller.Compact(ctx); !errors.Is(err, ErrCompactionActive) {
		t.Fatalf("second Compact() error = %v, want %v", err, ErrCompactionActive)
	}
	close(runtime.compactRelease)
	deadline := time.Now().Add(time.Second)
	for {
		err := controller.Submit(ctx, "after")
		if err == nil {
			break
		}
		if !errors.Is(err, ErrCompactionActive) || time.Now().After(deadline) {
			t.Fatalf("Submit() after compaction error = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	<-runtime.runStarted
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCloseCancelsActiveTurnAndIsIdempotent(t *testing.T) {
	runtime := newBlockingRuntime()
	controller := newTestController(t, runtime)
	if err := controller.Submit(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("immediate Close() error = %v", err)
	}
	<-runtime.runStarted
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.hardCancels != 1 || runtime.closes != 1 {
		t.Fatalf("hard cancels = %d, closes = %d, want 1 each", runtime.hardCancels, runtime.closes)
	}
}

func TestUnsupportedCommandsAreExplicit(t *testing.T) {
	runtime := newBlockingRuntime()
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Rename(ctx, "title"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Rename() error = %v, want %v", err, ErrUnsupported)
	}
	if err := controller.NewThread(ctx); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("NewThread() error = %v, want %v", err, ErrUnsupported)
	}
	if err := controller.Interrupt(ctx); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("Interrupt() error = %v, want %v", err, ErrNoActiveTurn)
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPageDelegatesOptionalRuntimeCapability(t *testing.T) {
	runtime := &pagedRuntime{
		blockingRuntime: newBlockingRuntime(),
		page:            frontend.TranscriptPage{Start: 3, End: 5, Total: 8, HasOlder: true, HasNewer: true},
	}
	controller := newTestController(t, runtime)
	page, err := controller.TranscriptPage(t.Context(), frontend.TranscriptPageRequest{Before: 5, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Start != runtime.page.Start || page.End != runtime.page.End || page.Total != runtime.page.Total ||
		page.HasOlder != runtime.page.HasOlder || page.HasNewer != runtime.page.HasNewer {
		t.Fatalf("page = %+v, want %+v", page, runtime.page)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPageReportsUnsupportedRuntimeCapability(t *testing.T) {
	controller := newTestController(t, newBlockingRuntime())
	_, err := controller.TranscriptPage(t.Context(), frontend.TranscriptPageRequest{Before: -1, Limit: 1})
	if !errors.Is(err, frontend.ErrTranscriptPagingUnsupported) {
		t.Fatalf("TranscriptPage() error = %v", err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRefreshIsSerializedAndOptional(t *testing.T) {
	runtime := &workspaceRefreshRuntime{blockingRuntime: newBlockingRuntime()}
	controller := newTestController(t, runtime)
	if err := controller.RefreshWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runtime.refreshes != 1 {
		t.Fatalf("refresh calls = %d", runtime.refreshes)
	}
	if err := controller.Submit(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if err := controller.RefreshWorkspace(t.Context()); !errors.Is(err, ErrTurnActive) {
		t.Fatalf("refresh during turn error = %v", err)
	}
	if err := controller.HardCancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	unsupported := newTestController(t, newBlockingRuntime())
	if err := unsupported.RefreshWorkspace(t.Context()); !errors.Is(err, frontend.ErrWorkspaceRefreshUnsupported) {
		t.Fatalf("unsupported refresh error = %v", err)
	}
	if err := unsupported.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
