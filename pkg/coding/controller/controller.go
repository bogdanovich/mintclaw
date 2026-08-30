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
	commandClose
)

type command struct {
	kind    commandKind
	ctx     context.Context
	content string
	input   frontend.TurnInput
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

// Controller serializes coding commands while exposing the current in-process
// presentation view. Exactly one actor owns admission state.
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

func (c *Controller) run(ctx context.Context, kind operationKind, input frontend.TurnInput, ready func()) {
	var err error
	if kind == operationTurn {
		err = c.runtime.RunTurn(ctx, input, ready)
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
