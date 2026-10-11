// Package gatewayclient owns a bounded, non-reconnecting connection to the
// authenticated local gateway. Callers own request correlation and semantics.
package gatewayclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/netbind"
)

var (
	ErrUnavailable     = errors.New("gateway client: local authenticated gateway unavailable")
	ErrQueueLimit      = errors.New("gateway client: receive queue limit exceeded")
	ErrFrameInspection = errors.New("gateway client: frame inspection refused")
)

type frameInspectionError struct{ cause error }

func (err *frameInspectionError) Error() string   { return ErrFrameInspection.Error() }
func (err *frameInspectionError) Unwrap() []error { return []error{ErrFrameInspection, err.cause} }

type DialError struct {
	StatusCode int
	Cause      error
}

func (err *DialError) Error() string { return "gateway client: connection failed" }
func (err *DialError) Unwrap() error { return err.Cause }

type Options struct {
	MaxFrameBytes     int64
	HeartbeatInterval time.Duration
	QueueCapacity     int
	// InspectFrame runs in the sole reader before decoding, including ignored
	// heartbeats. It must not retain the bytes or mutate caller-owned state.
	// Returning an error closes the connection without replay or raw diagnostics.
	InspectFrame func([]byte) error
}

type Client struct {
	connection   *websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	stopCancel   func() bool
	events       chan protocol.MintClawMessage
	workers      sync.WaitGroup
	closeOnce    sync.Once
	writes       chan struct{}
	errorMu      sync.Mutex
	failure      error
	inspectFrame func([]byte) error
}

func ChannelSettings(cfg *config.Config) (*config.MintClawSettings, error) {
	if cfg == nil {
		return nil, ErrUnavailable
	}
	channel := cfg.Channels.GetByType(config.ChannelMintClaw)
	if channel == nil || !channel.Enabled {
		return nil, ErrUnavailable
	}
	decoded, err := channel.GetDecoded()
	if err != nil {
		return nil, ErrUnavailable
	}
	settings, ok := decoded.(*config.MintClawSettings)
	if !ok || strings.TrimSpace(settings.Token.String()) == "" {
		return nil, ErrUnavailable
	}
	return settings, nil
}

func Endpoint(cfg *config.Config, sessionID string) (*url.URL, error) {
	if cfg == nil || cfg.Gateway.Port <= 0 || cfg.Gateway.Port > 65535 {
		return nil, ErrUnavailable
	}
	plan, err := netbind.BuildPlan(cfg.Gateway.Host, netbind.DefaultLoopback)
	if err != nil || !netbind.IsLoopbackHost(plan.ProbeHost) {
		return nil, ErrUnavailable
	}
	endpoint := &url.URL{
		Scheme: "ws", Host: net.JoinHostPort(plan.ProbeHost, strconv.Itoa(cfg.Gateway.Port)),
		Path: "/mintclaw/ws",
	}
	query := endpoint.Query()
	query.Set("session_id", sessionID)
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}

func Connect(ctx context.Context, cfg *config.Config, sessionID string, options Options) (*Client, error) {
	settings, err := ChannelSettings(cfg)
	if err != nil {
		return nil, err
	}
	endpoint, err := Endpoint(cfg, sessionID)
	if err != nil {
		return nil, err
	}
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = 1024*1024 + 64*1024
	}
	if options.QueueCapacity == 0 {
		options.QueueCapacity = 16
	}
	if options.HeartbeatInterval == 0 {
		readTimeout := time.Duration(settings.ReadTimeout) * time.Second
		if readTimeout <= 0 {
			readTimeout = time.Minute
		}
		options.HeartbeatInterval = min(20*time.Second, readTimeout/3)
	}
	if options.MaxFrameBytes < 1 || options.MaxFrameBytes > 2*1024*1024 ||
		options.QueueCapacity < 1 || options.QueueCapacity > 64 ||
		options.HeartbeatInterval <= 0 || options.HeartbeatInterval > time.Minute {
		return nil, ErrUnavailable
	}
	connection, response, err := websocket.DefaultDialer.DialContext(
		ctx, endpoint.String(), http.Header{"Authorization": {"Bearer " + settings.Token.String()}},
	)
	status := 0
	if response != nil {
		status = response.StatusCode
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}
	if err != nil {
		return nil, &DialError{StatusCode: status, Cause: err}
	}
	connection.SetReadLimit(options.MaxFrameBytes)
	child, cancel := context.WithCancel(ctx)
	client := &Client{
		connection: connection, ctx: child, cancel: cancel,
		events:       make(chan protocol.MintClawMessage, options.QueueCapacity),
		writes:       make(chan struct{}, 1),
		inspectFrame: options.InspectFrame,
	}
	client.stopCancel = context.AfterFunc(child, func() { _ = connection.Close() })
	client.workers.Add(2)
	go client.readLoop()
	go client.heartbeatLoop(sessionID, options.HeartbeatInterval)
	return client, nil
}

// Send never reconnects or retries a write whose admission is unknown.
func (client *Client) Send(ctx context.Context, message protocol.MintClawMessage) error {
	select {
	case client.writes <- struct{}{}:
		defer func() { <-client.writes }()
	case <-ctx.Done():
		return ctx.Err()
	case <-client.ctx.Done():
		return client.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := client.ctx.Err(); err != nil {
		return err
	}
	settled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		// A canceled active write has unknown admission. The connection cannot
		// be reused, and the callback must settle before releasing its write slot.
		client.fail(ctx.Err())
		_ = client.connection.Close()
		close(settled)
	})
	defer func() {
		if !stop() {
			<-settled
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := client.connection.SetWriteDeadline(deadline); err != nil {
		return err
	}
	err := client.connection.WriteJSON(message)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	return err
}

func (client *Client) Receive(ctx context.Context) (protocol.MintClawMessage, error) {
	if err := ctx.Err(); err != nil {
		return protocol.MintClawMessage{}, err
	}
	select {
	case message, open := <-client.events:
		if open {
			return message, nil
		}
		if err := client.Failure(); err != nil {
			return protocol.MintClawMessage{}, err
		}
		return protocol.MintClawMessage{}, context.Canceled
	case <-ctx.Done():
		return protocol.MintClawMessage{}, ctx.Err()
	}
}

func (client *Client) Failure() error {
	client.errorMu.Lock()
	defer client.errorMu.Unlock()
	return client.failure
}

// Close settles local workers only; it does not cancel a remote agent turn.
func (client *Client) Close() {
	client.closeOnce.Do(func() {
		client.cancel()
		_ = client.connection.Close()
		client.stopCancel()
		client.workers.Wait()
	})
}

func (client *Client) readLoop() {
	defer client.workers.Done()
	defer close(client.events)
	defer client.cancel()
	for {
		var message protocol.MintClawMessage
		if err := client.readMessage(&message); err != nil {
			// Caller closure cannot erase a safety failure from an already admitted frame.
			if client.ctx.Err() == nil || frameSafetyFailure(err) {
				client.fail(err)
			}
			return
		}
		// Protocol heartbeats must not consume conversation queue capacity while
		// an eval caller is waiting on its separate model driver.
		if message.Type == protocol.TypePong {
			continue
		}
		select {
		case client.events <- message:
		case <-client.ctx.Done():
			return
		default:
			client.fail(ErrQueueLimit)
			return
		}
	}
}

func (client *Client) readMessage(message *protocol.MintClawMessage) error {
	if client.inspectFrame == nil {
		return client.connection.ReadJSON(message)
	}
	_, data, err := client.connection.ReadMessage()
	if err != nil {
		return err
	}
	defer clear(data)
	if err = client.inspectFrame(data); err != nil {
		return &frameInspectionError{cause: err}
	}
	if json.Unmarshal(data, message) != nil {
		return ErrFrameInspection
	}
	return nil
}

func frameSafetyFailure(err error) bool {
	return errors.Is(err, websocket.ErrReadLimit) || errors.Is(err, ErrFrameInspection)
}

func (client *Client) fail(err error) {
	client.errorMu.Lock()
	// A concurrent shutdown/write failure cannot conceal an admitted unsafe frame.
	if client.failure == nil || frameSafetyFailure(err) && !frameSafetyFailure(client.failure) {
		client.failure = err
	}
	client.errorMu.Unlock()
	client.cancel()
}

func (client *Client) heartbeatLoop(sessionID string, interval time.Duration) {
	defer client.workers.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-client.ctx.Done():
			return
		case <-ticker.C:
			if err := client.Send(client.ctx, protocol.MintClawMessage{
				Type: protocol.TypePing, ID: uuid.NewString(), SessionID: sessionID, Timestamp: time.Now().UnixMilli(),
			}); err != nil {
				client.fail(err)
				return
			}
		}
	}
}
