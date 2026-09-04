// Package controller serializes coding-thread mutations without depending on
// a terminal implementation.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

var (
	ErrClosed           = errors.New("coding controller is closed")
	ErrTurnActive       = errors.New("coding turn is active")
	ErrCompactionActive = errors.New("coding compaction is active")
	ErrNoActiveTurn     = errors.New("no coding turn is active")
	ErrUnsupported      = frontend.ErrCommandUnsupported
	ErrHardCanceled     = errors.New("coding turn was hard-canceled")
)

// Runtime is the single-writer backend owned by a Controller. RunTurn and
// Compact may block; the controller always invokes them outside its mutation
// coordinator. The control methods must target only this runtime's thread.
type Runtime interface {
	RunTurn(context.Context, frontend.TurnInput, func()) error
	Interrupt(context.Context) error
	HardCancel(context.Context) error
	Compact(context.Context) error
	Close() error
}

type workspaceEvidenceRefresher interface {
	RefreshWorkspaceEvidence(context.Context) (codingworkspace.StatusResult, error)
}

type commandKind uint8

const (
	commandSubmit commandKind = iota
	commandInterrupt
	commandHardCancel
	commandCompact
	commandRename
	commandArchive
	commandUnarchive
	commandNewThread
	commandRefreshWorkspace
	commandRepositoryStatus
	commandRepositoryDiff
	commandClose
)

type command struct {
	kind        commandKind
	ctx         context.Context
	content     string
	input       frontend.TurnInput
	diffTarget  codingworkspace.DiffTarget
	reply       chan error
	statusReply chan repositoryStatusResponse
	diffReply   chan repositoryDiffResponse
}

type repositoryStatusResponse struct {
	status codingworkspace.StatusResult
	err    error
}

type repositoryDiffResponse struct {
	diff codingworkspace.DiffResult
	err  error
}

func (request command) replyError(err error) {
	switch request.kind {
	case commandRepositoryStatus:
		request.statusReply <- repositoryStatusResponse{err: err}
	case commandRepositoryDiff:
		request.diffReply <- repositoryDiffResponse{err: err}
	default:
		request.reply <- err
	}
}

type operationKind uint8

const (
	operationTurn operationKind = iota
	operationCompaction
	operationWorkspaceRefresh
	operationRepositoryStatus
	operationRepositoryDiff
)

type operationResult struct {
	id      uint64
	kind    operationKind
	request command
	status  codingworkspace.StatusResult
	diff    codingworkspace.DiffResult
	err     error
}

type evidenceOperation struct {
	id      uint64
	kind    operationKind
	request command
	ctx     context.Context
	cancel  context.CancelCauseFunc
}

// Controller serializes coding commands while exposing the current in-process
// presentation view. Exactly one actor owns admission state.
type Controller struct {
	projector       *frontend.Projector
	runtime         Runtime
	commands        chan command
	results         chan operationResult
	evidenceResults chan operationResult
	done            chan struct{}
	closeMu         sync.Mutex
	closeErr        error
}

var _ frontend.Controller = (*Controller)(nil)

func New(projector *frontend.Projector, runtime Runtime) (*Controller, error) {
	if projector == nil {
		return nil, fmt.Errorf("coding controller projector is required")
	}
	if runtime == nil {
		return nil, fmt.Errorf("coding controller runtime is required")
	}
	controller := &Controller{
		projector:       projector,
		runtime:         runtime,
		commands:        make(chan command),
		results:         make(chan operationResult, 1),
		evidenceResults: make(chan operationResult),
		done:            make(chan struct{}),
	}
	go controller.coordinate()
	return controller, nil
}

func (c *Controller) Snapshot(ctx context.Context) (frontend.ThreadSnapshot, error) {
	return c.projector.Snapshot(ctx)
}

func (c *Controller) Subscribe(
	ctx context.Context,
) (frontend.ThreadSnapshot, <-chan frontend.ThreadSnapshot, error) {
	return c.projector.Subscribe(ctx)
}

// TranscriptPage delegates optional bounded history hydration to the runtime.
func (c *Controller) TranscriptPage(
	ctx context.Context,
	request frontend.TranscriptPageRequest,
) (frontend.TranscriptPage, error) {
	pager, ok := c.runtime.(frontend.TranscriptPager)
	if !ok {
		return frontend.TranscriptPage{}, frontend.ErrTranscriptPagingUnsupported
	}
	return pager.TranscriptPage(ctx, request)
}

func (c *Controller) Submit(ctx context.Context, input frontend.TurnInput) error {
	if err := validateTurnInput(input); err != nil {
		return err
	}
	return c.sendInput(ctx, commandSubmit, "", input.Clone())
}

func validateTurnInput(input frontend.TurnInput) error {
	if len(input.Attachments) == 0 {
		return thread.ValidatePrompt(input.Text)
	}
	if len(input.Attachments) > frontend.MaxTurnAttachments {
		return fmt.Errorf("coding turn: at most %d attachments are allowed", frontend.MaxTurnAttachments)
	}
	if !utf8.ValidString(input.Text) || len(input.Text) > thread.MaxPromptBytes {
		return fmt.Errorf("coding thread transcript: prompt must be valid UTF-8 within %d bytes", thread.MaxPromptBytes)
	}
	for index, attachment := range input.Attachments {
		if attachment.Path == "" {
			return fmt.Errorf("coding turn: attachment %d path is required", index+1)
		}
		if !utf8.ValidString(attachment.Filename) || !utf8.ValidString(attachment.ContentType) {
			return fmt.Errorf("coding turn: attachment %d metadata must be valid UTF-8", index+1)
		}
		if strings.ContainsRune(attachment.Filename, '\x00') || strings.ContainsRune(attachment.ContentType, '\x00') {
			return fmt.Errorf("coding turn: attachment %d metadata contains NUL", index+1)
		}
	}
	return nil
}

func (c *Controller) Interrupt(ctx context.Context) error {
	return c.send(ctx, commandInterrupt, "")
}

func (c *Controller) HardCancel(ctx context.Context) error {
	return c.send(ctx, commandHardCancel, "")
}

func (c *Controller) Compact(ctx context.Context) error {
	return c.send(ctx, commandCompact, "")
}

func (c *Controller) Rename(ctx context.Context, title string) error {
	return c.send(ctx, commandRename, title)
}

func (c *Controller) SetArchived(ctx context.Context, archived bool) error {
	kind := commandUnarchive
	if archived {
		kind = commandArchive
	}
	return c.send(ctx, kind, "")
}

func (c *Controller) NewThread(ctx context.Context) error {
	return c.send(ctx, commandNewThread, "")
}

// RefreshWorkspace requests a fresh bounded repository observation without
// exposing runtime or Git implementation details to a frontend.
func (c *Controller) RefreshWorkspace(ctx context.Context) error {
	return c.send(ctx, commandRefreshWorkspace, "")
}

func (c *Controller) RepositoryStatus(ctx context.Context) (codingworkspace.StatusResult, error) {
	ctx = contextOrBackground(ctx)
	reply := make(chan repositoryStatusResponse, 1)
	request := command{kind: commandRepositoryStatus, ctx: ctx, statusReply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return codingworkspace.StatusResult{}, err
	}
	select {
	case response := <-reply:
		return response.status, response.err
	case <-c.done:
		select {
		case response := <-reply:
			return response.status, response.err
		default:
			return codingworkspace.StatusResult{}, ErrClosed
		}
	case <-ctx.Done():
		return codingworkspace.StatusResult{}, ctx.Err()
	}
}

func (c *Controller) RepositoryDiff(
	ctx context.Context,
	target codingworkspace.DiffTarget,
) (codingworkspace.DiffResult, error) {
	ctx = contextOrBackground(ctx)
	reply := make(chan repositoryDiffResponse, 1)
	request := command{kind: commandRepositoryDiff, ctx: ctx, diffTarget: target, diffReply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return codingworkspace.DiffResult{}, err
	}
	select {
	case response := <-reply:
		return response.diff, response.err
	case <-c.done:
		select {
		case response := <-reply:
			return response.diff, response.err
		default:
			return codingworkspace.DiffResult{}, ErrClosed
		}
	case <-ctx.Done():
		return codingworkspace.DiffResult{}, ctx.Err()
	}
}

func (c *Controller) Close(ctx context.Context) error {
	return c.send(ctx, commandClose, "")
}

func (c *Controller) send(ctx context.Context, kind commandKind, content string) error {
	return c.sendInput(ctx, kind, content, frontend.TurnInput{})
}

func (c *Controller) sendInput(
	ctx context.Context,
	kind commandKind,
	content string,
	input frontend.TurnInput,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reply := make(chan error, 1)
	request := command{kind: kind, ctx: ctx, content: content, input: input, reply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-c.done:
		select {
		case err := <-reply:
			return err
		default:
			if kind == commandClose {
				return c.closedError()
			}
			return ErrClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) enqueue(ctx context.Context, request command) error {
	select {
	case c.commands <- request:
	case <-c.done:
		if request.kind == commandClose {
			return c.closedError()
		}
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c *Controller) closedError() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closeErr
}

func (c *Controller) coordinate() {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	defer close(c.done)

	var active bool
	var hardCancelRequested bool
	var compacting bool
	var closing bool
	var operationCancel context.CancelCauseFunc
	var activeEvidence *evidenceOperation
	var evidenceQueue []evidenceOperation
	var nextEvidenceID uint64
	var closeReplies []chan error
	var closeErr error

	finishClose := func() bool {
		if !closing || active || compacting || activeEvidence != nil || len(evidenceQueue) != 0 {
			return false
		}
		closeErr = errors.Join(closeErr, c.runtime.Close())
		c.closeMu.Lock()
		c.closeErr = closeErr
		c.closeMu.Unlock()
		for _, reply := range closeReplies {
			reply <- closeErr
		}
		return true
	}

	startNextEvidence := func() {
		if closing || activeEvidence != nil || len(evidenceQueue) == 0 {
			return
		}
		operation := evidenceQueue[0]
		evidenceQueue = evidenceQueue[1:]
		activeEvidence = &operation
		go c.runEvidence(operation.ctx, operation.id, operation.kind, operation.request)
	}

	admitEvidence := func(kind operationKind, request command) {
		nextEvidenceID++
		operationCtx, cancel := context.WithCancelCause(request.ctx)
		evidenceQueue = append(evidenceQueue, evidenceOperation{
			id:      nextEvidenceID,
			kind:    kind,
			request: request,
			ctx:     operationCtx,
			cancel:  cancel,
		})
		startNextEvidence()
	}

	for {
		select {
		case result := <-c.evidenceResults:
			if activeEvidence == nil || activeEvidence.id != result.id {
				continue
			}
			operation := *activeEvidence
			if err := operation.ctx.Err(); err != nil {
				result.err = err
			}
			operation.cancel(context.Canceled)
			activeEvidence = nil
			if result.err == nil {
				switch result.kind {
				case operationWorkspaceRefresh, operationRepositoryStatus:
					c.projector.RepositoryStatusUpdated(result.status)
				case operationRepositoryDiff:
					c.projector.RepositoryDiffUpdated(result.diff)
				}
			}
			switch result.kind {
			case operationWorkspaceRefresh:
				result.request.reply <- result.err
			case operationRepositoryStatus:
				result.request.statusReply <- repositoryStatusResponse{status: result.status, err: result.err}
			case operationRepositoryDiff:
				result.request.diffReply <- repositoryDiffResponse{diff: result.diff, err: result.err}
			}
			startNextEvidence()
			if finishClose() {
				return
			}
		case result := <-c.results:
			switch result.kind {
			case operationTurn:
				active = false
				hardCancelRequested = false
			case operationCompaction:
				compacting = false
			}
			operationCancel = nil
			c.projectOperationError(result)
			if finishClose() {
				return
			}
		case request := <-c.commands:
			if closing && request.kind != commandClose {
				request.replyError(ErrClosed)
				continue
			}
			switch request.kind {
			case commandSubmit:
				switch {
				case active:
					request.reply <- ErrTurnActive
				case compacting:
					request.reply <- ErrCompactionActive
				default:
					active = true
					operationCtx, cancel := context.WithCancelCause(rootCtx)
					operationCancel = cancel
					ready := make(chan struct{})
					var readyOnce sync.Once
					go c.run(operationCtx, operationTurn, request.input, func() {
						readyOnce.Do(func() { close(ready) })
					})
					select {
					case <-ready:
						request.reply <- nil
					case result := <-c.results:
						active = false
						operationCancel = nil
						c.projectOperationError(result)
						request.reply <- result.err
					case <-request.ctx.Done():
						operationCancel(context.Cause(request.ctx))
						request.reply <- request.ctx.Err()
					}
				}
			case commandInterrupt:
				if !active {
					request.reply <- ErrNoActiveTurn
					continue
				}
				request.reply <- c.runtime.Interrupt(request.ctx)
			case commandHardCancel:
				if !active {
					request.reply <- ErrNoActiveTurn
					continue
				}
				err := c.runtime.HardCancel(request.ctx)
				if operationCancel != nil {
					operationCancel(ErrHardCanceled)
				}
				if err == nil {
					hardCancelRequested = true
				}
				request.reply <- err
			case commandCompact:
				switch {
				case active:
					request.reply <- ErrTurnActive
				case compacting:
					request.reply <- ErrCompactionActive
				default:
					compacting = true
					operationCtx, cancel := context.WithCancelCause(rootCtx)
					operationCancel = cancel
					go c.run(operationCtx, operationCompaction, frontend.TurnInput{}, nil)
					request.reply <- nil
				}
			case commandRename, commandArchive, commandUnarchive:
				switch {
				case active:
					request.reply <- ErrTurnActive
				case compacting:
					request.reply <- ErrCompactionActive
				default:
					if observer, ok := c.runtime.(frontend.BackgroundCompactionObserver); ok &&
						observer.BackgroundCompactionActive() {
						request.reply <- ErrCompactionActive
						continue
					}
					lifecycle, ok := c.runtime.(frontend.ThreadLifecycle)
					if !ok {
						request.reply <- ErrUnsupported
						continue
					}
					if request.kind == commandRename {
						request.reply <- lifecycle.Rename(request.ctx, request.content)
					} else {
						request.reply <- lifecycle.SetArchived(request.ctx, request.kind == commandArchive)
					}
				}
			case commandNewThread:
				request.reply <- ErrUnsupported
			case commandRefreshWorkspace:
				switch {
				case active:
					request.reply <- ErrTurnActive
				case compacting:
					request.reply <- ErrCompactionActive
				default:
					if _, ok := c.runtime.(workspaceEvidenceRefresher); !ok {
						request.reply <- frontend.ErrWorkspaceRefreshUnsupported
						continue
					}
					admitEvidence(operationWorkspaceRefresh, request)
				}
			case commandRepositoryStatus:
				if _, ok := c.runtime.(frontend.RepositoryEvidenceReader); !ok {
					request.replyError(frontend.ErrWorkspaceRefreshUnsupported)
					continue
				}
				admitEvidence(operationRepositoryStatus, request)
			case commandRepositoryDiff:
				if _, ok := c.runtime.(frontend.RepositoryEvidenceReader); !ok {
					request.replyError(frontend.ErrWorkspaceRefreshUnsupported)
					continue
				}
				admitEvidence(operationRepositoryDiff, request)
			case commandClose:
				closeReplies = append(closeReplies, request.reply)
				if closing {
					continue
				}
				closing = true
				if active && !hardCancelRequested {
					err := c.runtime.HardCancel(context.WithoutCancel(request.ctx))
					closeErr = errors.Join(closeErr, err)
					hardCancelRequested = err == nil
					if operationCancel != nil {
						operationCancel(ErrHardCanceled)
					}
				} else if compacting && operationCancel != nil {
					operationCancel(context.Canceled)
				}
				if activeEvidence != nil {
					activeEvidence.cancel(context.Canceled)
				}
				for _, operation := range evidenceQueue {
					operation.cancel(context.Canceled)
					operation.request.replyError(context.Canceled)
				}
				evidenceQueue = nil
				if finishClose() {
					return
				}
			}
		}
	}
}

func (c *Controller) run(ctx context.Context, kind operationKind, input frontend.TurnInput, ready func()) {
	var err error
	if kind == operationTurn {
		err = c.runtime.RunTurn(ctx, input, ready)
	} else {
		err = c.runtime.Compact(ctx)
	}
	c.results <- operationResult{kind: kind, err: err}
}

func (c *Controller) runEvidence(ctx context.Context, id uint64, kind operationKind, request command) {
	result := operationResult{id: id, kind: kind, request: request}
	switch kind {
	case operationWorkspaceRefresh:
		result.status, result.err = c.runtime.(workspaceEvidenceRefresher).RefreshWorkspaceEvidence(ctx)
	case operationRepositoryStatus:
		result.status, result.err = c.runtime.(frontend.RepositoryEvidenceReader).RepositoryStatus(ctx)
	case operationRepositoryDiff:
		result.diff, result.err = c.runtime.(frontend.RepositoryEvidenceReader).RepositoryDiff(ctx, request.diffTarget)
	}
	if err := ctx.Err(); err != nil {
		result.err = err
	}
	c.evidenceResults <- result
}

func (c *Controller) projectOperationError(result operationResult) {
	if result.err == nil || isOnlyIntentionalCancellation(result.err) {
		return
	}
	if result.kind == operationTurn {
		c.projector.Error("", "controller:turn-error", "coding turn failed")
	} else {
		c.projector.Error("", "controller:compaction-error", "coding compaction failed")
	}
}

func isOnlyIntentionalCancellation(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !isOnlyIntentionalCancellation(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return isOnlyIntentionalCancellation(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, ErrHardCanceled)
}
