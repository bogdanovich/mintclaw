//go:build linux

package transport

import (
	"context"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const maxConcurrentCalls = 8

// Call is borrowed by one trusted handler. Authorize is a point-in-time caller
// check: consumers must also check their current grant, policy revision, and
// live browser authority immediately before retrieval/injection/session use.
// Retained calls deny after completion. No method retrieves a secret.
type Call struct {
	ctx     context.Context
	caller  *Caller
	peer    credentialipc.Peer
	request intent.Request
}

func (call Call) Principal() credentialbroker.Principal {
	if call.caller == nil {
		return credentialbroker.Principal{}
	}
	return call.caller.principal
}

func (call Call) Request() intent.Request {
	request := call.request
	request.Accounts = slices.Clone(request.Accounts)
	return request
}

func (call Call) Authorize() error {
	if call.ctx == nil || call.ctx.Err() != nil || call.caller == nil ||
		!call.caller.lifetime.Alive() || !call.peer.Alive() {
		return ErrUnavailable
	}
	return nil
}

// Handler runs inside the trusted credential service. It must honor context
// cancellation and acknowledge private side effects before returning. Transport
// cannot forcibly terminate Go code or undo an injection already in flight.
type Handler func(context.Context, Call) (intent.Response, error)

type Server struct {
	caller  *Caller
	handler Handler
	serving atomic.Bool
}

func NewServer(caller *Caller, handler Handler) (*Server, error) {
	if caller == nil || !caller.lifetime.Alive() || handler == nil {
		return nil, ErrUnavailable
	}
	return &Server{caller: caller, handler: handler}, nil
}

// Serve borrows a qualified startup-owned Unix sequenced-packet listener and
// closes it when the context or pinned caller ends. Startup must establish
// private path permissions, separate OS identities and process role before
// reaching this entry point. Serve does not establish those host boundaries.
func (server *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	if server == nil || server.caller == nil || server.handler == nil || ctx == nil || ctx.Err() != nil ||
		listener == nil || !server.caller.lifetime.Alive() ||
		!server.serving.CompareAndSwap(false, true) {
		return ErrUnavailable
	}
	defer server.serving.Store(false)
	if prepareListener(listener) != nil {
		return ErrUnavailable
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-serveCtx.Done():
				_ = listener.Close()
				return
			case <-ticker.C:
				if !server.caller.lifetime.Alive() {
					cancel()
				}
			}
		}
	}()
	var workers sync.WaitGroup
	defer func() { cancel(); <-watchDone; workers.Wait() }()
	slots := make(chan struct{}, maxConcurrentCalls)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnavailable
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				server.handle(serveCtx, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func prepareListener(listener *net.UnixListener) error {
	return credentialipc.PrepareListener(listener)
}

func (server *Server) handle(parent context.Context, connection *net.UnixConn) {
	defer func() { _ = connection.Close() }()
	peer, err := credentialipc.ConnectedPeer(connection)
	if err != nil {
		return
	}
	defer peer.Close()
	if peer.PID != server.caller.pid || peer.UID != server.caller.uid || peer.GID != server.caller.gid ||
		!server.caller.lifetime.Alive() {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if connection.SetDeadline(deadline) != nil {
		return
	}
	watchDone := make(chan struct{})
	go watchCall(ctx, cancel, connection, peer, server.caller, watchDone)
	defer func() { cancel(); <-watchDone }()
	buffer := make([]byte, intent.MaxPacketBytes+1)
	defer clear(buffer)
	control := make([]byte, unix.CmsgSpace(12)+unix.CmsgSpace(16*4))
	defer clear(control)
	count, controlCount, flags, readErr := credentialipc.ReadPacket(connection, buffer, control)
	if !validSender(control[:controlCount], peer) || readErr != nil || count == 0 ||
		count > intent.MaxPacketBytes || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || ctx.Err() != nil {
		return
	}
	request, decodeErr := intent.DecodeRequest(buffer[:count])
	if decodeErr != nil {
		writeOutcome(connection, intent.Status, intent.Response{Code: intent.Denied})
		return
	}
	if !server.caller.permits(request) {
		writeOutcome(connection, request.Operation, intent.Response{Code: intent.Denied})
		return
	}
	call := Call{ctx, server.caller, peer, request}
	if call.Authorize() != nil {
		return
	}
	response := server.invoke(ctx, call)
	if call.Authorize() != nil {
		return
	}
	writeOutcome(connection, request.Operation, response)
}

func (server *Server) invoke(ctx context.Context, call Call) (response intent.Response) {
	response = intent.Response{Code: intent.Failed}
	defer func() {
		if recover() != nil {
			response = intent.Response{Code: intent.Failed}
		}
	}()
	result, err := server.handler(ctx, call)
	if err == nil && result.Validate(call.request.Operation) == nil {
		response = result
	}
	return response
}

func writeOutcome(connection *net.UnixConn, operation intent.Operation, response intent.Response) {
	data, err := intent.EncodeResponse(operation, response)
	if err != nil {
		return
	}
	defer clear(data)
	_, _, _ = connection.WriteMsgUnix(data, nil, nil)
}

func validSender(data []byte, peer credentialipc.Peer) bool {
	return credentialipc.ValidSender(data, peer)
}

func watchCall(
	ctx context.Context,
	cancel context.CancelFunc,
	connection *net.UnixConn,
	peer credentialipc.Peer,
	caller *Caller,
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = connection.Close()
			return
		case <-ticker.C:
			if !peer.Alive() || !caller.lifetime.Alive() || !connectionAlive(connection) {
				cancel()
			}
		}
	}
}

func connectionAlive(connection *net.UnixConn) bool {
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	alive := false
	err = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}}
		_, pollErr := unix.Poll(fds, 0)
		alive = pollErr == nil && fds[0].Revents == 0
	})
	return err == nil && alive
}
