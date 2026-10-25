package worker

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type heldTerminalReader struct {
	io.Reader
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (reader *heldTerminalReader) Read(data []byte) (int, error) {
	select {
	case <-reader.release:
		return reader.Reader.Read(data)
	case <-reader.closed:
		return 0, io.ErrClosedPipe
	}
}

func (reader *heldTerminalReader) Close() error {
	reader.once.Do(func() { close(reader.closed) })
	return nil
}

type failedRequestWriter struct {
	written int
	failed  chan struct{}
	once    sync.Once
}

func (writer *failedRequestWriter) Write([]byte) (int, error) {
	writer.once.Do(func() { close(writer.failed) })
	return writer.written, io.ErrClosedPipe
}

func (*failedRequestWriter) Close() error { return nil }

func TestClientWriteFailurePreservesTerminalReadWithoutReplayingCall(t *testing.T) {
	for _, written := range []int{0, 1} {
		t.Run(strconv.Itoa(written), func(t *testing.T) {
			binding := testBinding(t)
			terminal, err := eventRecord(projectedEvent{
				name: EventWorkerStopped,
				payload: WorkerStoppedPayload{
					ControlIdentity: binding.ControlIdentity(), Reason: WorkerStopIdle,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := Encode(terminal)
			if err != nil {
				t.Fatal(err)
			}
			reader := &heldTerminalReader{
				Reader:  strings.NewReader(string(encoded) + "\n"),
				release: make(chan struct{}), closed: make(chan struct{}),
			}
			writer := &failedRequestWriter{written: written, failed: make(chan struct{})}
			client, err := NewClient(reader, writer)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			// Bind the fixture to the same post-initialize identity as the idle worker.
			identity := binding.ControlIdentity()
			client.mu.Lock()
			client.eventIdentity = &identity
			client.mu.Unlock()
			callDone := make(chan error, 1)
			go func() {
				callDone <- client.StartTurn(t.Context(), "start-1", TurnStartParams{
					ControlIdentity: identity, Text: "synthetic request",
				})
			}()
			waitDone(t, writer.failed)
			select {
			case err = <-callDone:
				t.Fatalf("write failure closed the reader before terminal settlement: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			close(reader.release)
			select {
			case err = <-callDone:
				if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrControlStreamUncertain) != (written > 0) {
					t.Fatalf("failed request lost its own admission uncertainty: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("failed request did not settle after terminal evidence")
			}
			waitDone(t, client.Done())
			page := client.EventsAfter(0)
			if client.Err() != nil || len(page.Events) != 1 || page.Events[0].Record.Event != EventWorkerStopped {
				t.Fatalf("terminal evidence lost: error=%v events=%#v", client.Err(), page.Events)
			}
			if client.nextID.Load() != 1 {
				t.Fatal("failed request was replayed")
			}
		})
	}
}

func TestClientWriteFailureWithoutTerminalRemainsBoundedFailure(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "grace", true: "canceled"}[canceled], func(t *testing.T) {
			reader := &heldTerminalReader{
				Reader: strings.NewReader(""), release: make(chan struct{}), closed: make(chan struct{}),
			}
			writer := &failedRequestWriter{failed: make(chan struct{})}
			client, err := NewClient(reader, writer)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				go func() {
					<-writer.failed
					cancel()
				}()
			}
			identity := testBinding(t).ControlIdentity()
			callDone := make(chan error, 1)
			go func() {
				callDone <- client.StartTurn(ctx, "start-1", TurnStartParams{
					ControlIdentity: identity, Text: "synthetic request",
				})
			}()
			select {
			case err = <-callDone:
				if !errors.Is(err, io.ErrClosedPipe) || !errors.Is(client.Err(), io.ErrClosedPipe) ||
					len(client.EventsAfter(0).Events) != 0 {
					t.Fatalf("missing terminal evidence became success: call=%v client=%v", err, client.Err())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("write failure waited indefinitely for a terminal event")
			}
			waitDone(t, client.Done())
			waitDone(t, reader.closed)
		})
	}
}
