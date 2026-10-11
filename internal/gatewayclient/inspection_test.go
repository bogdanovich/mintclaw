package gatewayclient

import (
	"bytes"
	"context"
	"errors"
	"strings"
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
