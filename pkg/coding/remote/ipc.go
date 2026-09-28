package remote

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	defaultIOTimeout      = 5 * time.Second
	defaultDialTimeout    = 2 * time.Second
	defaultMaxConnections = 16
	defaultExecutionLimit = 2 * time.Minute
	defaultResponseGrace  = 2 * time.Second
)

// Handler processes one already authenticated, strictly decoded request.
// Implementations must return a response carrying the same request ID.
type Handler interface {
	HandleCodingRemote(context.Context, Request) Response
}

type HandlerFunc func(context.Context, Request) Response

func (handler HandlerFunc) HandleCodingRemote(ctx context.Context, request Request) Response {
	return handler(ctx, request)
}

// Server owns one same-user Unix socket. Each connection carries exactly one
// bounded request and response, which keeps framing and cancellation closed.
type Server struct {
	listener *net.UnixListener
	handler  Handler
	uid      uint32
	peerUID  func(*net.UnixConn) (uint32, error)

	ctx       context.Context
	cancel    context.CancelFunc
	semaphore chan struct{}
	done      chan struct{}

	closeOnce sync.Once
	errMu     sync.Mutex
	serveErr  error
}

// StartServer validates and binds socketPath before returning. The accept loop
// is stopped by parent cancellation or Close.
func StartServer(parent context.Context, socketPath string, handler Handler) (*Server, error) {
	return startServer(parent, socketPath, handler, unixPeerUID)
}

func startServer(
	parent context.Context,
	socketPath string,
	handler Handler,
	peerUID func(*net.UnixConn) (uint32, error),
) (*Server, error) {
	if parent == nil || handler == nil {
		return nil, errors.New("coding remote server requires a context and handler")
	}
	if peerUID == nil {
		return nil, errors.New("coding remote server requires peer authentication")
	}
	if err := prepareOwnerSocketPath(socketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen coding remote socket: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err = os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect coding remote socket: %w", err)
	}
	if err = validateOwnerSocketEndpoint(socketPath); err != nil {
		_ = listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	server := &Server{
		listener: listener, handler: handler, uid: currentUID(), peerUID: peerUID,
		ctx: ctx, cancel: cancel, semaphore: make(chan struct{}, defaultMaxConnections), done: make(chan struct{}),
	}
	go server.serve()
	return server, nil
}

func (server *Server) serve() {
	defer close(server.done)
	defer server.cancel()
	var workers sync.WaitGroup
	defer workers.Wait()
	go func() {
		<-server.ctx.Done()
		_ = server.listener.Close()
	}()
	for {
		connection, err := server.listener.AcceptUnix()
		if err != nil {
			if server.ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				server.setServeError(fmt.Errorf("accept coding remote connection: %w", err))
			}
			return
		}
		select {
		case server.semaphore <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-server.semaphore }()
				defer func() { _ = connection.Close() }()
				server.handleConnection(connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (server *Server) handleConnection(connection *net.UnixConn) {
	if server == nil || connection == nil || server.peerUID == nil {
		return
	}
	uid, err := server.peerUID(connection)
	if err != nil || uid != server.uid {
		return
	}
	if err = connection.SetReadDeadline(time.Now().Add(defaultIOTimeout)); err != nil {
		return
	}
	raw, err := readFrame(connection)
	if err != nil {
		return
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return
	}
	handlerDeadline := time.Now().Add(defaultIOTimeout)
	writeDeadline := handlerDeadline.Add(defaultResponseGrace)
	if request.Operation != OperationCapabilitiesList {
		now := time.Now()
		requestDeadline := time.UnixMilli(request.DeadlineUnixMS)
		if !requestDeadline.After(now) || requestDeadline.After(now.Add(defaultExecutionLimit)) {
			_ = connection.SetWriteDeadline(writeDeadline)
			_ = writeFrame(connection, Response{
				Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseDenied,
				Code: "DEADLINE_INVALID", Message: "coding remote execution deadline is invalid",
			})
			return
		}
		handlerDeadline = requestDeadline
		writeDeadline = requestDeadline.Add(defaultResponseGrace)
	}
	handlerCtx, cancel := context.WithDeadline(server.ctx, handlerDeadline)
	defer cancel()
	if err = connection.SetWriteDeadline(writeDeadline); err != nil {
		return
	}
	response := server.handler.HandleCodingRemote(handlerCtx, request)
	if response.RequestID != request.RequestID || response.Validate() != nil {
		return
	}
	_ = writeFrame(connection, response)
}

// Close stops admission, drains active bounded requests, and returns any
// accept-loop failure. It is idempotent.
func (server *Server) Close(ctx context.Context) error {
	if server == nil {
		return nil
	}
	server.closeOnce.Do(func() {
		server.cancel()
		_ = server.listener.Close()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-server.done:
		server.errMu.Lock()
		defer server.errMu.Unlock()
		return server.serveErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (server *Server) setServeError(err error) {
	server.errMu.Lock()
	server.serveErr = err
	server.errMu.Unlock()
}

// Client connects only to an explicitly configured owner-only endpoint.
type Client struct {
	socketPath string
	uid        uint32
	peerUID    func(*net.UnixConn) (uint32, error)
	dial       func(context.Context, string) (net.Conn, error)
}

// BrokerClient is the same-user transport used by the trusted local coding
// composition root.
type BrokerClient interface {
	Discover(context.Context, Request) (CapabilitySnapshot, error)
	Execute(context.Context, Request) (CapabilityResult, error)
	Artifact(context.Context, Request) (ArtifactResult, error)
}

func NewClient(socketPath string) (*Client, error) {
	if err := validateSocketPathSyntax(socketPath); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	return &Client{
		socketPath: socketPath, uid: currentUID(), peerUID: unixPeerUID,
		dial: func(ctx context.Context, path string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
	}, nil
}

func (client *Client) Discover(ctx context.Context, request Request) (CapabilitySnapshot, error) {
	if request.Operation != OperationCapabilitiesList {
		return CapabilitySnapshot{}, fmt.Errorf("%w: unsupported discovery operation", ErrInvalidMessage)
	}
	response, err := client.roundTrip(ctx, request)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	if response.Status != ResponseOK || response.Snapshot == nil {
		return CapabilitySnapshot{}, brokerResponseError(response)
	}
	if response.Snapshot.Grant != request.Grant ||
		response.Snapshot.GrantRevision != request.GrantRevision {
		return CapabilitySnapshot{}, fmt.Errorf("%w: response grant authority mismatch", ErrInvalidMessage)
	}
	return *response.Snapshot, nil
}

// Execute performs one revision-bound invoke, status, or cancel request.
func (client *Client) Execute(ctx context.Context, request Request) (CapabilityResult, error) {
	if request.Operation == OperationCapabilitiesList || request.Operation == OperationArtifactDescribe ||
		request.Operation == OperationArtifactFetch {
		return CapabilityResult{}, fmt.Errorf("%w: operation requires its dedicated client method", ErrInvalidMessage)
	}
	response, err := client.roundTrip(ctx, request)
	if err != nil {
		return CapabilityResult{}, err
	}
	if response.Status != ResponseOK || response.Result == nil {
		return CapabilityResult{}, brokerResponseError(response)
	}
	result := *response.Result
	if result.Grant != request.Grant || result.GrantRevision != request.GrantRevision ||
		result.DiscoveryRevision != request.DiscoveryRevision ||
		result.Capability != request.Capability || result.CapabilityRevision != request.CapabilityRevision {
		return CapabilityResult{}, fmt.Errorf("%w: response execution authority mismatch", ErrInvalidMessage)
	}
	if result.Operation != request.CapabilityOperation {
		return CapabilityResult{}, fmt.Errorf("%w: response operation mismatch", ErrInvalidMessage)
	}
	if result.InvocationID != request.InvocationID {
		return CapabilityResult{}, fmt.Errorf("%w: response invocation mismatch", ErrInvalidMessage)
	}
	return result, nil
}

// Artifact describes or fetches a bounded range of one invocation-owned job
// artifact. The caller assembles and verifies chunks before admitting bytes to
// a coding thread.
func (client *Client) Artifact(ctx context.Context, request Request) (ArtifactResult, error) {
	if request.Operation != OperationArtifactDescribe && request.Operation != OperationArtifactFetch {
		return ArtifactResult{}, fmt.Errorf("%w: unsupported artifact operation", ErrInvalidMessage)
	}
	response, err := client.roundTrip(ctx, request)
	if err != nil {
		return ArtifactResult{}, err
	}
	if response.Status != ResponseOK || response.Artifact == nil {
		return ArtifactResult{}, brokerResponseError(response)
	}
	result := *response.Artifact
	if result.Grant != request.Grant || result.GrantRevision != request.GrantRevision ||
		result.DiscoveryRevision != request.DiscoveryRevision ||
		result.Capability != request.Capability || result.CapabilityRevision != request.CapabilityRevision ||
		result.InvocationID != request.InvocationID || result.ArtifactRef != request.ArtifactRef {
		return ArtifactResult{}, fmt.Errorf("%w: response artifact authority mismatch", ErrInvalidMessage)
	}
	if request.Operation == OperationArtifactDescribe && result.DataBase64 != "" ||
		request.Operation == OperationArtifactFetch && result.Offset != request.Offset {
		return ArtifactResult{}, fmt.Errorf("%w: response artifact range mismatch", ErrInvalidMessage)
	}
	return result, nil
}

func (client *Client) roundTrip(ctx context.Context, request Request) (Response, error) {
	if client == nil || client.peerUID == nil || client.dial == nil {
		return Response{}, errors.New("coding remote client is unavailable")
	}
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	if err := validateOwnerSocketEndpoint(client.socketPath); err != nil {
		return Response{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	connection, err := client.dial(ctx, client.socketPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect coding remote broker: %w", err)
	}
	defer func() { _ = connection.Close() }()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return Response{}, errors.New("coding remote broker did not provide a Unix connection")
	}
	uid, err := client.peerUID(unixConnection)
	if err != nil || uid != client.uid {
		return Response{}, errors.New("coding remote broker peer authentication failed")
	}
	writeDeadline := time.Now().Add(defaultIOTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(writeDeadline) {
		writeDeadline = contextDeadline
	}
	if err = unixConnection.SetWriteDeadline(writeDeadline); err != nil {
		return Response{}, fmt.Errorf("set coding remote deadline: %w", err)
	}
	if err = writeFrame(unixConnection, request); err != nil {
		return Response{}, fmt.Errorf("write coding remote request: %w", err)
	}
	readDeadline := time.Now().Add(defaultIOTimeout)
	if request.Operation != OperationCapabilitiesList {
		readDeadline = time.UnixMilli(request.DeadlineUnixMS).Add(defaultResponseGrace)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(readDeadline) {
		readDeadline = contextDeadline
	}
	if err = unixConnection.SetReadDeadline(readDeadline); err != nil {
		return Response{}, fmt.Errorf("set coding remote response deadline: %w", err)
	}
	raw, err := readFrame(unixConnection)
	if err != nil {
		return Response{}, fmt.Errorf("read coding remote response: %w", err)
	}
	response, err := DecodeResponse(raw)
	if err != nil {
		return Response{}, err
	}
	if response.RequestID != request.RequestID {
		return Response{}, fmt.Errorf("%w: response request ID mismatch", ErrInvalidMessage)
	}
	return response, nil
}

func brokerResponseError(response Response) error {
	return &BrokerError{Status: response.Status, Code: response.Code, Message: response.Message}
}

// BrokerError contains only the server's bounded safe denial projection.
type BrokerError struct {
	Status  ResponseStatus
	Code    string
	Message string
}

func (err *BrokerError) Error() string {
	if err == nil || err.Message == "" {
		return "coding remote broker request failed"
	}
	return err.Message
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, fmt.Errorf("%w: frame is outside bounds", ErrInvalidMessage)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeFrame(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > MaxFrameBytes {
		return fmt.Errorf("%w: frame is outside bounds", ErrInvalidMessage)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err = writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}
