package cua

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 4, 2))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func fixtureResult(t *testing.T) *sdkmcp.CallToolResult {
	t.Helper()
	return &sdkmcp.CallToolResult{
		StructuredContent: map[string]any{
			"pid": 42, "window_id": 7, "screenshot_width": 4, "screenshot_height": 2,
			"screenshot_mime_type": "image/png", "screenshot_frame_valid": true,
			"window_bounds": map[string]any{"x": -100, "y": 50, "width": 2, "height": 1},
			"tree_markdown": "PROTECTED_FIXTURE_TEXT",
		},
		Content: []sdkmcp.Content{&sdkmcp.ImageContent{MIMEType: "image/png", Data: smallPNG(t)}},
	}
}

func TestDecodeCaptureGeometryAndPrivacy(t *testing.T) {
	scope := computer.Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "7", WindowGeneration: 1}
	capture, err := decodeCapture(fixtureResult(t), scope, 42, 7)
	if err != nil || capture.Geometry.Scale != 2 || capture.Geometry.OriginX != -100 {
		t.Fatalf("capture = %v, %v", capture, err)
	}
	x, y, err := capture.Geometry.DesktopPoint(2, 1)
	if err != nil || x != -99 || y != 50.5 {
		t.Fatal("wrong pixel-to-logical transform")
	}
	data, err := json.Marshal(capture)
	if err != nil || strings.Contains(string(data), "PROTECTED") ||
		strings.Contains(fmt.Sprintf("%#v", capture), "PROTECTED") {
		t.Fatal("protected payload escaped")
	}
	for name, mutate := range map[string]func(*sdkmcp.CallToolResult){
		"identity": func(r *sdkmcp.CallToolResult) { r.StructuredContent.(map[string]any)["pid"] = 99 },
		"frame":    func(r *sdkmcp.CallToolResult) { r.StructuredContent.(map[string]any)["screenshot_frame_valid"] = false },
		"geometry": func(r *sdkmcp.CallToolResult) {
			r.StructuredContent.(map[string]any)["window_bounds"] = map[string]any{"width": 3, "height": 100}
		},
		"scale":         func(r *sdkmcp.CallToolResult) { r.StructuredContent.(map[string]any)["screenshot_scale"] = 9 },
		"image":         func(r *sdkmcp.CallToolResult) { r.Content[0].(*sdkmcp.ImageContent).Data = []byte("invalid") },
		"mime":          func(r *sdkmcp.CallToolResult) { r.Content[0].(*sdkmcp.ImageContent).MIMEType = "image/jpeg" },
		"duplicate":     func(r *sdkmcp.CallToolResult) { r.Content = append(r.Content, r.Content[0]) },
		"missing-image": func(r *sdkmcp.CallToolResult) { r.Content = nil },
		"oversize-text": func(r *sdkmcp.CallToolResult) {
			r.StructuredContent.(map[string]any)["tree_markdown"] = strings.Repeat("x", computer.MaxAccessibilityBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := fixtureResult(t)
			mutate(result)
			if capture, err := decodeCapture(result, scope, 42, 7); err == nil || len(capture.Screenshot) != 0 {
				t.Fatal("invalid capture admitted")
			}
		})
	}
}

func TestBoundedPrivateReader(t *testing.T) {
	reader := newBoundedReader(io.NopCloser(strings.NewReader("{\"one\":1}\n{\"two\":2}\n")))
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "{\"one\":1}\n{\"two\":2}\n" {
		t.Fatal("NDJSON framing changed", err)
	}
	reader = newBoundedReader(
		io.NopCloser(strings.NewReader(strings.Repeat("x", 2*computer.MaxCaptureBytes+computer.MaxAccessibilityBytes))),
	)
	data, err = io.ReadAll(reader)
	if err == nil || len(data) != 0 {
		t.Fatal("oversize frame admitted")
	}
}

func helperConfig(t *testing.T, mode string) Config {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private native worker supports Linux/macOS only")
	}
	path := filepath.Join(t.TempDir(), "private-driver")
	body := fmt.Sprintf(
		"#!/bin/sh\nexport MINTCLAW_PRIVATE_CUA_HELPER=%s\nexec '%s' -test.run=TestPrivateCuaHelper\n",
		mode,
		os.Args[0],
	)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	target := computer.Target{
		Alias: "fixture", Placement: computer.PlacementGateway, OS: computer.OSLinux, Mode: computer.ModeAttached,
		Isolation: computer.IsolationNone, Backend: "cua",
		Scope: computer.Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "7", WindowGeneration: 1},
	}
	if runtime.GOOS == "darwin" {
		target.OS = computer.OSMacOS
	}
	return Config{
		Executable: path, SHA256: hex.EncodeToString(sum[:]), Target: target, PID: 42,
		ScopeAlive: func(context.Context, computer.Scope) bool { return true },
	}
}

func TestPrivateDriverPinnedAdmission(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"hash":           func(c *Config) { c.SHA256 = strings.Repeat("0", 64) },
		"credential-env": func(c *Config) { c.Env = map[string]string{"OPENAI_API_KEY": "must-not-inherit"} },
		"accessibility-override": func(c *Config) {
			c.Env = map[string]string{"CUA_DRIVER_RS_DISABLE_A11Y_ADVERTISE": "0"}
		},
		"window":        func(c *Config) { c.Target.Scope.WindowID = "ambient" },
		"guard":         func(c *Config) { c.ScopeAlive = func(context.Context, computer.Scope) bool { return false } },
		"missing-guard": func(c *Config) { c.ScopeAlive = nil },
	} {
		t.Run(name, func(t *testing.T) {
			config := helperConfig(t, "success")
			mutate(&config)
			if driver, err := New(t.Context(), config); err == nil || driver != nil {
				t.Fatal("invalid driver admission accepted")
			}
		})
	}
	config := helperConfig(t, "success")
	driver, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	manifestBytes, err := os.ReadFile(filepath.Join(driver.dir, "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if json.Unmarshal(manifestBytes, &manifest) != nil || strings.Contains(string(manifestBytes), "type_text") ||
		strings.Contains(string(manifestBytes), "existing_profile") || manifest["version"].(float64) != 3 {
		t.Fatal("private manifest admits broader authority")
	}
	capture, err := driver.Capture(t.Context(), config.Target)
	if err != nil || len(capture.Screenshot) == 0 {
		t.Fatal(err)
	}
	other := config.Target
	other.Scope.WindowGeneration++
	if _, err = driver.Capture(t.Context(), other); !errors.Is(err, computer.ErrStale) {
		t.Fatal("changed scope accepted", err)
	}
	if err = driver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(driver.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private metadata directory retained after worker close")
	}
}

func TestPrivateDriverCancellationAndSanitizedErrors(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			config := helperConfig(t, mode)
			driver, err := New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = driver.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			capture, err := driver.Capture(ctx, config.Target)
			if err == nil || strings.Contains(err.Error(), "PROTECTED") || len(capture.Screenshot) != 0 {
				t.Fatal("private error/canceled payload leaked")
			}
			if mode == "cancel" {
				if !driver.closed || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("canceled worker remained reusable", err)
				}
				if _, err = driver.Capture(t.Context(), config.Target); !errors.Is(err, computer.ErrUnavailable) {
					t.Fatal("worker auto-restarted after cancellation")
				}
			}
		})
	}
}

func TestPrivateCuaHelper(t *testing.T) {
	mode := os.Getenv("MINTCLAW_PRIVATE_CUA_HELPER")
	if mode == "" {
		return
	}
	if os.Getenv("CUA_DRIVER_RS_TELEMETRY_ENABLED") != "0" || os.Getenv("CUA_DRIVER_PERMISSION_MODE") != "bounded" ||
		os.Getenv("CUA_DRIVER_EMBEDDED") != "1" ||
		os.Getenv("CUA_DRIVER_RS_DISABLE_A11Y_ADVERTISE") != "1" ||
		os.Getenv("CUA_DRIVER_RS_A11Y_ADVERTISE_MODE") != "none" ||
		os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CUA_DRIVER_CAPABILITY_MANIFEST_APPROVED") != "1" {
		os.Exit(3)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "private-cua-fixture", Version: Version}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "get_window_state", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			if mode == "error" {
				return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: "PROTECTED_ERROR_TEXT"},
				}}, nil
			}
			if mode == "cancel" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return fixtureResult(t), nil
		})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
