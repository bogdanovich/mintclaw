package workerprocess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
)

const (
	workerProcessHelperEnvironment = "MINTCLAW_WORKERPROCESS_TEST_HELPER"
	workerProcessHelperBuildID     = "MINTCLAW_WORKERPROCESS_TEST_BUILD_ID"
)

func TestLauncherControlsOneBoundWorkerProcess(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenNew)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })

	if process.Binding() != binding {
		t.Fatalf("binding = %#v, want %#v", process.Binding(), binding)
	}
	ready := waitForProcessEvent(t, process, worker.EventWorkerReady)
	readyPayload, err := worker.DecodeEventPayload(ready.Event, ready.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if readyPayload.(*worker.WorkerReadyPayload).ControlIdentity != binding.ControlIdentity() {
		t.Fatalf("ready identity = %#v", readyPayload)
	}

	if err = process.StartTurn(t.Context(), "turn-start-1", "inspect this project", nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := process.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ControlIdentity != binding.ControlIdentity() || snapshot.Snapshot.ActiveTurnID != "turn-1" {
		t.Fatalf("active snapshot = %#v", snapshot)
	}
	if err = process.Interrupt(t.Context(), "turn-interrupt-1"); err != nil {
		t.Fatal(err)
	}
	if err = process.Steer(t.Context(), "steer-1", "finish", nil); err != nil {
		t.Fatal(err)
	}

	result, err := process.Wait(testTimeoutContext(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding != binding || result.WorkerStop == nil ||
		result.WorkerStop.Reason != worker.WorkerStopCompleted {
		t.Fatalf("process result = %#v", result)
	}
	if result.ProcessError != nil || result.ClientError != nil || result.Diagnostics.Truncated {
		t.Fatalf("process errors = process=%v client=%v diagnostics=%#v",
			result.ProcessError, result.ClientError, result.Diagnostics)
	}
	names := processEventNames(process.EventsAfter(0).Events)
	for _, expected := range []worker.EventName{
		worker.EventWorkerReady,
		worker.EventStatusChanged,
		worker.EventTurnTerminal,
		worker.EventWorkerStopped,
	} {
		if !slices.Contains(names, expected) {
			t.Fatalf("events = %v, missing %s", names, expected)
		}
	}
}

func TestLauncherShutsDownIdleBoundWorkerProcess(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenResume)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err = process.Shutdown(t.Context(), "shutdown-1"); err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait(testTimeoutContext(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStop == nil || result.WorkerStop.Reason != worker.WorkerStopShutdown ||
		result.ProcessError != nil || result.ClientError != nil {
		t.Fatalf("process result = %#v", result)
	}
}

func TestLauncherHardCancelsOneBoundWorkerProcess(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenResume)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err = process.StartTurn(t.Context(), "turn-start-1", "keep running", nil); err != nil {
		t.Fatal(err)
	}
	if err = process.HardCancel(t.Context(), "turn-cancel-1"); err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait(testTimeoutContext(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStop == nil || result.WorkerStop.Reason != worker.WorkerStopCanceled {
		t.Fatalf("process result = %#v", result)
	}
	if result.ClientError != nil {
		t.Fatalf("client error = %v", result.ClientError)
	}
}

func TestLauncherRejectsBuildMismatchBeforeStartingProcess(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	binding := testProcessBinding(t, "sha256:"+strings.Repeat("0", 64), worker.ThreadOpenNew)
	started := false
	launcher.buildID = func(string) (string, error) {
		started = true
		return buildID, nil
	}
	_, err := launcher.Launch(t.Context(), binding)
	if !errors.Is(err, ErrExecutableMismatch) {
		t.Fatalf("Launch() error = %v, want %v", err, ErrExecutableMismatch)
	}
	if !started {
		t.Fatal("executable identity was not checked")
	}
}

func TestBoundedBufferRetainsPrefixAndReportsTruncation(t *testing.T) {
	buffer := &boundedBuffer{limit: 5}
	if written, err := buffer.Write([]byte("abcdefgh")); err != nil || written != 8 {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	diagnostics := buffer.snapshot()
	if diagnostics.Stderr != "abcde" || !diagnostics.Truncated {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestWorkerProcessHelper(t *testing.T) {
	if os.Getenv(workerProcessHelperEnvironment) != "1" {
		return
	}
	server, err := worker.NewServer(worker.ServerConfig{
		BuildID: os.Getenv(workerProcessHelperBuildID),
		Open: func(_ context.Context, binding worker.Binding) (worker.TaskController, error) {
			return newProcessTestController(binding)
		},
	})
	if err == nil {
		err = server.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil && !errors.Is(err, worker.ErrTaskCanceled) {
		_, _ = os.Stderr.WriteString(err.Error())
		os.Exit(2)
	}
	os.Exit(0)
}

func newTestLauncher(t *testing.T) (*Launcher, string) {
	t.Helper()
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := worker.ExecutableBuildID(executable)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := NewLauncher(LauncherConfig{
		ExecutablePath: executable,
		ParentBuildID:  "mintclaw-parent-test",
		Environment: append(
			os.Environ(),
			workerProcessHelperEnvironment+"=1",
			workerProcessHelperBuildID+"="+buildID,
		),
		InitializeTimeout: 5 * time.Second,
		StopTimeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	launcher.commandArgs = []string{"-test.run=^TestWorkerProcessHelper$"}
	return launcher, buildID
}

func testProcessBinding(t *testing.T, buildID string, openMode worker.ThreadOpenMode) worker.Binding {
	t.Helper()
	root := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return worker.Binding{
		TaskID:                "task-1",
		TaskGenerationID:      "task-generation-1",
		WorkerGenerationID:    "worker-generation-1",
		ThreadID:              uuid.NewString(),
		ThreadOpenMode:        openMode,
		Project:               project,
		ExecutionRoot:         root,
		ExecutionRootIdentity: worker.ExecutionRootIdentity(root),
		Mode:                  worker.TaskModeInvestigate,
		ProviderProfile:       "default",
		Model:                 "gpt-test",
		Provider:              "openai",
		ExpectedWorkerBuildID: buildID,
	}
}

func waitForProcessEvent(t *testing.T, process *Process, event worker.EventName) worker.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		for _, retained := range process.EventsAfter(0).Events {
			if retained.Record.Event == event {
				return retained.Record
			}
		}
		select {
		case <-process.Wake():
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", event)
		}
	}
}

func processEventNames(events []worker.RetainedEvent) []worker.EventName {
	names := make([]worker.EventName, len(events))
	for index, event := range events {
		names[index] = event.Record.Event
	}
	return names
}

func testTimeoutContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	return ctx
}

type processTestController struct {
	projector *frontend.Projector

	mu         sync.Mutex
	active     bool
	settlement chan error
	settleOnce sync.Once
}

func newProcessTestController(binding worker.Binding) (*processTestController, error) {
	projector, err := frontend.NewProjector(binding.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		return nil, err
	}
	projector.Open(binding.ThreadOpenMode == worker.ThreadOpenResume)
	return &processTestController{projector: projector, settlement: make(chan error, 1)}, nil
}

func (controllerInstance *processTestController) Snapshot(ctx context.Context) (frontend.ThreadSnapshot, error) {
	return controllerInstance.projector.Snapshot(ctx)
}

func (controllerInstance *processTestController) Subscribe(
	ctx context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	return controllerInstance.projector.Subscribe(ctx)
}

func (controllerInstance *processTestController) Submit(_ context.Context, input frontend.TurnInput) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if controllerInstance.active {
		return controller.ErrTurnActive
	}
	controllerInstance.active = true
	if err := maybeStartProcessTestDescendant(); err != nil {
		controllerInstance.active = false
		return err
	}
	controllerInstance.projector.TurnStarted("turn-1", input.Text)
	return nil
}

func (controllerInstance *processTestController) Steer(_ context.Context, input frontend.SteerInput) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if !controllerInstance.active {
		return controller.ErrNoActiveTurn
	}
	if input.Text == "finish" {
		controllerInstance.active = false
		controllerInstance.projector.AssistantAccumulated("turn-1", "inspection complete", true)
		controllerInstance.projector.TurnCompleted("turn-1", "completed")
		controllerInstance.settleOnce.Do(func() { controllerInstance.settlement <- nil })
	}
	return nil
}

func (controllerInstance *processTestController) Interrupt(context.Context) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if !controllerInstance.active {
		return controller.ErrNoActiveTurn
	}
	return nil
}

func (controllerInstance *processTestController) HardCancel(context.Context) error {
	controllerInstance.mu.Lock()
	defer controllerInstance.mu.Unlock()
	if !controllerInstance.active {
		return controller.ErrNoActiveTurn
	}
	controllerInstance.active = false
	controllerInstance.projector.TurnInterrupted("turn-1", "canceled")
	controllerInstance.settleOnce.Do(func() { controllerInstance.settlement <- controller.ErrHardCanceled })
	return nil
}

func (*processTestController) Compact(context.Context) error { return frontend.ErrCommandUnsupported }

func (*processTestController) Rename(context.Context, string) error {
	return frontend.ErrCommandUnsupported
}

func (*processTestController) SetArchived(context.Context, bool) error {
	return frontend.ErrCommandUnsupported
}

func (*processTestController) NewThread(context.Context) error {
	return frontend.ErrCommandUnsupported
}

func (controllerInstance *processTestController) Close(context.Context) error {
	controllerInstance.mu.Lock()
	active := controllerInstance.active
	controllerInstance.mu.Unlock()
	if active {
		return controllerInstance.HardCancel(context.Background())
	}
	return nil
}

func (controllerInstance *processTestController) AwaitTurn(ctx context.Context) error {
	select {
	case err := <-controllerInstance.settlement:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (*processTestController) CodingWorkerQuestion(context.Context) (*worker.QuestionState, error) {
	return nil, nil
}
