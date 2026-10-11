package browserworker

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// channel borrows one startup-qualified inherited endpoint. Startup must keep
// agent processes out of both endpoints, pin the worker process/cgroup, and
// supply a termination operation that verifies its death. This does not admit
// an arbitrary socket or caller-selected PID as a credential consumer.
type channel struct {
	connection  net.Conn
	ctx         context.Context
	cancel      context.CancelFunc
	check       func(context.Context, string, string) error
	stop        func(context.Context) error
	writeMu     sync.Mutex
	slot        chan struct{}
	reply       chan packet
	pending     atomic.Uint32
	next        uint32
	done        chan struct{}
	cleaned     chan struct{}
	closeAck    chan bool
	retireOnce  sync.Once
	quarantined atomic.Bool
}

func newChannel(ctx context.Context, connection net.Conn,
	check func(context.Context, string, string) error, stop func(context.Context) error,
) (*channel, error) {
	if ctx == nil || ctx.Err() != nil || connection == nil || check == nil || stop == nil {
		return nil, errUnavailable
	}
	private, cancel := context.WithCancel(ctx)
	worker := &channel{
		connection: connection, ctx: private, cancel: cancel, check: check, stop: stop,
		slot: make(chan struct{}, 1), reply: make(chan packet, 1), done: make(chan struct{}),
		cleaned: make(chan struct{}), closeAck: make(chan bool, 1),
	}
	go worker.read()
	go func() { <-private.Done(); worker.retire() }()
	return worker, nil
}

func (worker *channel) write(kind byte, id uint32, body []byte) error {
	buffer, err := encodePacket(kind, id, body)
	if err != nil {
		return errUnavailable
	}
	defer clear(buffer)
	worker.writeMu.Lock()
	defer worker.writeMu.Unlock()
	if kind != 6 {
		select {
		case <-worker.done:
			return errUnavailable
		default:
		}
	}
	if worker.connection.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		return errUnavailable
	}
	for len(buffer) != 0 {
		count, writeErr := worker.connection.Write(buffer)
		if writeErr != nil || count == 0 {
			return errUnavailable
		}
		buffer = buffer[count:]
	}
	return nil
}

func (worker *channel) rpc(ctx context.Context, kind byte, body []byte) (byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, errUnavailable
	}
	select {
	case worker.slot <- struct{}{}:
		defer func() { <-worker.slot }()
	case <-ctx.Done():
		return 0, errUnavailable
	case <-worker.done:
		return 0, errUnavailable
	}
	select {
	case <-worker.done:
		return 0, errUnavailable
	default:
	}
	if ctx.Err() != nil {
		worker.retire()
		return 0, errUnavailable
	}
	unwatch := context.AfterFunc(ctx, worker.retire)
	defer unwatch()
	worker.next++
	if worker.next > 16 {
		worker.retire()
		return 0, errUnavailable
	}
	id := worker.next
	worker.pending.Store(id)
	defer worker.pending.Store(0)
	if worker.write(kind, id, body) != nil {
		worker.retire()
		return 0, errUnavailable
	}
	select {
	case result := <-worker.reply:
		defer clear(result.body)
		if result.id != id || ctx.Err() != nil {
			worker.retire()
			return 0, errUnavailable
		}
		select {
		case <-worker.done:
			return 0, errUnavailable
		default:
		}
		return result.body[0], nil
	case <-ctx.Done():
		worker.retire()
		return 0, errUnavailable
	case <-worker.done:
		return 0, errUnavailable
	}
}

func (worker *channel) read() {
	defer worker.retire()
	var lastCheck uint32
	slots := make(chan struct{}, 32)
	for {
		frame, err := readPacket(worker.connection)
		if err != nil {
			return
		}
		switch frame.kind {
		case 0x10:
			if len(frame.body) != 1 || frame.body[0] == 0 || frame.body[0] > 8 {
				clear(frame.body)
				return
			}
			if frame.id == 0 {
				code := frame.body[0]
				clear(frame.body)
				if code != 7 && code != 8 {
					return
				}
				select {
				case worker.closeAck <- code == 7:
				default:
				}
				return
			}
			if frame.id != worker.pending.Load() {
				clear(frame.body)
				return
			}
			select {
			case worker.reply <- frame:
			default:
				clear(frame.body)
				return
			}
		case 0x20:
			operation, origin, decodeErr := decodeCheck(frame.body)
			clear(frame.body)
			if decodeErr != nil || lastCheck == 0xffffffff || frame.id != lastCheck+1 {
				return
			}
			lastCheck = frame.id
			select {
			case slots <- struct{}{}:
				go func(id uint32) {
					defer func() { <-slots }()
					worker.answerCheck(id, operation, origin)
				}(frame.id)
			default:
				return
			}
		default:
			clear(frame.body)
			return
		}
	}
}

func (worker *channel) answerCheck(id uint32, operation, origin string) {
	ctx, cancel := context.WithTimeout(worker.ctx, 500*time.Millisecond)
	defer cancel()
	admitted := false
	func() {
		defer func() { _ = recover() }()
		admitted = worker.check(ctx, operation, origin) == nil && ctx.Err() == nil
	}()
	select {
	case <-worker.done:
		return
	default:
	}
	code := byte(0)
	if admitted {
		code = 1
	}
	if worker.write(0x21, id, []byte{code}) != nil || !admitted {
		worker.retire()
	}
}

// Retirement preempts an in-flight operation. No operation waits for a busy
// login to finish before closing. Unknown acknowledgement or failed verified
// termination permanently quarantines this worker and denies reuse.
func (worker *channel) retire() {
	worker.retireOnce.Do(func() {
		close(worker.done)
		worker.cancel()
		go func() {
			defer close(worker.cleaned)
			acknowledged := false
			if worker.write(6, 0, nil) == nil {
				timer := time.NewTimer(4 * time.Second)
				select {
				case acknowledged = <-worker.closeAck:
				case <-timer.C:
				}
				timer.Stop()
			}
			_ = worker.connection.Close()
			terminated := terminate(worker.stop) == nil
			worker.quarantined.Store(!acknowledged || !terminated)
		}()
	})
}

func terminate(stop func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopped := make(chan bool, 1)
	go func() {
		ok := false
		defer func() { _ = recover(); stopped <- ok }()
		ok = stop(ctx) == nil && ctx.Err() == nil
	}()
	select {
	case ok := <-stopped:
		if ok {
			return nil
		}
	case <-ctx.Done():
	}
	return errUnavailable
}

func (worker *channel) close(ctx context.Context) error {
	if worker == nil || ctx == nil {
		return errUnavailable
	}
	worker.retire()
	select {
	case <-worker.cleaned:
		if worker.quarantined.Load() {
			return errUnavailable
		}
		return nil
	case <-ctx.Done():
		return errUnavailable
	}
}
