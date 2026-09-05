package controller

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
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

type blockingWorkspaceRefreshRuntime struct {
	*blockingRuntime
	statusStarted  chan struct{}
	statusRelease  chan struct{}
	refreshStarted chan struct{}
	refreshRelease chan struct{}
}

func (runtime *blockingWorkspaceRefreshRuntime) RepositoryStatus(
	ctx context.Context,
) (codingworkspace.StatusResult, error) {
	if runtime.statusStarted == nil {
		return codingworkspace.StatusResult{}, nil
	}
	runtime.statusStarted <- struct{}{}
	select {
	case <-runtime.statusRelease:
		return codingworkspace.StatusResult{}, nil
	case <-ctx.Done():
		return codingworkspace.StatusResult{}, ctx.Err()
	}
}

func (*blockingWorkspaceRefreshRuntime) RepositoryDiff(
	context.Context,
	codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	return codingworkspace.DiffResult{}, nil
}

func (runtime *blockingWorkspaceRefreshRuntime) RefreshWorkspaceEvidence(
	ctx context.Context,
) (codingworkspace.StatusResult, error) {
	runtime.refreshStarted <- struct{}{}
	select {
	case <-runtime.refreshRelease:
		return codingworkspace.StatusResult{SchemaVersion: codingworkspace.RepositoryStatusSchemaV1}, nil
	case <-ctx.Done():
		return codingworkspace.StatusResult{}, ctx.Err()
	}
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

type reviewTestRuntime struct {
	*blockingRuntime
	started              chan string
	release              chan struct{}
	events               []codingreview.Event
	result               codingreview.Result
	err                  error
	target               codingreview.Target
	backgroundCompacting atomic.Bool
}

type ignoredReviewEventErrorRuntime struct {
	*blockingRuntime
}

type mutatingReviewEventRuntime struct {
	*blockingRuntime
	emitted chan struct{}
	release chan struct{}
}

type cancelIgnoringReviewRuntime struct {
	*blockingRuntime
	started chan struct{}
	release chan struct{}
}

func (runtime *cancelIgnoringReviewRuntime) RunReview(
	_ context.Context,
	reviewID string,
	target codingreview.Target,
	_ func(codingreview.Event) error,
) (codingreview.Result, error) {
	close(runtime.started)
	<-runtime.release
	return codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: reviewID, Target: target,
		EvidenceGeneration: "generation-1", Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}, nil
}

func (runner *mutatingReviewEventRuntime) RunReview(
	ctx context.Context,
	reviewID string,
	target codingreview.Target,
	emit func(codingreview.Event) error,
) (codingreview.Result, error) {
	finding := codingreview.Finding{
		Severity: codingreview.SeverityMinor, Title: "Original", Explanation: "Original explanation.",
		Confidence: 0.8, LocationState: codingreview.LocationCurrent, Path: "main.go", StartLine: 4, EndLine: 4,
	}
	if err := emit(codingreview.Event{Kind: codingreview.EventFinding, Finding: &finding}); err != nil {
		return codingreview.Result{}, err
	}
	close(runner.emitted)
	for index := 0; ; index++ {
		select {
		case <-runner.release:
			resultFinding := finding
			resultFinding.Title = "Original"
			return codingreview.Result{
				SchemaVersion: codingreview.SchemaVersion, ReviewID: reviewID, Target: target,
				EvidenceGeneration: "generation-1", Summary: "One finding.",
				Findings: []codingreview.Finding{resultFinding}, CompletedAt: time.Now().UTC(),
			}, nil
		case <-ctx.Done():
			return codingreview.Result{}, context.Cause(ctx)
		default:
			finding.Title = fmt.Sprintf("runtime mutation %d", index)
			runtime.Gosched()
		}
	}
}

func (runtime *ignoredReviewEventErrorRuntime) RunReview(
	_ context.Context,
	reviewID string,
	target codingreview.Target,
	emit func(codingreview.Event) error,
) (codingreview.Result, error) {
	_ = emit(codingreview.Event{Kind: codingreview.EventFinding})
	return codingreview.Result{
		SchemaVersion: codingreview.SchemaVersion, ReviewID: reviewID, Target: target,
		EvidenceGeneration: "generation-1", Summary: "No findings.", CompletedAt: time.Now().UTC(),
	}, nil
}

func (runtime *reviewTestRuntime) RunReview(
	ctx context.Context,
	reviewID string,
	target codingreview.Target,
	emit func(codingreview.Event) error,
) (codingreview.Result, error) {
	runtime.target = target
	runtime.started <- reviewID
	for _, event := range runtime.events {
		if err := emit(event); err != nil {
			return codingreview.Result{}, err
		}
	}
	select {
	case <-runtime.release:
	case <-ctx.Done():
		return codingreview.Result{}, context.Cause(ctx)
	}
	result := runtime.result.Clone()
	result.ReviewID = reviewID
	result.Target = target
	return result, runtime.err
}

func (runtime *reviewTestRuntime) BackgroundCompactionActive() bool {
	return runtime.backgroundCompacting.Load()
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
	if err := controller.Review(
		ctx,
		codingreview.Target{Kind: codingreview.TargetCurrent},
	); !errors.Is(
		err,
		ErrUnsupported,
	) {
		t.Fatalf("Review() error = %v, want %v", err, ErrUnsupported)
	}
	if err := controller.Interrupt(ctx); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("Interrupt() error = %v, want %v", err, ErrNoActiveTurn)
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReviewLifecycleIsSerializedAndProjectsOnlyCorrelatedResult(t *testing.T) {
	finding := codingreview.Finding{
		Severity: codingreview.SeverityMajor, Title: "Handle error", Explanation: "The error is ignored.",
		Confidence: 0.9, LocationState: codingreview.LocationCurrent, Path: "main.go", StartLine: 4, EndLine: 4,
	}
	runtime := &reviewTestRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan string, 1),
		release:         make(chan struct{}),
		events: []codingreview.Event{
			{Kind: codingreview.EventProgress, Progress: "checking changed files"},
			{Kind: codingreview.EventFinding, Finding: &finding},
		},
		result: codingreview.Result{
			SchemaVersion: codingreview.SchemaVersion, EvidenceGeneration: "generation-1",
			ResolvedRevision: "base-tip", MergeBase: "merge-base", CompletedAt: time.Now().UTC(),
			Summary: "One issue found.", Findings: []codingreview.Finding{finding},
		},
	}
	controller := newTestController(t, runtime)
	target := codingreview.Target{Kind: codingreview.TargetBase, Ref: "main", Instructions: "Focus on failures."}
	if err := controller.Review(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	reviewID := <-runtime.started
	if runtime.target != target {
		t.Fatalf("runtime target = %#v, want %#v", runtime.target, target)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "overlap"}); !errors.Is(err, ErrReviewActive) {
		t.Fatalf("Submit() during review error = %v", err)
	}
	if err := controller.Compact(t.Context()); !errors.Is(err, ErrReviewActive) {
		t.Fatalf("Compact() during review error = %v", err)
	}
	waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && snapshot.Review.ReviewID == reviewID &&
			snapshot.Review.Phase == codingreview.PhaseProgress && len(snapshot.Review.Findings) == 1
	})
	close(runtime.release)
	completed := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && snapshot.Review.Phase == codingreview.PhaseCompleted
	})
	if completed.Activity != frontend.ActivityIdle || completed.Review.Result == nil ||
		completed.Review.Result.EvidenceGeneration != "generation-1" {
		t.Fatalf("completed review snapshot = %#v", completed)
	}
	completed.Review.Findings[0].Title = "caller mutation"
	again, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again.Review.Findings[0].Title != finding.Title {
		t.Fatal("snapshot review findings share caller-owned memory")
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptCancelsReviewWithoutProjectingFailure(t *testing.T) {
	runtime := &reviewTestRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan string, 1),
		release:         make(chan struct{}),
		result: codingreview.Result{
			SchemaVersion: codingreview.SchemaVersion, EvidenceGeneration: "generation-1",
			CompletedAt: time.Now().UTC(), Summary: "No findings.",
		},
	}
	controller := newTestController(t, runtime)
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	if err := controller.Interrupt(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && snapshot.Review.Phase == codingreview.PhaseInterrupted
	})
	for _, entry := range snapshot.Entries {
		if entry.ID == "controller:review-error" {
			t.Fatal("intentional review interruption was projected as a failure")
		}
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedReviewCancellationDominatesRuntimeSuccess(t *testing.T) {
	runtime := &cancelIgnoringReviewRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	if err := controller.Interrupt(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(runtime.release)
	snapshot := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && snapshot.Review.Phase == codingreview.PhaseInterrupted
	})
	if snapshot.Review.Result != nil {
		t.Fatalf("canceled review projected successful result = %#v", snapshot.Review.Result)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReviewRejectsBackgroundCompaction(t *testing.T) {
	runtime := &reviewTestRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan string, 1),
		release:         make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "work"}); err != nil {
		t.Fatal(err)
	}
	<-runtime.runStarted
	runtime.backgroundCompacting.Store(true)
	close(runtime.runRelease)
	deadline := time.Now().Add(time.Second)
	for {
		err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent})
		if errors.Is(err, ErrCompactionActive) {
			break
		}
		if !errors.Is(err, ErrTurnActive) || time.Now().After(deadline) {
			t.Fatalf("post-turn background Review() error = %v, want %v", err, ErrCompactionActive)
		}
		time.Sleep(time.Millisecond)
	}
	runtime.backgroundCompacting.Store(false)
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReviewAdmissionReplyWinsConcurrentClose(t *testing.T) {
	reply := make(chan error, 1)
	reply <- nil
	done := make(chan struct{})
	close(done)
	if err := awaitReviewAdmission(t.Context(), reply, done); err != nil {
		t.Fatalf("accepted review admission error = %v", err)
	}
}

func TestInvalidReviewResultEndsLifecycleAndProjectsFailure(t *testing.T) {
	runtime := &reviewTestRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan string, 1),
		release:         make(chan struct{}),
		result: codingreview.Result{
			SchemaVersion:      codingreview.SchemaVersion,
			EvidenceGeneration: "generation-1",
			CompletedAt:        time.Now().UTC(),
		},
	}
	controller := newTestController(t, runtime)
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	close(runtime.release)
	snapshot := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		if snapshot.Review == nil || snapshot.Review.Phase != codingreview.PhaseInterrupted {
			return false
		}
		for _, entry := range snapshot.Entries {
			if entry.ID == "controller:review-error" {
				return true
			}
		}
		return false
	})
	if snapshot.Activity != frontend.ActivityIdle {
		t.Fatalf("failed review left activity = %q", snapshot.Activity)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "continue"}); err != nil {
		t.Fatalf("failed review stranded controller: %v", err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoredReviewEventCallbackErrorFailsReview(t *testing.T) {
	controller := newTestController(t, &ignoredReviewEventErrorRuntime{blockingRuntime: newBlockingRuntime()})
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	snapshot := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		if snapshot.Review == nil || snapshot.Review.Phase != codingreview.PhaseInterrupted {
			return false
		}
		for _, entry := range snapshot.Entries {
			if entry.ID == "controller:review-error" {
				return true
			}
		}
		return false
	})
	if snapshot.Activity != frontend.ActivityIdle {
		t.Fatalf("failed callback left activity = %q", snapshot.Activity)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReviewEventClonesRuntimeOwnedFindingBeforeProjection(t *testing.T) {
	runtime := &mutatingReviewEventRuntime{
		blockingRuntime: newBlockingRuntime(),
		emitted:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	<-runtime.emitted
	snapshot := waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && len(snapshot.Review.Findings) == 1
	})
	if snapshot.Review.Findings[0].Title != "Original" {
		t.Fatalf("projected runtime-owned mutation = %q", snapshot.Review.Findings[0].Title)
	}
	close(runtime.release)
	waitControllerSnapshot(t, controller, func(snapshot frontend.ThreadSnapshot) bool {
		return snapshot.Review != nil && snapshot.Review.Phase == codingreview.PhaseCompleted
	})
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseCancelsReviewAndWaitsForRuntime(t *testing.T) {
	runtime := &reviewTestRuntime{
		blockingRuntime: newBlockingRuntime(),
		started:         make(chan string, 1),
		release:         make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	if err := controller.Review(t.Context(), codingreview.Target{Kind: codingreview.TargetCurrent}); err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	closed := make(chan error, 1)
	go func() {
		closed <- controller.Close(context.Background())
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the active review")
	}
	if runtime.closes != 1 {
		t.Fatalf("runtime Close() calls = %d, want 1", runtime.closes)
	}
}

func waitControllerSnapshot(
	t *testing.T,
	controller *Controller,
	accept func(frontend.ThreadSnapshot) bool,
) frontend.ThreadSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := controller.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if accept(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for controller snapshot: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
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

func TestWorkspaceRefreshExcludesTurnAndCompactionWhileActive(t *testing.T) {
	runtime := &blockingWorkspaceRefreshRuntime{
		blockingRuntime: newBlockingRuntime(),
		refreshStarted:  make(chan struct{}, 1),
		refreshRelease:  make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	refreshErr := make(chan error, 1)
	go func() { refreshErr <- controller.RefreshWorkspace(t.Context()) }()
	<-runtime.refreshStarted
	if err := controller.Submit(
		t.Context(),
		frontend.TurnInput{Text: "work"},
	); !errors.Is(
		err,
		ErrWorkspaceRefreshActive,
	) {
		t.Fatalf("Submit() during workspace refresh error = %v", err)
	}
	if err := controller.Compact(t.Context()); !errors.Is(err, ErrWorkspaceRefreshActive) {
		t.Fatalf("Compact() during workspace refresh error = %v", err)
	}
	close(runtime.refreshRelease)
	if err := <-refreshErr; err != nil {
		t.Fatal(err)
	}
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "after refresh"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.HardCancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedWorkspaceRefreshExcludesLaterTurn(t *testing.T) {
	runtime := &blockingWorkspaceRefreshRuntime{
		blockingRuntime: newBlockingRuntime(),
		statusStarted:   make(chan struct{}, 1),
		statusRelease:   make(chan struct{}),
		refreshStarted:  make(chan struct{}, 1),
		refreshRelease:  make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	statusErr := make(chan error, 1)
	go func() {
		_, err := controller.RepositoryStatus(t.Context())
		statusErr <- err
	}()
	<-runtime.statusStarted
	refreshReply := make(chan error, 1)
	if err := controller.enqueue(t.Context(), command{
		kind:  commandRefreshWorkspace,
		ctx:   t.Context(),
		reply: refreshReply,
	}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Submit(
		t.Context(),
		frontend.TurnInput{Text: "work"},
	); !errors.Is(
		err,
		ErrWorkspaceRefreshActive,
	) {
		t.Fatalf("Submit() with queued workspace refresh error = %v", err)
	}
	close(runtime.statusRelease)
	if err := <-statusErr; err != nil {
		t.Fatal(err)
	}
	<-runtime.refreshStarted
	if err := controller.Submit(
		t.Context(),
		frontend.TurnInput{Text: "still blocked"},
	); !errors.Is(
		err,
		ErrWorkspaceRefreshActive,
	) {
		t.Fatalf("Submit() with active queued workspace refresh error = %v", err)
	}
	close(runtime.refreshRelease)
	if err := <-refreshReply; err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledQueuedWorkspaceRefreshStopsExcludingTurnsImmediately(t *testing.T) {
	runtime := &blockingWorkspaceRefreshRuntime{
		blockingRuntime: newBlockingRuntime(),
		statusStarted:   make(chan struct{}, 1),
		statusRelease:   make(chan struct{}),
		refreshStarted:  make(chan struct{}, 1),
		refreshRelease:  make(chan struct{}),
	}
	controller := newTestController(t, runtime)
	statusErr := make(chan error, 1)
	go func() {
		_, err := controller.RepositoryStatus(t.Context())
		statusErr <- err
	}()
	<-runtime.statusStarted
	refreshCtx, cancelRefresh := context.WithCancel(t.Context())
	refreshReply := make(chan error, 1)
	if err := controller.enqueue(t.Context(), command{
		kind:  commandRefreshWorkspace,
		ctx:   refreshCtx,
		reply: refreshReply,
	}); err != nil {
		t.Fatal(err)
	}
	cancelRefresh()
	if err := controller.Submit(t.Context(), frontend.TurnInput{Text: "work"}); err != nil {
		t.Fatalf("Submit() after queued refresh cancellation = %v", err)
	}
	<-runtime.runStarted
	if err := <-refreshReply; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queued refresh error = %v", err)
	}
	close(runtime.statusRelease)
	if err := <-statusErr; err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.refreshStarted:
		t.Fatal("canceled queued refresh was executed")
	default:
	}
	if err := controller.HardCancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(t.Context()); err != nil {
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
