package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const (
	MaxServerIdempotencyEntries = 512
	DefaultWorkerIdleTimeout    = 5 * time.Minute
	MaxWorkerIdleTimeout        = 30 * time.Minute
	serverCleanupTimeout        = 5 * time.Second
	serverInputQueue            = 4
)

var (
	ErrControlStreamUncertain = errors.New("coding worker control stream outcome is uncertain")
	ErrTaskCanceled           = errors.New("coding worker task was canceled")
	ErrTaskFailed             = errors.New("coding worker task failed")
	ErrWorkerIdleTimeout      = errors.New("coding worker did not start before its idle timeout")
	errQuestionNotSteerable   = errors.New("coding worker question is not steerable")
	errQuestionConflict       = errors.New("coding worker question identity conflicts")
)

// TaskController is the complete in-process capability required by one
// task-scoped worker. It deliberately excludes terminal and process concerns.
type TaskController interface {
	frontend.Controller
	frontend.Steerer
	frontend.TurnSettler
}

type ControllerFactory func(context.Context, Binding) (TaskController, error)

type ServerConfig struct {
	BuildID     string
	Open        ControllerFactory
	IdleTimeout time.Duration
}

type Server struct {
	buildID     string
	open        ControllerFactory
	idleTimeout time.Duration
}

func NewServer(config ServerConfig) (*Server, error) {
	if !validBuildID(config.BuildID) {
		return nil, errors.New("coding worker server build identity is invalid")
	}
	if config.Open == nil {
		return nil, errors.New("coding worker controller factory is required")
	}
	idleTimeout := config.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = DefaultWorkerIdleTimeout
	}
	if idleTimeout < 0 || idleTimeout > MaxWorkerIdleTimeout {
		return nil, errors.New("coding worker idle timeout is invalid")
	}
	return &Server{buildID: config.BuildID, open: config.Open, idleTimeout: idleTimeout}, nil
}

// Serve runs one private inherited-pipe worker session. It accepts one bound
// task turn and returns after that turn settles, shutdown, or transport loss.
func (server *Server) Serve(ctx context.Context, input io.ReadCloser, output io.Writer) error {
	if server == nil || server.open == nil || !validBuildID(server.buildID) {
		return errors.New("coding worker server is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil || output == nil {
		return errors.New("coding worker server requires inherited input and output")
	}
	reader, err := newWireReader(input)
	if err != nil {
		return err
	}
	serveCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	defer func() { _ = input.Close() }()

	incoming := make(chan wireReadResult, serverInputQueue)
	go readWorkerRequests(serveCtx, reader, incoming)
	idleTimer := time.NewTimer(server.idleTimeout)
	idle := idleTimer.C
	defer idleTimer.Stop()

	session := serverSession{
		server:      server,
		idempotency: make(map[string]cachedOperation),
	}
	defer func() { _ = session.closeController() }()

	for {
		select {
		case received := <-incoming:
			if received.err != nil {
				if errors.Is(received.err, ErrIncompatibleProtocol) {
					if response, ok := incompatibleInitializeResponse(received.raw); ok {
						_, _ = writeWireRecord(output, response)
					}
				}
				_ = session.closeController()
				_ = session.emitStopped(output, WorkerStopFailed, protocolError(ErrorUncertain))
				return ErrControlStreamUncertain
			}
			response, events, stop := session.handle(serveCtx, received.record)
			if _, err = writeWireRecord(output, response); err != nil {
				return ErrControlStreamUncertain
			}
			for _, event := range events {
				if _, err = writeWireRecord(output, event); err != nil {
					return ErrControlStreamUncertain
				}
			}
			if stop {
				return session.finishShutdown(output, response.Error)
			}
			if session.turnStarted {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idle = nil
			}
		case source, open := <-session.updates:
			if !open {
				session.updates = nil
				continue
			}
			if err = session.publishSnapshot(serveCtx, output, source); err != nil {
				_ = session.closeController()
				if !errors.Is(err, ErrControlStreamUncertain) {
					if stopErr := session.emitStopped(
						output,
						WorkerStopFailed,
						protocolError(ErrorInternal),
					); stopErr != nil {
						return ErrControlStreamUncertain
					}
				}
				return err
			}
		case settlement := <-session.turnDone:
			return session.finishTurn(output, settlement)
		case <-idle:
			if closeErr := session.closeController(); closeErr != nil {
				if err = session.emitStopped(output, WorkerStopFailed, protocolError(ErrorInternal)); err != nil {
					return ErrControlStreamUncertain
				}
				return ErrTaskFailed
			}
			if err = session.emitStopped(output, WorkerStopIdle, nil); err != nil {
				return ErrControlStreamUncertain
			}
			return ErrWorkerIdleTimeout
		case <-serveCtx.Done():
			_ = session.closeController()
			_ = session.emitStopped(output, WorkerStopFailed, protocolError(ErrorUncertain))
			if errors.Is(context.Cause(serveCtx), context.Canceled) && ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrControlStreamUncertain
		}
	}
}

func readWorkerRequests(
	ctx context.Context,
	reader *wireReader,
	incoming chan<- wireReadResult,
) {
	for {
		received := reader.read()
		select {
		case incoming <- received:
		case <-ctx.Done():
			return
		}
		if received.err != nil {
			return
		}
	}
}

type cachedOperation struct {
	method      Method
	fingerprint [sha256.Size]byte
	response    Record
}

type serverSession struct {
	server           *Server
	controller       TaskController
	binding          *Binding
	state            projectedState
	updates          <-chan frontend.ThreadSnapshot
	subscribeCancel  context.CancelFunc
	turnDone         <-chan error
	turnStarted      bool
	controllerClosed bool
	acceptedQuestion *QuestionAnswerRef
	idempotency      map[string]cachedOperation
}

func (session *serverSession) handle(
	ctx context.Context,
	request Record,
) (Record, []Record, bool) {
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return failedResponse(request, protocolError(ErrorInvalidRequest)), nil, false
	}
	if request.Method.RequiresIdempotencyKey() {
		if cached, exists := session.idempotency[request.IdempotencyKey]; exists {
			if cached.method != request.Method || cached.fingerprint != fingerprint {
				return failedResponse(request, protocolError(ErrorInvalidRequest)), nil, false
			}
			response := cloneRecord(cached.response)
			response.ID = request.ID
			return response, nil, false
		}
		if len(session.idempotency) >= MaxServerIdempotencyEntries {
			return failedResponse(request, protocolError(ErrorWorkerStopping)), nil, true
		}
	}

	response, events, stop := session.execute(ctx, request)
	if request.Method.RequiresIdempotencyKey() {
		session.idempotency[request.IdempotencyKey] = cachedOperation{
			method: request.Method, fingerprint: fingerprint, response: cloneRecord(response),
		}
	}
	return response, events, stop
}

func (session *serverSession) execute(
	ctx context.Context,
	request Record,
) (Record, []Record, bool) {
	if request.Method == MethodInitialize {
		return session.initialize(ctx, request)
	}
	if session.controller == nil || session.binding == nil {
		return failedResponse(request, protocolError(ErrorNotInitialized)), nil, false
	}
	payload, err := DecodeRequestPayload(request.Method, request.Params)
	if err != nil {
		return failedResponse(request, protocolError(ErrorInvalidRequest)), nil, false
	}
	identity := generationIdentity(payload)
	if identity == nil || session.binding.Authorize(*identity) != nil {
		return failedResponse(request, protocolError(ErrorIdentityMismatch)), nil, false
	}

	switch typed := payload.(type) {
	case *TurnStartParams:
		if session.turnStarted {
			return failedResponse(request, protocolError(ErrorTurnActive)), nil, false
		}
		if err = session.controller.Submit(ctx, typed.FrontendInput()); err == nil {
			session.turnStarted = true
			turnDone := make(chan error, 1)
			session.turnDone = turnDone
			go func() { turnDone <- session.controller.AwaitTurn(ctx) }()
		}
	case *TurnSteerParams:
		steerID := request.IdempotencyKey
		if typed.QuestionAnswer != nil {
			if err = session.authorizeQuestion(*typed.QuestionAnswer); err == nil {
				steerID = typed.QuestionAnswer.AnswerID
			}
		}
		if err == nil {
			err = session.controller.Steer(ctx, frontend.SteerInput{ID: steerID, Text: typed.Text})
			if err == nil && typed.QuestionAnswer != nil {
				accepted := *typed.QuestionAnswer
				session.acceptedQuestion = &accepted
			}
		}
	case *GenerationParams:
		switch request.Method {
		case MethodTurnInterrupt:
			err = session.controller.Interrupt(ctx)
		case MethodTurnCancel:
			err = session.controller.HardCancel(ctx)
		case MethodSnapshotRead:
			return session.snapshotResponse(ctx, request)
		case MethodShutdown:
			if session.turnStarted {
				return failedResponse(request, protocolError(ErrorTurnActive)), nil, false
			}
			err = session.closeController()
			if err != nil {
				return failedResponse(request, mapControllerError(request.Method, err)), nil, true
			}
			return successfulResponse(request, AckResult{}), nil, true
		}
	default:
		err = errors.New("unsupported coding worker request payload")
	}
	if err != nil {
		return failedResponse(request, mapControllerError(request.Method, err)), nil, false
	}
	return successfulResponse(request, AckResult{}), nil, false
}

func (session *serverSession) initialize(
	ctx context.Context,
	request Record,
) (Record, []Record, bool) {
	if session.controller != nil || session.binding != nil {
		return failedResponse(request, protocolError(ErrorInvalidRequest)), nil, false
	}
	payload, err := DecodeRequestPayload(MethodInitialize, request.Params)
	if err != nil {
		code := ErrorInvalidRequest
		if errors.Is(err, ErrIncompatibleProtocol) {
			code = ErrorUnsupportedVersion
		}
		return failedResponse(request, protocolError(code)), nil, true
	}
	params := payload.(*InitializeParams)
	if params.Binding.ExpectedWorkerBuildID != session.server.buildID {
		return failedResponse(request, protocolError(ErrorUnsupportedVersion)), nil, true
	}
	controllerInstance, err := session.server.open(ctx, params.Binding)
	if err != nil || controllerInstance == nil {
		return failedResponse(request, protocolError(ErrorInternal)), nil, true
	}
	subscribeCtx, cancelSubscribe := context.WithCancel(ctx)
	initial, updates, err := controllerInstance.Subscribe(subscribeCtx)
	if err != nil {
		cancelSubscribe()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), serverCleanupTimeout)
		_ = controllerInstance.Close(closeCtx)
		cancelClose()
		return failedResponse(request, protocolError(ErrorInternal)), nil, true
	}
	state, err := projectControllerState(ctx, params.Binding.ControlIdentity(), controllerInstance, initial)
	if err != nil {
		cancelSubscribe()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), serverCleanupTimeout)
		_ = controllerInstance.Close(closeCtx)
		cancelClose()
		return failedResponse(request, protocolError(ErrorInternal)), nil, true
	}
	binding := params.Binding
	session.binding = &binding
	session.controller = controllerInstance
	session.state = state
	session.updates = updates
	session.subscribeCancel = cancelSubscribe
	result := InitializeResult{Identity: BoundIdentity{
		ProtocolVersion: ProtocolV1,
		WorkerBuildID:   session.server.buildID,
		Binding:         binding,
	}}
	ready, err := eventRecord(projectedEvent{
		name: EventWorkerReady,
		payload: WorkerReadyPayload{
			ControlIdentity: binding.ControlIdentity(),
			Snapshot:        state.snapshot,
		},
	})
	if err != nil {
		return failedResponse(request, protocolError(ErrorInternal)), nil, true
	}
	return successfulResponse(request, result), []Record{ready}, false
}

func (session *serverSession) snapshotResponse(ctx context.Context, request Record) (Record, []Record, bool) {
	source, err := session.controller.Snapshot(ctx)
	if err != nil {
		return failedResponse(request, mapControllerError(request.Method, err)), nil, false
	}
	state, err := projectControllerState(ctx, session.binding.ControlIdentity(), session.controller, source)
	if err != nil {
		return failedResponse(request, protocolError(ErrorInternal)), nil, false
	}
	// A question returned by snapshot.read is immediately eligible for exact
	// correlation even if its coalesced subscription update is still queued.
	// Keep the item/status event baseline unchanged so an older queued snapshot
	// cannot regress or suppress subsequent semantic events.
	session.state.question = state.question
	return successfulResponse(request, SnapshotResult{
		ControlIdentity: session.binding.ControlIdentity(),
		Snapshot:        state.snapshot,
	}), nil, false
}

func (session *serverSession) authorizeQuestion(reference QuestionAnswerRef) error {
	question := session.state.question
	if question == nil || question.Status != QuestionWaiting {
		return errQuestionNotSteerable
	}
	if question.QuestionID != reference.QuestionID || question.Revision != reference.QuestionRevision {
		return errQuestionConflict
	}
	if session.acceptedQuestion != nil &&
		session.acceptedQuestion.QuestionID == reference.QuestionID &&
		session.acceptedQuestion.QuestionRevision == reference.QuestionRevision {
		return errQuestionConflict
	}
	return nil
}

func (session *serverSession) publishSnapshot(
	ctx context.Context,
	output io.Writer,
	source frontend.ThreadSnapshot,
) error {
	if session.controller == nil || session.binding == nil {
		return nil
	}
	next, err := projectControllerState(ctx, session.binding.ControlIdentity(), session.controller, source)
	if err != nil {
		return ErrTaskFailed
	}
	for _, event := range eventsBetween(session.binding.ControlIdentity(), session.state, next) {
		record, eventErr := eventRecord(event)
		if eventErr != nil {
			return ErrTaskFailed
		}
		if _, eventErr = writeWireRecord(output, record); eventErr != nil {
			return ErrControlStreamUncertain
		}
	}
	session.state = next
	return nil
}

func (session *serverSession) finishTurn(output io.Writer, settlement error) error {
	ctx, cancel := context.WithTimeout(context.Background(), serverCleanupTimeout)
	defer cancel()
	var finalizationErr error
	if session.controller != nil {
		source, err := session.controller.Snapshot(ctx)
		if err == nil {
			if publishErr := session.publishSnapshot(ctx, output, source); publishErr != nil {
				finalizationErr = errors.Join(finalizationErr, publishErr)
			}
		} else {
			finalizationErr = errors.Join(finalizationErr, err)
		}
	}
	closeErr := session.closeController()
	finalizationErr = errors.Join(finalizationErr, closeErr)
	reason, returnErr := session.turnStopOutcome(settlement, finalizationErr)
	var stoppedError *ProtocolError
	if reason == WorkerStopFailed {
		stoppedError = protocolError(ErrorInternal)
	}
	if err := session.emitStopped(output, reason, stoppedError); err != nil {
		return ErrControlStreamUncertain
	}
	return returnErr
}

func (session *serverSession) turnStopOutcome(
	settlement error,
	finalizationErr error,
) (WorkerStopReason, error) {
	if finalizationErr != nil {
		return WorkerStopFailed, ErrTaskFailed
	}
	if session.state.snapshot.LastTurn != nil {
		switch session.state.snapshot.LastTurn.Outcome {
		case TurnOutcomeCompleted:
			if settlement == nil {
				return WorkerStopCompleted, nil
			}
		case TurnOutcomeInterrupted:
			return WorkerStopCanceled, ErrTaskCanceled
		}
	}
	if errors.Is(settlement, context.Canceled) || errors.Is(settlement, controller.ErrHardCanceled) {
		return WorkerStopCanceled, ErrTaskCanceled
	}
	return WorkerStopFailed, ErrTaskFailed
}

func (session *serverSession) finishShutdown(output io.Writer, requestError *ProtocolError) error {
	closeErr := session.closeController()
	if requestError != nil || closeErr != nil {
		if err := session.emitStopped(output, WorkerStopFailed, protocolError(ErrorInternal)); err != nil {
			return ErrControlStreamUncertain
		}
		return ErrTaskFailed
	}
	if err := session.emitStopped(output, WorkerStopShutdown, nil); err != nil {
		return ErrControlStreamUncertain
	}
	return nil
}

func (session *serverSession) emitStopped(
	output io.Writer,
	reason WorkerStopReason,
	protocolFailure *ProtocolError,
) error {
	if session.binding == nil {
		return nil
	}
	record, err := eventRecord(projectedEvent{
		name: EventWorkerStopped,
		payload: WorkerStoppedPayload{
			ControlIdentity: session.binding.ControlIdentity(),
			Reason:          reason,
			Error:           protocolFailure,
		},
	})
	if err != nil {
		return err
	}
	_, err = writeWireRecord(output, record)
	return err
}

func (session *serverSession) closeController() error {
	if session.controller == nil || session.controllerClosed {
		return nil
	}
	session.controllerClosed = true
	if session.subscribeCancel != nil {
		session.subscribeCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverCleanupTimeout)
	defer cancel()
	return session.controller.Close(ctx)
}

func generationIdentity(payload any) *ControlIdentity {
	switch typed := payload.(type) {
	case *TurnStartParams:
		return &typed.ControlIdentity
	case *TurnSteerParams:
		return &typed.ControlIdentity
	case *GenerationParams:
		return &typed.ControlIdentity
	default:
		return nil
	}
}

func requestFingerprint(request Record) ([sha256.Size]byte, error) {
	payload, err := DecodeRequestPayload(request.Method, request.Params)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(append([]byte(request.Method+"\x00"), canonical...)), nil
}

func successfulResponse(request Record, result any) Record {
	raw, err := MarshalPayload(result)
	if err != nil {
		return failedResponse(request, protocolError(ErrorInternal))
	}
	ok := true
	return Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            request.ID,
		Method:        request.Method,
		OK:            &ok,
		Result:        raw,
	}
}

func failedResponse(request Record, protocolFailure *ProtocolError) Record {
	ok := false
	return Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            request.ID,
		Method:        request.Method,
		OK:            &ok,
		Error:         protocolFailure,
	}
}

func protocolError(code ErrorCode) *ProtocolError {
	messages := map[ErrorCode]string{
		ErrorInvalidRequest:     "request is invalid",
		ErrorUnsupportedVersion: "worker protocol or build is incompatible",
		ErrorNotInitialized:     "worker is not initialized",
		ErrorIdentityMismatch:   "worker control identity does not match",
		ErrorTurnActive:         "coding task turn is already active",
		ErrorNoActiveTurn:       "coding task has no active turn",
		ErrorTurnNotSteerable:   "coding operation is not steerable",
		ErrorSteerConflict:      "steer identity conflicts with accepted guidance",
		ErrorSteerLimit:         "coding turn steer limit reached",
		ErrorWorkerStopping:     "coding worker is stopping",
		ErrorCanceled:           "coding operation was canceled",
		ErrorInterrupted:        "coding operation was interrupted",
		ErrorUncertain:          "coding worker control outcome is uncertain",
		ErrorInternal:           "coding worker operation failed",
	}
	message := messages[code]
	if strings.TrimSpace(message) == "" {
		code = ErrorInternal
		message = messages[code]
	}
	return &ProtocolError{Code: code, Message: message}
}

func mapControllerError(method Method, err error) *ProtocolError {
	switch {
	case errors.Is(err, errQuestionNotSteerable),
		method == MethodTurnSteer && errors.Is(err, frontend.ErrCommandUnsupported),
		method == MethodTurnSteer && (errors.Is(err, controller.ErrCompactionActive) ||
			errors.Is(err, controller.ErrReviewActive)):
		return protocolError(ErrorTurnNotSteerable)
	case errors.Is(err, errQuestionConflict), errors.Is(err, controller.ErrSteerConflict):
		return protocolError(ErrorSteerConflict)
	case errors.Is(err, controller.ErrSteerLimit):
		return protocolError(ErrorSteerLimit)
	case errors.Is(err, controller.ErrTurnActive):
		return protocolError(ErrorTurnActive)
	case errors.Is(err, controller.ErrNoActiveTurn):
		return protocolError(ErrorNoActiveTurn)
	case errors.Is(err, controller.ErrClosed):
		return protocolError(ErrorWorkerStopping)
	case errors.Is(err, controller.ErrHardCanceled), errors.Is(err, context.Canceled):
		return protocolError(ErrorCanceled)
	default:
		return protocolError(ErrorInternal)
	}
}

func incompatibleInitializeResponse(raw []byte) (Record, bool) {
	var envelope struct {
		SchemaVersion int        `json:"schema_version"`
		Type          RecordType `json:"type"`
		ID            string     `json:"id"`
		Method        Method     `json:"method"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.SchemaVersion != ProtocolV1 ||
		envelope.Type != RecordRequest || envelope.Method != MethodInitialize || !validIdentifier(envelope.ID) {
		return Record{}, false
	}
	return failedResponse(
		Record{ID: envelope.ID, Method: envelope.Method},
		protocolError(ErrorUnsupportedVersion),
	), true
}
