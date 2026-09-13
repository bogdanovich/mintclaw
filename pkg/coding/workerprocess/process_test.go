package workerprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
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

func TestLauncherRequiresOwnedWorktreeForMutation(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	_, _, owner, binding := testOwnedProcessFixture(t, buildID, "worker-mutate")
	defer func() { _ = owner.Release() }()

	if _, err := launcher.Launch(t.Context(), binding); !errors.Is(err, ErrWorktreeOwnerRequired) {
		t.Fatalf("Launch(mutation) error = %v, want %v", err, ErrWorktreeOwnerRequired)
	}
}

func TestLaunchOwnedRetainsOwnerThroughHandoff(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	manager, allocation, owner, binding := testOwnedProcessFixture(t, buildID, "worker-owned")
	process, err := launcher.LaunchOwned(t.Context(), binding, owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	waitForProcessEvent(t, process.Process, worker.EventWorkerReady)
	releaseStarted := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() {
		close(releaseStarted)
		releaseDone <- owner.Release()
	}()
	<-releaseStarted
	select {
	case err := <-releaseDone:
		t.Fatalf("concurrent owner release completed while worker was live: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	successorRequest := worktree.OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: binding.TaskID,
		TaskGenerationID: binding.TaskGenerationID, ThreadID: binding.ThreadID,
		WorkerGenerationID: "worker-successor",
	}
	if contender, contenderErr := manager.AcquireOwner(t.Context(), successorRequest); !errors.Is(
		contenderErr,
		worktree.ErrOwnerBusy,
	) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("live successor error = %v, want %v", contenderErr, worktree.ErrOwnerBusy)
	}
	if err := process.Shutdown(t.Context(), "shutdown-owned"); err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait(testTimeoutContext(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.Process.Outcome() != OutcomeShutdown || result.FinalizationError != nil ||
		result.Handoff == nil || result.Handoff.Class != worktree.HandoffReady {
		t.Fatalf("owned result = %#v", result)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("concurrent owner release after worker completion: %v", err)
	}
	loaded, err := manager.LoadHandoff(t.Context(), allocation.WorktreeID)
	if err != nil || loaded.HandoffID != result.Handoff.HandoffID {
		t.Fatalf("durable handoff = %#v, %v", loaded, err)
	}
	successor, err := manager.AcquireOwner(t.Context(), successorRequest)
	if err != nil {
		t.Fatalf("successor after handoff: %v", err)
	}
	if err := successor.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchOwnedReleasesOwnerAfterInitializationFailure(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	manager, allocation, owner, binding := testOwnedProcessFixture(t, buildID, "worker-failed-launch")
	binding.ExpectedWorkerBuildID = "sha256:" + strings.Repeat("0", 64)

	if _, err := launcher.LaunchOwned(t.Context(), binding, owner); !errors.Is(err, ErrExecutableMismatch) {
		t.Fatalf("LaunchOwned(build mismatch) error = %v, want %v", err, ErrExecutableMismatch)
	}
	successor, err := manager.AcquireOwner(t.Context(), worktree.OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: binding.TaskID,
		TaskGenerationID: binding.TaskGenerationID, ThreadID: binding.ThreadID,
		WorkerGenerationID: "worker-after-failure",
	})
	if err != nil {
		t.Fatalf("owner remained held after failed launch: %v", err)
	}
	if err := successor.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchOwnedFailurePreservesPendingFinalizationHandle(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	manager, allocation, owner, binding := testOwnedProcessFixture(t, buildID, "worker-pending-launch")
	recordPath := filepath.Join(
		manager.StateRoot(),
		"worktrees",
		"allocations",
		allocation.WorktreeID,
		"allocation.json",
	)
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	repairNeeded := false
	var launchError *OwnedLaunchError
	t.Cleanup(func() {
		if repairNeeded {
			_ = os.Remove(recordPath)
			_ = os.WriteFile(recordPath, recordData, 0o600)
		}
		if launchError != nil {
			_, _ = launchError.RetryFinalization(context.Background())
		} else {
			_ = owner.Release()
		}
	})
	originalBuildID := launcher.buildID
	launcher.buildID = func(path string) (string, error) {
		repairNeeded = true
		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(recordPath, 0o700); err != nil {
			t.Fatal(err)
		}
		return originalBuildID(path)
	}
	binding.ExpectedWorkerBuildID = "sha256:" + strings.Repeat("0", 64)

	process, err := launcher.LaunchOwned(t.Context(), binding, owner)
	if process != nil || !errors.Is(err, ErrExecutableMismatch) ||
		!errors.Is(err, worktree.ErrFinalizationPending) || !errors.As(err, &launchError) {
		t.Fatalf("LaunchOwned() = %#v, %v; want retry-capable pending error", process, err)
	}
	if err := owner.Validate(ownerRequestForBinding(binding)); err != nil {
		t.Fatalf("owner released while finalization remained pending: %v", err)
	}
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	repairNeeded = false
	if contender, contenderErr := manager.AcquireOwner(t.Context(), worktree.OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: binding.TaskID,
		TaskGenerationID: binding.TaskGenerationID, ThreadID: binding.ThreadID,
		WorkerGenerationID: "worker-before-finalization-retry",
	}); !errors.Is(contenderErr, worktree.ErrOwnerBusy) {
		if contender != nil {
			_ = contender.Release()
		}
		t.Fatalf("successor before retry error = %v, want %v", contenderErr, worktree.ErrOwnerBusy)
	}

	handoff, err := launchError.RetryFinalization(t.Context())
	if err != nil || handoff.Class != worktree.HandoffReady {
		t.Fatalf("RetryFinalization() = %#v, %v", handoff, err)
	}
	successor, err := manager.AcquireOwner(t.Context(), worktree.OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: binding.TaskID,
		TaskGenerationID: binding.TaskGenerationID, ThreadID: binding.ThreadID,
		WorkerGenerationID: "worker-after-finalization-retry",
	})
	if err != nil {
		t.Fatalf("successor after finalization retry: %v", err)
	}
	if err := successor.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestResultOutcomeRequiresAuthenticatedWorkerStop(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   Outcome
	}{
		{name: "missing stop", result: Result{ProcessError: errors.New("crash")}, want: OutcomeUncertain},
		{
			name: "completed",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopCompleted,
			}},
			want: OutcomeCompleted,
		},
		{
			name: "canceled",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopCanceled,
			}},
			want: OutcomeInterrupted,
		},
		{
			name: "shutdown",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopShutdown,
			}},
			want: OutcomeShutdown,
		},
		{
			name: "idle",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopIdle,
			}},
			want: OutcomeIdle,
		},
		{
			name: "failed",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopFailed,
				Error:  &worker.ProtocolError{Code: worker.ErrorInternal},
			}},
			want: OutcomeFailed,
		},
		{
			name: "worker reported uncertain",
			result: Result{WorkerStop: &worker.WorkerStoppedPayload{
				Reason: worker.WorkerStopFailed,
				Error:  &worker.ProtocolError{Code: worker.ErrorUncertain},
			}},
			want: OutcomeUncertain,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.result.Outcome(); got != test.want {
				t.Fatalf("Outcome() = %q, want %q", got, test.want)
			}
		})
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
		ExecutionRoot:         project.ProjectRoot,
		ExecutionRootIdentity: worker.ExecutionRootIdentity(project.ProjectRoot),
		Mode:                  worker.TaskModeInvestigate,
		ProviderProfile:       "default",
		Model:                 "gpt-test",
		Provider:              "openai",
		ExpectedWorkerBuildID: buildID,
	}
}

func testOwnedProcessFixture(
	t *testing.T,
	buildID string,
	workerGeneration string,
) (*worktree.Manager, worktree.Allocation, *worktree.Owner, worker.Binding) {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.Mkdir(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runWorkerProcessGit(t, sourceRoot, "init", "-b", "main")
	runWorkerProcessGit(t, sourceRoot, "config", "user.email", "mintclaw@example.invalid")
	runWorkerProcessGit(t, sourceRoot, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(sourceRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWorkerProcessGit(t, sourceRoot, "add", "README.md")
	runWorkerProcessGit(t, sourceRoot, "commit", "-m", "fixture")
	project, err := thread.ResolveProject(t.Context(), sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	binding := worker.Binding{
		TaskID: "task-owned", TaskGenerationID: "task-generation-owned",
		WorkerGenerationID: workerGeneration, ThreadID: thread.NewThreadID(),
		ThreadOpenMode: worker.ThreadOpenNew, Project: project, Mode: worker.TaskModeMutate,
		ProviderProfile: "default", Model: "gpt-test", Provider: "openai",
		ExpectedWorkerBuildID: buildID,
	}
	manager, err := worktree.NewManager(worktree.Config{
		StateRoot: filepath.Join(root, "state"), WorktreeParent: filepath.Join(root, "executions"),
	})
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := manager.Allocate(t.Context(), worktree.Request{
		TaskID: binding.TaskID, TaskGenerationID: binding.TaskGenerationID,
		ThreadID: binding.ThreadID, Source: project, BaseRevision: project.GitHead,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding.ExecutionRoot = allocation.ExecutionRoot
	binding.ExecutionRootIdentity = worker.ExecutionRootIdentity(allocation.ExecutionRoot)
	owner, err := manager.AcquireOwner(t.Context(), ownerRequestForBinding(binding))
	if err != nil {
		t.Fatal(err)
	}
	return manager, allocation, owner, binding
}

func runWorkerProcessGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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
