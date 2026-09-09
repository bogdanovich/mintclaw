package coding

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
)

const nativeWorkerTestBuildID = "sha256:native-worker-test"

type nativeWorkerTestController struct {
	*execTestController
}

func (*nativeWorkerTestController) Steer(context.Context, frontend.SteerInput) error {
	return nil
}

func TestNativeWorkerFactoryCreatesAndStrictlyResumesBoundThread(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	var requests []codingTurnRequest
	var resumedValues []bool
	deps.newController = func(request codingTurnRequest, resumed bool) (frontend.Controller, error) {
		requests = append(requests, request)
		resumedValues = append(resumedValues, resumed)
		controllerInstance, controllerErr := newExecTestController(request, false, false)
		if controllerErr != nil {
			return nil, controllerErr
		}
		return &nativeWorkerTestController{execTestController: controllerInstance}, nil
	}
	binding := nativeWorkerBinding(project, worker.ThreadOpenNew, worker.TaskModeInvestigate)
	controllerInstance, err := openNativeWorkerController(t.Context(), deps, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || resumedValues[0] || !requests[0].ReadOnly ||
		requests[0].ExecutionRoot != project.ProjectRoot || requests[0].Metadata.ThreadID != binding.ThreadID {
		t.Fatalf("new worker request = %+v, resumed=%v", requests, resumedValues)
	}
	if err := controllerInstance.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(binding.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.PendingFirstPrompt || metadata.Model != binding.Model || metadata.Provider != binding.Provider {
		t.Fatalf("new worker metadata = %+v", metadata)
	}

	now = now.Add(time.Minute)
	binding.ThreadOpenMode = worker.ThreadOpenResume
	binding.Mode = worker.TaskModeMutate
	binding.Model = "fixture-resume-model"
	controllerInstance, err = openNativeWorkerController(t.Context(), deps, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !resumedValues[1] || requests[1].ReadOnly ||
		requests[1].Metadata.Model != binding.Model {
		t.Fatalf("resume worker request = %+v, resumed=%v", requests, resumedValues)
	}
	if err := controllerInstance.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err = store.Load(binding.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.UpdatedAt.Equal(now) || metadata.Model != binding.Model {
		t.Fatalf("resumed updated_at = %v, want %v", metadata.UpdatedAt, now)
	}

	binding.ThreadOpenMode = worker.ThreadOpenNew
	if _, err := openNativeWorkerController(t.Context(), deps, binding); !errors.Is(err, thread.ErrThreadExists) {
		t.Fatalf("duplicate new worker error = %v, want %v", err, thread.ErrThreadExists)
	}
	if len(requests) != 2 {
		t.Fatalf("duplicate new reached controller construction: %d calls", len(requests))
	}
}

func TestNativeWorkerFactoryFailsClosedBeforeThreadState(t *testing.T) {
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	tests := []struct {
		name   string
		mutate func(*worker.Binding, *dependencies)
	}{
		{
			name: "provider profile",
			mutate: func(binding *worker.Binding, _ *dependencies) {
				binding.ProviderProfile = "remote"
			},
		},
		{
			name: "execution root",
			mutate: func(binding *worker.Binding, _ *dependencies) {
				binding.ExecutionRoot = otherRoot
				binding.ExecutionRootIdentity = worker.ExecutionRootIdentity(otherRoot)
			},
		},
		{
			name: "project snapshot",
			mutate: func(binding *worker.Binding, _ *dependencies) {
				nested := filepath.Join(projectRoot, "nested")
				if err := os.Mkdir(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				binding.Project.InvocationCWD = nested
			},
		},
		{
			name: "model selection",
			mutate: func(_ *worker.Binding, deps *dependencies) {
				deps.resolveModel = func(string) (string, string, error) {
					return "different-model", "fixture", nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			now := time.Date(2026, time.September, 8, 13, 0, 0, 0, time.UTC)
			deps := testDependencies(home, projectRoot, &now)
			controllerCalls := 0
			deps.newController = func(codingTurnRequest, bool) (frontend.Controller, error) {
				controllerCalls++
				return nil, nil
			}
			binding := nativeWorkerBinding(project, worker.ThreadOpenNew, worker.TaskModeMutate)
			test.mutate(&binding, &deps)
			if _, err := openNativeWorkerController(t.Context(), deps, binding); err == nil {
				t.Fatal("invalid binding was accepted")
			}
			if controllerCalls != 0 {
				t.Fatalf("invalid binding reached controller construction: %d calls", controllerCalls)
			}
			if _, err := os.Stat(filepath.Join(home, "coding")); !os.IsNotExist(err) {
				t.Fatalf("invalid binding created thread state: %v", err)
			}
		})
	}
}

func TestNativeWorkerResumeHonorsThreadLease(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 8, 14, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		controllerInstance, controllerErr := newExecTestController(request, false, false)
		if controllerErr != nil {
			return nil, controllerErr
		}
		return &nativeWorkerTestController{execTestController: controllerInstance}, nil
	}
	binding := nativeWorkerBinding(project, worker.ThreadOpenNew, worker.TaskModeMutate)
	created, err := openNativeWorkerController(t.Context(), deps, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(binding.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	binding.ThreadOpenMode = worker.ThreadOpenResume
	if _, err := openNativeWorkerController(t.Context(), deps, binding); !errors.Is(err, thread.ErrLeaseBusy) {
		t.Fatalf("busy resume error = %v, want %v", err, thread.ErrLeaseBusy)
	}
}

func TestNativeWorkerFactoryReleasesLeaseWhenControllerIsNotTaskCapable(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 8, 14, 30, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		return newExecTestController(request, false, false)
	}
	binding := nativeWorkerBinding(project, worker.ThreadOpenNew, worker.TaskModeMutate)
	if _, err := openNativeWorkerController(t.Context(), deps, binding); err == nil ||
		!strings.Contains(err.Error(), "lacks task control capabilities") {
		t.Fatalf("non-task controller error = %v", err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(binding.ThreadID)
	if err != nil {
		t.Fatalf("reacquire after rejected controller: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCodeWorkerHiddenCommandServesOneNativeTask(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 8, 15, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	deps.workerBuildID = func() (string, error) { return nativeWorkerTestBuildID, nil }
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		controllerInstance, controllerErr := newExecTestController(request, false, false)
		if controllerErr != nil {
			return nil, controllerErr
		}
		return &nativeWorkerTestController{execTestController: controllerInstance}, nil
	}
	binding := nativeWorkerBinding(project, worker.ThreadOpenNew, worker.TaskModeInvestigate)
	binding.ExpectedWorkerBuildID = nativeWorkerTestBuildID

	childInput, parentOutput := io.Pipe()
	parentInput, childOutput := io.Pipe()
	command := newCodeCommand(deps)
	command.SetArgs([]string{"_worker"})
	command.SetIn(childInput)
	command.SetOut(childOutput)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	commandDone := make(chan error, 1)
	go func() {
		err := command.Execute()
		_ = childOutput.CloseWithError(err)
		commandDone <- err
	}()

	client, err := worker.NewClient(parentInput, parentOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Initialize(t.Context(), "initialize-1", worker.InitializeParams{
		MinProtocolVersion: worker.ProtocolV1,
		MaxProtocolVersion: worker.ProtocolV1,
		ParentBuildID:      "parent-test-build",
		Binding:            binding,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.StartTurn(t.Context(), "turn-1", worker.TurnStartParams{
		ControlIdentity: binding.ControlIdentity(),
		Text:            "inspect the repository",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-commandDone:
		if err != nil {
			t.Fatalf("hidden worker command error = %v; stderr=%q", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hidden worker command did not stop after one settled turn")
	}
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("worker client did not observe process stream completion")
	}
	page := client.EventsAfter(0)
	var ready, stopped bool
	for _, retained := range page.Events {
		ready = ready || retained.Record.Event == worker.EventWorkerReady
		stopped = stopped || retained.Record.Event == worker.EventWorkerStopped
	}
	if !ready || !stopped || client.Err() != nil {
		t.Fatalf("worker events ready=%v stopped=%v client error=%v", ready, stopped, client.Err())
	}
	metadata, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Load(binding.ThreadID); err != nil {
		t.Fatalf("hidden worker did not publish exact thread: %v", err)
	}
}

func TestCodeWorkerCommandIsPrivate(t *testing.T) {
	projectRoot := t.TempDir()
	now := time.Now()
	deps := testDependencies(t.TempDir(), projectRoot, &now)
	command := newCodeCommand(deps)
	found, _, err := command.Find([]string{"_worker"})
	if err != nil || found == nil || !found.Hidden {
		t.Fatalf("Find(_worker) = %#v, %v", found, err)
	}
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "_worker") {
		t.Fatalf("public code help exposed private worker: %q", output.String())
	}
}

func TestCodeWorkerCommandKeepsInitializationCauseOffStderr(t *testing.T) {
	projectRoot := t.TempDir()
	now := time.Now()
	deps := testDependencies(t.TempDir(), projectRoot, &now)
	deps.workerBuildID = func() (string, error) {
		return "", errors.New("secret path /private/worker-build")
	}
	command := newCodeCommand(deps)
	var stderr bytes.Buffer
	command.SetOut(io.Discard)
	command.SetErr(&stderr)
	command.SetArgs([]string{"_worker"})
	err := command.Execute()
	if err == nil || err.Error() != "coding worker terminated" {
		t.Fatalf("worker initialization error = %v", err)
	}
	if strings.Contains(stderr.String(), "/private/worker-build") {
		t.Fatalf("worker stderr leaked initialization cause: %q", stderr.String())
	}
}

func nativeWorkerBinding(
	project thread.ProjectIdentity,
	openMode worker.ThreadOpenMode,
	mode worker.TaskMode,
) worker.Binding {
	return worker.Binding{
		TaskID:                "task-1",
		TaskGenerationID:      "task-generation-1",
		WorkerGenerationID:    "worker-generation-1",
		ThreadID:              uuid.NewString(),
		ThreadOpenMode:        openMode,
		Project:               project,
		ExecutionRoot:         project.ProjectRoot,
		ExecutionRootIdentity: worker.ExecutionRootIdentity(project.ProjectRoot),
		Mode:                  mode,
		ProviderProfile:       nativeWorkerProviderProfile,
		Model:                 "fixture-alias",
		Provider:              "fixture",
		ExpectedWorkerBuildID: nativeWorkerTestBuildID,
	}
}
