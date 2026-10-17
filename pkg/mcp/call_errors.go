package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CallTransportError identifies a malformed MCP response that prevents reliable
// tool execution. Its cause stays available internally without entering model
// output or runtime events through Error.
type CallTransportError struct {
	Err error
}

func (*CallTransportError) Error() string {
	return "MCP tool transport returned an invalid response"
}

func (e *CallTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func callCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func malformedCallResponse(err error) bool {
	if err == nil || callCanceled(err) {
		return false
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func shouldReconnectCallError(err error) bool {
	if err == nil || callCanceled(err) {
		return false
	}
	if errors.Is(err, mcp.ErrSessionMissing) || errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) {
		return true
	}
	// Remote JSON-RPC errors describe application failures. Their message is
	// server-controlled and must not turn an ordinary error into session loss.
	var remoteErr *jsonrpc.Error
	if errors.As(err, &remoteErr) {
		return false
	}

	// Keep compatibility with SDK/stdio wrappers that flatten closed pipe
	// causes into text here, before any user-facing summary is produced.
	message := strings.ToLower(err.Error())
	return strings.Contains(message, mcp.ErrSessionMissing.Error()) ||
		strings.Contains(message, "client is closing") ||
		strings.Contains(message, "connection closed") ||
		strings.Contains(message, "file already closed") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "unexpected eof")
}
