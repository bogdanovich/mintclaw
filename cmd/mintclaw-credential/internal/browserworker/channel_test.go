package browserworker

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannelMalformedPeerAlwaysTerminates(t *testing.T) {
	for _, name := range []string{"oversized", "unknown-kind", "unsolicited-result", "bad-check-id", "truncated"} {
		t.Run(name, func(t *testing.T) {
			parent, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			var stops atomic.Int32
			worker, err := newChannel(context.Background(), parent,
				func(context.Context, string, string) error { return nil },
				func(context.Context) error { stops.Add(1); return nil })
			if err != nil {
				t.Fatal("channel unavailable")
			}
			go func() {
				var data []byte
				switch name {
				case "oversized":
					data = binary.BigEndian.AppendUint32(nil, maxFrameBytes+1)
				case "unknown-kind":
					data, _ = encodePacket(99, 1, nil)
				case "unsolicited-result":
					data, _ = encodePacket(0x10, 1, []byte{3})
				case "bad-check-id":
					data, _ = encodePacket(0x20, 2, []byte{1, 0, 1, 'x'})
				case "truncated":
					data = []byte{0, 0, 0, 5, 1}
				}
				_, _ = peer.Write(data)
				_ = peer.Close()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			select {
			case <-worker.done:
			case <-ctx.Done():
				t.Fatal("malformed peer did not retire")
			}
			if worker.close(ctx) == nil || stops.Load() != 1 {
				t.Fatal("unknown cleanup did not quarantine and terminate")
			}
			if _, callErr := worker.rpc(ctx, 2, nil); callErr == nil {
				t.Fatal("retired channel reopened")
			}
		})
	}
}

func TestChannelClosePreemptsPendingOperation(t *testing.T) {
	parent, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	var stops atomic.Int32
	worker, err := newChannel(context.Background(), parent,
		func(context.Context, string, string) error { return nil },
		func(context.Context) error { stops.Add(1); return nil })
	if err != nil {
		t.Fatal("channel unavailable")
	}
	started := make(chan struct{})
	go func() {
		frame, readErr := readPacket(peer)
		if readErr != nil {
			return
		}
		clear(frame.body)
		close(started)
		frame, readErr = readPacket(peer)
		if readErr != nil || frame.kind != 6 || frame.id != 0 || len(frame.body) != 0 {
			return
		}
		ack, _ := encodePacket(0x10, 0, []byte{7})
		_, _ = peer.Write(ack)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, callErr := worker.rpc(ctx, 2, nil); result <- callErr }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("operation did not begin")
	}
	if worker.close(ctx) != nil || stops.Load() != 1 {
		t.Fatal("close did not preempt and verify termination")
	}
	select {
	case callErr := <-result:
		if callErr == nil {
			t.Fatal("interrupted operation succeeded")
		}
	case <-ctx.Done():
		t.Fatal("pending operation outlived termination")
	}
}

func TestChannelTerminationFailureQuarantinesAcknowledgedClose(t *testing.T) {
	parent, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	worker, err := newChannel(context.Background(), parent,
		func(context.Context, string, string) error { return nil },
		func(context.Context) error { panic("synthetic private stop diagnostic") })
	if err != nil {
		t.Fatal("channel unavailable")
	}
	go func() {
		_, _ = readPacket(peer)
		ack, _ := encodePacket(0x10, 0, []byte{7})
		_, _ = peer.Write(ack)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if !errors.Is(worker.close(ctx), errUnavailable) {
		t.Fatal("failed termination was admitted")
	}
}

func TestChannelRetirementDoesNotQueueBehindBlockedCallbacks(t *testing.T) {
	parent, peer := net.Pipe()
	defer func() { _ = peer.Close(); _ = parent.Close() }()
	var checks, stops atomic.Int32
	admitted := make(chan struct{})
	worker, err := newChannel(context.Background(), parent,
		func(context.Context, string, string) error {
			if checks.Add(1) == 32 {
				close(admitted)
			}
			return nil
		}, func(context.Context) error { stops.Add(1); return nil })
	if err != nil {
		t.Fatal("channel unavailable")
	}
	go func() {
		for id := uint32(1); id <= 32; id++ {
			frame, _ := encodePacket(0x20, id, []byte{1, 0, 1, 'x'})
			if _, writeErr := peer.Write(frame); writeErr != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	select {
	case <-admitted:
	case <-ctx.Done():
		t.Fatal("callback fixture did not fill")
	}
	// The peer never reads replies or acknowledges close. Retirement must skip
	// queued ordinary writes, then still force verified termination promptly.
	if worker.close(ctx) == nil || stops.Load() != 1 || ctx.Err() != nil {
		t.Fatal("blocked callback writes delayed verified termination")
	}
}

type observedWrite struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (connection *observedWrite) Write(data []byte) (int, error) {
	connection.once.Do(func() { close(connection.started) })
	return connection.Conn.Write(data)
}

func TestChannelCancellationPreemptsBlockedWrite(t *testing.T) {
	parent, peer := net.Pipe()
	defer func() { _ = peer.Close(); _ = parent.Close() }()
	connection := &observedWrite{Conn: parent, started: make(chan struct{})}
	var stops atomic.Int32
	worker, err := newChannel(context.Background(), connection,
		func(context.Context, string, string) error { return nil },
		func(context.Context) error { stops.Add(1); return nil })
	if err != nil {
		t.Fatal("channel unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	operation, abort := context.WithCancel(ctx)
	defer abort()
	result := make(chan error, 1)
	go func() { _, callErr := worker.rpc(operation, 2, nil); result <- callErr }()
	select {
	case <-connection.started:
	case <-ctx.Done():
		t.Fatal("write did not start")
	}
	abort()
	select {
	case <-worker.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation waited for write timeout")
	}
	if worker.close(ctx) == nil || stops.Load() != 1 {
		t.Fatal("blocked write did not terminate and quarantine")
	}
	select {
	case callErr := <-result:
		if callErr == nil {
			t.Fatal("canceled write succeeded")
		}
	case <-ctx.Done():
		t.Fatal("canceled write outlived termination")
	}
}
