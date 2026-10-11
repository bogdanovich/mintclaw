package gatewayclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func testConfig(t *testing.T, serverURL string) *config.Config {
	t.Helper()
	endpoint, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(map[string]any{"token": "test-token", "read_timeout": 60})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port = endpoint.Hostname(), port
	cfg.Channels = config.ChannelsConfig{"mintclaw": {
		Type: config.ChannelMintClaw, Enabled: true, Settings: settings,
	}}
	return cfg
}

func testServer(t *testing.T, handle func(*websocket.Conn, protocol.MintClawMessage)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Has("token") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		for {
			var request protocol.MintClawMessage
			if connection.ReadJSON(&request) != nil {
				return
			}
			handle(connection, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func connectTest(t *testing.T, server *httptest.Server, options Options) *Client {
	t.Helper()
	client, err := Connect(t.Context(), testConfig(t, server.URL), "case-session", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestClientKeepsReadingHeartbeatsBetweenUserTurns(t *testing.T) {
	var pings atomic.Int32
	observed := make(chan struct{})
	server := testServer(t, func(connection *websocket.Conn, request protocol.MintClawMessage) {
		if request.Type == protocol.TypePing {
			_ = connection.WriteJSON(protocol.MintClawMessage{Type: protocol.TypePong, ID: request.ID})
			if pings.Add(1) == 5 {
				close(observed)
			}
			return
		}
		_ = connection.WriteJSON(protocol.MintClawMessage{
			Type: protocol.TypeMessageUpdate, SessionID: request.SessionID,
			Payload: map[string]any{"content": "final", "request_id": request.ID, "final": true},
		})
	})
	client := connectTest(t, server, Options{HeartbeatInterval: 10 * time.Millisecond, QueueCapacity: 1})
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat stalled while the consumer was idle")
	}
	for _, id := range []string{"request-one", "request-two"} {
		if err := client.Send(t.Context(), protocol.MintClawMessage{
			Type: protocol.TypeMessageSend, ID: id, SessionID: "case-session",
		}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		message, err := client.Receive(ctx)
		cancel()
		if err != nil || message.Payload["request_id"] != id || message.SessionID != "case-session" {
			t.Fatal("same-connection follow-up was lost", err)
		}
	}
}

func TestClientQueueOverflowIsExplicit(t *testing.T) {
	server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
		for range 20 {
			_ = connection.WriteJSON(protocol.MintClawMessage{Type: protocol.TypeMessageUpdate})
		}
	})
	client := connectTest(t, server, Options{QueueCapacity: 2})
	if err := client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("receive queue did not stop at its bound")
	}
	if !errors.Is(client.Failure(), ErrQueueLimit) {
		t.Fatal("overflow was not retained as a failure")
	}
	for range 2 {
		if _, err := client.Receive(t.Context()); err != nil {
			t.Fatal("already admitted queued events were discarded", err)
		}
	}
	if _, err := client.Receive(t.Context()); !errors.Is(err, ErrQueueLimit) {
		t.Fatal("overflow was silently ignored", err)
	}
}

func TestClientFrameLimitAndDisconnectDoNotReplay(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(strconv.FormatBool(oversized), func(t *testing.T) {
			var requests atomic.Int32
			server := testServer(t, func(connection *websocket.Conn, _ protocol.MintClawMessage) {
				requests.Add(1)
				if oversized {
					_ = connection.WriteJSON(protocol.MintClawMessage{
						Type: protocol.TypeMessageCreate, Payload: map[string]any{"content": strings.Repeat("x", 256)},
					})
				}
				_ = connection.Close()
			})
			client := connectTest(t, server, Options{MaxFrameBytes: 128})
			if err := client.Send(t.Context(), protocol.MintClawMessage{Type: protocol.TypeMessageSend}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := client.Receive(ctx)
			if err == nil || requests.Load() != 1 || oversized && !errors.Is(err, websocket.ErrReadLimit) {
				t.Fatal("disconnect/limit was hidden or request replayed", err)
			}
			client.Close()
		})
	}
}

func TestClientCancellationSettlesWorkersAndDoesNotSend(t *testing.T) {
	var requests atomic.Int32
	server := testServer(t, func(*websocket.Conn, protocol.MintClawMessage) { requests.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	client, err := Connect(ctx, testConfig(t, server.URL), "case-session", Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = client.Send(
		ctx,
		protocol.MintClawMessage{Type: protocol.TypeMessageSend},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal("canceled request was sent", err)
	}
	if _, err = client.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel did not unblock receive", err)
	}
	client.Close()
	client.Close()
	if requests.Load() != 0 {
		t.Fatal("canceled request reached the gateway")
	}
}

type blockedWriteConn struct {
	net.Conn
	armed     atomic.Bool
	started   chan struct{}
	released  chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (connection *blockedWriteConn) Write(data []byte) (int, error) {
	if !connection.armed.Load() {
		return connection.Conn.Write(data)
	}
	connection.startOnce.Do(func() { close(connection.started) })
	<-connection.released
	return 0, net.ErrClosed
}

func (connection *blockedWriteConn) Close() error {
	connection.closeOnce.Do(func() { close(connection.released) })
	return connection.Conn.Close()
}

func TestClientCallerCancellationInterruptsActiveWrite(t *testing.T) {
	server := testServer(t, func(*websocket.Conn, protocol.MintClawMessage) {})
	var socket *blockedWriteConn
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		socket = &blockedWriteConn{Conn: connection, started: make(chan struct{}), released: make(chan struct{})}
		return socket, nil
	}}
	connection, response, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"),
		http.Header{"Authorization": {"Bearer test-token"}})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	child, stopChild := context.WithCancel(t.Context())
	client := &Client{
		connection: connection, ctx: child, cancel: stopChild,
		writes: make(chan struct{}, 1), stopCancel: func() bool { return true },
	}
	t.Cleanup(client.Close)
	socket.armed.Store(true)
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- client.Send(caller, protocol.MintClawMessage{Type: protocol.TypeMessageSend}) }()
	select {
	case <-socket.started:
	case <-time.After(time.Second):
		t.Fatal("write never started")
	}
	cancel()
	select {
	case err = <-finished:
		if !errors.Is(err, context.Canceled) || !errors.Is(client.Failure(), context.Canceled) ||
			len(client.writes) != 0 {
			t.Fatal("active cancellation was not retained and settled before releasing the write slot", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active write waited for its original deadline instead of caller cancellation")
	}
}

func TestEndpointAdmissionAndAuthFailureAreNonEchoing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret-response-canary", http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL)
	endpoint, err := Endpoint(cfg, "session?with&symbols")
	if err != nil || endpoint.Query().Get("session_id") != "session?with&symbols" || endpoint.Query().Has("token") {
		t.Fatal("endpoint did not preserve session or exposed credentials")
	}
	_, err = Connect(t.Context(), cfg, "case-session", Options{})
	var dial *DialError
	if !errors.As(err, &dial) || dial.StatusCode != http.StatusUnauthorized ||
		strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "test-token") {
		t.Fatal("auth failure did not retain safe classification", err)
	}
	for _, host := range []string{"192.0.2.1", "malformed://secret-host-canary"} {
		cfg.Gateway.Host = host
		if _, err = Endpoint(cfg, "case-session"); !errors.Is(err, ErrUnavailable) ||
			strings.Contains(err.Error(), "canary") {
			t.Fatal("nonlocal gateway was admitted or echoed", err)
		}
	}
}

func TestClientWriteWaitRespectsCallerCancellation(t *testing.T) {
	server := testServer(t, func(*websocket.Conn, protocol.MintClawMessage) {})
	client := connectTest(t, server, Options{})
	client.writes <- struct{}{}
	defer func() { <-client.writes }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := client.Send(
		ctx,
		protocol.MintClawMessage{Type: protocol.TypeMessageSend},
	); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatal("write wait ignored the caller deadline", err)
	}
}
