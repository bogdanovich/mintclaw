package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

type execTestController struct {
	projector    *frontend.Projector
	lease        *thread.Lease
	closeOnce    sync.Once
	failed       bool
	blocked      bool
	interrupt    chan struct{}
	interruptErr error
	awaitStarted chan struct{}
	awaitRelease chan struct{}
	awaitErr     error
}

func newExecTestController(request codingTurnRequest, failed, blocked bool) (*execTestController, error) {
	projector, err := frontend.NewProjector(request.Metadata.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		return nil, err
	}
	projector.Open(false)
	return &execTestController{
		projector: projector,
		lease:     request.Lease,
		failed:    failed,
		blocked:   blocked,
		interrupt: make(chan struct{}),
	}, nil
}

func (c *execTestController) Snapshot(ctx context.Context) (frontend.ThreadSnapshot, error) {
	return c.projector.Snapshot(ctx)
}

func (c *execTestController) Subscribe(
	ctx context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	return c.projector.Subscribe(ctx)
}

func (c *execTestController) Submit(_ context.Context, input frontend.TurnInput) error {
	const turnID = "turn-fixture"
	c.projector.TurnStarted(turnID, input.Text)
	if c.blocked {
		return nil
	}
	c.projector.ToolStarted(turnID, "call-1", "read_file", "path")
	c.projector.ToolCompleted(turnID, "call-1", "read_file", "fixture", time.Millisecond, c.failed, nil)
	if c.failed {
		c.projector.TurnFailed(turnID, "turn failed")
		return nil
	}
	c.projector.AssistantAccumulated(turnID, "fixture response", true)
	c.projector.TurnCompleted(turnID, "completed")
	return nil
}

func (c *execTestController) AwaitTurn(ctx context.Context) error {
	if c.awaitStarted != nil {
		select {
		case <-c.awaitStarted:
		default:
			close(c.awaitStarted)
		}
	}
	if c.awaitRelease != nil {
		select {
		case <-c.awaitRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.awaitErr != nil {
		return c.awaitErr
	}
	if c.failed {
		return errors.New("fixture turn failure")
	}
	return nil
}

func (c *execTestController) Interrupt(context.Context) error {
	select {
	case <-c.interrupt:
	default:
		close(c.interrupt)
	}
	if c.interruptErr != nil {
		return c.interruptErr
	}
	c.projector.TurnInterrupted("turn-fixture", "interrupted")
	return nil
}

func (c *execTestController) HardCancel(ctx context.Context) error {
	return c.Interrupt(ctx)
}

func (*execTestController) Compact(context.Context) error { return frontend.ErrCommandUnsupported }
func (*execTestController) Rename(context.Context, string) error {
	return frontend.ErrCommandUnsupported
}

func (*execTestController) SetArchived(context.Context, bool) error {
	return frontend.ErrCommandUnsupported
}
func (*execTestController) NewThread(context.Context) error { return frontend.ErrCommandUnsupported }

func (c *execTestController) Close(context.Context) (err error) {
	c.closeOnce.Do(func() {
		if c.lease != nil {
			err = c.lease.Release()
		}
	})
	return err
}

func TestCodeExecJSONLNewAndResumeUseOneDurableThread(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		return newExecTestController(request, false, false)
	}

	createdOutput := executeCommand(t, newCodeCommand(deps), "exec", "inspect", "this", "--json")
	created := decodeExecEvents(t, createdOutput)
	assertExecEventSequence(
		t,
		created,
		"thread.started",
		"turn.started",
		"item.completed",
		"item.completed",
		"item.completed",
		"turn.completed",
	)
	threadID := created[0].ThreadID
	if threadID == "" || created[0].Resumed == nil || *created[0].Resumed ||
		created[0].ProjectRoot == "" || created[0].SessionKey == "" {
		t.Fatalf("created thread event = %+v", created[0])
	}
	if created[3].Item == nil || created[3].Item.Kind != frontend.PresentationTurnSeparator ||
		created[3].Item.Turn == nil || created[3].Item.Turn.Outcome != frontend.TurnOutcomeCompleted ||
		created[4].Item == nil || created[4].Item.Kind != frontend.PresentationFinalAnswer {
		t.Fatalf("typed terminal items = %+v", created)
	}
	for _, event := range created {
		if event.SchemaVersion != ExecSchemaVersion {
			t.Fatalf("schema version = %d in %+v", event.SchemaVersion, event)
		}
	}

	now = now.Add(time.Minute)
	resumedOutput := executeCommand(
		t,
		newCodeCommand(deps),
		"exec",
		"resume",
		threadID,
		"continue",
		"--json",
	)
	resumed := decodeExecEvents(t, resumedOutput)
	assertExecEventSequence(
		t,
		resumed,
		"thread.started",
		"turn.started",
		"item.completed",
		"item.completed",
		"item.completed",
		"turn.completed",
	)
	if resumed[0].Resumed == nil || !*resumed[0].Resumed || resumed[0].ThreadID != threadID {
		t.Fatalf("resumed thread event = %+v", resumed[0])
	}

	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.UpdatedAt.Equal(now) {
		t.Fatalf("resume updated_at = %s, want %s", metadata.UpdatedAt, now)
	}
}

func TestCodeExecPlainWritesOnlyFinalAssistantResponse(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		return newExecTestController(request, false, false)
	}

	output := executeCommand(t, newCodeCommand(deps), "exec", "inspect this")
	if string(output) != "fixture response\n" {
		t.Fatalf("plain output = %q", output)
	}
	if bytes.Contains(output, []byte("\x1b[")) {
		t.Fatalf("plain output contains terminal control sequence: %q", output)
	}
}

func TestCodeExecFailedToolHasStableJSONLAndExitClassification(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.newController = func(request codingTurnRequest, _ bool) (frontend.Controller, error) {
		return newExecTestController(request, true, false)
	}

	command := newCodeCommand(deps)
	command.SilenceErrors = true
	command.SilenceUsage = true
	output, err := executeCommandError(command, "exec", "fail", "--json")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitCodeToolFailure || exitErr.Category != ExecFailureTool {
		t.Fatalf("error = %#v, want tool exit", err)
	}
	events := decodeExecEvents(t, output)
	assertExecEventSequence(
		t,
		events,
		"thread.started",
		"turn.started",
		"item.completed",
		"item.completed",
		"turn.failed",
	)
	last := events[len(events)-1]
	if last.Error == nil || last.Error.Category != ExecFailureTool {
		t.Fatalf("terminal event = %+v", last)
	}
}

func TestCodeExecInvalidProjectStillEmitsParseableJSONL(t *testing.T) {
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(t.TempDir(), t.TempDir(), &now)
	deps.cwd = func() (string, error) { return "", os.ErrNotExist }

	command := newCodeCommand(deps)
	command.SilenceErrors = true
	command.SilenceUsage = true
	output, err := executeCommandError(command, "exec", "inspect", "--json")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitCodeInvalidProject ||
		exitErr.Category != ExecFailureInvalidProject {
		t.Fatalf("error = %#v, want invalid-project exit", err)
	}
	events := decodeExecEvents(t, output)
	assertExecEventSequence(t, events, "error")
	if events[0].Error == nil || events[0].Error.Category != ExecFailureInvalidProject {
		t.Fatalf("error event = %+v", events[0])
	}
}

func TestExecuteControllerTurnInterruptsOnCancellation(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	controller := &execTestController{
		projector: projector,
		blocked:   true,
		interrupt: make(chan struct{}),
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	exitErr := executeControllerTurn(ctx, controller, renderer, frontend.TurnInput{Text: "stop"})
	if exitErr == nil || exitErr.Code != ExitCodeInterrupted {
		t.Fatalf("exit error = %#v", exitErr)
	}
	select {
	case <-controller.interrupt:
	default:
		t.Fatal("controller was not interrupted")
	}
	events := decodeExecEvents(t, output.Bytes())
	assertExecEventSequence(t, events, "turn.started", "turn.failed")
	if events[1].Error == nil || events[1].Error.Category != ExecFailureInterrupted ||
		events[1].TurnID != "turn-fixture" {
		t.Fatalf("interrupted terminal event = %+v", events[1])
	}
}

func TestExecuteControllerTurnReportsInterruptionWhenInterruptRequestFails(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	controller := &execTestController{
		projector: projector, blocked: true, interrupt: make(chan struct{}),
		interruptErr: errors.New("interrupt request failed"),
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	exitErr := executeControllerTurn(ctx, controller, renderer, frontend.TurnInput{Text: "stop"})
	if exitErr == nil || exitErr.Code != ExitCodeInterrupted {
		t.Fatalf("exit error = %#v", exitErr)
	}
	events := decodeExecEvents(t, output.Bytes())
	assertExecEventSequence(t, events, "turn.started", "turn.failed")
	if events[1].Error == nil || events[1].Error.Category != ExecFailureInterrupted {
		t.Fatalf("interrupted terminal event = %+v", events[1])
	}
}

func TestExecuteControllerTurnWaitsForSettlementBeforeSuccess(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	controller := &execTestController{
		projector:    projector,
		interrupt:    make(chan struct{}),
		awaitStarted: make(chan struct{}),
		awaitRelease: make(chan struct{}),
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	finished := make(chan *ExitError, 1)
	go func() {
		finished <- executeControllerTurn(
			t.Context(),
			controller,
			renderer,
			frontend.TurnInput{Text: "inspect"},
		)
	}()
	select {
	case <-controller.awaitStarted:
	case <-time.After(time.Second):
		t.Fatal("headless execution did not reach settlement barrier")
	}
	select {
	case result := <-finished:
		t.Fatalf("execution returned before settlement: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	close(controller.awaitRelease)
	select {
	case result := <-finished:
		if result != nil {
			t.Fatalf("settled execution error = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("execution did not return after settlement")
	}
	events := decodeExecEvents(t, output.Bytes())
	if events[len(events)-1].Type != "turn.completed" {
		t.Fatalf("terminal event = %+v", events[len(events)-1])
	}
}

func TestExecuteControllerTurnHonorsCancellationDuringSettlement(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	controller := &execTestController{
		projector:    projector,
		interrupt:    make(chan struct{}),
		awaitStarted: make(chan struct{}),
		awaitRelease: make(chan struct{}),
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan *ExitError, 1)
	go func() {
		finished <- executeControllerTurn(ctx, controller, renderer, frontend.TurnInput{Text: "inspect"})
	}()
	select {
	case <-controller.awaitStarted:
	case <-time.After(time.Second):
		t.Fatal("headless execution did not reach settlement barrier")
	}
	cancel()
	select {
	case <-controller.interrupt:
	case <-time.After(time.Second):
		t.Fatal("controller was not interrupted during settlement")
	}
	close(controller.awaitRelease)
	select {
	case result := <-finished:
		if result == nil || result.Code != ExitCodeInterrupted {
			t.Fatalf("settled cancellation result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("execution did not return after interrupted settlement")
	}
	events := decodeExecEvents(t, output.Bytes())
	if events[len(events)-1].Type != "turn.failed" ||
		events[len(events)-1].Error.Category != ExecFailureInterrupted {
		t.Fatalf("terminal event = %+v", events[len(events)-1])
	}
	for _, event := range events {
		if event.Type == "turn.completed" {
			t.Fatalf("settlement cancellation emitted success: %+v", events)
		}
	}
}

func TestExecuteControllerTurnSettlementErrorReplacesProjectedSuccess(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	controller := &execTestController{
		projector: projector,
		interrupt: make(chan struct{}),
		awaitErr:  errors.New("post-turn persistence failed"),
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	exitErr := executeControllerTurn(
		t.Context(),
		controller,
		renderer,
		frontend.TurnInput{Text: "inspect"},
	)
	if exitErr == nil || exitErr.Code != ExitCodeModelFailure {
		t.Fatalf("settlement error = %#v", exitErr)
	}
	events := decodeExecEvents(t, output.Bytes())
	if events[len(events)-1].Type != "turn.failed" {
		t.Fatalf("terminal event = %+v", events[len(events)-1])
	}
	for _, event := range events {
		if event.Type == "turn.completed" {
			t.Fatalf("settlement failure emitted success: %+v", events)
		}
	}
}

func TestCodeExecResumeMissingIDUsesJSONLErrorContract(t *testing.T) {
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(t.TempDir(), t.TempDir(), &now)
	command := newCodeCommand(deps)
	command.SilenceErrors = true
	command.SilenceUsage = true
	output, err := executeCommandError(command, "exec", "resume", "--json")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitCodeModelFailure {
		t.Fatalf("error = %#v, want classified exec error", err)
	}
	events := decodeExecEvents(t, output)
	assertExecEventSequence(t, events, "error")
}

func TestClassifyExecErrorDistinguishesContextFailure(t *testing.T) {
	err := &providererrors.ProviderError{Kind: providererrors.KindContextOverflow, SafeMessage: "too large"}
	exitErr := classifyExecError(err, nil)
	if exitErr.Code != ExitCodeContextFailure || exitErr.Category != ExecFailureContext {
		t.Fatalf("exit error = %+v", exitErr)
	}
}

func TestExecRendererEmitsRevisionSafeItemLifecycle(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.Open(false)
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	projector.TurnStarted("turn-fixture", "inspect")
	projector.ToolStarted("turn-fixture", "call-1", "exec", "cmd")
	first, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.observeItems(first); err != nil {
		t.Fatalf("first observe = %v", err)
	}
	projector.ToolOutput("turn-fixture", "call-1", "running")
	second, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.observeItems(second); err != nil {
		t.Fatalf("second observe = %v", err)
	}
	if err := renderer.observeItems(second); err != nil {
		t.Fatalf("duplicate observe = %v", err)
	}
	projector.ToolCompleted("turn-fixture", "call-1", "exec", "done", time.Millisecond, false, nil)
	third, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.observeItems(third); err != nil {
		t.Fatalf("third observe = %v", err)
	}
	events := decodeExecEvents(t, output.Bytes())
	assertExecEventSequence(t, events, "item.started", "item.updated", "item.completed")
	if events[0].Item == nil || events[1].Item == nil || events[2].Item == nil ||
		events[0].Item.Revision >= events[1].Item.Revision ||
		events[1].Item.Revision >= events[2].Item.Revision {
		t.Fatalf("item revisions are not monotonic: %+v", events)
	}
}

func TestExecRendererEmitsWorkBoundaryBeforeDeferredFinal(t *testing.T) {
	projector, err := frontend.NewProjector("thread-fixture", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	renderer := &execRenderer{
		out: &output, json: true, threadID: "thread-fixture", seenRevisions: make(map[string]uint64),
	}
	projector.TurnStarted("turn-fixture", "inspect")
	projector.ToolStarted("turn-fixture", "call-1", "read_file", "")
	projector.ToolCompleted("turn-fixture", "call-1", "read_file", "", time.Millisecond, false, nil)
	if err = renderer.observeItems(snapshotForExecTest(t, projector)); err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-fixture", "fixture", false)
	if err = renderer.observeItems(snapshotForExecTest(t, projector)); err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-fixture", "fixture response", true)
	if err = renderer.observeItems(snapshotForExecTest(t, projector)); err != nil {
		t.Fatal(err)
	}
	projector.TurnCompleted("turn-fixture", "completed")
	if err = renderer.observeItems(snapshotForExecTest(t, projector)); err != nil {
		t.Fatal(err)
	}

	events := decodeExecEvents(t, output.Bytes())
	if len(events) != 3 || events[0].Item == nil || events[0].Item.Kind != frontend.PresentationToolCall ||
		events[1].Item == nil || events[1].Item.Kind != frontend.PresentationTurnSeparator ||
		events[2].Item == nil || events[2].Item.Kind != frontend.PresentationFinalAnswer {
		t.Fatalf("exec item order = %+v", events)
	}
}

func snapshotForExecTest(t *testing.T, projector *frontend.Projector) frontend.ThreadSnapshot {
	t.Helper()
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func decodeExecEvents(t *testing.T, output []byte) []execEvent {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	var events []execEvent
	for {
		var event execEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode JSONL: %v\n%s", err, output)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatalf("no JSONL events in %q", output)
	}
	return events
}

func assertExecEventSequence(t *testing.T, events []execEvent, want ...string) {
	t.Helper()
	got := make([]string, len(events))
	for index := range events {
		got[index] = events[index].Type
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", got, want)
	}
}
