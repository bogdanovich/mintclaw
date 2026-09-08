package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const testWorkerBuildID = "mintclaw-test-build"

type workerTestController struct {
	projector *frontend.Projector

	mu          sync.Mutex
	started     bool
	active      bool
	turnID      string
	settled     chan error
	settleOnce  sync.Once
	submits     int
	steers      []frontend.SteerInput
	interrupts  int
	hardCancels int
	closes      int
	question    *QuestionState
	snapshotErr error
	closeErr    error
}

func newWorkerTestController(binding Binding) (*workerTestController, error) {
	projector, err := frontend.NewProjector(binding.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		return nil, err
	}
	projector.Open(binding.ThreadOpenMode == ThreadOpenResume)
	return &workerTestController{
		projector: projector,
		settled:   make(chan error, 1),
		turnID:    "turn-1",
	}, nil
}

func (controllerInstance *workerTestController) Snapshot(
	ctx context.Context,
) (frontend.ThreadSnapshot, error) {
	controllerInstance.mu.Lock()
	err := controllerInstance.snapshotErr
	controllerInstance.mu.Unlock()
	if err != nil {
		return frontend.ThreadSnapshot{}, err
	}
	return controllerInstance.projector.Snapshot(ctx)
}

func (controllerInstance *workerTestController) Subscribe(
	ctx context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	return controllerInstance.projector.Subscribe(ctx)
}

func (controllerInstance *workerTestController) Submit(_ context.Context, input frontend.TurnInput) error {
	controllerInstance.mu.Lock()
	if controllerInstance.active || controllerInstance.started {
		controllerInstance.mu.Unlock()
		return controller.ErrTurnActive
	}
	controllerInstance.started = true
	controllerInstance.active = true
	controllerInstance.submits++
	turnID := controllerInstance.turnID
	controllerInstance.mu.Unlock()
	controllerInstance.projector.TurnStarted(turnID, input.Text)
	return nil
}

func (controllerInstance *workerTestController) Steer(_ context.Context, input frontend.SteerInput) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if !controllerInstance.active {
		return controller.ErrNoActiveTurn
	}
	controllerInstance.steers = append(controllerInstance.steers, input)
	return nil
}

func (controllerInstance *workerTestController) Interrupt(context.Context) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if !controllerInstance.active {
		return controller.ErrNoActiveTurn
	}
	controllerInstance.interrupts++
	return nil
}

func (controllerInstance *workerTestController) HardCancel(context.Context) error {
	controllerInstance.mu.Lock()
	if !controllerInstance.active {
		controllerInstance.mu.Unlock()
		return controller.ErrNoActiveTurn
	}
	controllerInstance.hardCancels++
	controllerInstance.active = false
	turnID := controllerInstance.turnID
	controllerInstance.mu.Unlock()
	controllerInstance.projector.TurnInterrupted(turnID, "canceled")
	controllerInstance.settleOnce.Do(func() { controllerInstance.settled <- controller.ErrHardCanceled })
	return nil
}

func (*workerTestController) Compact(context.Context) error { return frontend.ErrCommandUnsupported }
func (*workerTestController) Rename(context.Context, string) error {
	return frontend.ErrCommandUnsupported
}

func (*workerTestController) SetArchived(context.Context, bool) error {
	return frontend.ErrCommandUnsupported
}
func (*workerTestController) NewThread(context.Context) error { return frontend.ErrCommandUnsupported }

func (controllerInstance *workerTestController) Close(context.Context) error {
	controllerInstance.mu.Lock()
	controllerInstance.closes++
	active := controllerInstance.active
	closeErr := controllerInstance.closeErr
	controllerInstance.mu.Unlock()
	if active {
		return errors.Join(controllerInstance.HardCancel(context.Background()), closeErr)
	}
	return closeErr
}

func (controllerInstance *workerTestController) AwaitTurn(ctx context.Context) error {
	controllerInstance.mu.Lock()
	started := controllerInstance.started
	controllerInstance.mu.Unlock()
	if !started {
		return controller.ErrNoActiveTurn
	}
	select {
	case err := <-controllerInstance.settled:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (controllerInstance *workerTestController) CodingWorkerQuestion(
	context.Context,
) (*QuestionState, error) {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if controllerInstance.question == nil {
		return nil, nil
	}
	question := *controllerInstance.question
	question.Options = append([]QuestionOption(nil), controllerInstance.question.Options...)
	return &question, nil
}

func (controllerInstance *workerTestController) complete() {
	controllerInstance.mu.Lock()
	if !controllerInstance.active {
		controllerInstance.mu.Unlock()
		return
	}
	controllerInstance.active = false
	turnID := controllerInstance.turnID
	controllerInstance.mu.Unlock()
	controllerInstance.projector.AssistantAccumulated(turnID, "inspection complete", true)
	controllerInstance.projector.TurnCompleted(turnID, "completed")
	controllerInstance.settleOnce.Do(func() { controllerInstance.settled <- nil })
}

type workerTestHarness struct {
	client      *Client
	controllers <-chan *workerTestController
	serverDone  <-chan error
	cancel      context.CancelFunc
}

func newWorkerTestHarness(
	t *testing.T,
	configure func(*workerTestController),
) *workerTestHarness {
	t.Helper()
	workerInput, parentOutput := io.Pipe()
	parentInput, workerOutput := io.Pipe()
	controllers := make(chan *workerTestController, 1)
	server, err := NewServer(ServerConfig{
		BuildID: testWorkerBuildID,
		Open: func(_ context.Context, binding Binding) (TaskController, error) {
			controllerInstance, openErr := newWorkerTestController(binding)
			if openErr != nil {
				return nil, openErr
			}
			if configure != nil {
				configure(controllerInstance)
			}
			controllers <- controllerInstance
			return controllerInstance, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(ctx, workerInput, workerOutput)
		_ = workerOutput.Close()
	}()
	client, err := NewClient(parentInput, parentOutput)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	harness := &workerTestHarness{
		client: client, controllers: controllers, serverDone: serverDone, cancel: cancel,
	}
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
	})
	return harness
}

func initializeHarness(
	t *testing.T,
	harness *workerTestHarness,
	binding Binding,
) *workerTestController {
	t.Helper()
	result, err := harness.client.Initialize(t.Context(), "initialize-1", InitializeParams{
		MinProtocolVersion: ProtocolV1,
		MaxProtocolVersion: ProtocolV1,
		ParentBuildID:      "parent-test-build",
		Binding:            binding,
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if result.Identity.Binding != binding || result.Identity.WorkerBuildID != testWorkerBuildID {
		t.Fatalf("Initialize() = %#v", result)
	}
	select {
	case controllerInstance := <-harness.controllers:
		return controllerInstance
	case <-time.After(time.Second):
		t.Fatal("controller factory was not called")
		return nil
	}
}

func TestWorkerServerClientControlsOneTaskAndProjectsSemanticEvents(t *testing.T) {
	binding := testBinding(t)
	harness := newWorkerTestHarness(t, nil)
	controllerInstance := initializeHarness(t, harness, binding)
	waitForWorkerEvent(t, harness.client, EventWorkerReady)

	start := TurnStartParams{ControlIdentity: binding.ControlIdentity(), Text: "inspect the repository"}
	if err := harness.client.StartTurn(t.Context(), "start-1", start); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if err := harness.client.StartTurn(t.Context(), "start-1", start); err != nil {
		t.Fatalf("idempotent StartTurn() error = %v", err)
	}
	assertRemoteCode(
		t,
		harness.client.StartTurn(t.Context(), "start-2", start),
		ErrorTurnActive,
	)

	steer := TurnSteerParams{ControlIdentity: binding.ControlIdentity(), Text: "focus on the parser"}
	if err := harness.client.Steer(t.Context(), "steer-1", steer); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if err := harness.client.Steer(t.Context(), "steer-1", steer); err != nil {
		t.Fatalf("idempotent Steer() error = %v", err)
	}
	conflicting := steer
	conflicting.Text = "focus elsewhere"
	assertRemoteCode(
		t,
		harness.client.Steer(t.Context(), "steer-1", conflicting),
		ErrorInvalidRequest,
	)

	snapshot, err := harness.client.ReadSnapshot(t.Context(), GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	})
	if err != nil {
		t.Fatalf("ReadSnapshot() error = %v", err)
	}
	if snapshot.Snapshot.ActiveTurnID != "turn-1" || snapshot.Snapshot.Activity != ActivityRunning {
		t.Fatalf("active snapshot = %#v", snapshot.Snapshot)
	}
	if err = harness.client.Interrupt(t.Context(), "interrupt-1", GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}

	controllerInstance.complete()
	waitDone(t, harness.client.Done())
	if err = harness.client.Err(); err != nil {
		t.Fatalf("client terminal error = %v", err)
	}
	select {
	case err = <-harness.serverDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not exit after task settlement")
	}

	controllerInstance.mu.Lock()
	if controllerInstance.submits != 1 || len(controllerInstance.steers) != 1 ||
		controllerInstance.interrupts != 1 || controllerInstance.closes != 1 {
		t.Fatalf(
			"controller calls = submits:%d steers:%d interrupts:%d closes:%d",
			controllerInstance.submits,
			len(controllerInstance.steers),
			controllerInstance.interrupts,
			controllerInstance.closes,
		)
	}
	controllerInstance.mu.Unlock()
	page := harness.client.EventsAfter(0)
	assertEventOrder(
		t,
		page,
		EventWorkerReady,
		EventItemUpdated,
		EventStatusChanged,
		EventTurnTerminal,
		EventWorkerStopped,
	)
}

func TestWorkerQuestionAnswerRequiresExactCorrelation(t *testing.T) {
	binding := testBinding(t)
	harness := newWorkerTestHarness(t, func(controllerInstance *workerTestController) {
		controllerInstance.question = &QuestionState{
			QuestionID: "question-1", Revision: 2, Status: QuestionWaiting, Prompt: "Continue?",
		}
	})
	controllerInstance := initializeHarness(t, harness, binding)
	ready := waitForWorkerEvent(t, harness.client, EventWorkerReady)
	readyPayload, err := DecodeEventPayload(EventWorkerReady, ready.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if readyPayload.(*WorkerReadyPayload).Snapshot.Question == nil {
		t.Fatal("worker.ready omitted the pending question")
	}
	if err = harness.client.StartTurn(t.Context(), "start-1", TurnStartParams{
		ControlIdentity: binding.ControlIdentity(), Text: "inspect",
	}); err != nil {
		t.Fatal(err)
	}
	stale := TurnSteerParams{
		ControlIdentity: binding.ControlIdentity(), Text: "yes",
		QuestionAnswer: &QuestionAnswerRef{QuestionID: "question-1", QuestionRevision: 1, AnswerID: "answer-1"},
	}
	assertRemoteCode(t, harness.client.Steer(t.Context(), "answer-request-1", stale), ErrorSteerConflict)
	stale.QuestionAnswer.QuestionRevision = 2
	if err = harness.client.Steer(t.Context(), "answer-request-2", stale); err != nil {
		t.Fatalf("correlated answer error = %v", err)
	}
	stale.QuestionAnswer.AnswerID = "competing-answer"
	assertRemoteCode(t, harness.client.Steer(t.Context(), "answer-request-3", stale), ErrorSteerConflict)
	controllerInstance.mu.Lock()
	if len(controllerInstance.steers) != 1 || controllerInstance.steers[0].ID != "answer-1" {
		t.Fatalf("question steers = %#v", controllerInstance.steers)
	}
	controllerInstance.mu.Unlock()
	if err = harness.client.Cancel(t.Context(), "cancel-1", GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	waitDone(t, harness.client.Done())
	select {
	case err = <-harness.serverDone:
		if !errors.Is(err, ErrTaskCanceled) {
			t.Fatalf("Serve() error = %v, want %v", err, ErrTaskCanceled)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not exit after cancellation")
	}
}

func TestSnapshotQuestionCanBeAnsweredBeforeSubscriptionUpdate(t *testing.T) {
	binding := testBinding(t)
	harness := newWorkerTestHarness(t, nil)
	controllerInstance := initializeHarness(t, harness, binding)
	waitForWorkerEvent(t, harness.client, EventWorkerReady)

	controllerInstance.mu.Lock()
	controllerInstance.question = &QuestionState{
		QuestionID: "question-from-snapshot",
		Revision:   3,
		Status:     QuestionWaiting,
		Prompt:     "Use the focused parser?",
	}
	controllerInstance.mu.Unlock()
	snapshot, err := harness.client.ReadSnapshot(t.Context(), GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Snapshot.Question == nil || snapshot.Snapshot.Question.Revision != 3 {
		t.Fatalf("snapshot question = %#v", snapshot.Snapshot.Question)
	}
	if err = harness.client.StartTurn(t.Context(), "start-1", TurnStartParams{
		ControlIdentity: binding.ControlIdentity(), Text: "inspect",
	}); err != nil {
		t.Fatal(err)
	}
	if err = harness.client.Steer(t.Context(), "answer-request", TurnSteerParams{
		ControlIdentity: binding.ControlIdentity(),
		Text:            "yes",
		QuestionAnswer: &QuestionAnswerRef{
			QuestionID:       "question-from-snapshot",
			QuestionRevision: 3,
			AnswerID:         "answer-from-snapshot",
		},
	}); err != nil {
		t.Fatalf("answer after snapshot error = %v", err)
	}
	controllerInstance.mu.Lock()
	if len(controllerInstance.steers) != 1 || controllerInstance.steers[0].ID != "answer-from-snapshot" {
		t.Fatalf("snapshot question steers = %#v", controllerInstance.steers)
	}
	controllerInstance.mu.Unlock()
	if err = harness.client.Cancel(t.Context(), "cancel-1", GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, harness.client.Done())
}

func TestWorkerRejectsMismatchedIdentityAndBuildBeforeFactoryWork(t *testing.T) {
	binding := testBinding(t)
	harness := newWorkerTestHarness(t, nil)
	initializeHarness(t, harness, binding)
	wrong := binding.ControlIdentity()
	wrong.WorkerGenerationID = "wrong-generation"
	assertRemoteCode(t, harness.client.StartTurn(t.Context(), "start-wrong", TurnStartParams{
		ControlIdentity: wrong, Text: "inspect",
	}), ErrorIdentityMismatch)
	if err := harness.client.Shutdown(t.Context(), "shutdown-1", GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, harness.client.Done())

	var factoryCalls int
	server, err := NewServer(ServerConfig{
		BuildID: "different-build",
		Open: func(context.Context, Binding) (TaskController, error) {
			factoryCalls++
			return nil, errors.New("must not run")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, serverDone := startPipeServer(t, server)
	_, err = client.Initialize(t.Context(), "initialize-build", InitializeParams{
		MinProtocolVersion: ProtocolV1, MaxProtocolVersion: ProtocolV1,
		ParentBuildID: "parent-build", Binding: binding,
	})
	assertRemoteCode(t, err, ErrorUnsupportedVersion)
	if factoryCalls != 0 {
		t.Fatalf("mismatched build called factory %d time(s)", factoryCalls)
	}
	select {
	case err = <-serverDone:
		if !errors.Is(err, ErrTaskFailed) {
			t.Fatalf("build mismatch server error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("build mismatch server did not exit")
	}
}

func TestWorkerReturnsStableUnsupportedVersionWithoutOpeningController(t *testing.T) {
	binding := testBinding(t)
	factoryCalls := 0
	server, err := NewServer(ServerConfig{
		BuildID: testWorkerBuildID,
		Open: func(context.Context, Binding) (TaskController, error) {
			factoryCalls++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerInput, parentOutput := io.Pipe()
	parentInput, workerOutput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), workerInput, workerOutput)
		_ = workerOutput.Close()
	}()
	params := InitializeParams{
		MinProtocolVersion: ProtocolV1 + 1, MaxProtocolVersion: ProtocolV1 + 1,
		ParentBuildID: "parent-build", Binding: binding,
	}
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(
		`{"schema_version":1,"type":"request","id":"initialize-1","method":"initialize",`+
			`"idempotency_key":"initialize-1","params":%s}`+"\n",
		payload,
	)
	if _, err = io.WriteString(parentOutput, line); err != nil {
		t.Fatal(err)
	}
	reader, err := newWireReader(parentInput)
	if err != nil {
		t.Fatal(err)
	}
	received := reader.read()
	if received.err != nil || received.record.Error == nil ||
		received.record.Error.Code != ErrorUnsupportedVersion {
		t.Fatalf("version response = %#v, %v", received.record, received.err)
	}
	if factoryCalls != 0 {
		t.Fatalf("version mismatch called factory %d time(s)", factoryCalls)
	}
	select {
	case err = <-serverDone:
		if !errors.Is(err, ErrControlStreamUncertain) {
			t.Fatalf("version mismatch server error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("version mismatch server did not exit")
	}
	_ = parentOutput.Close()
	_ = parentInput.Close()
}

func TestMalformedWorkerInputStopsInitializedController(t *testing.T) {
	binding := testBinding(t)
	controllers := make(chan *workerTestController, 1)
	server, err := NewServer(ServerConfig{
		BuildID: testWorkerBuildID,
		Open: func(_ context.Context, binding Binding) (TaskController, error) {
			controllerInstance, openErr := newWorkerTestController(binding)
			if openErr != nil {
				return nil, openErr
			}
			controllers <- controllerInstance
			return controllerInstance, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerInput, parentOutput := io.Pipe()
	parentInput, workerOutput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), workerInput, workerOutput)
		_ = workerOutput.Close()
	}()
	initialize := requestRecord(t, "initialize-1", MethodInitialize, "initialize-1", InitializeParams{
		MinProtocolVersion: ProtocolV1, MaxProtocolVersion: ProtocolV1,
		ParentBuildID: "parent-build", Binding: binding,
	})
	if _, err = writeWireRecord(parentOutput, initialize); err != nil {
		t.Fatal(err)
	}
	reader, err := newWireReader(parentInput)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if received := reader.read(); received.err != nil {
			t.Fatalf("initialize wire response error = %v", received.err)
		}
	}
	controllerInstance := <-controllers
	if _, err = io.WriteString(parentOutput, "{}\n"); err != nil {
		t.Fatal(err)
	}
	stopped := reader.read()
	if stopped.err != nil || stopped.record.Event != EventWorkerStopped {
		t.Fatalf("malformed stop record = %#v, %v", stopped.record, stopped.err)
	}
	payload, err := DecodeEventPayload(EventWorkerStopped, stopped.record.Payload)
	if err != nil || payload.(*WorkerStoppedPayload).Error.Code != ErrorUncertain {
		t.Fatalf("malformed stop payload = %#v, %v", payload, err)
	}
	select {
	case err = <-serverDone:
		if !errors.Is(err, ErrControlStreamUncertain) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after malformed input")
	}
	controllerInstance.mu.Lock()
	closes := controllerInstance.closes
	controllerInstance.mu.Unlock()
	if closes != 1 {
		t.Fatalf("controller closes = %d, want 1", closes)
	}
	_ = parentOutput.Close()
	_ = parentInput.Close()
}

func TestInitializedWorkerExitsAfterBoundedIdleWindow(t *testing.T) {
	binding := testBinding(t)
	controllers := make(chan *workerTestController, 1)
	server, err := NewServer(ServerConfig{
		BuildID:     testWorkerBuildID,
		IdleTimeout: 100 * time.Millisecond,
		Open: func(_ context.Context, binding Binding) (TaskController, error) {
			controllerInstance, openErr := newWorkerTestController(binding)
			if openErr != nil {
				return nil, openErr
			}
			controllers <- controllerInstance
			return controllerInstance, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, serverDone := startPipeServer(t, server)
	if _, err = client.Initialize(t.Context(), "initialize-1", InitializeParams{
		MinProtocolVersion: ProtocolV1,
		MaxProtocolVersion: ProtocolV1,
		ParentBuildID:      "parent-build",
		Binding:            binding,
	}); err != nil {
		t.Fatal(err)
	}
	controllerInstance := <-controllers
	waitForWorkerEvent(t, client, EventWorkerReady)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-client.Done():
				return
			case <-ticker.C:
				_, _ = client.ReadSnapshot(t.Context(), GenerationParams{
					ControlIdentity: binding.ControlIdentity(),
				})
			}
		}
	}()
	waitDone(t, client.Done())
	waitDone(t, pollDone)
	if err = client.Err(); err != nil {
		t.Fatalf("idle worker client error = %v", err)
	}
	stopped := waitForWorkerEvent(t, client, EventWorkerStopped)
	payload, err := DecodeEventPayload(EventWorkerStopped, stopped.Payload)
	if err != nil || payload.(*WorkerStoppedPayload).Reason != WorkerStopIdle {
		t.Fatalf("idle stop payload = %#v, %v", payload, err)
	}
	select {
	case err = <-serverDone:
		if !errors.Is(err, ErrWorkerIdleTimeout) {
			t.Fatalf("idle worker server error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle worker server did not exit")
	}
	controllerInstance.mu.Lock()
	closes := controllerInstance.closes
	controllerInstance.mu.Unlock()
	if closes != 1 {
		t.Fatalf("idle controller closes = %d, want 1", closes)
	}
}

func TestCanceledTurnWithFinalizationFailureReportsWorkerFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*workerTestController)
	}{
		{
			name: "final snapshot",
			configure: func(controllerInstance *workerTestController) {
				controllerInstance.snapshotErr = errors.New("final snapshot failed")
			},
		},
		{
			name: "controller close",
			configure: func(controllerInstance *workerTestController) {
				controllerInstance.closeErr = errors.New("controller close failed")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := testBinding(t)
			harness := newWorkerTestHarness(t, test.configure)
			initializeHarness(t, harness, binding)
			waitForWorkerEvent(t, harness.client, EventWorkerReady)
			if err := harness.client.StartTurn(t.Context(), "start-1", TurnStartParams{
				ControlIdentity: binding.ControlIdentity(), Text: "inspect",
			}); err != nil {
				t.Fatal(err)
			}
			if err := harness.client.Cancel(t.Context(), "cancel-1", GenerationParams{
				ControlIdentity: binding.ControlIdentity(),
			}); err != nil {
				t.Fatal(err)
			}
			waitDone(t, harness.client.Done())
			stopped := waitForWorkerEvent(t, harness.client, EventWorkerStopped)
			payload, err := DecodeEventPayload(EventWorkerStopped, stopped.Payload)
			if err != nil {
				t.Fatal(err)
			}
			outcome := payload.(*WorkerStoppedPayload)
			if outcome.Reason != WorkerStopFailed || outcome.Error == nil || outcome.Error.Code != ErrorInternal {
				t.Fatalf("worker stop outcome = %#v, want failed internal error", outcome)
			}
			select {
			case err = <-harness.serverDone:
				if !errors.Is(err, ErrTaskFailed) {
					t.Fatalf("Serve() error = %v, want %v", err, ErrTaskFailed)
				}
			case <-time.After(time.Second):
				t.Fatal("server did not exit after failed cancellation finalization")
			}
		})
	}
}

func TestClientClassifiesLostMutatingResponseAsUncertainWithoutBlindReplay(t *testing.T) {
	binding := testBinding(t)
	client, requests, responses := newManualClient(t)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		reader, _ := newWireReader(requests)
		if received := reader.read(); received.err == nil {
			_ = responses.Close()
		}
	}()
	err := client.StartTurn(t.Context(), "start-1", TurnStartParams{
		ControlIdentity: binding.ControlIdentity(), Text: "inspect",
	})
	if !errors.Is(err, ErrControlStreamUncertain) {
		t.Fatalf("lost mutating response error = %v, want uncertainty", err)
	}
	waitDone(t, workerDone)

	readClient, readRequests, readResponses := newManualClient(t)
	go func() {
		reader, _ := newWireReader(readRequests)
		if received := reader.read(); received.err == nil {
			_ = readResponses.Close()
		}
	}()
	_, err = readClient.ReadSnapshot(t.Context(), GenerationParams{ControlIdentity: binding.ControlIdentity()})
	if errors.Is(err, ErrControlStreamUncertain) || !errors.Is(err, ErrWorkerDisconnected) {
		t.Fatalf("lost snapshot response error = %v", err)
	}
}

func TestClientAcceptsLateResponseForCanceledCallAndContinues(t *testing.T) {
	binding := testBinding(t)
	client, requests, responses := newManualClient(t)
	firstRead := make(chan struct{})
	releaseFirst := make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		reader, _ := newWireReader(requests)
		first := reader.read()
		if first.err != nil {
			workerDone <- first.err
			return
		}
		close(firstRead)
		<-releaseFirst
		if _, err := writeWireRecord(responses, successfulResponse(first.record, AckResult{})); err != nil {
			workerDone <- err
			return
		}
		second := reader.read()
		if second.err != nil {
			workerDone <- second.err
			return
		}
		result := SnapshotResult{
			ControlIdentity: binding.ControlIdentity(),
			Snapshot:        Snapshot{ThreadID: binding.ThreadID, Activity: ActivityIdle},
		}
		if _, err := writeWireRecord(responses, successfulResponse(second.record, result)); err != nil {
			workerDone <- err
			return
		}
		workerDone <- nil
	}()
	ctx, cancel := context.WithCancel(t.Context())
	callDone := make(chan error, 1)
	go func() {
		callDone <- client.StartTurn(ctx, "start-1", TurnStartParams{
			ControlIdentity: binding.ControlIdentity(), Text: "inspect",
		})
	}()
	waitDone(t, firstRead)
	cancel()
	if err := <-callDone; !errors.Is(err, ErrControlStreamUncertain) {
		t.Fatalf("canceled accepted call error = %v", err)
	}
	close(releaseFirst)
	if _, err := client.ReadSnapshot(t.Context(), GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatalf("call after late response error = %v", err)
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientBindsLateInitializeResponseBeforeReadyEvent(t *testing.T) {
	binding := testBinding(t)
	client, requests, responses := newManualClient(t)
	requestRead := make(chan struct{})
	releaseResponse := make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		reader, err := newWireReader(requests)
		if err != nil {
			workerDone <- err
			return
		}
		initialize := reader.read()
		if initialize.err != nil {
			workerDone <- initialize.err
			return
		}
		close(requestRead)
		<-releaseResponse
		result := InitializeResult{Identity: BoundIdentity{
			ProtocolVersion: ProtocolV1,
			WorkerBuildID:   testWorkerBuildID,
			Binding:         binding,
		}}
		if _, err = writeWireRecord(responses, successfulResponse(initialize.record, result)); err != nil {
			workerDone <- err
			return
		}
		ready, err := eventRecord(projectedEvent{
			name: EventWorkerReady,
			payload: WorkerReadyPayload{
				ControlIdentity: binding.ControlIdentity(),
				Snapshot: Snapshot{
					ThreadID: binding.ThreadID,
					Activity: ActivityIdle,
				},
			},
		})
		if err != nil {
			workerDone <- err
			return
		}
		if _, err = writeWireRecord(responses, ready); err != nil {
			workerDone <- err
			return
		}
		snapshotRequest := reader.read()
		if snapshotRequest.err != nil {
			workerDone <- snapshotRequest.err
			return
		}
		snapshot := SnapshotResult{
			ControlIdentity: binding.ControlIdentity(),
			Snapshot: Snapshot{
				ThreadID: binding.ThreadID,
				Activity: ActivityIdle,
			},
		}
		_, err = writeWireRecord(responses, successfulResponse(snapshotRequest.record, snapshot))
		workerDone <- err
	}()

	ctx, cancel := context.WithCancel(t.Context())
	initializeDone := make(chan error, 1)
	go func() {
		_, err := client.Initialize(ctx, "initialize-1", InitializeParams{
			MinProtocolVersion: ProtocolV1,
			MaxProtocolVersion: ProtocolV1,
			ParentBuildID:      "parent-build",
			Binding:            binding,
		})
		initializeDone <- err
	}()
	waitDone(t, requestRead)
	cancel()
	if err := <-initializeDone; !errors.Is(err, ErrControlStreamUncertain) {
		t.Fatalf("late Initialize() error = %v, want uncertainty", err)
	}
	close(releaseResponse)
	waitForWorkerEvent(t, client, EventWorkerReady)
	if _, err := client.ReadSnapshot(t.Context(), GenerationParams{
		ControlIdentity: binding.ControlIdentity(),
	}); err != nil {
		t.Fatalf("snapshot after late initialize response error = %v", err)
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientEventHistoryIsBoundedAndReportsGap(t *testing.T) {
	binding := testBinding(t)
	identity := binding.ControlIdentity()
	client := &Client{wake: make(chan struct{}, 1), eventIdentity: &identity}
	for index := 0; index < MaxClientEvents+20; index++ {
		record, err := eventRecord(projectedEvent{
			name: EventContextUsage,
			payload: ContextUsagePayload{
				ControlIdentity: binding.ControlIdentity(),
				Usage:           ContextUsage{UsedTokens: index, LimitTokens: MaxClientEvents + 20},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		client.retainEvent(record)
	}
	page := client.EventsAfter(0)
	if !page.HistoryGap || len(page.Events) != MaxClientEvents || page.RetainedFrom != 21 ||
		page.NextCursor != MaxClientEvents+20 {
		t.Fatalf("count-bounded event page = %#v", page)
	}

	client.events = nil
	client.eventBytes = 0
	client.nextCursor = 0
	largeText := strings.Repeat("x", MaxEventTextBytes)
	for index := 0; index < 160; index++ {
		item := validEventItem(uint64(index + 1))
		item.ID = fmt.Sprintf("message:turn-1:item-%d", index+1)
		item.Message.Text = largeText
		record, err := eventRecord(projectedEvent{
			name: EventItemUpdated,
			payload: ItemUpdatedPayload{
				ControlIdentity: binding.ControlIdentity(), Item: item,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		client.retainEvent(record)
	}
	page = client.EventsAfter(0)
	if !page.HistoryGap || len(page.Events) >= 160 || client.eventBytes > MaxClientEventBytes {
		t.Fatalf("byte-bounded event page = %d events, %d bytes, gap=%t",
			len(page.Events), client.eventBytes, page.HistoryGap)
	}
}

func TestClientRejectsEventOutsideInitializedControlIdentity(t *testing.T) {
	binding := testBinding(t)
	client, requests, responses := newManualClient(t)
	workerDone := make(chan error, 1)
	go func() {
		reader, err := newWireReader(requests)
		if err != nil {
			workerDone <- err
			return
		}
		request := reader.read()
		if request.err != nil {
			workerDone <- request.err
			return
		}
		result := InitializeResult{Identity: BoundIdentity{
			ProtocolVersion: ProtocolV1,
			WorkerBuildID:   testWorkerBuildID,
			Binding:         binding,
		}}
		if _, err = writeWireRecord(responses, successfulResponse(request.record, result)); err != nil {
			workerDone <- err
			return
		}
		wrong := binding.ControlIdentity()
		wrong.WorkerGenerationID = "unrelated-worker-generation"
		stopped, err := eventRecord(projectedEvent{
			name: EventWorkerStopped,
			payload: WorkerStoppedPayload{
				ControlIdentity: wrong,
				Reason:          WorkerStopCompleted,
			},
		})
		if err == nil {
			_, err = writeWireRecord(responses, stopped)
		}
		_ = responses.Close()
		workerDone <- err
	}()

	if _, err := client.Initialize(t.Context(), "initialize-1", InitializeParams{
		MinProtocolVersion: ProtocolV1,
		MaxProtocolVersion: ProtocolV1,
		ParentBuildID:      "parent-build",
		Binding:            binding,
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	waitDone(t, client.Done())
	if !errors.Is(client.Err(), ErrClientProtocol) {
		t.Fatalf("mismatched event error = %v, want %v", client.Err(), ErrClientProtocol)
	}
	if page := client.EventsAfter(0); len(page.Events) != 0 {
		t.Fatalf("retained mismatched events = %d, want 0", len(page.Events))
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
}

func TestWireReaderRejectsAndDrainsOversizedLine(t *testing.T) {
	reader, err := newWireReader(strings.NewReader(strings.Repeat("x", MaxRecordBytes+2) + "\nnext\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.readLine(); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("oversized line error = %v", err)
	}
	line, err := reader.readLine()
	if err != nil || string(line) != "next" {
		t.Fatalf("line after oversized frame = %q, %v", line, err)
	}
}

func startPipeServer(t *testing.T, server *Server) (*Client, <-chan error) {
	t.Helper()
	workerInput, parentOutput := io.Pipe()
	parentInput, workerOutput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), workerInput, workerOutput)
		_ = workerOutput.Close()
	}()
	client, err := NewClient(parentInput, parentOutput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, serverDone
}

func newManualClient(t *testing.T) (*Client, *io.PipeReader, *io.PipeWriter) {
	t.Helper()
	clientInput, responses := io.Pipe()
	requests, clientOutput := io.Pipe()
	client, err := NewClient(clientInput, clientOutput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = requests.Close()
		_ = responses.Close()
	})
	return client, requests, responses
}

func requestRecord(t *testing.T, id string, method Method, key string, params any) Record {
	t.Helper()
	payload, err := MarshalPayload(params)
	if err != nil {
		t.Fatal(err)
	}
	return Record{
		SchemaVersion: ProtocolV1, Type: RecordRequest, ID: id,
		Method: method, IdempotencyKey: key, Params: payload,
	}
}

func waitForWorkerEvent(t *testing.T, client *Client, event EventName) Record {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		for _, retained := range client.EventsAfter(0).Events {
			if retained.Record.Event == event {
				return retained.Record
			}
		}
		select {
		case <-client.Wake():
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", event)
			return Record{}
		}
	}
}

func assertRemoteCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Failure.Code != code {
		t.Fatalf("remote error = %v, want code %s", err, code)
	}
}

func assertEventOrder(t *testing.T, page EventPage, names ...EventName) {
	t.Helper()
	next := 0
	for _, event := range page.Events {
		if next < len(names) && event.Record.Event == names[next] {
			next++
		}
	}
	if next != len(names) {
		t.Fatalf("events = %v, missing ordered suffix %v", eventNames(page.Events), names[next:])
	}
}

func eventNames(events []RetainedEvent) []EventName {
	names := make([]EventName, len(events))
	for index, event := range events {
		names[index] = event.Record.Event
	}
	return names
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for completion")
	}
}
