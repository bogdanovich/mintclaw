package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

const (
	ExecSchemaVersion = 1

	ExitCodeModelFailure   = 1
	ExitCodeInvalidProject = 2
	ExitCodeToolFailure    = 3
	ExitCodeContextFailure = 4
	ExitCodeSuspended      = 6
	ExitCodeInterrupted    = 130

	headlessExecCloseTimeout      = 10 * time.Second
	headlessExecInterruptTimeout  = 10 * time.Second
	headlessExecSettlementTimeout = 10 * time.Second
)

type ExecFailureCategory string

const (
	ExecFailureModel          ExecFailureCategory = "model_failure"
	ExecFailureInvalidProject ExecFailureCategory = "invalid_project"
	ExecFailureTool           ExecFailureCategory = "tool_failure"
	ExecFailureContext        ExecFailureCategory = "context_failure"
	ExecFailureSuspended      ExecFailureCategory = "suspended"
	ExecFailureInterrupted    ExecFailureCategory = "interrupted"
)

// ExitError gives the process entry point a stable exit status while retaining
// the inspectable underlying coding error for ordinary CLI rendering.
type ExitError struct {
	Code     int
	Category ExecFailureCategory
	Err      error
	reported bool
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return "coding execution failed"
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type execOptions struct {
	model       string
	attachments []string
	json        bool
	resume      bool
	threadID    string
}

type execEvent struct {
	SchemaVersion int                    `json:"schema_version"`
	Type          string                 `json:"type"`
	ThreadID      string                 `json:"thread_id,omitempty"`
	SessionKey    string                 `json:"session_key,omitempty"`
	ProjectRoot   string                 `json:"project_root,omitempty"`
	InvocationCWD string                 `json:"invocation_cwd,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Provider      string                 `json:"provider,omitempty"`
	Resumed       *bool                  `json:"resumed,omitempty"`
	TurnID        string                 `json:"turn_id,omitempty"`
	Item          *execItem              `json:"item,omitempty"`
	Usage         *frontend.ContextUsage `json:"usage,omitempty"`
	Error         *execEventError        `json:"error,omitempty"`
}

type execEventError struct {
	Category ExecFailureCategory `json:"category"`
	Message  string              `json:"message"`
}

type execItem struct {
	ID         string                         `json:"id"`
	Revision   uint64                         `json:"revision"`
	Kind       frontend.PresentationKind      `json:"type"`
	Status     frontend.PresentationLifecycle `json:"status"`
	Text       string                         `json:"text,omitempty"`
	Truncated  bool                           `json:"truncated,omitempty"`
	DurationMS int64                          `json:"duration_ms,omitempty"`
	Tool       *frontend.ToolState            `json:"tool,omitempty"`
	Plan       *frontend.PlanState            `json:"plan,omitempty"`
	Compaction *frontend.CompactionState      `json:"compaction,omitempty"`
	Turn       *frontend.TurnBoundaryState    `json:"turn,omitempty"`
}

type execRenderer struct {
	out           io.Writer
	json          bool
	threadID      string
	seenRevisions map[string]uint64
	lastAssistant string
	turnID        string
}

func newCodeExecCommand(deps dependencies) *cobra.Command {
	var options execOptions
	cmd := &cobra.Command{
		Use:   "exec [prompt]",
		Short: "Run a coding turn without the terminal UI",
		Long: "Run one durable coding turn for the current project. Plain mode prints the final response; " +
			"--json emits schema-versioned JSONL events and no terminal control sequences.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodeExecCommand(cmd, deps, options, strings.Join(args, " "))
		},
	}
	cmd.Flags().StringVar(&options.model, "model", "", "Persist a model override for this thread")
	cmd.Flags().StringArrayVar(&options.attachments, "attach", nil, "Attach a local file (repeatable)")
	cmd.Flags().BoolVar(&options.json, "json", false, "Emit schema-versioned JSONL events")

	var resumed execOptions
	resumed.resume = true
	resume := &cobra.Command{
		Use:   "resume <thread-id> [prompt]",
		Short: "Run a turn on an existing coding thread without the terminal UI",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return renderClassifiedExecError(cmd.OutOrStdout(), resumed.json, &ExitError{
					Code: ExitCodeModelFailure, Category: ExecFailureModel,
					Err: errors.New("coding thread ID is required"),
				})
			}
			resumed.threadID = args[0]
			return runCodeExecCommand(cmd, deps, resumed, strings.Join(args[1:], " "))
		},
	}
	resume.Flags().StringVar(&resumed.model, "model", "", "Replace the persisted model override")
	resume.Flags().StringArrayVar(&resumed.attachments, "attach", nil, "Attach a local file (repeatable)")
	resume.Flags().BoolVar(&resumed.json, "json", false, "Emit schema-versioned JSONL events")
	cmd.AddCommand(resume)
	return cmd
}

func runCodeExecCommand(cmd *cobra.Command, deps dependencies, options execOptions, prompt string) error {
	input := turnInputFromPaths(prompt, options.attachments)
	if strings.TrimSpace(prompt) == "" && len(input.Attachments) == 0 {
		return renderClassifiedExecError(cmd.OutOrStdout(), options.json, &ExitError{
			Code: ExitCodeModelFailure, Category: ExecFailureModel,
			Err: errors.New("coding prompt or --attach is required"),
		})
	}
	execCtx, stopSignals := newExecSignalContext(cmd.Context())
	defer stopSignals()
	return runCodeExec(execCtx, cmd.OutOrStdout(), deps, options, input)
}

func runCodeExec(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	options execOptions,
	input frontend.TurnInput,
) error {
	deps = completeDependencies(deps)
	var store *thread.Store
	var metadata thread.Metadata
	var lease *thread.Lease
	var err error
	if options.resume {
		var project thread.ProjectIdentity
		project, store, err = resolveEnvironment(ctx, deps)
		if err != nil {
			return renderClassifiedExecError(out, options.json, classifyExecError(err, nil))
		}
		metadata, lease, err = prepareResumedThread(
			ctx,
			store,
			project,
			deps,
			options.threadID,
			resumeOptions{threadID: options.threadID, model: options.model},
			true,
		)
	} else {
		_, store, metadata, lease, err = prepareNewThread(
			ctx,
			deps,
			turnDisplayContent(input),
			options.model,
			false,
		)
	}
	if err != nil {
		return renderClassifiedExecError(out, options.json, classifyExecError(err, nil))
	}
	frontendController, err := deps.newController(codingTurnRequest{
		Store: store, Lease: lease, Metadata: metadata,
	}, options.resume)
	if err != nil {
		err = errors.Join(err, lease.Release())
		return renderClassifiedExecError(out, options.json, classifyExecError(err, nil))
	}
	renderer := &execRenderer{
		out: out, json: options.json, threadID: metadata.ThreadID, seenRevisions: make(map[string]uint64),
	}
	if err := renderer.threadStarted(metadata, options.resume); err != nil {
		return closeExecController(frontendController, err)
	}
	resultErr := executeControllerTurn(ctx, frontendController, renderer, input)
	closeErr := closeExecController(frontendController, nil)
	if resultErr == nil && closeErr != nil {
		resultErr = classifyExecError(closeErr, nil)
	} else if resultErr != nil && closeErr != nil {
		resultErr.Err = errors.Join(resultErr.Err, closeErr)
	}
	if resultErr != nil {
		return renderClassifiedExecError(out, options.json, resultErr)
	}
	return nil
}

func executeControllerTurn(
	ctx context.Context,
	frontendController frontend.Controller,
	renderer *execRenderer,
	input frontend.TurnInput,
) *ExitError {
	observeCtx, cancelObserve := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelObserve()
	snapshot, updates, err := frontendController.Subscribe(observeCtx)
	if err != nil {
		return classifyExecError(fmt.Errorf("code exec: subscribe: %w", err), nil)
	}
	if err := frontendController.Submit(ctx, input); err != nil {
		return classifyExecError(fmt.Errorf("code exec: submit: %w", err), &snapshot)
	}
	currentCtx, cancelCurrent := context.WithTimeout(context.WithoutCancel(ctx), headlessExecSettlementTimeout)
	current, currentErr := frontendController.Snapshot(currentCtx)
	cancelCurrent()
	if currentErr == nil {
		snapshot = current
	}
	renderer.captureTurnID(snapshot)
	if err := renderer.emit(execEvent{
		Type: "turn.started", ThreadID: renderer.threadID, TurnID: renderer.turnID,
	}); err != nil {
		return classifyExecError(fmt.Errorf("code exec: render turn start: %w", err), &snapshot)
	}
	if err := renderer.observeItems(snapshot); err != nil {
		return classifyExecError(err, &snapshot)
	}
	if snapshot.LastTurn != nil {
		return settleControllerTurn(ctx, frontendController, renderer, snapshot)
	}
	for {
		select {
		case next, ok := <-updates:
			if !ok {
				return classifyExecError(
					errors.New("code exec: observation ended before the turn completed"),
					&snapshot,
				)
			}
			snapshot = next
			if err := renderer.observeItems(snapshot); err != nil {
				return classifyExecError(err, &snapshot)
			}
			if snapshot.LastTurn != nil {
				return settleControllerTurn(ctx, frontendController, renderer, snapshot)
			}
		case <-ctx.Done():
			return interruptAndSettleControllerTurn(frontendController, renderer, snapshot, ctx.Err())
		}
	}
}

func settleControllerTurn(
	ctx context.Context,
	frontendController frontend.Controller,
	renderer *execRenderer,
	snapshot frontend.ThreadSnapshot,
) *ExitError {
	settler, ok := frontendController.(frontend.TurnSettler)
	if !ok {
		return classifyExecError(errors.New("code exec: controller cannot await turn settlement"), &snapshot)
	}
	settleCtx, cancel := context.WithTimeout(context.Background(), headlessExecSettlementTimeout)
	settled := make(chan error, 1)
	go func() { settled <- settler.AwaitTurn(settleCtx) }()
	var settlementErr error
	select {
	case settlementErr = <-settled:
		if err := ctx.Err(); err != nil {
			cancel()
			return interruptAndSettleControllerTurn(frontendController, renderer, snapshot, err)
		}
	case <-ctx.Done():
		cancel()
		return interruptAndSettleControllerTurn(frontendController, renderer, snapshot, ctx.Err())
	}
	finalSnapshot, snapshotErr := frontendController.Snapshot(settleCtx)
	cancel()
	if snapshotErr != nil {
		settlementErr = errors.Join(settlementErr, fmt.Errorf("code exec: read settled turn: %w", snapshotErr))
	} else {
		snapshot = finalSnapshot
	}
	if renderErr := renderer.observeItems(snapshot); renderErr != nil {
		settlementErr = errors.Join(settlementErr, renderErr)
	}
	terminal, renderErr := renderer.finishTurn(snapshot, settlementErr)
	if renderErr != nil {
		return classifyExecError(errors.Join(settlementErr, renderErr), &snapshot)
	}
	return terminal
}

func interruptAndSettleControllerTurn(
	frontendController frontend.Controller,
	renderer *execRenderer,
	snapshot frontend.ThreadSnapshot,
	interruptCause error,
) *ExitError {
	interruptCtx, cancel := context.WithTimeout(context.Background(), headlessExecInterruptTimeout)
	interruptErr := frontendController.Interrupt(interruptCtx)
	cancel()
	if errors.Is(interruptErr, controller.ErrNoActiveTurn) {
		interruptErr = nil
	}
	return settleInterruptedControllerTurn(
		frontendController,
		renderer,
		snapshot,
		errors.Join(interruptCause, interruptErr),
	)
}

func settleInterruptedControllerTurn(
	frontendController frontend.Controller,
	renderer *execRenderer,
	snapshot frontend.ThreadSnapshot,
	interruptCause error,
) *ExitError {
	settler, ok := frontendController.(frontend.TurnSettler)
	if !ok {
		failure := &ExitError{
			Code: ExitCodeInterrupted, Category: ExecFailureInterrupted, Err: interruptCause,
		}
		failure, _ = renderer.reportFailure(renderer.turnID, failure)
		return failure
	}
	settleCtx, cancel := context.WithTimeout(context.Background(), headlessExecSettlementTimeout)
	settlementErr := settler.AwaitTurn(settleCtx)
	finalSnapshot, snapshotErr := frontendController.Snapshot(settleCtx)
	cancel()
	if snapshotErr == nil {
		snapshot = finalSnapshot
	}
	if renderErr := renderer.observeItems(snapshot); renderErr != nil {
		settlementErr = errors.Join(settlementErr, renderErr)
	}
	failure := &ExitError{
		Code: ExitCodeInterrupted, Category: ExecFailureInterrupted,
		Err: errors.Join(interruptCause, settlementErr, snapshotErr),
	}
	failure, _ = renderer.reportFailure(renderer.turnID, failure)
	return failure
}

func closeExecController(frontendController frontend.Controller, prior error) error {
	closeCtx, cancel := context.WithTimeout(context.Background(), headlessExecCloseTimeout)
	defer cancel()
	return errors.Join(prior, frontendController.Close(closeCtx))
}

func (r *execRenderer) threadStarted(metadata thread.Metadata, resumed bool) error {
	resumedValue := resumed
	return r.emit(execEvent{
		Type:          "thread.started",
		ThreadID:      metadata.ThreadID,
		SessionKey:    metadata.SessionKey,
		ProjectRoot:   metadata.Project.ProjectRoot,
		InvocationCWD: metadata.Project.InvocationCWD,
		Model:         metadata.Model,
		Provider:      metadata.Provider,
		Resumed:       &resumedValue,
	})
}

func (r *execRenderer) captureTurnID(snapshot frontend.ThreadSnapshot) {
	if snapshot.LastTurn != nil && snapshot.LastTurn.TurnID != "" {
		r.turnID = snapshot.LastTurn.TurnID
		return
	}
	for index := range snapshot.Items {
		if snapshot.Items[index].TurnID != "" {
			r.turnID = snapshot.Items[index].TurnID
			return
		}
	}
}

func (r *execRenderer) observeItems(snapshot frontend.ThreadSnapshot) error {
	r.captureTurnID(snapshot)
	for index := range snapshot.Items {
		item := snapshot.Items[index]
		if item.Kind == frontend.PresentationUserMessage || r.seenRevisions[item.ID] >= item.Revision {
			continue
		}
		_, seen := r.seenRevisions[item.ID]
		r.seenRevisions[item.ID] = item.Revision
		eventType := "item.updated"
		if !seen {
			eventType = "item.started"
		}
		if item.Lifecycle != frontend.PresentationActive {
			eventType = "item.completed"
		}
		projected := projectExecItem(item)
		if item.Kind == frontend.PresentationFinalAnswer && item.Message != nil {
			r.lastAssistant = item.Message.Text
		}
		if err := r.emit(execEvent{
			Type: eventType, ThreadID: r.threadID, TurnID: item.TurnID, Item: &projected,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *execRenderer) finishTurn(
	snapshot frontend.ThreadSnapshot,
	settlementErr error,
) (*ExitError, error) {
	if snapshot.LastTurn == nil {
		failure := classifyExecError(
			errors.Join(settlementErr, errors.New("settled turn has no terminal snapshot")),
			&snapshot,
		)
		return r.reportFailure(r.turnID, failure)
	}
	turnID := snapshot.LastTurn.TurnID
	if settlementErr != nil {
		failure := classifyExecError(settlementErr, &snapshot)
		return r.reportFailure(turnID, failure)
	}
	switch snapshot.LastTurn.Outcome {
	case frontend.TurnOutcomeCompleted:
		usage := snapshot.ContextUsage
		if err := r.emit(execEvent{
			Type: "turn.completed", ThreadID: r.threadID, TurnID: turnID, Usage: &usage,
		}); err != nil {
			return nil, err
		}
		if !r.json && r.lastAssistant != "" {
			_, err := fmt.Fprintln(r.out, r.lastAssistant)
			return nil, err
		}
		return nil, nil
	case frontend.TurnOutcomeInterrupted:
		failure := &ExitError{
			Code: ExitCodeInterrupted, Category: ExecFailureInterrupted, Err: errors.New("coding turn interrupted"),
		}
		return r.reportFailure(turnID, failure)
	case frontend.TurnOutcomeSuspended:
		failure := &ExitError{
			Code: ExitCodeSuspended, Category: ExecFailureSuspended, Err: errors.New("coding turn suspended"),
		}
		return r.reportFailure(turnID, failure)
	case frontend.TurnOutcomeFailed:
		failure := classifyExecError(errors.New("coding turn failed"), &snapshot)
		return r.reportFailure(turnID, failure)
	default:
		failure := classifyExecError(fmt.Errorf("unknown coding turn outcome %q", snapshot.LastTurn.Outcome), &snapshot)
		return r.reportFailure(turnID, failure)
	}
}

func projectExecItem(item frontend.PresentationItem) execItem {
	projected := execItem{
		ID: item.ID, Revision: item.Revision, Kind: item.Kind, Status: item.Lifecycle,
		DurationMS: item.Duration.Milliseconds(),
	}
	if item.Message != nil {
		projected.Text = item.Message.Text
		projected.Truncated = item.Message.Truncated
	}
	if item.Tool != nil {
		tool := *item.Tool
		projected.Tool = &tool
	}
	if item.Plan != nil {
		plan := *item.Plan
		plan.Steps = append([]frontend.PlanStepState(nil), item.Plan.Steps...)
		projected.Plan = &plan
	}
	if item.Compaction != nil {
		compaction := *item.Compaction
		projected.Compaction = &compaction
	}
	if item.Turn != nil {
		turn := *item.Turn
		projected.Turn = &turn
	}
	return projected
}

func (r *execRenderer) emitFailure(turnID string, failure *ExitError) error {
	return r.emit(execEvent{
		Type: "turn.failed", ThreadID: r.threadID, TurnID: turnID,
		Error: &execEventError{Category: failure.Category, Message: failure.Error()},
	})
}

func (r *execRenderer) reportFailure(turnID string, failure *ExitError) (*ExitError, error) {
	if err := r.emitFailure(turnID, failure); err != nil {
		return failure, err
	}
	failure.reported = r.json
	return failure, nil
}

func (r *execRenderer) emit(event execEvent) error {
	if !r.json {
		return nil
	}
	event.SchemaVersion = ExecSchemaVersion
	return json.NewEncoder(r.out).Encode(event)
}

func renderClassifiedExecError(out io.Writer, jsonOutput bool, failure *ExitError) error {
	if failure == nil {
		return nil
	}
	if jsonOutput && !failure.reported {
		event := execEvent{
			SchemaVersion: ExecSchemaVersion,
			Type:          "error",
			Error:         &execEventError{Category: failure.Category, Message: failure.Error()},
		}
		if err := json.NewEncoder(out).Encode(event); err != nil {
			return &ExitError{Code: failure.Code, Category: failure.Category, Err: errors.Join(failure, err)}
		}
	}
	return failure
}

func classifyExecError(err error, snapshot *frontend.ThreadSnapshot) *ExitError {
	if err == nil {
		return nil
	}
	var existing *ExitError
	if errors.As(err, &existing) {
		return existing
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, controller.ErrHardCanceled) {
		return &ExitError{Code: ExitCodeInterrupted, Category: ExecFailureInterrupted, Err: err}
	}
	var projectErr *projectResolutionError
	if errors.As(err, &projectErr) {
		return &ExitError{Code: ExitCodeInvalidProject, Category: ExecFailureInvalidProject, Err: err}
	}
	var providerErr *providererrors.ProviderError
	if errors.As(err, &providerErr) && providerErr.Kind.Canonical() == providererrors.KindContextOverflow {
		return &ExitError{Code: ExitCodeContextFailure, Category: ExecFailureContext, Err: err}
	}
	var failoverErr *providers.FailoverError
	if errors.As(err, &failoverErr) && failoverErr.Reason == providers.FailoverContextOverflow {
		return &ExitError{Code: ExitCodeContextFailure, Category: ExecFailureContext, Err: err}
	}
	if snapshot != nil {
		turnID := ""
		if snapshot.LastTurn != nil {
			turnID = snapshot.LastTurn.TurnID
		}
		if snapshot.LastCompaction != nil && snapshot.LastCompaction.Status == frontend.CompactionFailed &&
			snapshot.LastCompaction.TurnID == turnID {
			return &ExitError{Code: ExitCodeContextFailure, Category: ExecFailureContext, Err: err}
		}
		for _, item := range snapshot.Items {
			if item.Tool != nil && item.Tool.TurnID == turnID && item.Tool.Status == frontend.ToolFailed {
				return &ExitError{Code: ExitCodeToolFailure, Category: ExecFailureTool, Err: err}
			}
		}
	}
	return &ExitError{Code: ExitCodeModelFailure, Category: ExecFailureModel, Err: err}
}
