package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

type blockingRuntime struct {
	mu             sync.Mutex
	runStarted     chan frontend.TurnInput
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

type repositoryEvidenceRuntime struct {
	*blockingRuntime
	target codingworkspace.DiffTarget
}

type blockingRepositoryEvidenceRuntime struct {
	*blockingRuntime
	statusStarted chan struct{}
}

type orderedRepositoryEvidenceRuntime struct {
	*blockingRuntime
	mu            sync.Mutex
	statusCalls   int
	statusStarted chan int
	firstRelease  chan struct{}
}

func (runtime *orderedRepositoryEvidenceRuntime) RepositoryStatus(
	context.Context,
) (codingworkspace.StatusResult, error) {
	runtime.mu.Lock()
	call := runtime.statusCalls
	runtime.statusCalls++
	runtime.mu.Unlock()
	runtime.statusStarted <- call
	if call == 0 {
		<-runtime.firstRelease
	}
	root := "/new"
	if call == 0 {
		root = "/old"
	}
	return codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Snapshot:      codingworkspace.Snapshot{ProjectRoot: root},
	}, nil
}

func (*orderedRepositoryEvidenceRuntime) RepositoryDiff(
	context.Context,
	codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	return codingworkspace.DiffResult{}, nil
}

type commitBoundaryRepositoryRuntime struct {
	*blockingRuntime
	statusStarted    chan struct{}
	statusRelease    chan struct{}
	statusReturned   chan struct{}
	interruptStarted chan struct{}
	interruptRelease chan struct{}
}

func (runtime *commitBoundaryRepositoryRuntime) RepositoryStatus(
	context.Context,
) (codingworkspace.StatusResult, error) {
	close(runtime.statusStarted)
	<-runtime.statusRelease
	close(runtime.statusReturned)
	return codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Snapshot:      codingworkspace.Snapshot{ProjectRoot: "/canceled"},
	}, nil
}

func (*commitBoundaryRepositoryRuntime) RepositoryDiff(
	context.Context,
	codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	return codingworkspace.DiffResult{}, nil
}

func (runtime *commitBoundaryRepositoryRuntime) Interrupt(context.Context) error {
	close(runtime.interruptStarted)
	<-runtime.interruptRelease
	return nil
}

func (runtime *blockingRepositoryEvidenceRuntime) RepositoryStatus(
	ctx context.Context,
) (codingworkspace.StatusResult, error) {
	runtime.statusStarted <- struct{}{}
	<-ctx.Done()
	return codingworkspace.StatusResult{SchemaVersion: codingworkspace.RepositoryStatusSchemaV1}, nil
}

func (*blockingRepositoryEvidenceRuntime) RepositoryDiff(
	context.Context,
	codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	return codingworkspace.DiffResult{}, nil
}

func (*repositoryEvidenceRuntime) RepositoryStatus(
	context.Context,
) (codingworkspace.StatusResult, error) {
	return codingworkspace.StatusResult{SchemaVersion: codingworkspace.RepositoryStatusSchemaV1}, nil
}

func (runtime *repositoryEvidenceRuntime) RepositoryDiff(
	_ context.Context,
	target codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	runtime.target = target
	return codingworkspace.DiffResult{SchemaVersion: codingworkspace.RepositoryDiffSchemaV1, Target: target}, nil
}

type lifecycleRuntime struct {
	*blockingRuntime
	renamed              string
	archived             bool
	backgroundCompacting atomic.Bool
}

func (r *lifecycleRuntime) BackgroundCompactionActive() bool { return r.backgroundCompacting.Load() }

func (r *lifecycleRuntime) Rename(_ context.Context, title string) error {
	r.renamed = title
	return nil
}

func (r *lifecycleRuntime) SetArchived(_ context.Context, archived bool) error {
	r.archived = archived
	return nil
}

type cancelCauseRuntime struct {
	*blockingRuntime
	joinedError error
}

func (r *cancelCauseRuntime) RunTurn(ctx context.Context, input frontend.TurnInput, ready func()) error {
	r.runStarted <- input
	ready()
	<-ctx.Done()
	return errors.Join(context.Cause(ctx), r.joinedError)
}

func (r *workspaceRefreshRuntime) RefreshWorkspaceEvidence(
	context.Context,
) (codingworkspace.StatusResult, error) {
	r.refreshes++
	return codingworkspace.StatusResult{SchemaVersion: codingworkspace.RepositoryStatusSchemaV1}, r.refreshErr
}

func (r *pagedRuntime) TranscriptPage(
	context.Context,
	frontend.TranscriptPageRequest,
) (frontend.TranscriptPage, error) {
	return r.page, nil
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{
		runStarted:     make(chan frontend.TurnInput, 1),
		runRelease:     make(chan struct{}),
		compactStarted: make(chan struct{}, 1),
		compactRelease: make(chan struct{}),
	}
}

func (r *blockingRuntime) RunTurn(ctx context.Context, input frontend.TurnInput, ready func()) error {
	r.runStarted <- input
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
	if err := controller.Submit(ctx, frontend.TurnInput{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Interrupt(ctx); err != nil {
		t.Fatalf("immediate Interrupt() error = %v", err)
	}
	if err := controller.Submit(ctx, frontend.TurnInput{Text: "second"}); !errors.Is(err, ErrTurnActive) {
		t.Fatalf("second Submit() error = %v, want %v", err, ErrTurnActive)
	}
	if err := controller.HardCancel(ctx); err != nil {
		t.Fatalf("immediate HardCancel() error = %v", err)
	}
	select {
	case input := <-runtime.runStarted:
		if input.Text != "first" {
			t.Fatalf("input = %#v, want first", input)
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

func TestSubmitClonesStructuredInputBeforeAsyncRuntime(t *testing.T) {
	runtime := newBlockingRuntime()
	controller := newTestController(t, runtime)
	attachments := []frontend.TurnAttachment{{
		Path:        "/tmp/screenshot.png",
		Filename:    "screenshot.png",
		ContentType: "image/png",
	}}
	input := frontend.TurnInput{Text: "inspect this", Attachments: attachments}
	if err := controller.Submit(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	attachments[0].Path = "/tmp/replaced.png"
	input.Attachments[0].Filename = "replaced.png"
	received := <-runtime.runStarted
	if received.Attachments[0].Path != "/tmp/screenshot.png" ||
		received.Attachments[0].Filename != "screenshot.png" {
		t.Fatalf("runtime input changed with caller slice: %#v", received)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitValidatesStructuredInputBounds(t *testing.T) {
	controller := newTestController(t, newBlockingRuntime())
	tooMany := make([]frontend.TurnAttachment, frontend.MaxTurnAttachments+1)
	for index := range tooMany {
		tooMany[index].Path = "/tmp/file"
	}
	for _, test := range []struct {
		name  string
		input frontend.TurnInput
	}{
		{name: "empty", input: frontend.TurnInput{}},
		{name: "missing path", input: frontend.TurnInput{Attachments: []frontend.TurnAttachment{{}}}},
		{name: "too many attachments", input: frontend.TurnInput{Attachments: tooMany}},
		{
			name: "invalid metadata",
			input: frontend.TurnInput{Attachments: []frontend.TurnAttachment{{
				Path:     "/tmp/file",
				Filename: string([]byte{0xff}),
			}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := controller.Submit(t.Context(), test.input); err == nil {
				t.Fatal("Submit() accepted invalid structured input")
			}
		})
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHardCancelCauseIsNotProjectedAsTurnFailure(t *testing.T) {
	runtime := &cancelCauseRuntime{blockingRuntime: newBlockingRuntime()}
	controller := newTestController(t, runtime)
	ctx := context.Background()
	if err := controller.Submit(ctx, frontend.TurnInput{Text: "first"}); err != nil {
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
	if err := controller.Submit(ctx, frontend.TurnInput{Text: "first"}); err != nil {
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
	if err := controller.Submit(ctx, frontend.TurnInput{Text: "held"}); !errors.Is(err, ErrCompactionActive) {
		t.Fatalf("Submit() during compaction error = %v, want %v", err, ErrCompactionActive)
	}
	if err := controller.Compact(ctx); !errors.Is(err, ErrCompactionActive) {
		t.Fatalf("second Compact() error = %v, want %v", err, ErrCompactionActive)
	}
	close(runtime.compactRelease)
	deadline := time.Now().Add(time.Second)
	for {
		err := controller.Submit(ctx, frontend.TurnInput{Text: "after"})
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
	if err := controller.Submit(context.Background(), frontend.TurnInput{Text: "work"}); err != nil {
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

func TestLifecycleCommandsDelegateOnlyWhileIdle(t *testing.T) {
	runtime := &lifecycleRuntime{blockingRuntime: newBlockingRuntime()}
	controller := newTestController(t, runtime)
	if err := controller.Rename(t.Context(), "new title"); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetArchived(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if runtime.renamed != "new title" || !runtime.archived {
		t.Fatalf("lifecycle runtime = rename %q archived %t", runtime.renamed, runtime.archived)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "work"}); err != nil {
		t.Fatal(err)
	}
	<-runtime.runStarted
	if err := controller.SetArchived(t.Context(), false); !errors.Is(err, ErrTurnActive) {
		t.Fatalf("active SetArchived() error = %v", err)
	}
	runtime.backgroundCompacting.Store(true)
	close(runtime.runRelease)
	deadline := time.Now().Add(time.Second)
	for {
		err := controller.SetArchived(t.Context(), false)
		if errors.Is(err, ErrCompactionActive) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-turn background SetArchived() error = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	runtime.backgroundCompacting.Store(false)
	if err := controller.SetArchived(t.Context(), false); err != nil {
		t.Fatalf("SetArchived() after background compaction = %v", err)
	}
	if runtime.archived {
		t.Fatal("unarchive did not reach lifecycle runtime")
	}
	if err := controller.Close(t.Context()); err != nil {
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
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "work"}); err != nil {
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

func TestRepositoryEvidenceDelegatesTypedReadOnlyCapability(t *testing.T) {
	runtime := &repositoryEvidenceRuntime{blockingRuntime: newBlockingRuntime()}
	controller := newTestController(t, runtime)
	status, err := controller.RepositoryStatus(t.Context())
	if err != nil || status.SchemaVersion != codingworkspace.RepositoryStatusSchemaV1 {
		t.Fatalf("status = %#v / %v", status, err)
	}
	target := codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetBase, Ref: "main"}
	diff, err := controller.RepositoryDiff(t.Context(), target)
	if err != nil || diff.SchemaVersion != codingworkspace.RepositoryDiffSchemaV1 || runtime.target != target {
		t.Fatalf("diff/target = %#v / %#v / %v", diff, runtime.target, err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RepositoryStatus(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("repository status after close error = %v", err)
	}

	unsupported := newTestController(t, newBlockingRuntime())
	if _, err := unsupported.RepositoryStatus(t.Context()); !errors.Is(err, frontend.ErrWorkspaceRefreshUnsupported) {
		t.Fatalf("unsupported repository status error = %v", err)
	}
	if err := unsupported.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryEvidenceRunsOutsideActorAndCloseCancelsIt(t *testing.T) {
	runtime := &blockingRepositoryEvidenceRuntime{
		blockingRuntime: newBlockingRuntime(),
		statusStarted:   make(chan struct{}, 1),
	}
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	valid := codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Snapshot:      codingworkspace.Snapshot{ProjectRoot: "/valid"},
	}
	projector.RepositoryStatusUpdated(valid)
	controller, err := New(projector, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "work"}); err != nil {
		t.Fatal(err)
	}
	statusErr := make(chan error, 1)
	go func() {
		_, statusErrValue := controller.RepositoryStatus(context.Background())
		statusErr <- statusErrValue
	}()
	<-runtime.statusStarted
	if err := controller.Interrupt(t.Context()); err != nil {
		t.Fatalf("Interrupt() while repository read is active = %v", err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatalf("Close() with repository read = %v", err)
	}
	if err := <-statusErr; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
		t.Fatalf("RepositoryStatus() after Close() = %v", err)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RepositoryStatus == nil || snapshot.RepositoryStatus.Snapshot.ProjectRoot != "/valid" {
		t.Fatalf("canceled repository read replaced valid state = %#v", snapshot.RepositoryStatus)
	}
}

func TestRepositoryEvidencePreservesAdmissionOrder(t *testing.T) {
	runtime := &orderedRepositoryEvidenceRuntime{
		blockingRuntime: newBlockingRuntime(),
		statusStarted:   make(chan int, 2),
		firstRelease:    make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	results := make(chan codingworkspace.StatusResult, 2)
	errs := make(chan error, 2)
	go func() {
		status, err := controller.RepositoryStatus(t.Context())
		results <- status
		errs <- err
	}()
	if call := <-runtime.statusStarted; call != 0 {
		t.Fatalf("first evidence call = %d", call)
	}
	go func() {
		status, err := controller.RepositoryStatus(t.Context())
		results <- status
		errs <- err
	}()
	select {
	case call := <-runtime.statusStarted:
		t.Fatalf("evidence call %d started before the first completed", call)
	case <-time.After(25 * time.Millisecond):
	}
	close(runtime.firstRelease)
	if call := <-runtime.statusStarted; call != 1 {
		t.Fatalf("second evidence call = %d", call)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		<-results
	}
	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RepositoryStatus == nil || snapshot.RepositoryStatus.Snapshot.ProjectRoot != "/new" {
		t.Fatalf("repository status = %#v, want newest admitted result", snapshot.RepositoryStatus)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryEvidenceRechecksCancellationBeforeProjection(t *testing.T) {
	runtime := &commitBoundaryRepositoryRuntime{
		blockingRuntime:  newBlockingRuntime(),
		statusStarted:    make(chan struct{}),
		statusRelease:    make(chan struct{}),
		statusReturned:   make(chan struct{}),
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
	}
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.RepositoryStatusUpdated(codingworkspace.StatusResult{
		SchemaVersion: codingworkspace.RepositoryStatusSchemaV1,
		Snapshot:      codingworkspace.Snapshot{ProjectRoot: "/valid"},
	})
	controller, err := New(projector, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "hold actor control"}); err != nil {
		t.Fatal(err)
	}
	<-runtime.runStarted
	requestCtx, cancel := context.WithCancel(t.Context())
	statusErr := make(chan error, 1)
	go func() {
		_, requestErr := controller.RepositoryStatus(requestCtx)
		statusErr <- requestErr
	}()
	<-runtime.statusStarted
	interruptErr := make(chan error, 1)
	go func() { interruptErr <- controller.Interrupt(t.Context()) }()
	<-runtime.interruptStarted
	close(runtime.statusRelease)
	<-runtime.statusReturned
	cancel()
	if err := <-statusErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("RepositoryStatus() error = %v, want cancellation", err)
	}
	close(runtime.interruptRelease)
	if err := <-interruptErr; err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if _, err := controller.RepositoryDiff(t.Context(), codingworkspace.DiffTarget{}); err != nil {
		t.Fatalf("evidence barrier error = %v", err)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RepositoryStatus == nil || snapshot.RepositoryStatus.Snapshot.ProjectRoot != "/valid" {
		t.Fatalf("canceled evidence was projected = %#v", snapshot.RepositoryStatus)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
