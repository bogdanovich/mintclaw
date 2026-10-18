package gatewayclient

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func TestMessageObservationPrecedesHeartbeatDiscardAndQueueing(t *testing.T) {
	for _, inspected := range []bool{false, true} {
		server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
			_ = connection.WriteJSON(protocol.MintClawMessage{Type: protocol.TypePong})
			_ = connection.WriteJSON(protocol.MintClawMessage{
				Type:    protocol.TypeMessageCreate,
				Payload: map[string]any{"message_id": "uncorrelated-media"},
			})
		})
		var projections []string
		var inspections atomic.Int32
		options := Options{ObserveMessage: func(message protocol.MintClawMessage) error {
			if inspected && inspections.Load() <= int32(len(projections)) {
				return errors.New("observer preceded raw inspection")
			}
			projections = append(projections, message.Type)
			return nil
		}}
		if inspected {
			options.InspectFrame = func([]byte) error { inspections.Add(1); return nil }
		}
		client := connectTest(t, server, options)
		if err := client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		message, err := client.Receive(ctx)
		cancel()
		client.Close()
		if err != nil || message.Type != protocol.TypeMessageCreate ||
			!slices.Equal(projections, []string{protocol.TypePong, protocol.TypeMessageCreate}) {
			t.Fatal("uncorrelated observation was filtered or heartbeat entered queue", err)
		}
	}
}

func TestRawInspectionRefusesBeforeMessageObservation(t *testing.T) {
	var observed, sends atomic.Int32
	server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
		sends.Add(1)
		_ = connection.WriteJSON(protocol.MintClawMessage{Type: protocol.TypePong})
	})
	private := errors.New("private-observer-control")
	client := connectTest(t, server, Options{
		InspectFrame:   func([]byte) error { return private },
		ObserveMessage: func(protocol.MintClawMessage) error { observed.Add(1); return nil },
	})
	if err := client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err := client.Receive(ctx)
	client.Close()
	if !errors.Is(err, private) || !errors.Is(err, ErrFrameInspection) ||
		strings.Contains(err.Error(), private.Error()) || observed.Load() != 0 || sends.Load() != 1 {
		t.Fatal("unsafe raw frame reached observer, was echoed, or replayed", err)
	}
}

func TestCloseJoinsMessageObservationEvenBeforeAFrameIsQueued(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		var sends atomic.Int32
		server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
			sends.Add(1)
			_ = connection.WriteJSON(protocol.MintClawMessage{
				Type:    protocol.TypeMessageCreate,
				Payload: map[string]any{"message_id": "admitted-observation"},
			})
		})
		entered, release := make(chan struct{}), make(chan struct{})
		releaseObserver := sync.OnceFunc(func() { close(release) })
		defer releaseObserver()
		private := errors.New("private-observer-diagnostic")
		projection := ""
		client := connectTest(t, server, Options{ObserveMessage: func(message protocol.MintClawMessage) error {
			close(entered)
			<-release
			projection, _ = message.Payload["message_id"].(string)
			if refuse {
				return private
			}
			return nil
		}})
		if err := client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("decoded message was not observed")
		}
		closed := make(chan struct{})
		go func() { client.Close(); close(closed) }()
		select {
		case <-client.ctx.Done():
		case <-ctx.Done():
			t.Fatal("close did not cancel")
		}
		releaseObserver()
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("close did not join the observer")
		}
		if projection != "admitted-observation" || sends.Load() != 1 ||
			refuse && (!errors.Is(client.Failure(), private) || !errors.Is(client.Failure(), ErrFrameInspection)) ||
			!refuse && client.Failure() != nil {
			t.Fatal("in-flight projection or its refusal disappeared during close", client.Failure())
		}
		if err := client.Failure(); err != nil && strings.Contains(err.Error(), private.Error()) {
			t.Fatal("observer diagnostic escaped")
		}
	}
}
