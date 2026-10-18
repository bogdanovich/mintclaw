// Package cua provides an explicitly injected, private, read-only Cua Driver
// adapter. The driver is an OS mechanism, not a public MCP server/model tool.
package cua

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/mcp"
)

// Version is the qualified protocol version, not a floating latest release.
const Version = "0.34.1"

// Config is trusted local configuration. ScopeAlive must attest the exact
// selected window incarnation, not merely that its PID is alive. In particular,
// an attached-window host must detect reused native window IDs. No production
// configuration/facade constructs this adapter until that admission is wired.
type Config struct {
	Executable string
	SHA256     string
	Target     computer.Target
	PID        uint32
	Env        map[string]string
	ScopeAlive func(context.Context, computer.Scope) bool
}

type Driver struct {
	mu      sync.Mutex
	config  Config
	pipe    mcp.IsolatedCommandConnection
	session *sdkmcp.ClientSession
	dir     string
	closed  bool
}

var _ computer.CaptureDriver = (*Driver)(nil)

func New(ctx context.Context, config Config) (*Driver, error) {
	return newDriver(ctx, config, []string{"get_window_state"})
}

func newDriver(ctx context.Context, config Config, tools []string) (*Driver, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	windowID, err := strconv.ParseUint(config.Target.Scope.WindowID, 10, 64)
	if err != nil || windowID == 0 || config.PID == 0 || config.Target.Validate() != nil ||
		config.Target.Backend != "cua" || config.ScopeAlive == nil || !filepath.IsAbs(config.Executable) ||
		len(config.SHA256) != 64 || runtime.GOOS != "linux" && runtime.GOOS != "darwin" ||
		runtime.GOOS == "linux" && config.Target.OS != computer.OSLinux ||
		runtime.GOOS == "darwin" && config.Target.OS != computer.OSMacOS {
		return nil, computer.ErrInvalid
	}
	info, err := os.Lstat(config.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return nil, computer.ErrDenied
	}
	file, err := os.Open(config.Executable)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != config.SHA256 {
		return nil, computer.ErrDenied
	}
	// Explicit minimal environment: no ambient provider credentials, grants,
	// daemon endpoint, telemetry preference or driver history configuration.
	env := []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "CUA_DRIVER_RS_TELEMETRY_ENABLED=0", "CUA_DRIVER_PERMISSION_MODE=bounded",
		// An owned sidecar must inherit the host's OS identity, not relaunch as
		// a standalone daemon or raise its own permission/setup UI.
		"CUA_DRIVER_EMBEDDED=1",
		// Never advertise accessibility by changing desktop-wide settings. The
		// driver's automatic advertisement can enable an audible screen reader.
		"CUA_DRIVER_RS_DISABLE_A11Y_ADVERTISE=1", "CUA_DRIVER_RS_A11Y_ADVERTISE_MODE=none",
	}
	for key, value := range config.Env {
		switch key {
		case "DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS":
		default:
			return nil, computer.ErrInvalid
		}
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, computer.ErrInvalid
		}
		env = append(env, key+"="+value)
	}
	if !config.ScopeAlive(ctx, config.Target.Scope) {
		return nil, computer.ErrStale
	}
	dir, err := os.MkdirTemp("", "mintclaw-cua-worker-")
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	manifest := map[string]any{
		"version": 3, "expires_after": "30m", "idle_timeout": "5m",
		"allow": map[string]any{"tools": tools},
		"resources": map[string]any{"desktop": map[string]any{
			"windows": []map[string]any{{"pid": config.PID, "window_id": windowID}},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, computer.ErrInvalid
	}
	manifestPath := filepath.Join(dir, "capabilities.json")
	if err = os.WriteFile(manifestPath, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, computer.ErrUnavailable
	}
	env = append(env, "CUA_DRIVER_CAPABILITY_MANIFEST_FILE="+manifestPath, "CUA_DRIVER_CAPABILITY_MANIFEST_APPROVED=1")
	sort.Strings(env)
	command := exec.Command(config.Executable, "mcp", "--direct", "--no-overlay")
	command.Env = env
	command.Dir = dir
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pipe, err := mcp.StartPrivateIsolatedCommand(startupCtx, "computer-cua", command, time.Second)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, computer.ErrUnavailable
	}
	driver := &Driver{config: config, pipe: pipe, dir: dir}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "mintclaw-private-computer", Version: "1"}, nil)
	driver.session, err = client.Connect(startupCtx, &sdkmcp.IOTransport{
		Reader: newBoundedReader(pipe), Writer: pipe,
	}, nil)
	if err != nil || driver.session.InitializeResult().ServerInfo.Version != Version {
		_ = driver.Close()
		return nil, computer.ErrUnavailable
	}
	return driver, nil
}

func (driver *Driver) Capture(ctx context.Context, target computer.Target) (computer.Capture, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.closed {
		return computer.Capture{}, computer.ErrUnavailable
	}
	if target != driver.config.Target || !driver.config.ScopeAlive(ctx, target.Scope) {
		return computer.Capture{}, computer.ErrStale
	}
	windowID, _ := strconv.ParseUint(target.Scope.WindowID, 10, 64)
	result, err := driver.call(ctx, "get_window_state", map[string]any{
		"pid": driver.config.PID, "window_id": windowID,
		// The pinned Linux AT-SPI bridge correlates roots by geometry rather
		// than native window identity. Do not request an unproven subtree.
		"include_screenshot": true, "include_accessibility_tree": false,
		"max_elements": 128, "max_depth": 12, "max_image_dimension": 0, "timeout_ms": 1000,
	})
	if err != nil {
		return computer.Capture{}, err
	}
	if !driver.config.ScopeAlive(ctx, target.Scope) {
		return computer.Capture{}, computer.ErrStale
	}
	capture, err := decodeCapture(result, target.Scope, driver.config.PID, windowID)
	// Defense in depth if the worker ignores the requested projection. Exact
	// native AX-root attestation is a separate capability, not a PID heuristic.
	capture.AccessibilityText = ""
	return capture, err
}

// call is private to the adapter/qualification tests. It never retries a lost
// call or reconnects automatically. Cancellation terminates the owned worker;
// any replacement must get new trusted worker/session identity.
func (driver *Driver) call(ctx context.Context, tool string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := driver.session.CallTool(callCtx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || callCtx.Err() != nil {
		driver.closed = true
		_ = driver.pipe.Abort()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, computer.ErrUnavailable
	}
	if result == nil || result.IsError {
		return nil, computer.ErrUnavailable
	}
	return result, nil
}

// Close destroys only the private worker, never the selected application.
func (driver *Driver) Close() error {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.closed = true
	if driver.pipe != nil {
		if err := driver.pipe.Abort(); err != nil {
			return computer.ErrUnavailable
		}
	}
	if driver.session != nil {
		_ = driver.session.Close()
	}
	if err := os.RemoveAll(driver.dir); err != nil {
		return computer.ErrUnavailable
	}
	return nil
}

type windowState struct {
	PID          uint32  `json:"pid"`
	WindowID     uint64  `json:"window_id"`
	Width        int     `json:"screenshot_width"`
	Height       int     `json:"screenshot_height"`
	Scale        float64 `json:"screenshot_scale"`
	MIME         string  `json:"screenshot_mime_type"`
	FrameValid   bool    `json:"screenshot_frame_valid"`
	Composited   bool    `json:"screenshot_composited"`
	Tree         string  `json:"tree_markdown"`
	WindowBounds struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"window_bounds"`
}

func decodeCapture(
	result *sdkmcp.CallToolResult, scope computer.Scope, pid uint32, windowID uint64,
) (computer.Capture, error) {
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return computer.Capture{}, computer.ErrInvalid
	}
	var state windowState
	if json.Unmarshal(data, &state) != nil || state.PID != pid || state.WindowID != windowID ||
		!state.FrameValid || state.Composited {
		return computer.Capture{}, computer.ErrStale
	}
	// The pinned live platform output may omit screenshot_scale. Derive the
	// actual pixel/logical transform from both attested native frame dimensions;
	// never assume scale=1, and reject inconsistent/cropped image geometry.
	if state.WindowBounds.Width <= 0 || state.WindowBounds.Height <= 0 {
		return computer.Capture{}, computer.ErrInvalid
	}
	scale := float64(state.Width) / state.WindowBounds.Width
	if math.Abs(float64(state.Height)-state.WindowBounds.Height*scale) > 1 ||
		state.Scale != 0 && math.Abs(state.Scale-scale)*state.WindowBounds.Width > 1 {
		return computer.Capture{}, computer.ErrInvalid
	}
	capture := computer.Capture{
		Scope: scope, ContentType: state.MIME,
		Geometry: computer.Geometry{
			Width: state.Width, Height: state.Height, Scale: scale,
			OriginX: state.WindowBounds.X, OriginY: state.WindowBounds.Y,
		},
		AccessibilityText: state.Tree,
	}
	for _, content := range result.Content {
		if img, ok := content.(*sdkmcp.ImageContent); ok {
			if len(capture.Screenshot) != 0 || img.MIMEType != state.MIME || len(img.Data) > computer.MaxCaptureBytes {
				return computer.Capture{}, computer.ErrInvalid
			}
			capture.Screenshot = img.Data
		}
	}
	if len(capture.Screenshot) == 0 || len(capture.AccessibilityText) > computer.MaxAccessibilityBytes ||
		capture.Geometry.Validate() != nil {
		return computer.Capture{}, computer.ErrInvalid
	}
	imageConfig, format, err := image.DecodeConfig(bytes.NewReader(capture.Screenshot))
	if err != nil || imageConfig.Width != state.Width || imageConfig.Height != state.Height ||
		format != "png" && format != "jpeg" || state.MIME != "image/"+format {
		return computer.Capture{}, computer.ErrInvalid
	}
	return capture, nil
}

// NDJSON framing is bounded before SDK decoding/allocation. No stdout/stderr
// payload is mirrored into logs, history, artifacts or a screenshot HTTP server.
type boundedReader struct {
	pipe    io.ReadCloser
	scanner *bufio.Scanner
	pending []byte
}

func newBoundedReader(pipe io.ReadCloser) *boundedReader {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 4096), 2*computer.MaxCaptureBytes+computer.MaxAccessibilityBytes)
	return &boundedReader{pipe: pipe, scanner: scanner}
}

func (reader *boundedReader) Read(data []byte) (int, error) {
	if len(reader.pending) == 0 {
		if !reader.scanner.Scan() {
			if reader.scanner.Err() != nil {
				return 0, errors.New("private computer response exceeds framing limit or is unavailable")
			}
			return 0, io.EOF
		}
		reader.pending = append(append([]byte(nil), reader.scanner.Bytes()...), '\n')
	}
	n := copy(data, reader.pending)
	reader.pending = reader.pending[n:]
	return n, nil
}

func (reader *boundedReader) Close() error { return reader.pipe.Close() }
