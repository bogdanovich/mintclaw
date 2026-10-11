package gatewayclient

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func TestFrameInspectorSeesDuplicateKeysBeforeProjection(t *testing.T) {
	server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
		_ = connection.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"message.update","payload":{"content":"first","content":"second"}}`))
	})
	var observed atomic.Bool
	client := connectTest(t, server, Options{InspectFrame: func(data []byte) error {
		observed.Store(bytes.Contains(data, []byte(`"first"`)) && bytes.Contains(data, []byte(`"second"`)))
		return nil
	}})
	if client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}) != nil {
		t.Fatal("send failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	message, err := client.Receive(ctx)
	if err != nil || !observed.Load() || message.Payload[protocol.PayloadKeyContent] != "second" {
		t.Fatal("inspection did not precede map decoding", err)
	}
}

func TestFrameInspectorRefusesIgnoredAndMalformedFramesWithoutReplay(t *testing.T) {
	for _, frame := range []string{
		`{"type":"pong","payload":{"secret":"private-control"}}`,
		`{"type":"message.update","payload":{"content":"private-control","content":"safe"}}`,
		`{"type":"message.update"} trailing`,
	} {
		t.Run(frame[:min(len(frame), 20)], func(t *testing.T) {
			var sends atomic.Int32
			server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
				sends.Add(1)
				_ = connection.WriteMessage(websocket.TextMessage, []byte(frame))
			})
			privateErr := errors.New("private-control must not enter diagnostics")
			client := connectTest(t, server, Options{InspectFrame: func(data []byte) error {
				if bytes.Contains(data, []byte("private-control")) {
					return privateErr
				}
				return nil
			}})
			if client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}) != nil {
				t.Fatal("send failed")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, err := client.Receive(ctx); !errors.Is(err, ErrFrameInspection) ||
				strings.Contains(err.Error(), "private-control") {
				t.Fatal("refused raw frame was projected or echoed", err)
			}
			client.Close()
			if sends.Load() != 1 || !errors.Is(client.Failure(), ErrFrameInspection) {
				t.Fatal("inspection replayed a request or lost failure identity")
			}
		})
	}
}

func TestCloseRetainsInFlightFrameSafetyFailureAfterBufferedFinal(t *testing.T) {
	privateErr := errors.New("synthetic private frame")
	for name, control := range map[string]struct {
		frame string
		err   error
	}{
		"inspection": {frame: `{"type":"pong","payload":{"secret":"private-control"}}`, err: privateErr},
		"decoding":   {frame: `{malformed`},
	} {
		t.Run(name, func(t *testing.T) {
			var sends, inspected atomic.Int32
			server := testServer(t, func(connection *websocket.Conn, request protocol.MintClawMessage) {
				sends.Add(1)
				_ = connection.WriteJSON(protocol.MintClawMessage{
					Type: protocol.TypeMessageUpdate, SessionID: request.SessionID,
					Payload: map[string]any{"content": "buffered final", "final": true, "request_id": request.ID},
				})
				_ = connection.WriteMessage(websocket.TextMessage, []byte(control.frame))
			})
			inspecting, release := make(chan struct{}), make(chan struct{})
			releaseInspection := sync.OnceFunc(func() { close(release) })
			defer releaseInspection()
			client := connectTest(t, server, Options{InspectFrame: func([]byte) error {
				if inspected.Add(1) == 1 {
					return nil
				}
				close(inspecting)
				<-release
				return control.err
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := client.Send(ctx, protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-inspecting:
			case <-ctx.Done():
				t.Fatal("second raw frame was not admitted to inspection")
			}
			message, err := client.Receive(ctx)
			if err != nil || message.Payload["content"] != "buffered final" {
				t.Fatal("buffered final was not retained", err)
			}
			closed := make(chan struct{})
			go func() { client.Close(); close(closed) }()
			select {
			case <-client.ctx.Done():
			case <-ctx.Done():
				t.Fatal("close did not cancel the reader context")
			}
			releaseInspection()
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal("close did not settle the in-flight inspector")
			}
			if !errors.Is(client.Failure(), ErrFrameInspection) ||
				control.err != nil && !errors.Is(client.Failure(), control.err) || sends.Load() != 1 {
				t.Fatal("caller closure concealed an admitted frame failure or replayed input")
			}
		})
	}
}

func TestFrameSafetyFailureOverridesConcurrentTransportFailure(t *testing.T) {
	privateErr := errors.New("synthetic private frame")
	for _, earlier := range []error{context.Canceled, context.DeadlineExceeded, net.ErrClosed} {
		for _, later := range []error{websocket.ErrReadLimit, &frameInspectionError{cause: privateErr}} {
			ctx, cancel := context.WithCancel(t.Context())
			client := &Client{ctx: ctx, cancel: cancel}
			client.fail(earlier)
			client.fail(later)
			client.fail(earlier)
			if !errors.Is(client.Failure(), later) {
				t.Fatal("routine transport failure concealed an admitted safety failure")
			}
			client.fail(ErrFrameInspection)
			if !errors.Is(client.Failure(), later) {
				t.Fatal("the first safety failure was overwritten")
			}
			cancel()
		}
	}
}
