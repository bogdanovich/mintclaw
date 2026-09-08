package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

const (
	MaxClientEvents          = 256
	MaxClientEventBytes      = 8 << 20
	maxClientAbandonedCalls  = 512
	clientEventEnvelopeBytes = 256
)

var (
	ErrClientClosed       = errors.New("coding worker client is closed")
	ErrWorkerDisconnected = errors.New("coding worker disconnected without a terminal event")
	ErrClientProtocol     = errors.New("coding worker client received an invalid protocol sequence")
)

type RemoteError struct {
	Method  Method
	Failure ProtocolError
}

func (failure *RemoteError) Error() string {
	if failure == nil {
		return "coding worker request failed"
	}
	return fmt.Sprintf("coding worker %s: %s", failure.Method, failure.Failure.Message)
}

type OutcomeUncertainError struct {
	Method Method
	Cause  error
}

func (failure *OutcomeUncertainError) Error() string {
	if failure == nil {
		return ErrControlStreamUncertain.Error()
	}
	return fmt.Sprintf("coding worker %s outcome is uncertain", failure.Method)
}

func (failure *OutcomeUncertainError) Unwrap() []error {
	if failure == nil {
		return []error{ErrControlStreamUncertain}
	}
	return []error{ErrControlStreamUncertain, failure.Cause}
}

type RetainedEvent struct {
	Cursor uint64
	Record Record
}

type EventPage struct {
	Events       []RetainedEvent
	NextCursor   uint64
	HistoryGap   bool
	RetainedFrom uint64
}

type pendingCall struct {
	method            Method
	reply             chan Record
	initializeBinding *Binding
}

type Client struct {
	input     io.ReadCloser
	output    io.WriteCloser
	reader    *wireReader
	writeGate chan struct{}
	prefix    string
	nextID    atomic.Uint64

	mu            sync.Mutex
	pending       map[string]pendingCall
	abandoned     map[string]pendingCall
	events        []RetainedEvent
	eventBytes    int
	nextCursor    uint64
	eventIdentity *ControlIdentity
	err           error
	done          chan struct{}
	wake          chan struct{}
	finishOnce    sync.Once
	closeOnce     sync.Once
	closeErr      error
}

func NewClient(input io.ReadCloser, output io.WriteCloser) (*Client, error) {
	if input == nil || output == nil {
		return nil, errors.New("coding worker client requires both pipe directions")
	}
	reader, err := newWireReader(input)
	if err != nil {
		return nil, err
	}
	client := &Client{
		input:     input,
		output:    output,
		reader:    reader,
		writeGate: make(chan struct{}, 1),
		prefix:    uuid.NewString(),
		pending:   make(map[string]pendingCall),
		abandoned: make(map[string]pendingCall),
		done:      make(chan struct{}),
		wake:      make(chan struct{}, 1),
	}
	client.writeGate <- struct{}{}
	go client.readRecords()
	return client, nil
}

func (client *Client) Initialize(
	ctx context.Context,
	idempotencyKey string,
	params InitializeParams,
) (InitializeResult, error) {
	result, err := client.Call(ctx, MethodInitialize, idempotencyKey, params)
	if err != nil {
		return InitializeResult{}, err
	}
	initialized, ok := result.(*InitializeResult)
	if !ok || initialized.Identity.Binding != params.Binding ||
		initialized.Identity.WorkerBuildID != params.Binding.ExpectedWorkerBuildID {
		client.finish(ErrClientProtocol)
		return InitializeResult{}, ErrClientProtocol
	}
	return *initialized, nil
}

func (client *Client) StartTurn(
	ctx context.Context,
	idempotencyKey string,
	params TurnStartParams,
) error {
	_, err := client.Call(ctx, MethodTurnStart, idempotencyKey, params)
	return err
}

func (client *Client) Steer(
	ctx context.Context,
	idempotencyKey string,
	params TurnSteerParams,
) error {
	_, err := client.Call(ctx, MethodTurnSteer, idempotencyKey, params)
	return err
}

func (client *Client) Interrupt(
	ctx context.Context,
	idempotencyKey string,
	params GenerationParams,
) error {
	_, err := client.Call(ctx, MethodTurnInterrupt, idempotencyKey, params)
	return err
}

func (client *Client) Cancel(
	ctx context.Context,
	idempotencyKey string,
	params GenerationParams,
) error {
	_, err := client.Call(ctx, MethodTurnCancel, idempotencyKey, params)
	return err
}

func (client *Client) ReadSnapshot(ctx context.Context, params GenerationParams) (SnapshotResult, error) {
	result, err := client.Call(ctx, MethodSnapshotRead, "", params)
	if err != nil {
		return SnapshotResult{}, err
	}
	snapshot, ok := result.(*SnapshotResult)
	if !ok || snapshot.ControlIdentity != params.ControlIdentity {
		client.finish(ErrClientProtocol)
		return SnapshotResult{}, ErrClientProtocol
	}
	return *snapshot, nil
}

func (client *Client) Shutdown(
	ctx context.Context,
	idempotencyKey string,
	params GenerationParams,
) error {
	_, err := client.Call(ctx, MethodShutdown, idempotencyKey, params)
	return err
}

// Call sends one request exactly once. A mutating request whose response is
// lost after any bytes reached the pipe returns OutcomeUncertainError; callers
// must reconcile rather than replaying it automatically.
func (client *Client) Call(
	ctx context.Context,
	method Method,
	idempotencyKey string,
	params any,
) (any, error) {
	if client == nil {
		return nil, ErrClientClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := MarshalPayload(params)
	if err != nil {
		return nil, err
	}
	request := Record{
		SchemaVersion:  ProtocolV1,
		Type:           RecordRequest,
		ID:             client.requestID(),
		Method:         method,
		IdempotencyKey: idempotencyKey,
		Params:         raw,
	}
	if _, err = Encode(request); err != nil {
		return nil, err
	}

	select {
	case <-client.writeGate:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.done:
		return nil, client.closedError()
	}

	call := pendingCall{method: method, reply: make(chan Record, 1)}
	if method == MethodInitialize {
		decoded, decodeErr := DecodeRequestPayload(method, raw)
		if decodeErr != nil {
			client.writeGate <- struct{}{}
			return nil, decodeErr
		}
		binding := decoded.(*InitializeParams).Binding
		call.initializeBinding = &binding
	}
	client.mu.Lock()
	if client.err != nil || client.isDoneLocked() {
		closedErr := client.closedErrorLocked()
		client.mu.Unlock()
		client.writeGate <- struct{}{}
		return nil, closedErr
	}
	client.pending[request.ID] = call
	client.mu.Unlock()

	written, writeErr := writeWireRecord(client.output, request)
	client.writeGate <- struct{}{}
	if writeErr != nil {
		client.abandon(request.ID)
		client.finish(writeErr)
		return nil, classifyCallFailure(method, written > 0, writeErr)
	}
	return client.awaitResponse(ctx, request, call)
}

func (client *Client) awaitResponse(
	ctx context.Context,
	request Record,
	call pendingCall,
) (any, error) {
	for {
		select {
		case response := <-call.reply:
			return responseResult(response)
		case <-ctx.Done():
			select {
			case response := <-call.reply:
				return responseResult(response)
			default:
			}
			if !client.abandon(request.ID) {
				select {
				case response := <-call.reply:
					return responseResult(response)
				default:
				}
			}
			return nil, classifyCallFailure(request.Method, true, ctx.Err())
		case <-client.done:
			select {
			case response := <-call.reply:
				return responseResult(response)
			default:
			}
			client.abandon(request.ID)
			return nil, classifyCallFailure(request.Method, true, client.closedError())
		}
	}
}

func responseResult(response Record) (any, error) {
	if response.OK == nil {
		return nil, ErrClientProtocol
	}
	if !*response.OK {
		if response.Error == nil {
			return nil, ErrClientProtocol
		}
		return nil, &RemoteError{Method: response.Method, Failure: *response.Error}
	}
	return DecodeResultPayload(response.Method, response.Result)
}

func classifyCallFailure(method Method, requestMayBeAccepted bool, cause error) error {
	if requestMayBeAccepted && method.RequiresIdempotencyKey() {
		return &OutcomeUncertainError{Method: method, Cause: cause}
	}
	return cause
}

func (client *Client) EventsAfter(cursor uint64) EventPage {
	if client == nil {
		return EventPage{HistoryGap: true}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	page := EventPage{NextCursor: client.nextCursor}
	if len(client.events) == 0 {
		page.HistoryGap = cursor > client.nextCursor
		return page
	}
	page.RetainedFrom = client.events[0].Cursor
	page.HistoryGap = cursor > client.nextCursor || cursor+1 < page.RetainedFrom
	for _, event := range client.events {
		if event.Cursor <= cursor {
			continue
		}
		page.Events = append(page.Events, RetainedEvent{
			Cursor: event.Cursor,
			Record: cloneRecord(event.Record),
		})
	}
	return page
}

// Wake is coalesced. Callers drain EventsAfter and use snapshot.read when the
// returned page reports HistoryGap.
func (client *Client) Wake() <-chan struct{} {
	if client == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return client.wake
}

func (client *Client) Done() <-chan struct{} {
	if client == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return client.done
}

func (client *Client) Err() error {
	if client == nil {
		return ErrClientClosed
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.err
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.finish(ErrClientClosed)
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closeErr
}

func (client *Client) readRecords() {
	for {
		received := client.reader.read()
		if received.err != nil {
			client.finish(errors.Join(ErrWorkerDisconnected, received.err))
			return
		}
		switch received.record.Type {
		case RecordResponse:
			if !client.deliverResponse(received.record) {
				client.finish(ErrClientProtocol)
				return
			}
		case RecordEvent:
			if !client.retainEvent(received.record) {
				client.finish(ErrClientProtocol)
				return
			}
			if received.record.Event == EventWorkerStopped {
				client.finish(nil)
				return
			}
		default:
			client.finish(ErrClientProtocol)
			return
		}
	}
}

func (client *Client) deliverResponse(response Record) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	pending, exists := client.pending[response.ID]
	if exists {
		if pending.method != response.Method {
			return false
		}
		if pending.initializeBinding != nil &&
			!client.bindInitializedResponseLocked(response, *pending.initializeBinding) {
			return false
		}
		delete(client.pending, response.ID)
		pending.reply <- cloneRecord(response)
		return true
	}
	abandonedCall, abandoned := client.abandoned[response.ID]
	if abandoned && abandonedCall.method == response.Method {
		if abandonedCall.initializeBinding != nil &&
			!client.bindInitializedResponseLocked(response, *abandonedCall.initializeBinding) {
			return false
		}
		delete(client.abandoned, response.ID)
		return true
	}
	return false
}

func (client *Client) bindInitializedResponseLocked(response Record, binding Binding) bool {
	if response.OK == nil || !*response.OK {
		return true
	}
	result, err := DecodeResultPayload(MethodInitialize, response.Result)
	if err != nil {
		return false
	}
	initialized, ok := result.(*InitializeResult)
	if !ok || initialized.Identity.Binding != binding ||
		initialized.Identity.WorkerBuildID != binding.ExpectedWorkerBuildID {
		return false
	}
	identity := binding.ControlIdentity()
	if client.eventIdentity != nil && *client.eventIdentity != identity {
		return false
	}
	client.eventIdentity = &identity
	return true
}

func (client *Client) retainEvent(record Record) bool {
	identity, ok := eventControlIdentity(record)
	if !ok {
		return false
	}
	encodedBytes := len(record.Payload) + clientEventEnvelopeBytes
	client.mu.Lock()
	if client.eventIdentity == nil || *client.eventIdentity != identity {
		client.mu.Unlock()
		return false
	}
	client.nextCursor++
	client.events = append(client.events, RetainedEvent{Cursor: client.nextCursor, Record: cloneRecord(record)})
	client.eventBytes += encodedBytes
	for len(client.events) > MaxClientEvents || client.eventBytes > MaxClientEventBytes {
		evicted := client.events[0]
		client.eventBytes -= len(evicted.Record.Payload) + clientEventEnvelopeBytes
		client.events = client.events[1:]
	}
	client.mu.Unlock()
	select {
	case client.wake <- struct{}{}:
	default:
	}
	return true
}

func eventControlIdentity(record Record) (ControlIdentity, bool) {
	payload, err := DecodeEventPayload(record.Event, record.Payload)
	if err != nil {
		return ControlIdentity{}, false
	}
	switch typed := payload.(type) {
	case *WorkerReadyPayload:
		return typed.ControlIdentity, true
	case *ItemUpdatedPayload:
		return typed.ControlIdentity, true
	case *StatusChangedPayload:
		return typed.ControlIdentity, true
	case *QuestionStatePayload:
		return typed.ControlIdentity, true
	case *ContextUsagePayload:
		return typed.ControlIdentity, true
	case *TurnTerminalPayload:
		return typed.ControlIdentity, true
	case *WorkerStoppedPayload:
		return typed.ControlIdentity, true
	default:
		return ControlIdentity{}, false
	}
}

func (client *Client) abandon(requestID string) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	call, exists := client.pending[requestID]
	if !exists {
		return false
	}
	delete(client.pending, requestID)
	if len(client.abandoned) < maxClientAbandonedCalls {
		client.abandoned[requestID] = call
	}
	return true
}

func (client *Client) requestID() string {
	return fmt.Sprintf("request-%s-%d", client.prefix, client.nextID.Add(1))
}

func (client *Client) finish(err error) {
	client.finishOnce.Do(func() {
		client.mu.Lock()
		client.err = err
		client.mu.Unlock()
		close(client.done)
		select {
		case client.wake <- struct{}{}:
		default:
		}
		client.closeOnce.Do(func() {
			closeErr := errors.Join(client.output.Close(), client.input.Close())
			client.mu.Lock()
			client.closeErr = closeErr
			client.mu.Unlock()
		})
	})
}

func (client *Client) isDoneLocked() bool {
	select {
	case <-client.done:
		return true
	default:
		return false
	}
}

func (client *Client) closedError() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closedErrorLocked()
}

func (client *Client) closedErrorLocked() error {
	if client.err != nil {
		return client.err
	}
	return ErrClientClosed
}
