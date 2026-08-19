// Package controller serializes coding-thread mutations without depending on
// a terminal implementation.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

var (
	ErrClosed           = errors.New("coding controller is closed")
	ErrTurnActive       = errors.New("coding turn is active")
	ErrCompactionActive = errors.New("coding compaction is active")
	ErrNoActiveTurn     = errors.New("no coding turn is active")
	ErrUnsupported      = errors.New("coding controller command is not supported")
	ErrHardCanceled     = errors.New("coding turn was hard-canceled")
)

// Runtime is the single-writer backend owned by a Controller. RunTurn and
// Compact may block; the controller always invokes them outside its mutation
// coordinator. The control methods must target only this runtime's thread.
type Runtime interface {
	RunTurn(context.Context, string, func()) error
	Interrupt(context.Context) error
	HardCancel(context.Context) error
	Compact(context.Context) error
	Close() error
}

type commandKind uint8

const (
	commandSubmit commandKind = iota
	commandInterrupt
	commandHardCancel
	commandCompact
	commandRename
	commandNewThread
	commandRefreshWorkspace
	commandClose
)

type command struct {
	kind    commandKind
	ctx     context.Context
	content string
	reply   chan error
}

type operationKind uint8

const (
	operationTurn operationKind = iota
	operationCompaction
)

type operationResult struct {
	kind operationKind
	err  error
}

// Controller is a transport-neutral frontend controller. Exactly one actor
// owns admission state, even when Snapshot and Watch have multiple readers.
type Controller struct {
	projector *frontend.Projector
	runtime   Runtime
	commands  chan command
	results   chan operationResult
	done      chan struct{}
	closeMu   sync.Mutex
	closeErr  error
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
		projector: projector,
		runtime:   runtime,
		commands:  make(chan command),
		results:   make(chan operationResult, 1),
		done:      make(chan struct{}),
	}
	go controller.coordinate()
	return controller, nil
}

func (c *Controller) Snapshot(ctx context.Context) (frontend.ThreadSnapshot, error) {
	return c.projector.Snapshot(ctx)
}

func (c *Controller) ChangesSince(ctx context.Context, revision frontend.Revision) ([]frontend.Delta, error) {
	return c.projector.ChangesSince(ctx, revision)
}

func (c *Controller) Watch(ctx context.Context, revision frontend.Revision) (<-chan frontend.Delta, error) {
	return c.projector.Watch(ctx, revision)
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

func (c *Controller) Submit(ctx context.Context, prompt string) error {
	if err := thread.ValidatePrompt(prompt); err != nil {
		return err
	}
	return c.send(ctx, commandSubmit, prompt)
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

func (c *Controller) NewThread(ctx context.Context) error {
	return c.send(ctx, commandNewThread, "")
}

// RefreshWorkspace requests a fresh bounded repository observation without
// exposing runtime or Git implementation details to a frontend.
func (c *Controller) RefreshWorkspace(ctx context.Context) error {
	return c.send(ctx, commandRefreshWorkspace, "")
}

func (c *Controller) Close(ctx context.Context) error {
	return c.send(ctx, commandClose, "")
}

func (c *Controller) send(ctx context.Context, kind commandKind, content string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reply := make(chan error, 1)
	request := command{kind: kind, ctx: ctx, content: content, reply: reply}
	select {
	case c.commands <- request:
	case <-c.done:
		if kind == commandClose {
			return c.closedError()
		}
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
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
	var closeReplies []chan error
	var closeErr error

	finishClose := func() bool {
		if !closing || active || compacting {
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

	for {
		select {
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
				request.reply <- ErrClosed
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
					go c.run(operationCtx, operationTurn, request.content, func() {
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
					go c.run(operationCtx, operationCompaction, "", nil)
					request.reply <- nil
				}
			case commandRename, commandNewThread:
				request.reply <- ErrUnsupported
			case commandRefreshWorkspace:
				switch {
				case active:
					request.reply <- ErrTurnActive
				case compacting:
					request.reply <- ErrCompactionActive
				default:
					refresher, ok := c.runtime.(frontend.WorkspaceRefresher)
					if !ok {
						request.reply <- frontend.ErrWorkspaceRefreshUnsupported
						continue
					}
					request.reply <- refresher.RefreshWorkspace(request.ctx)
				}
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
				if finishClose() {
					return
				}
			}
		}
	}
}

func (c *Controller) run(ctx context.Context, kind operationKind, prompt string, ready func()) {
	var err error
	if kind == operationTurn {
		err = c.runtime.RunTurn(ctx, prompt, ready)
	} else {
		err = c.runtime.Compact(ctx)
	}
	c.results <- operationResult{kind: kind, err: err}
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
