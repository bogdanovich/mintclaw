package mintclaw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

// mintclawConn represents a single WebSocket connection.

type mintclawConn struct {
	id         string
	conn       *websocket.Conn
	sessionID  string
	writeOnce  sync.Once
	writeLock  chan struct{}
	queueMu    sync.Mutex
	writeQueue chan mintclawWriteRequest
	closeCh    chan struct{}
	closed     atomic.Bool
	cancel     context.CancelFunc // cancels per-connection goroutines (e.g. pingLoop)
}

const (
	mintclawWriteTimeout   = 15 * time.Second
	mintclawWriteQueueSize = 32
)

var errMintClawWriteQueueFull = errors.New("mintclaw connection write queue full")

type mintclawWriteRequest struct {
	ctx     context.Context
	cancel  context.CancelFunc
	writeFn func() error
	result  chan error
}

var allowedInlineImageMIMETypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/webp": {},
	"image/bmp":  {},
}

func outboundMessageIsThought(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsThought()
}

func outboundMessageIsToolCalls(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsToolCalls()
}

func (pc *mintclawConn) write(ctx context.Context, writeFn func() error) error {
	if pc.closed.Load() {
		return fmt.Errorf("connection closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writeCtx, cancel := context.WithTimeout(ctx, mintclawWriteTimeout)
	defer cancel()

	pc.writeOnce.Do(func() {
		pc.writeLock = make(chan struct{}, 1)
		pc.writeLock <- struct{}{}
	})
	select {
	case <-writeCtx.Done():
		return writeCtx.Err()
	case <-pc.writeLock:
	}
	defer func() { pc.writeLock <- struct{}{} }()

	if pc.closed.Load() {
		return fmt.Errorf("connection closed")
	}
	if err := writeCtx.Err(); err != nil {
		return err
	}
	deadline, _ := writeCtx.Deadline()
	if err := pc.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer func() { _ = pc.conn.SetWriteDeadline(time.Time{}) }()

	var writeState atomic.Uint32
	writeFinished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-writeCtx.Done():
			if writeState.CompareAndSwap(0, 2) {
				pc.close()
			}
		case <-writeFinished:
		}
	}()

	err := writeFn()
	if writeState.CompareAndSwap(0, 1) {
		close(writeFinished)
	}
	<-watcherDone
	if err != nil {
		var timeoutErr net.Error
		if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
			pc.close()
			return context.DeadlineExceeded
		}
		if ctxErr := writeCtx.Err(); ctxErr != nil {
			if ctxErr == context.DeadlineExceeded {
				pc.close()
			}
			return ctxErr
		}
		return err
	}
	// A successful WebSocket write is treated as delivered even if cancellation
	// won the connection-lifecycle race. Reporting an error here could retry a
	// frame that the peer already received; the closed connection is used only
	// to prevent future writes after the ambiguous cancellation boundary.
	return nil
}

// writeJSON sends a JSON message with context-aware serialization and a
// bounded socket deadline. Gorilla permits only one concurrent writer.
func (pc *mintclawConn) writeJSON(ctx context.Context, v any) error {
	return pc.write(ctx, func() error {
		return pc.conn.WriteJSON(v)
	})
}

func (pc *mintclawConn) writeMessage(ctx context.Context, messageType int, data []byte) error {
	return pc.write(ctx, func() error {
		return pc.conn.WriteMessage(messageType, data)
	})
}

func (pc *mintclawConn) enqueueWrite(ctx context.Context, writeFn func() error) <-chan error {
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan error, 1)
	writeCtx, cancel := context.WithTimeout(ctx, mintclawWriteTimeout)
	request := mintclawWriteRequest{ctx: writeCtx, cancel: cancel, writeFn: writeFn, result: result}

	pc.queueMu.Lock()
	if pc.closed.Load() {
		pc.queueMu.Unlock()
		cancel()
		result <- fmt.Errorf("connection closed")
		return result
	}
	if pc.writeQueue == nil {
		pc.writeQueue = make(chan mintclawWriteRequest, mintclawWriteQueueSize)
		pc.closeCh = make(chan struct{})
		go pc.runWriteQueue(pc.writeQueue, pc.closeCh)
	}
	select {
	case pc.writeQueue <- request:
		pc.queueMu.Unlock()
	case <-pc.closeCh:
		pc.queueMu.Unlock()
		cancel()
		result <- fmt.Errorf("connection closed")
	default:
		pc.queueMu.Unlock()
		cancel()
		result <- errMintClawWriteQueueFull
		pc.close()
	}
	return result
}

func (pc *mintclawConn) runWriteQueue(queue <-chan mintclawWriteRequest, closeCh <-chan struct{}) {
	for {
		if pc.closed.Load() {
			pc.failQueuedWrites(queue)
			return
		}
		select {
		case <-closeCh:
			pc.failQueuedWrites(queue)
			return
		case request := <-queue:
			err := pc.write(request.ctx, request.writeFn)
			request.cancel()
			request.result <- err
			if err != nil || pc.closed.Load() {
				pc.close()
				pc.failQueuedWrites(queue)
				return
			}
		}
	}
}

func (pc *mintclawConn) failQueuedWrites(queue <-chan mintclawWriteRequest) {
	for {
		select {
		case request := <-queue:
			request.cancel()
			request.result <- fmt.Errorf("connection closed")
		default:
			return
		}
	}
}

func (pc *mintclawConn) enqueueJSON(ctx context.Context, v any) <-chan error {
	return pc.enqueueWrite(ctx, func() error {
		return pc.conn.WriteJSON(v)
	})
}

// close closes the connection.
func (pc *mintclawConn) close() {
	pc.queueMu.Lock()
	if !pc.closed.CompareAndSwap(false, true) {
		pc.queueMu.Unlock()
		return
	}
	closeCh := pc.closeCh
	cancel := pc.cancel
	conn := pc.conn
	if closeCh != nil {
		close(closeCh)
	}
	pc.queueMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// MintClawChannel implements the native MintClaw Protocol WebSocket channel.
