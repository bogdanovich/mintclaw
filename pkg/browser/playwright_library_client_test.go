package browser

import (
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type retryableLibraryConnection struct {
	mu          sync.Mutex
	closeErrors []error
	abortErrors []error
	closeCalls  int
	abortCalls  int
}

func (connection *retryableLibraryConnection) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (connection *retryableLibraryConnection) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (connection *retryableLibraryConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	index := connection.closeCalls
	connection.closeCalls++
	if index < len(connection.closeErrors) {
		return connection.closeErrors[index]
	}
	return nil
}

func (connection *retryableLibraryConnection) Abort() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	index := connection.abortCalls
	connection.abortCalls++
	if index < len(connection.abortErrors) {
		return connection.abortErrors[index]
	}
	return nil
}

func TestDecodePlaywrightLibraryResultBoundsContentTypes(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	result, err := decodePlaywrightLibraryResult(playwrightLibraryWireResponse{
		ID: 1,
		Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{
			{Type: "text", Text: "bounded"},
			{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(png)},
		}},
	})
	if err != nil || result == nil || len(result.Content) != 2 {
		t.Fatalf("decodePlaywrightLibraryResult() = %#v, %v", result, err)
	}
	for _, wire := range []playwrightLibraryWireResponse{
		{ID: 1, Error: "private failure"},
		{ID: 1, Result: &playwrightLibraryWireResult{}},
		{ID: 1, Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{Type: "audio"}}}},
		{ID: 1, Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{
			Type: "image", MIMEType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(png),
		}}}},
	} {
		if decoded, decodeErr := decodePlaywrightLibraryResult(wire); decodeErr == nil || decoded != nil {
			t.Fatalf("malformed response decoded as %#v, %v", decoded, decodeErr)
		}
	}
}

func TestPlaywrightLibraryCatalogMatchesPinnedWorkerContract(t *testing.T) {
	catalog := playwrightLibraryCatalog()
	revision, err := validatePlaywrightCatalog(catalog)
	if err != nil || revision == "" || len(catalog) != len(pinnedPlaywrightToolSchemas) {
		t.Fatalf("direct catalog revision = %q, tools = %d, error = %v", revision, len(catalog), err)
	}
}

func TestPlaywrightLibraryExecutionTimeoutRequiresExactPrivateResponse(t *testing.T) {
	exact := &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: playwrightLibraryExecutionTimeoutText},
		},
	}
	if !playwrightLibraryExecutionTimedOut(exact) {
		t.Fatal("exact privileged execution timeout was not classified")
	}
	for _, result := range []*sdkmcp.CallToolResult{
		nil,
		{IsError: false, Content: exact.Content},
		{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: playwrightLibraryExecutionTimeoutText + " private detail"},
		}},
		{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: playwrightLibraryExecutionTimeoutText},
			&sdkmcp.TextContent{Text: "extra"},
		}},
	} {
		if playwrightLibraryExecutionTimedOut(result) {
			t.Fatalf("non-exact privileged execution error classified as timeout: %#v", result)
		}
	}
}

func TestPlaywrightLibraryCloseRetriesProcessCleanup(t *testing.T) {
	transient := errors.New("transient process cleanup failure")
	connection := &retryableLibraryConnection{closeErrors: []error{transient, nil}}
	client := &playwrightLibraryClient{
		connection: connection,
		done:       closedLibraryDone(),
		readErr:    io.EOF,
	}
	if err := client.Close(); !errors.Is(err, transient) {
		t.Fatalf("first Close() error = %v, want %v", err, transient)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("idempotent Close() error = %v", err)
	}
	if connection.closeCalls != 2 {
		t.Fatalf("connection Close() calls = %d, want 2", connection.closeCalls)
	}
}

func TestPlaywrightLibraryCloseAcceptsUnavailableShutdownProtocol(t *testing.T) {
	connection := &retryableLibraryConnection{}
	client := &playwrightLibraryClient{
		connection: connection,
		done:       closedLibraryDone(),
		readErr:    io.ErrClosedPipe,
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("connection Close() calls = %d, want 1", connection.closeCalls)
	}
}

func TestPlaywrightLibraryAbortRetriesProcessCleanup(t *testing.T) {
	transient := errors.New("transient process abort failure")
	connection := &retryableLibraryConnection{abortErrors: []error{transient, nil}}
	client := &playwrightLibraryClient{connection: connection}
	if err := client.Abort(); !errors.Is(err, transient) {
		t.Fatalf("first Abort() error = %v, want %v", err, transient)
	}
	if err := client.Abort(); err != nil {
		t.Fatalf("retry Abort() error = %v", err)
	}
	if err := client.Abort(); err != nil {
		t.Fatalf("idempotent Abort() error = %v", err)
	}
	if connection.abortCalls != 2 {
		t.Fatalf("connection Abort() calls = %d, want 2", connection.abortCalls)
	}
}

func closedLibraryDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
