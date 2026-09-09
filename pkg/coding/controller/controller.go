// Package controller serializes coding-thread mutations without depending on
// a terminal implementation.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

var (
	ErrClosed                 = errors.New("coding controller is closed")
	ErrTurnActive             = errors.New("coding turn is active")
	ErrCompactionActive       = errors.New("coding compaction is active")
	ErrReviewActive           = errors.New("coding review is active")
	ErrWorkspaceRefreshActive = errors.New("coding workspace refresh is active")
	ErrNoActiveTurn           = errors.New("no coding turn is active")
	ErrSteerConflict          = errors.New("coding steer ID conflicts with an accepted steer")
	ErrSteerLimit             = errors.New("coding turn steer limit reached")
	ErrUnsupported            = frontend.ErrCommandUnsupported
	ErrHardCanceled           = errors.New("coding turn was hard-canceled")
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

type runtimeStatusReader interface {
	RuntimeStatus(context.Context) frontend.RuntimeStatus
}

type steeringRuntime interface {
	Steer(context.Context, frontend.SteerInput) error
}

// reviewRuntime validates its result, calls the supplied commit function
// immediately before durable selected-thread publication, and returns only
// after publication. The controller linearizes commit against cancellation and
// remains the sole owner of frontend lifecycle projection.
type reviewRuntime interface {
	RunReview(
		context.Context,
		string,
		codingreview.Target,
		func(codingreview.Event) error,
		func() error,
	) (codingreview.Result, error)
}

type reviewAvailability interface {
	ReviewAvailable() bool
}

type turnSettlementErrorSource interface {
	TurnSettlementError() error
}

type commandKind uint8

const (
	commandSubmit commandKind = iota
	commandSteer
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
	commandReview
	commandAwaitTurn
	commandClose
)

type command struct {
	kind         commandKind
	ctx          context.Context
	content      string
	input        frontend.TurnInput
	steer        frontend.SteerInput
	diffTarget   codingworkspace.DiffTarget
	reviewTarget codingreview.Target
	reply        chan error
	statusReply  chan repositoryStatusResponse
	diffReply    chan repositoryDiffResponse
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
	operationNone operationKind = iota
	operationTurn
	operationCompaction
	operationWorkspaceRefresh
	operationRepositoryStatus
	operationRepositoryDiff
	operationReview
)

type operationResult struct {
	id              uint64
	kind            operationKind
	request         command
	status          codingworkspace.StatusResult
	runtimeStatus   *frontend.RuntimeStatus
	diff            codingworkspace.DiffResult
	review          codingreview.Result
	reviewID        string
	reviewCommitted bool
	projectErr      error
	err             error
}

type reviewOperationEvent struct {
	reviewID string
	event    codingreview.Event
}

type reviewCommitRequest struct {
	reviewID string
	reply    chan error
}

// Controller serializes coding commands while exposing the current in-process
// presentation view. Exactly one actor owns admission state.
type Controller struct {
	projector       *frontend.Projector
	runtime         Runtime
	commands        chan command
	results         chan operationResult
	evidenceResults chan operationResult
	reviewEvents    chan reviewOperationEvent
	reviewCommits   chan reviewCommitRequest
	done            chan struct{}
	closeMu         sync.Mutex
	closeErr        error
}

var (
	_ frontend.Controller  = (*Controller)(nil)
	_ frontend.Steerer     = (*Controller)(nil)
	_ frontend.Reviewer    = (*Controller)(nil)
	_ frontend.TurnSettler = (*Controller)(nil)
)

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
		reviewEvents:    make(chan reviewOperationEvent),
		reviewCommits:   make(chan reviewCommitRequest),
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
	ctx = contextOrBackground(ctx)
	reply := make(chan error, 1)
	request := command{kind: commandSubmit, ctx: ctx, input: input.Clone(), reply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return err
	}
	return awaitTurnAdmission(reply, c.done)
}

// Steer appends bounded guidance to the active turn. Acceptance is
// idempotent for the lifetime of that turn and never starts a new turn.
func (c *Controller) Steer(ctx context.Context, input frontend.SteerInput) error {
	if err := validateSteerInput(input); err != nil {
		return err
	}
	ctx = contextOrBackground(ctx)
	reply := make(chan error, 1)
	request := command{kind: commandSteer, ctx: ctx, steer: input, reply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return err
	}
	return awaitTurnAdmission(reply, c.done)
}

// awaitTurnAdmission waits for the actor-owned admission decision after the
// request has entered the command queue. The actor checks cancellation before
// starting work, so its reply definitively says whether a turn was admitted.
func awaitTurnAdmission(reply <-chan error, done <-chan struct{}) error {
	select {
	case err := <-reply:
		return err
	case <-done:
		select {
		case err := <-reply:
			return err
		default:
			return ErrClosed
		}
	}
}

// AwaitTurn waits for the currently admitted turn, or returns the retained
// settlement of the most recently admitted turn. It never cancels the turn.
func (c *Controller) AwaitTurn(ctx context.Context) error {
	return c.send(ctx, commandAwaitTurn, "")
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

func validateSteerInput(input frontend.SteerInput) error {
	if !validSteerID(input.ID) {
		return fmt.Errorf(
			"coding steer: ID must match [A-Za-z0-9][A-Za-z0-9._:-]* within %d bytes",
			frontend.MaxSteerIDBytes,
		)
	}
	if err := thread.ValidatePrompt(input.Text); err != nil {
		return fmt.Errorf("coding steer: %w", err)
	}
	return nil
}

func validSteerID(value string) bool {
	if value == "" || len(value) > frontend.MaxSteerIDBytes {
		return false
	}
	for index, character := range value {
		letter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		suffixPunctuation := index > 0 && (character == '.' || character == '_' || character == ':' || character == '-')
		if character <= unicode.MaxASCII && (letter || digit || suffixPunctuation) {
			continue
		}
		return false
	}
	return true
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

func (c *Controller) Review(ctx context.Context, target codingreview.Target) error {
	if err := target.Validate(); err != nil {
		return err
	}
	ctx = contextOrBackground(ctx)
	reply := make(chan error, 1)
	request := command{kind: commandReview, ctx: ctx, reviewTarget: target, reply: reply}
	if err := c.enqueue(ctx, request); err != nil {
		return err
	}
	return awaitReviewAdmission(reply, c.done)
}

// awaitReviewAdmission waits for the actor-owned admission decision after the
// request has been enqueued. The actor rechecks request cancellation before it
// starts detached review work, so its reply must win any later cancellation.
func awaitReviewAdmission(reply <-chan error, done <-chan struct{}) error {
	select {
	case err := <-reply:
		return err
	case <-done:
		select {
		case err := <-reply:
			return err
		default:
			return ErrClosed
		}
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

	var primary primaryOperation
	var closing bool
	var pendingTurnAdmission *command
	var pendingTurnReady <-chan struct{}
	var pendingTurnCanceled <-chan struct{}
	var turnWaiters []command
	var turnSettlementAvailable bool
	var turnSettlementErr error
	var evidence evidenceQueueState
	var closeReplies []chan error
	var closeErr error
	var turnSteering *turnSteeringState

	finishClose := func() bool {
		if !closing || primary.active() || !evidence.empty() {
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

	backgroundCompactionActive := func() bool {
		observer, ok := c.runtime.(frontend.BackgroundCompactionObserver)
		return ok && observer.BackgroundCompactionActive()
	}

	startNextEvidence := func() {
		operation, ok := evidence.startNext(closing)
		if !ok {
			return
		}
		go c.runEvidence(operation.ctx, operation.id, operation.kind, operation.request)
	}

	admitEvidence := func(kind operationKind, request command) {
		evidence.admit(kind, request)
		startNextEvidence()
	}

	for {
		select {
		case <-pendingTurnReady:
			turnSteering.open()
			pendingTurnAdmission.reply <- nil
			pendingTurnAdmission = nil
			pendingTurnReady = nil
			pendingTurnCanceled = nil
		case <-pendingTurnCanceled:
			select {
			case <-pendingTurnReady:
				turnSteering.open()
				pendingTurnAdmission.reply <- nil
			default:
				primary.cancel(context.Cause(pendingTurnAdmission.ctx))
				pendingTurnAdmission.reply <- pendingTurnAdmission.ctx.Err()
			}
			pendingTurnAdmission = nil
			pendingTurnReady = nil
			pendingTurnCanceled = nil
		case update := <-c.reviewEvents:
			if primary.matchesReview(update.reviewID) {
				_ = c.projector.ReviewEvent(update.reviewID, update.event)
			}
		case request := <-c.reviewCommits:
			request.reply <- primary.commitReview(request.reviewID)
		case result := <-c.evidenceResults:
			var matched bool
			result.err, matched = evidence.complete(result.id, result.err)
			if !matched {
				continue
			}
			if result.err == nil {
				switch result.kind {
				case operationWorkspaceRefresh, operationRepositoryStatus:
					if result.runtimeStatus != nil {
						c.projector.RepositoryStatusAndRuntimeUpdated(result.status, *result.runtimeStatus)
					} else {
						c.projector.RepositoryStatusUpdated(result.status)
					}
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
			if result.kind == operationTurn && pendingTurnAdmission != nil {
				select {
				case <-pendingTurnReady:
					turnSteering.open()
					pendingTurnAdmission.reply <- nil
				default:
					admissionErr := result.err
					if admissionErr == nil {
						admissionErr = errors.New("coding turn returned before admission")
						result.err = admissionErr
					}
					pendingTurnAdmission.reply <- admissionErr
				}
				pendingTurnAdmission = nil
				pendingTurnReady = nil
				pendingTurnCanceled = nil
			}
			result.err = primary.finish(result)
			if result.kind == operationReview {
				if result.err != nil {
					c.projector.ReviewInterrupted(result.reviewID)
				} else if err := c.projector.ReviewCompleted(result.review); err != nil {
					result.err = err
					c.projector.ReviewInterrupted(result.reviewID)
				}
			}
			c.projectOperationError(result)
			if result.kind == operationTurn {
				turnSteering = nil
				turnSettlementAvailable = true
				turnSettlementErr = result.err
				for _, waiter := range turnWaiters {
					if err := waiter.ctx.Err(); err != nil {
						waiter.reply <- err
					} else {
						waiter.reply <- result.err
					}
				}
				turnWaiters = nil
			}
			if finishClose() {
				return
			}
		case request := <-c.commands:
			if closing && request.kind != commandClose {
				request.replyError(ErrClosed)
				continue
			}
			evidence.pruneCanceled()
			switch request.kind {
			case commandSubmit:
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
					continue
				}
				if err := primary.admissionError(); err != nil {
					request.reply <- err
					continue
				}
				if evidence.workspaceRefreshPending() {
					request.reply <- ErrWorkspaceRefreshActive
					continue
				}
				turnSettlementAvailable = false
				turnSettlementErr = nil
				turnSteering = newTurnSteeringState()
				operationCtx := primary.start(rootCtx, operationTurn)
				ready := make(chan struct{})
				var readyOnce sync.Once
				go c.run(operationCtx, operationTurn, request.input, func() {
					readyOnce.Do(func() { close(ready) })
				}, turnSteering)
				pendingTurnAdmission = &request
				pendingTurnReady = ready
				pendingTurnCanceled = request.ctx.Done()
			case commandSteer:
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
					continue
				}
				switch {
				case primary.is(operationReview):
					request.reply <- ErrReviewActive
					continue
				case primary.is(operationCompaction):
					request.reply <- ErrCompactionActive
					continue
				case !primary.is(operationTurn):
					request.reply <- ErrNoActiveTurn
					continue
				}
				request.reply <- turnSteering.steer(
					request.ctx,
					request.steer,
					func(ctx context.Context, input frontend.SteerInput) error {
						runtime, ok := c.runtime.(steeringRuntime)
						if !ok {
							return ErrUnsupported
						}
						return runtime.Steer(ctx, input)
					},
				)
			case commandInterrupt:
				if primary.is(operationReview) {
					primary.cancel(context.Canceled)
					request.reply <- nil
					continue
				}
				if !primary.is(operationTurn) {
					request.reply <- ErrNoActiveTurn
					continue
				}
				request.reply <- c.runtime.Interrupt(request.ctx)
			case commandHardCancel:
				if primary.is(operationReview) {
					primary.cancel(ErrHardCanceled)
					request.reply <- nil
					continue
				}
				if !primary.is(operationTurn) {
					request.reply <- ErrNoActiveTurn
					continue
				}
				turnSteering.close()
				err := c.runtime.HardCancel(request.ctx)
				primary.cancel(ErrHardCanceled)
				if err == nil {
					primary.recordTurnHardCancel()
				}
				request.reply <- err
			case commandCompact:
				if err := primary.admissionError(); err != nil {
					request.reply <- err
					continue
				}
				if evidence.workspaceRefreshPending() {
					request.reply <- ErrWorkspaceRefreshActive
					continue
				}
				operationCtx := primary.start(rootCtx, operationCompaction)
				go c.run(operationCtx, operationCompaction, frontend.TurnInput{}, nil, nil)
				request.reply <- nil
			case commandRename, commandArchive, commandUnarchive:
				if err := primary.admissionError(); err != nil {
					request.reply <- err
					continue
				}
				if backgroundCompactionActive() {
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
			case commandNewThread:
				request.reply <- ErrUnsupported
			case commandRefreshWorkspace:
				if err := primary.admissionError(); err != nil {
					request.reply <- err
					continue
				}
				if _, ok := c.runtime.(workspaceEvidenceRefresher); !ok {
					request.reply <- frontend.ErrWorkspaceRefreshUnsupported
					continue
				}
				admitEvidence(operationWorkspaceRefresh, request)
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
			case commandReview:
				runner, ok := c.runtime.(reviewRuntime)
				if !ok {
					request.reply <- ErrUnsupported
					continue
				}
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
					continue
				}
				if availability, declared := c.runtime.(reviewAvailability); declared &&
					!availability.ReviewAvailable() {
					request.reply <- ErrUnsupported
					continue
				}
				if err := primary.admissionError(); err != nil {
					request.reply <- err
					continue
				}
				if backgroundCompactionActive() {
					request.reply <- ErrCompactionActive
					continue
				}
				if evidence.workspaceRefreshPending() {
					request.reply <- ErrWorkspaceRefreshActive
					continue
				}
				reviewID := codingreview.NewID()
				if err := c.projector.ReviewEntered(reviewID, request.reviewTarget); err != nil {
					request.reply <- err
					continue
				}
				operationCtx := primary.startReview(rootCtx, reviewID)
				go c.runReview(operationCtx, runner, reviewID, request.reviewTarget)
				request.reply <- nil
			case commandAwaitTurn:
				switch {
				case primary.is(operationTurn):
					turnWaiters = append(turnWaiters, request)
				case turnSettlementAvailable:
					request.reply <- turnSettlementErr
				default:
					request.reply <- ErrNoActiveTurn
				}
			case commandClose:
				closeReplies = append(closeReplies, request.reply)
				if closing {
					continue
				}
				closing = true
				if primary.is(operationTurn) && !primary.turnHardCancelRequested() {
					turnSteering.close()
					err := c.runtime.HardCancel(context.WithoutCancel(request.ctx))
					closeErr = errors.Join(closeErr, err)
					if err == nil {
						primary.recordTurnHardCancel()
					}
					primary.cancel(ErrHardCanceled)
				} else if primary.is(operationCompaction) || primary.is(operationReview) {
					primary.cancel(context.Canceled)
				}
				evidence.cancelAll(context.Canceled)
				if finishClose() {
					return
				}
			}
		}
	}
}

func (c *Controller) run(
	ctx context.Context,
	kind operationKind,
	input frontend.TurnInput,
	ready func(),
	turnSteering *turnSteeringState,
) {
	var err error
	var projectErr error
	if kind == operationTurn {
		err = c.runtime.RunTurn(ctx, input, ready)
		turnSteering.close()
		projectErr = err
		if source, ok := c.runtime.(turnSettlementErrorSource); ok {
			err = errors.Join(err, source.TurnSettlementError())
		}
	} else {
		err = c.runtime.Compact(ctx)
	}
	c.results <- operationResult{kind: kind, projectErr: projectErr, err: err}
}

func (c *Controller) runReview(
	ctx context.Context,
	runner reviewRuntime,
	reviewID string,
	target codingreview.Target,
) {
	var emitMu sync.Mutex
	var emitErr error
	recordEmitError := func(err error) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if emitErr == nil {
			emitErr = err
		}
		return err
	}
	emit := func(event codingreview.Event) error {
		event = event.Clone()
		if err := event.Validate(); err != nil {
			return recordEmitError(err)
		}
		select {
		case c.reviewEvents <- reviewOperationEvent{reviewID: reviewID, event: event}:
			return nil
		case <-ctx.Done():
			return recordEmitError(context.Cause(ctx))
		case <-c.done:
			return recordEmitError(ErrClosed)
		}
	}
	var commitMu sync.Mutex
	commitCalled := false
	committed := false
	commit := func() error {
		commitMu.Lock()
		if commitCalled {
			commitMu.Unlock()
			return fmt.Errorf("coding review publication commit was already requested")
		}
		commitCalled = true
		commitMu.Unlock()
		request := reviewCommitRequest{reviewID: reviewID, reply: make(chan error, 1)}
		select {
		case c.reviewCommits <- request:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-c.done:
			return ErrClosed
		}
		// Once the coordinator accepts the request, its reply is the
		// linearization result. A concurrent cancellation must not race and
		// overwrite an already accepted publication commit.
		err := <-request.reply
		if err == nil {
			commitMu.Lock()
			committed = true
			commitMu.Unlock()
		}
		return err
	}
	result, err := runner.RunReview(ctx, reviewID, target, emit, commit)
	emitMu.Lock()
	err = errors.Join(err, emitErr)
	emitMu.Unlock()
	commitMu.Lock()
	resultCommitted := committed
	commitMu.Unlock()
	if err == nil {
		switch {
		case result.ReviewID != reviewID:
			err = fmt.Errorf("coding review result ID does not match active review")
		case result.Target != target:
			err = fmt.Errorf("coding review result target does not match active review")
		default:
			err = result.Validate()
		}
	}
	c.results <- operationResult{
		kind: operationReview, reviewID: reviewID, review: result, reviewCommitted: resultCommitted, err: err,
	}
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
	if result.err == nil && (kind == operationWorkspaceRefresh || kind == operationRepositoryStatus) {
		if reader, ok := c.runtime.(runtimeStatusReader); ok {
			status := reader.RuntimeStatus(ctx)
			result.runtimeStatus = &status
		}
	}
	if err := ctx.Err(); err != nil {
		result.err = err
	}
	c.evidenceResults <- result
}

func (c *Controller) projectOperationError(result operationResult) {
	err := result.err
	if result.kind == operationTurn {
		err = result.projectErr
	}
	if err == nil || isOnlyIntentionalCancellation(err) {
		return
	}
	switch result.kind {
	case operationTurn:
		c.projector.Error("", "controller:turn-error", "coding turn failed")
	case operationReview:
		c.projector.Error("", "controller:review-error", "coding review failed")
	default:
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
