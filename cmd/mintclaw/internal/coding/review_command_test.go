package coding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
)

type headlessReviewController struct {
	*frontend.Projector
	result                          codingreview.Result
	reviewErr                       error
	interrupts                      atomic.Int32
	closes                          atomic.Int32
	observerCanceledBeforeInterrupt atomic.Bool
	closeFn                         func() error
	block                           bool
	started                         chan struct{}
	observeCtx                      context.Context
}

func (controller *headlessReviewController) Review(_ context.Context, target codingreview.Target) error {
	if controller.reviewErr != nil {
		return controller.reviewErr
	}
	result := controller.result.Clone()
	result.Target = target
	if err := controller.ReviewEntered(result.ReviewID, target); err != nil {
		return err
	}
	if controller.started != nil {
		close(controller.started)
	}
	if controller.block {
		return nil
	}
	return controller.ReviewCompleted(result)
}

func (controller *headlessReviewController) Submit(context.Context, frontend.TurnInput) error {
	return frontend.ErrCommandUnsupported
}

func (controller *headlessReviewController) Interrupt(context.Context) error {
	if controller.observeCtx != nil && controller.observeCtx.Err() != nil {
		controller.observerCanceledBeforeInterrupt.Store(true)
	}
	controller.interrupts.Add(1)
	return nil
}

func (controller *headlessReviewController) Subscribe(
	ctx context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	controller.observeCtx = ctx
	return controller.Projector.Subscribe(ctx)
}

func (*headlessReviewController) HardCancel(context.Context) error        { return nil }
func (*headlessReviewController) Compact(context.Context) error           { return nil }
func (*headlessReviewController) Rename(context.Context, string) error    { return nil }
func (*headlessReviewController) SetArchived(context.Context, bool) error { return nil }
func (*headlessReviewController) NewThread(context.Context) error         { return nil }
func (controller *headlessReviewController) Close(context.Context) error {
	controller.closes.Add(1)
	if controller.closeFn != nil {
		return controller.closeFn()
	}
	return nil
}

func TestExecuteHeadlessReviewReturnsAuthoritativeCompletedResult(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	result := codingreview.Result{
		SchemaVersion:      codingreview.SchemaVersion,
		ReviewID:           codingreview.NewID(),
		Target:             codingreview.Target{Kind: codingreview.TargetCurrent},
		EvidenceGeneration: "generation-1",
		Summary:            "No issues found.",
		CompletedAt:        time.Unix(1, 0).UTC(),
	}
	controller := &headlessReviewController{Projector: projector, result: result}
	got, err := executeHeadlessReview(t.Context(), controller, result.Target)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewID != result.ReviewID || got.Summary != result.Summary {
		t.Fatalf("headless review result = %#v", got)
	}
	got.Summary = "consumer mutation"
	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Review == nil || snapshot.Review.Result == nil ||
		snapshot.Review.Result.Summary != result.Summary {
		t.Fatalf("headless result aliased projection = %#v", snapshot.Review)
	}
}

func TestExecuteHeadlessReviewRejectsUnsupportedAndAdmissionFailure(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	unsupported := &controllerWithoutReview{Projector: projector}
	_, err = executeHeadlessReview(t.Context(), unsupported, codingreview.Target{Kind: codingreview.TargetCurrent})
	if !errors.Is(err, frontend.ErrCommandUnsupported) {
		t.Fatalf("unsupported review error = %v", err)
	}
	controller := &headlessReviewController{Projector: projector, reviewErr: frontend.ErrCommandUnsupported}
	_, err = executeHeadlessReview(t.Context(), controller, codingreview.Target{Kind: codingreview.TargetCurrent})
	if !errors.Is(err, frontend.ErrCommandUnsupported) || !strings.Contains(err.Error(), "admit") {
		t.Fatalf("admission review error = %v", err)
	}
}

type controllerWithoutReview struct{ *frontend.Projector }

func (*controllerWithoutReview) Submit(context.Context, frontend.TurnInput) error { return nil }
func (*controllerWithoutReview) Interrupt(context.Context) error                  { return nil }
func (*controllerWithoutReview) HardCancel(context.Context) error                 { return nil }
func (*controllerWithoutReview) Compact(context.Context) error                    { return nil }
func (*controllerWithoutReview) Rename(context.Context, string) error             { return nil }
func (*controllerWithoutReview) SetArchived(context.Context, bool) error          { return nil }
func (*controllerWithoutReview) NewThread(context.Context) error                  { return nil }
func (*controllerWithoutReview) Close(context.Context) error                      { return nil }

func TestValidateReviewCommandOptions(t *testing.T) {
	if err := validateReviewCommandOptions(reviewCommandOptions{
		threadID: "thread", last: true, target: codingreview.Target{Kind: codingreview.TargetCurrent},
	}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("thread plus last error = %v", err)
	}
	if err := validateReviewCommandOptions(reviewCommandOptions{
		target: codingreview.Target{Kind: codingreview.TargetBase},
	}); err == nil || !strings.Contains(err.Error(), "review ref") {
		t.Fatalf("missing base ref error = %v", err)
	}
}

func TestReviewCommandSelectsThreadAndRendersJSON(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.September, 4, 20, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "inspect", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	result := codingreview.Result{
		SchemaVersion:      codingreview.SchemaVersion,
		ReviewID:           codingreview.NewID(),
		Target:             codingreview.Target{Kind: codingreview.TargetCurrent},
		EvidenceGeneration: "generation-1",
		Summary:            "No issues found.",
		CompletedAt:        now,
	}
	deps.newController = func(request codingTurnRequest, resumed bool) (frontend.Controller, error) {
		if !resumed || request.Metadata.ThreadID != created.ThreadID {
			t.Fatalf("review controller request = resumed=%v metadata=%#v", resumed, request.Metadata)
		}
		projector, err := frontend.NewProjector(request.Metadata.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			return nil, err
		}
		return &headlessReviewController{
			Projector: projector,
			result:    result,
			closeFn:   request.Lease.Release,
		}, nil
	}
	var rendered reviewCommandResult
	if err := json.Unmarshal(
		executeCommand(t, newReviewCommand(deps), created.ThreadID, "--json"),
		&rendered,
	); err != nil {
		t.Fatal(err)
	}
	if rendered.Action != "reviewed" || rendered.ThreadID != created.ThreadID ||
		rendered.Review.ReviewID != result.ReviewID || rendered.Review.Summary != result.Summary {
		t.Fatalf("review command output = %#v", rendered)
	}
}

func TestReviewCommandSignalContextInterruptsBlockingReview(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.September, 4, 20, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "inspect", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	var cancel context.CancelFunc
	deps.reviewContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancelContext := context.WithCancel(parent)
		cancel = cancelContext
		return ctx, cancelContext
	}
	started := make(chan struct{})
	var controller *headlessReviewController
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		projector, err := frontend.NewProjector(request.Metadata.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			return nil, err
		}
		controller = &headlessReviewController{
			Projector: projector,
			result: codingreview.Result{
				SchemaVersion: codingreview.SchemaVersion, ReviewID: codingreview.NewID(),
				EvidenceGeneration: "generation-1", Summary: "not completed", CompletedAt: now,
			},
			block: true, started: started, closeFn: request.Lease.Release,
		}
		return controller, nil
	}
	command := newReviewCommand(deps)
	command.SetArgs([]string{created.ThreadID})
	result := make(chan error, 1)
	go func() { result <- command.Execute() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("headless review did not enter")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled review error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("signal-context cancellation did not stop headless review")
	}
	if controller.interrupts.Load() != 1 {
		t.Fatalf("blocking review interrupt calls = %d", controller.interrupts.Load())
	}
	if controller.observerCanceledBeforeInterrupt.Load() {
		t.Fatal("review observation was canceled before cooperative interrupt")
	}
	if controller.closes.Load() != 1 {
		t.Fatalf("blocking review close calls = %d", controller.closes.Load())
	}
}
