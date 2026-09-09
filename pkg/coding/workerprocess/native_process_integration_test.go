//go:build integration && (linux || darwin)

package workerprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const (
	nativeWorkerBinaryEnvironment  = "MINTCLAW_CODING_WORKER_TEST_BINARY"
	requireNativeWorkerEnvironment = "MINTCLAW_REQUIRE_CODING_WORKER_E2E"
	nativeWorkerModelAlias         = "worker-e2e-model"
)

func TestNativeMintClawWorkerStartsSteersResumesAndShutsDown(t *testing.T) {
	fixture := newNativeWorkerFixture(t)
	binding := fixture.binding(worker.ThreadOpenNew, "worker-generation-1")
	process := fixture.launch(t, binding)
	t.Cleanup(func() { _ = process.Close() })
	waitForProcessEvent(t, process, worker.EventWorkerReady)

	const initialPrompt = "inspect the repository through the native worker"
	if err := process.StartTurn(t.Context(), "turn-start-1", initialPrompt, nil); err != nil {
		t.Fatal(err)
	}
	first := fixture.provider.next(t)
	snapshot, err := process.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ControlIdentity != binding.ControlIdentity() || snapshot.Snapshot.ActiveTurnID == "" {
		t.Fatalf("active worker snapshot = %#v", snapshot)
	}

	const steering = "focus the answer on the top-level files"
	if err = process.Steer(t.Context(), "steer-1", steering, nil); err != nil {
		t.Fatal(err)
	}
	if err = process.Interrupt(t.Context(), "interrupt-1"); err != nil {
		t.Fatal(err)
	}
	first.respond(t, openAIToolCallResponse(
		"I will inspect the top-level files.",
		"list-root",
		"list_dir",
		`{"path":"."}`,
	))
	second := fixture.provider.next(t)
	second.requireMessage(t, initialPrompt)
	second.requireMessage(t, steering)
	second.requireMessage(t, "finish the current work and summarize")
	second.respond(t, openAITextResponse("native worker inspection complete"))

	result := waitForNativeWorkerResult(t, process)
	if result.Outcome() != OutcomeCompleted || result.WorkerStop == nil ||
		result.ProcessError != nil || result.ClientError != nil {
		t.Fatalf("completed native worker result = %#v", result)
	}
	fixture.requireLeaseAvailable(t)

	resumedBinding := binding
	resumedBinding.ThreadOpenMode = worker.ThreadOpenResume
	resumedBinding.WorkerGenerationID = "worker-generation-2"
	resumed := fixture.launch(t, resumedBinding)
	t.Cleanup(func() { _ = resumed.Close() })
	if err = resumed.StartTurn(t.Context(), "turn-start-2", "summarize the previous result", nil); err != nil {
		t.Fatal(err)
	}
	resumedRequest := fixture.provider.next(t)
	resumedRequest.requireMessage(t, initialPrompt)
	resumedRequest.requireMessage(t, "native worker inspection complete")
	resumedRequest.requireMessage(t, "summarize the previous result")
	resumedRequest.respond(t, openAITextResponse("resumed native thread complete"))
	resumedResult := waitForNativeWorkerResult(t, resumed)
	if resumedResult.Outcome() != OutcomeCompleted || resumedResult.WorkerStop == nil ||
		resumedResult.ProcessError != nil || resumedResult.ClientError != nil {
		t.Fatalf("resumed native worker result = %#v", resumedResult)
	}

	shutdownBinding := resumedBinding
	shutdownBinding.WorkerGenerationID = "worker-generation-3"
	shutdown := fixture.launch(t, shutdownBinding)
	t.Cleanup(func() { _ = shutdown.Close() })
	if err = shutdown.Shutdown(t.Context(), "shutdown-1"); err != nil {
		t.Fatal(err)
	}
	shutdownResult := waitForNativeWorkerResult(t, shutdown)
	if shutdownResult.Outcome() != OutcomeShutdown || shutdownResult.WorkerStop == nil ||
		shutdownResult.ProcessError != nil || shutdownResult.ClientError != nil {
		t.Fatalf("shutdown native worker result = %#v", shutdownResult)
	}
	fixture.provider.requireCallCount(t, 3)
	fixture.requireLeaseAvailable(t)
}

func TestNativeMintClawWorkerCrashReleasesLeaseWithoutBlindReplay(t *testing.T) {
	fixture := newNativeWorkerFixture(t)
	binding := fixture.binding(worker.ThreadOpenNew, "worker-generation-crashed")
	process := fixture.launch(t, binding)
	t.Cleanup(func() { _ = process.Close() })

	const acceptedPrompt = "perform this accepted investigation exactly once"
	if err := process.StartTurn(t.Context(), "turn-start-crashed", acceptedPrompt, nil); err != nil {
		t.Fatal(err)
	}
	crashedRequest := fixture.provider.next(t)
	store := fixture.store(t)
	if lease, err := store.AcquireLease(binding.ThreadID); !errors.Is(err, thread.ErrLeaseBusy) {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatalf("lease while worker is live = %#v, %v; want %v", lease, err, thread.ErrLeaseBusy)
	}

	contenderBinding := binding
	contenderBinding.ThreadOpenMode = worker.ThreadOpenResume
	contenderBinding.WorkerGenerationID = "worker-generation-live-contender"
	if contender, err := fixture.launcher.Launch(t.Context(), contenderBinding); err == nil {
		_ = contender.Close()
		t.Fatal("a successor worker acquired the live generation's thread lease")
	}

	if err := process.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	crashedResult := waitForNativeWorkerResult(t, process)
	if crashedResult.Outcome() != OutcomeUncertain || crashedResult.WorkerStop != nil ||
		crashedResult.ProcessError == nil || !errors.Is(crashedResult.ClientError, worker.ErrWorkerDisconnected) {
		t.Fatalf("crashed native worker result = %#v", crashedResult)
	}
	crashedRequest.waitDone(t)

	successorBinding := contenderBinding
	successorBinding.WorkerGenerationID = "worker-generation-successor"
	successor := fixture.launch(t, successorBinding)
	t.Cleanup(func() { _ = successor.Close() })
	fixture.provider.requireNoCall(t, 250*time.Millisecond)

	const recoveryPrompt = "inspect the retained state without repeating the lost request"
	if err := successor.StartTurn(t.Context(), "turn-start-successor", recoveryPrompt, nil); err != nil {
		t.Fatal(err)
	}
	recoveryRequest := fixture.provider.next(t)
	recoveryRequest.requireMessageCount(t, acceptedPrompt, 1)
	recoveryRequest.requireMessageCount(t, recoveryPrompt, 1)
	recoveryRequest.respond(t, openAITextResponse("retained state inspected without replay"))
	recoveryResult := waitForNativeWorkerResult(t, successor)
	if recoveryResult.Outcome() != OutcomeCompleted {
		t.Fatalf("successor native worker result = %#v", recoveryResult)
	}
	fixture.provider.requireCallCount(t, 2)
	fixture.requireLeaseAvailable(t)
}

func TestNativeMintClawWorkerDisconnectAndHardCancelAreExplicit(t *testing.T) {
	t.Run("parent disconnect", func(t *testing.T) {
		fixture := newNativeWorkerFixture(t)
		process := fixture.launch(t, fixture.binding(worker.ThreadOpenNew, "worker-generation-disconnect"))
		if err := process.client.Close(); err != nil {
			t.Fatal(err)
		}
		result := waitForNativeWorkerResult(t, process)
		if result.Outcome() != OutcomeUncertain || result.WorkerStop != nil ||
			result.ProcessError == nil || !errors.Is(result.ClientError, worker.ErrClientClosed) {
			t.Fatalf("disconnected native worker result = %#v", result)
		}
		fixture.requireLeaseAvailable(t)
	})

	t.Run("hard cancel", func(t *testing.T) {
		fixture := newNativeWorkerFixture(t)
		process := fixture.launch(t, fixture.binding(worker.ThreadOpenNew, "worker-generation-canceled"))
		t.Cleanup(func() { _ = process.Close() })
		if err := process.StartTurn(t.Context(), "turn-start-canceled", "wait for cancellation", nil); err != nil {
			t.Fatal(err)
		}
		request := fixture.provider.next(t)
		if err := process.HardCancel(t.Context(), "hard-cancel-1"); err != nil {
			t.Fatal(err)
		}
		result := waitForNativeWorkerResult(t, process)
		if result.Outcome() != OutcomeInterrupted || result.WorkerStop == nil {
			t.Fatalf("hard-canceled native worker result = %#v", result)
		}
		request.waitDone(t)
		fixture.provider.requireCallCount(t, 1)
		fixture.requireLeaseAvailable(t)
	})

	t.Run("hard cancel racing process loss", func(t *testing.T) {
		fixture := newNativeWorkerFixture(t)
		process := fixture.launch(t, fixture.binding(worker.ThreadOpenNew, "worker-generation-cancel-race"))
		t.Cleanup(func() { _ = process.Close() })
		if err := process.StartTurn(
			t.Context(),
			"turn-start-cancel-race",
			"race cancellation with loss",
			nil,
		); err != nil {
			t.Fatal(err)
		}
		request := fixture.provider.next(t)
		cancelCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cancelDone := make(chan error, 1)
		go func() { cancelDone <- process.HardCancel(cancelCtx, "hard-cancel-race-1") }()
		if err := process.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatal(err)
		}
		cancelErr := <-cancelDone
		if cancelErr != nil && !errors.Is(cancelErr, worker.ErrControlStreamUncertain) &&
			!errors.Is(cancelErr, ErrProcessNotRunning) {
			t.Fatalf("hard cancel race error = %v", cancelErr)
		}
		result := waitForNativeWorkerResult(t, process)
		switch result.Outcome() {
		case OutcomeInterrupted:
			if result.WorkerStop == nil {
				t.Fatalf("known cancel race lacks worker stop: %#v", result)
			}
		case OutcomeUncertain:
			if result.WorkerStop != nil || result.ProcessError == nil || result.ClientError == nil {
				t.Fatalf("uncertain cancel race is not explicit: %#v", result)
			}
		default:
			t.Fatalf("cancel race outcome = %q: %#v", result.Outcome(), result)
		}
		request.waitDone(t)
		fixture.provider.requireCallCount(t, 1)
		fixture.requireLeaseAvailable(t)
	})
}

type nativeWorkerFixture struct {
	home     string
	project  thread.ProjectIdentity
	threadID string
	buildID  string
	launcher *Launcher
	provider *nativeWorkerProvider
}

func newNativeWorkerFixture(t *testing.T) *nativeWorkerFixture {
	t.Helper()
	binary := strings.TrimSpace(os.Getenv(nativeWorkerBinaryEnvironment))
	if binary == "" {
		if os.Getenv(requireNativeWorkerEnvironment) == "1" {
			t.Fatalf("%s is required", nativeWorkerBinaryEnvironment)
		}
		t.Skipf("set %s to a built mintclaw binary", nativeWorkerBinaryEnvironment)
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := worker.ExecutableBuildID(binary)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	projectRoot := t.TempDir()
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider := newNativeWorkerProvider(t)
	writeNativeWorkerConfig(t, home, provider.server.URL)
	environment := replaceEnvironment(os.Environ(), config.EnvHome, home)
	launcher, err := NewLauncher(LauncherConfig{
		ExecutablePath:    binary,
		ParentBuildID:     "mintclaw-worker-e2e-parent",
		Environment:       environment,
		InitializeTimeout: 15 * time.Second,
		StopTimeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &nativeWorkerFixture{
		home:     home,
		project:  project,
		threadID: uuid.NewString(),
		buildID:  buildID,
		launcher: launcher,
		provider: provider,
	}
}

func (fixture *nativeWorkerFixture) binding(
	openMode worker.ThreadOpenMode,
	workerGeneration string,
) worker.Binding {
	return worker.Binding{
		TaskID:                "task-native-worker-e2e",
		TaskGenerationID:      "task-generation-native-worker-e2e",
		WorkerGenerationID:    workerGeneration,
		ThreadID:              fixture.threadID,
		ThreadOpenMode:        openMode,
		Project:               fixture.project,
		ExecutionRoot:         fixture.project.ProjectRoot,
		ExecutionRootIdentity: worker.ExecutionRootIdentity(fixture.project.ProjectRoot),
		Mode:                  worker.TaskModeInvestigate,
		ProviderProfile:       "default",
		Model:                 nativeWorkerModelAlias,
		Provider:              "openai",
		ExpectedWorkerBuildID: fixture.buildID,
	}
}

func (fixture *nativeWorkerFixture) launch(t *testing.T, binding worker.Binding) *Process {
	t.Helper()
	process, err := fixture.launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func (fixture *nativeWorkerFixture) store(t *testing.T) *thread.Store {
	t.Helper()
	store, err := thread.NewStore(filepath.Join(fixture.home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (fixture *nativeWorkerFixture) requireLeaseAvailable(t *testing.T) {
	t.Helper()
	lease, err := fixture.store(t).AcquireLease(fixture.threadID)
	if err != nil {
		t.Fatalf("acquire released native worker lease: %v", err)
	}
	if err = lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func writeNativeWorkerConfig(t *testing.T, home string, providerURL string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = nativeWorkerModelAlias
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelFallbacks = nil
	cfg.Agents.Defaults.Routing = nil
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: nativeWorkerModelAlias,
		Provider:  "openai",
		Model:     "fixture-model",
		APIBase:   providerURL,
		Enabled:   true,
	}}
	if _, err := config.NewRepository(filepath.Join(home, "config.json")).Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func replaceEnvironment(environment []string, key string, value string) []string {
	prefix := key + "="
	replaced := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			replaced = append(replaced, entry)
		}
	}
	return append(replaced, prefix+value)
}

func waitForNativeWorkerResult(t *testing.T, process *Process) Result {
	t.Helper()
	result, err := process.Wait(testTimeoutContext(t, 15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type nativeWorkerProvider struct {
	server *httptest.Server
	calls  chan *nativeWorkerProviderCall

	mu    sync.Mutex
	count int
}

type nativeWorkerProviderCall struct {
	body     []byte
	response chan string
	done     chan struct{}
}

func newNativeWorkerProvider(t *testing.T) *nativeWorkerProvider {
	t.Helper()
	provider := &nativeWorkerProvider{calls: make(chan *nativeWorkerProviderCall, 8)}
	provider.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() { _ = request.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
		if err != nil {
			http.Error(writer, "read request", http.StatusBadRequest)
			return
		}
		call := &nativeWorkerProviderCall{
			body:     body,
			response: make(chan string, 1),
			done:     make(chan struct{}),
		}
		provider.mu.Lock()
		provider.count++
		provider.mu.Unlock()
		select {
		case provider.calls <- call:
		case <-request.Context().Done():
			close(call.done)
			return
		}
		defer close(call.done)
		select {
		case response := <-call.response:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, response)
		case <-request.Context().Done():
		}
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (provider *nativeWorkerProvider) next(t *testing.T) *nativeWorkerProviderCall {
	t.Helper()
	select {
	case call := <-provider.calls:
		return call
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for native worker provider request")
		return nil
	}
}

func (provider *nativeWorkerProvider) requireNoCall(t *testing.T, duration time.Duration) {
	t.Helper()
	select {
	case call := <-provider.calls:
		t.Fatalf("native worker blindly replayed a provider request: %s", call.body)
	case <-time.After(duration):
	}
}

func (provider *nativeWorkerProvider) requireCallCount(t *testing.T, want int) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.count != want {
		t.Fatalf("native worker provider calls = %d, want %d", provider.count, want)
	}
}

func (call *nativeWorkerProviderCall) respond(t *testing.T, response string) {
	t.Helper()
	select {
	case call.response <- response:
	case <-call.done:
		t.Fatal("native worker provider request ended before its scripted response")
	}
}

func (call *nativeWorkerProviderCall) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-call.done:
	case <-time.After(5 * time.Second):
		t.Fatal("native worker provider request remained active after process termination")
	}
}

func (call *nativeWorkerProviderCall) requireMessage(t *testing.T, content string) {
	t.Helper()
	if count := call.messageCount(t, content); count == 0 {
		t.Fatalf("provider request does not contain %q: %s", content, call.body)
	}
}

func (call *nativeWorkerProviderCall) requireMessageCount(t *testing.T, content string, want int) {
	t.Helper()
	if count := call.messageCount(t, content); count != want {
		t.Fatalf("provider message count for %q = %d, want %d: %s", content, count, want, call.body)
	}
}

func (call *nativeWorkerProviderCall) messageCount(t *testing.T, content string) int {
	t.Helper()
	var request struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(call.body, &request); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	count := 0
	for _, message := range request.Messages {
		var text string
		if err := json.Unmarshal(message.Content, &text); err == nil {
			if strings.Contains(text, content) {
				count++
			}
			continue
		}
		if strings.Contains(string(message.Content), content) {
			count++
		}
	}
	return count
}

func openAITextResponse(content string) string {
	encoded, _ := json.Marshal(content)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(encoded) +
		`},"finish_reason":"stop"}]}`
}

func openAIToolCallResponse(content string, id string, name string, arguments string) string {
	encodedContent, _ := json.Marshal(content)
	encodedID, _ := json.Marshal(id)
	encodedName, _ := json.Marshal(name)
	encodedArguments, _ := json.Marshal(arguments)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(encodedContent) +
		`,"tool_calls":[{"id":` + string(encodedID) +
		`,"type":"function","function":{"name":` + string(encodedName) +
		`,"arguments":` + string(encodedArguments) + `}}]},"finish_reason":"tool_calls"}]}`
}
