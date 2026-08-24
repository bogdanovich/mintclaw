package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type admissionGenerationProvider struct {
	mockProvider
	calls atomic.Int32
}

func (provider *admissionGenerationProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls.Add(1)
	return provider.mockProvider.Chat(ctx, messages, tools, model, opts)
}

func TestAcquireAgentTurnSerializesConfiguredAgent(t *testing.T) {
	al := &AgentLoop{
		turns: &turnRuntime{
			admissions: &agentTurnAdmissionController{
				limits:  map[string]int{"browser": 1},
				active:  make(map[string]int),
				changed: make(chan struct{}),
			},
		},
	}

	firstCtx, releaseFirst, err := al.turns.acquireAgentTurn(context.Background(), "browser")
	if err != nil {
		t.Fatalf("first acquireAgentTurn() error = %v", err)
	}
	defer releaseFirst()

	// Nested turns inherit the admission and must not deadlock on the same agent.
	_, releaseNested, err := al.turns.acquireAgentTurn(firstCtx, "browser")
	if err != nil {
		t.Fatalf("nested acquireAgentTurn() error = %v", err)
	}
	releaseNested()

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = al.turns.acquireAgentTurn(waitCtx, "browser")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquireAgentTurn() error = %v, want deadline exceeded", err)
	}
}

func TestAcquireAgentTurnAllowsUnconfiguredAgent(t *testing.T) {
	al := &AgentLoop{
		turns: &turnRuntime{
			admissions: &agentTurnAdmissionController{
				limits:  map[string]int{"browser": 1},
				active:  make(map[string]int),
				changed: make(chan struct{}),
			},
		},
	}

	_, release, err := al.turns.acquireAgentTurn(context.Background(), "main")
	if err != nil {
		t.Fatalf("acquireAgentTurn() error = %v", err)
	}
	release()
}

func TestAgentTurnAdmissionDoesNotGrantAfterDeadlineDuringWaitCallback(t *testing.T) {
	controller := &agentTurnAdmissionController{
		limits:  map[string]int{"browser": 1},
		active:  make(map[string]int),
		changed: make(chan struct{}),
	}
	releaseBusy, err := controller.acquire(t.Context(), "browser")
	if err != nil {
		t.Fatalf("initial acquire() error = %v", err)
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelWait()
	result := make(chan error, 1)
	go func() {
		_, acquireErr := controller.acquireObserved(waitCtx, "browser", func(_, _ int) {
			close(callbackStarted)
			<-releaseCallback
		})
		result <- acquireErr
	}()

	<-callbackStarted
	releaseBusy()
	<-waitCtx.Done()
	close(releaseCallback)
	if err = <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireObserved() error = %v, want deadline exceeded", err)
	}

	nextCtx, cancelNext := context.WithTimeout(t.Context(), time.Second)
	defer cancelNext()
	releaseNext, err := controller.acquire(nextCtx, "browser")
	if err != nil {
		t.Fatalf("capacity leaked after expired acquire: %v", err)
	}
	releaseNext()
}

func TestAgentTurnAdmissionReloadPreservesActiveTurns(t *testing.T) {
	controller := &agentTurnAdmissionController{
		limits:  make(map[string]int),
		active:  make(map[string]int),
		changed: make(chan struct{}),
	}
	release, err := controller.acquire(context.Background(), "browser")
	if err != nil {
		t.Fatalf("initial acquire() error = %v", err)
	}

	controller.update(&AgentRegistry{agents: map[string]*AgentInstance{
		"browser": {ID: "browser", MaxParallelTurns: 1},
	}})

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = controller.acquire(waitCtx, "browser")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire() after reload error = %v, want deadline exceeded", err)
	}

	release()
	nextRelease, err := controller.acquire(context.Background(), "browser")
	if err != nil {
		t.Fatalf("acquire() after release error = %v", err)
	}
	nextRelease()
}

func TestQuiesceTurnsDrainsActiveAndBlocksNewAdmissions(t *testing.T) {
	controller := newAgentTurnAdmissionController(nil)
	loop := &AgentLoop{turns: &turnRuntime{admissions: controller}}
	releaseActive, err := controller.acquire(context.Background(), "main")
	if err != nil {
		t.Fatalf("initial acquire() error = %v", err)
	}

	quiesced := make(chan func(), 1)
	go func() {
		resume, quiesceErr := loop.QuiesceTurns(context.Background())
		if quiesceErr != nil {
			quiesced <- nil
			return
		}
		quiesced <- resume
	}()
	select {
	case <-quiesced:
		t.Fatal("QuiesceTurns returned before the active turn drained")
	case <-time.After(20 * time.Millisecond):
	}
	releaseActive()

	var resume func()
	select {
	case resume = <-quiesced:
		if resume == nil {
			t.Fatal("QuiesceTurns returned an error")
		}
	case <-time.After(time.Second):
		t.Fatal("QuiesceTurns did not finish after the active turn drained")
	}

	admitted := make(chan struct{})
	go func() {
		release, acquireErr := controller.acquire(context.Background(), "main")
		if acquireErr == nil {
			release()
		}
		close(admitted)
	}()
	select {
	case <-admitted:
		t.Fatal("new turn was admitted while reload was quiesced")
	case <-time.After(20 * time.Millisecond):
	}
	resume()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("new turn was not admitted after reload resumed")
	}
}

func TestQuiesceTurnsCancellationRestoresAdmissions(t *testing.T) {
	t.Parallel()

	controller := newAgentTurnAdmissionController(nil)
	release, err := controller.acquire(context.Background(), "default")
	if err != nil {
		t.Fatalf("acquire() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = controller.pause(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause() error = %v, want context.Canceled", err)
	}
	release()

	releaseAfterCancel, err := controller.acquire(context.Background(), "default")
	if err != nil {
		t.Fatalf("acquire() after canceled pause error = %v", err)
	}
	releaseAfterCancel()
}

func TestQuiesceTurnsRequiresEveryPauseToResume(t *testing.T) {
	t.Parallel()

	controller := newAgentTurnAdmissionController(nil)
	firstResume, err := controller.pause(context.Background())
	if err != nil {
		t.Fatalf("first pause() error = %v", err)
	}
	secondResume, err := controller.pause(context.Background())
	if err != nil {
		t.Fatalf("second pause() error = %v", err)
	}
	firstResume()

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = controller.acquire(waitCtx, "default"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire() after one resume error = %v, want deadline exceeded", err)
	}
	secondResume()
	release, err := controller.acquire(context.Background(), "default")
	if err != nil {
		t.Fatalf("acquire() after every resume error = %v", err)
	}
	release()
}

func TestTurnWaitingBehindReloadUsesCurrentAgentGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	oldProvider := &admissionGenerationProvider{}
	loop := NewAgentLoop(cfg, msgBus, oldProvider)
	t.Cleanup(loop.Close)
	oldAgent := loop.GetRegistry().GetDefaultAgent()

	resume, err := loop.QuiesceTurns(context.Background())
	if err != nil {
		t.Fatalf("QuiesceTurns() error = %v", err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, turnErr := loop.runAgentLoop(context.Background(), oldAgent, turnSpec{
			Dispatch: DispatchRequest{
				SessionKey:  "generation-waiter",
				UserMessage: "use the current generation",
			},
			NoHistory:       true,
			DefaultResponse: defaultResponse,
		})
		turnDone <- turnErr
	}()
	select {
	case err = <-turnDone:
		t.Fatalf("runAgentLoop() returned while turns were quiesced: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	next := *cfg
	newProvider := &admissionGenerationProvider{}
	prepared, err := loop.PrepareConfigReload(context.Background(), newProvider, &next)
	if err != nil {
		t.Fatalf("PrepareConfigReload() error = %v", err)
	}
	if err = prepared.Commit(context.Background()); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	resume()
	select {
	case err = <-turnDone:
		if err != nil {
			t.Fatalf("runAgentLoop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runAgentLoop() did not resume after reload")
	}
	if got := oldProvider.calls.Load(); got != 0 {
		t.Fatalf("old provider calls = %d, want 0", got)
	}
	if got := newProvider.calls.Load(); got != 1 {
		t.Fatalf("new provider calls = %d, want 1", got)
	}
}

func TestReloadProviderAndConfigRefreshesAgentTurnAdmissions(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "browser", Default: true}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer al.Close()

	_, release, err := al.turns.acquireAgentTurn(context.Background(), "browser")
	if err != nil {
		t.Fatalf("initial acquireAgentTurn() error = %v", err)
	}

	reloaded := config.DefaultConfig()
	reloaded.Agents.Defaults.Workspace = cfg.Agents.Defaults.Workspace
	reloaded.Agents.Defaults.ContextManager = "none"
	reloaded.Agents.List = []config.AgentConfig{{
		ID:               "browser",
		Default:          true,
		MaxParallelTurns: 1,
	}}
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, reloaded)
	}()
	select {
	case err = <-reloadDone:
		t.Fatalf("ReloadProviderAndConfig() returned before the active turn drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err = <-reloadDone; err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}

	_, release, err = al.turns.acquireAgentTurn(context.Background(), "browser")
	if err != nil {
		t.Fatalf("first acquireAgentTurn() after reload error = %v", err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = al.turns.acquireAgentTurn(waitCtx, "browser")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireAgentTurn() after reload error = %v, want deadline exceeded", err)
	}
}

func TestInheritAgentTurnAdmissionsDetachesCancellation(t *testing.T) {
	released := false
	lease := newAgentTurnAdmissionLease(func() { released = true })
	source, cancel := context.WithCancel(context.WithValue(
		context.Background(),
		agentTurnAdmissionsKey{},
		map[string]*agentTurnAdmissionLease{"browser": lease},
	))
	detached, releaseDetached := inheritAgentTurnAdmissions(context.Background(), source)
	cancel()

	if err := detached.Err(); err != nil {
		t.Fatalf("detached context error = %v", err)
	}
	admissions, ok := detached.Value(agentTurnAdmissionsKey{}).(map[string]*agentTurnAdmissionLease)
	if !ok {
		t.Fatal("detached context has no admissions")
	}
	if admissions["browser"] != lease {
		t.Fatal("detached context did not inherit browser admission")
	}
	lease.releaseRef()
	if released {
		t.Fatal("root release released controller while detached child retained lease")
	}
	releaseDetached()
	if !released {
		t.Fatal("detached release did not release controller after root completed")
	}
}

func TestInheritedAdmissionsAllowAgentRoundTrip(t *testing.T) {
	al := &AgentLoop{turns: &turnRuntime{admissions: &agentTurnAdmissionController{
		limits:  map[string]int{"agent-a": 1, "agent-b": 1},
		active:  make(map[string]int),
		changed: make(chan struct{}),
	}}}
	aCtx, releaseA, err := al.turns.acquireAgentTurn(context.Background(), "agent-a")
	if err != nil {
		t.Fatalf("acquire agent-a error = %v", err)
	}
	defer releaseA()

	bBaseCtx, releaseAncestors := inheritAgentTurnAdmissions(context.Background(), aCtx)
	defer releaseAncestors()
	bCtx, releaseB, err := al.turns.acquireAgentTurn(bBaseCtx, "agent-b")
	if err != nil {
		t.Fatalf("acquire agent-b error = %v", err)
	}
	defer releaseB()

	nestedBaseCtx, releaseNestedAncestors := inheritAgentTurnAdmissions(context.Background(), bCtx)
	defer releaseNestedAncestors()
	_, releaseNestedA, err := al.turns.acquireAgentTurn(nestedBaseCtx, "agent-a")
	if err != nil {
		t.Fatalf("reacquire inherited agent-a error = %v", err)
	}
	releaseNestedA()
}
