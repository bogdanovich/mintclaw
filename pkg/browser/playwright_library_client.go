package browser

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
	localmcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

const (
	playwrightLibraryProtocolResponseBytes = 16 * 1024 * 1024
	playwrightLibraryShutdownTimeout       = 5 * time.Second
	playwrightLibraryProtocolVersion       = "mintclaw.playwright_library.v1"
)

type playwrightLibraryWireRequest struct {
	ID     uint64         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type playwrightLibraryWireContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type playwrightLibraryWireResult struct {
	IsError bool                           `json:"is_error"`
	Content []playwrightLibraryWireContent `json:"content"`
}

type playwrightLibraryWireResponse struct {
	ID     uint64                       `json:"id"`
	Result *playwrightLibraryWireResult `json:"result,omitempty"`
	Error  string                       `json:"error,omitempty"`
}

type playwrightLibraryCallResponse struct {
	result *sdkmcp.CallToolResult
	err    error
}

type playwrightLibraryExecutionEnvelope struct {
	Value           json.RawMessage `json:"value"`
	Actions         int             `json:"actions"`
	NetworkRequests int             `json:"network_requests"`
}

// playwrightLibraryClient speaks MintClaw's bounded JSON-lines protocol to
// the official Playwright-library sidecar. It neither initializes MCP nor
// exposes the sidecar operation catalog outside this package.
type playwrightLibraryClient struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	nextID  uint64
	pending map[uint64]chan playwrightLibraryCallResponse
	done    chan struct{}
	readErr error

	connection localmcp.IsolatedCommandConnection
	lease      *localmcp.ExclusiveServerLease
	closeMu    sync.Mutex
	closed     bool
}

func newLibraryPlaywrightClient() playwrightDriverClient {
	return &playwrightLibraryClient{}
}

func (client *playwrightLibraryClient) Connect(
	ctx context.Context,
	_ string,
	cfg config.MCPServerConfig,
) ([]*sdkmcp.Tool, error) {
	if client == nil || cfg.Type != "stdio" || strings.TrimSpace(cfg.Command) == "" ||
		strings.TrimSpace(cfg.ExclusiveLockFile) == "" || cfg.EnvFile != "" {
		return nil, errors.New("invalid playwright library sidecar configuration")
	}
	lease, err := localmcp.AcquireExclusiveServerLease(playwrightPrivateServerName, cfg.ExclusiveLockFile)
	if err != nil {
		return nil, err
	}
	command := exec.Command(cfg.Command, cfg.Args...)
	command.Env, err = playwrightLibraryEnvironment(os.Environ(), cfg.Env)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	connection, err := localmcp.StartIsolatedCommand(
		ctx,
		"browser_playwright_library",
		command,
		playwrightLibraryShutdownTimeout,
	)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	client.mu.Lock()
	client.connection = connection
	client.lease = lease
	client.pending = make(map[uint64]chan playwrightLibraryCallResponse)
	client.done = make(chan struct{})
	client.mu.Unlock()
	go client.readResponses(connection)
	initialized, err := client.call(ctx, "mintclaw_initialize", map[string]any{
		"protocol": playwrightLibraryProtocolVersion,
	})
	if err == nil {
		if initialized == nil || initialized.IsError || len(initialized.Content) != 1 {
			err = errors.New("playwright library sidecar initialization failed")
		} else if content, ok := initialized.Content[0].(*sdkmcp.TextContent); !ok ||
			content.Text != "MINTCLAW_PLAYWRIGHT_LIBRARY_V1|ready" {
			err = errors.New("playwright library sidecar protocol is incompatible")
		}
	}
	if err != nil {
		return nil, errors.Join(err, client.Abort())
	}
	return playwrightLibraryCatalog(), nil
}

func playwrightLibraryEnvironment(parent []string, explicit map[string]string) ([]string, error) {
	type value struct{ name, content string }
	values := make(map[string]value)
	normalize := func(name string) string {
		if runtime.GOOS == "windows" {
			return strings.ToLower(name)
		}
		return name
	}
	for _, entry := range parent {
		if index := strings.IndexByte(entry, '='); index > 0 {
			values[normalize(entry[:index])] = value{entry[:index], entry[index+1:]}
		}
	}
	for name, content := range explicit {
		if !localmcp.IsValidEnvironmentName(name) || strings.ContainsRune(content, 0) {
			return nil, errors.New("invalid playwright library sidecar environment")
		}
		values[normalize(name)] = value{name, content}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		item := values[key]
		result = append(result, item.name+"="+item.content)
	}
	return result, nil
}

func playwrightLibraryCatalog() []*sdkmcp.Tool {
	names := make([]string, 0, len(pinnedPlaywrightToolSchemas))
	for name := range pinnedPlaywrightToolSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]*sdkmcp.Tool, 0, len(names))
	for _, name := range names {
		var schema any
		if json.Unmarshal(pinnedPlaywrightToolSchemas[name], &schema) != nil {
			return nil
		}
		tools = append(tools, &sdkmcp.Tool{Name: name, InputSchema: schema})
	}
	return tools
}

func (client *playwrightLibraryClient) Ping(ctx context.Context) error {
	result, err := client.call(ctx, "browser_ping", map[string]any{})
	if err != nil {
		return err
	}
	if result == nil || result.IsError {
		return errors.New("playwright library sidecar rejected ping")
	}
	return nil
}

func (client *playwrightLibraryClient) CallTool(
	ctx context.Context,
	tool string,
	arguments map[string]any,
) (*sdkmcp.CallToolResult, error) {
	return client.call(ctx, tool, arguments)
}

func (client *playwrightLibraryClient) ExecutePrivileged(
	ctx context.Context,
	request DriverExecutionRequest,
) (DriverExecutionResult, error) {
	result, err := client.call(ctx, "mintclaw_browser_execute", map[string]any{
		"source":   request.Source,
		"language": request.Language,
		"effect":   request.Effect,
		"limits":   request.Limits.Effective(),
		"network": map[string]any{
			"mode":            request.NetworkMode,
			"allowed_origins": append([]string(nil), request.AllowedOrigins...),
		},
	})
	if err != nil {
		return DriverExecutionResult{}, err
	}
	if result == nil || result.IsError || len(result.Content) == 0 {
		return DriverExecutionResult{}, ErrDriverRejected
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || len(text.Text) > request.Limits.Effective().OutputBytes+4096 {
		return DriverExecutionResult{}, ErrDriverIncompatible
	}
	var envelope playwrightLibraryExecutionEnvelope
	if json.Unmarshal([]byte(text.Text), &envelope) != nil || len(envelope.Value) == 0 ||
		!json.Valid(envelope.Value) || envelope.Actions < 0 ||
		envelope.Actions > request.Limits.Effective().Actions || envelope.NetworkRequests < 0 ||
		envelope.NetworkRequests > request.Limits.Effective().NetworkRequests {
		return DriverExecutionResult{}, ErrDriverIncompatible
	}
	decoded := DriverExecutionResult{
		Value:   append(json.RawMessage(nil), envelope.Value...),
		Actions: envelope.Actions, NetworkRequests: envelope.NetworkRequests,
	}
	var artifactBytes int
	for _, content := range result.Content[1:] {
		image, imageOK := content.(*sdkmcp.ImageContent)
		if !imageOK || image.MIMEType != "image/png" || len(image.Data) == 0 {
			return DriverExecutionResult{}, ErrDriverIncompatible
		}
		artifactBytes += len(image.Data)
		if len(decoded.Artifacts) >= request.Limits.Effective().Artifacts ||
			artifactBytes > request.Limits.Effective().ArtifactBytes {
			return DriverExecutionResult{}, ErrDriverIncompatible
		}
		decoded.Artifacts = append(decoded.Artifacts, DriverScreenshot{
			Data: append([]byte(nil), image.Data...), ContentType: "image/png",
		})
	}
	return decoded, nil
}

func (client *playwrightLibraryClient) call(
	ctx context.Context,
	method string,
	params map[string]any,
) (*sdkmcp.CallToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client.mu.Lock()
	if client.connection == nil || client.done == nil {
		client.mu.Unlock()
		return nil, errors.New("playwright library sidecar is unavailable")
	}
	select {
	case <-client.done:
		err := client.readErr
		client.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, err
	default:
	}
	client.nextID++
	id := client.nextID
	response := make(chan playwrightLibraryCallResponse, 1)
	client.pending[id] = response
	connection := client.connection
	client.mu.Unlock()

	encoded, err := json.Marshal(playwrightLibraryWireRequest{ID: id, Method: method, Params: params})
	if err == nil {
		encoded = append(encoded, '\n')
		client.writeMu.Lock()
		_, err = connection.Write(encoded)
		client.writeMu.Unlock()
	}
	if err != nil {
		client.removePending(id)
		return nil, err
	}
	select {
	case outcome := <-response:
		return outcome.result, outcome.err
	case <-ctx.Done():
		client.removePending(id)
		return nil, ctx.Err()
	case <-client.done:
		client.removePending(id)
		client.mu.Lock()
		err = client.readErr
		client.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, err
	}
}

func (client *playwrightLibraryClient) removePending(id uint64) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func (client *playwrightLibraryClient) readResponses(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), playwrightLibraryProtocolResponseBytes)
	for scanner.Scan() {
		var wire playwrightLibraryWireResponse
		if err := json.Unmarshal(scanner.Bytes(), &wire); err != nil || wire.ID == 0 {
			client.finishReads(errors.New("invalid playwright library sidecar response"))
			return
		}
		result, err := decodePlaywrightLibraryResult(wire)
		client.mu.Lock()
		response := client.pending[wire.ID]
		delete(client.pending, wire.ID)
		client.mu.Unlock()
		if response != nil {
			response <- playwrightLibraryCallResponse{result: result, err: err}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	client.finishReads(err)
}

func decodePlaywrightLibraryResult(
	wire playwrightLibraryWireResponse,
) (*sdkmcp.CallToolResult, error) {
	if wire.Error != "" || wire.Result == nil || len(wire.Result.Content) == 0 ||
		len(wire.Result.Content) > config.BrowserMaxExecuteArtifacts+1 {
		return nil, errors.New("playwright library sidecar rejected the private protocol request")
	}
	result := &sdkmcp.CallToolResult{IsError: wire.Result.IsError}
	for _, content := range wire.Result.Content {
		switch content.Type {
		case "text":
			result.Content = append(result.Content, &sdkmcp.TextContent{Text: content.Text})
		case "image":
			data, err := base64.StdEncoding.DecodeString(content.Data)
			if err != nil || content.MIMEType != "image/png" || len(data) == 0 {
				return nil, errors.New("invalid playwright library sidecar image")
			}
			result.Content = append(result.Content, &sdkmcp.ImageContent{
				Data: data, MIMEType: content.MIMEType,
			})
		default:
			return nil, errors.New("unsupported playwright library sidecar content")
		}
	}
	return result, nil
}

func (client *playwrightLibraryClient) finishReads(err error) {
	client.mu.Lock()
	if client.done == nil {
		client.mu.Unlock()
		return
	}
	select {
	case <-client.done:
		client.mu.Unlock()
		return
	default:
	}
	client.readErr = err
	close(client.done)
	for id, response := range client.pending {
		delete(client.pending, id)
		response <- playwrightLibraryCallResponse{err: err}
	}
	client.mu.Unlock()
}

func (client *playwrightLibraryClient) Close() error {
	client.closeMu.Lock()
	defer client.closeMu.Unlock()
	if client.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), playwrightLibraryShutdownTimeout)
	defer cancel()
	// The private shutdown request is cooperative and best effort. Successful
	// process-tree cleanup below is the cleanup authority even when the sidecar
	// has already exited or its protocol channel is unavailable.
	_, _ = client.call(ctx, "shutdown", map[string]any{})
	client.mu.Lock()
	connection, lease := client.connection, client.lease
	client.mu.Unlock()
	if connection != nil {
		if err := connection.Close(); err != nil {
			return err
		}
	}
	if lease != nil {
		if err := lease.Close(); err != nil {
			return err
		}
	}
	client.closed = true
	return nil
}

func (client *playwrightLibraryClient) Abort() error {
	client.closeMu.Lock()
	defer client.closeMu.Unlock()
	if client.closed {
		return nil
	}
	client.mu.Lock()
	connection, lease := client.connection, client.lease
	client.mu.Unlock()
	if connection != nil {
		if err := connection.Abort(); err != nil {
			return err
		}
	}
	if lease != nil {
		if err := lease.Close(); err != nil {
			return err
		}
	}
	client.closed = true
	return nil
}

var _ playwrightDriverClient = (*playwrightLibraryClient)(nil)
