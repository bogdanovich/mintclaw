package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCallToolClassifiesMalformedResponsesWithoutReplaying(t *testing.T) {
	connectCalls := 0
	originalConnect := connectServerFunc
	t.Cleanup(func() { connectServerFunc = originalConnect })
	connectServerFunc = func(context.Context, string, config.MCPServerConfig) (*ServerConnection, error) {
		connectCalls++
		return nil, errors.New("unexpected reconnect")
	}
	var value any
	syntaxErr := json.Unmarshal([]byte("{private-response-canary"), &value)
	for _, tc := range []struct {
		name   string
		err    error
		raw    json.RawMessage
		remote *jsonrpc.Error
		fatal  bool
	}{
		{name: "malformed response", raw: json.RawMessage("{private-response-canary"), fatal: true},
		{
			name:  "long wrapped malformed response",
			err:   fmt.Errorf("private-response-canary %s: %w", strings.Repeat("detail ", 80), syntaxErr),
			fatal: true,
		},
		{name: "invalid response shape", raw: json.RawMessage(`{"content":"private-response-canary"}`), fatal: true},
		{name: "unknown content", raw: json.RawMessage(`{"content":[{"type":"private-response-canary"}]}`), fatal: true},
		{
			name:  "unknown content mentioning transport",
			raw:   json.RawMessage(`{"content":[{"type":"private-response-canary unexpected EOF"}]}`),
			fatal: true,
		},
		{
			name:  "invalid image encoding",
			raw:   json.RawMessage(`{"content":[{"type":"image","data":"!private-response-canary","mimeType":"image/png"}]}`),
			fatal: true,
		},
		{
			name:  "invalid audio encoding",
			raw:   json.RawMessage(`{"content":[{"type":"audio","data":"!private-response-canary","mimeType":"audio/wav"}]}`),
			fatal: true,
		},
		{name: "nil content", raw: json.RawMessage(`{"content":[null]}`), fatal: true},
		{
			name:  "invalid nested content",
			raw:   json.RawMessage(`{"content":[{"type":"tool_result","content":[{"type":"private-response-canary"}]}]}`),
			fatal: true,
		},
		{name: "ordinary error", err: errors.New("typeof is unavailable")},
		{
			name:   "server error mentioning transport",
			remote: &jsonrpc.Error{Code: -32603, Message: "unexpected EOF in user input"},
		},
		{
			name:   "server error mentioning decoding",
			remote: &jsonrpc.Error{Code: -32603, Message: `unrecognized content type "user input"`},
		},
		{
			name: "canceled decoder lookalike",
			err:  fmt.Errorf("unrecognized content type private-response-canary: %w", context.Canceled),
		},
		{
			name: "timed out decoder lookalike",
			err:  fmt.Errorf("invalid content type private-response-canary: %w", context.DeadlineExceeded),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, transport, err := newScriptedServerConnection("", nil, tc.err)
			if err != nil {
				t.Fatal(err)
			}
			transport.toolCallRawResult = tc.raw
			transport.toolCallResponseError = tc.remote
			connectCalls = 0
			manager := NewManager()
			manager.servers["test"] = conn
			t.Cleanup(func() { _ = manager.Close() })
			_, err = manager.CallTool(t.Context(), "test", "echo", nil)
			var fatal *CallTransportError
			if errors.As(err, &fatal) != tc.fatal {
				t.Fatalf("CallTool() error = %v, fatal = %t, want %t", err, fatal != nil, tc.fatal)
			}
			if tc.fatal &&
				((tc.err != nil && !errors.Is(err, tc.err)) || fatal.Err == nil || strings.Contains(err.Error(), "private-response-canary")) {
				t.Fatalf("malformed response cause was lost or exposed: %v", err)
			}
			if !tc.fatal &&
				((tc.err != nil && !errors.Is(err, tc.err)) || (tc.remote != nil && !errors.Is(err, tc.remote))) {
				t.Fatalf("ordinary cause was lost: %v", err)
			}
			if connectCalls != 0 {
				t.Fatalf("reconnects = %d, want 0", connectCalls)
			}
			if transport.toolCallCalls != 1 {
				t.Fatalf("tool calls = %d, want 1", transport.toolCallCalls)
			}
		})
	}
}

func TestReconnectClassificationUsesCausesBeforeCompatibilityText(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "EOF", err: io.EOF, want: true},
		{name: "closed pipe", err: io.ErrClosedPipe, want: true},
		{name: "closed file", err: os.ErrClosed, want: true},
		{name: "missing session", err: sdkmcp.ErrSessionMissing, want: true},
		{name: "closed connection", err: sdkmcp.ErrConnectionClosed, want: true},
		{name: "canceled with misleading wrapper", err: fmt.Errorf("connection closed: %w", context.Canceled)},
		{name: "timeout with misleading wrapper", err: fmt.Errorf("unexpected EOF: %w", context.DeadlineExceeded)},
		{name: "remote server error", err: &jsonrpc.Error{Code: -32603, Message: "broken pipe in supplied document"}},
		{name: "typeof", err: errors.New("typeof failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReconnectCallError(tc.err); got != tc.want {
				t.Fatalf("shouldReconnectCallError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
