package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

func TestGatewayJSONConsoleNativeStartupAndShutdownWithoutModels(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native signal shutdown control runs on Linux and macOS")
	}
	home := t.TempDir()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if listener.Close() != nil {
		t.Fatal("cannot release startup test address")
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port, cfg.Gateway.LogLevel = "127.0.0.1", port, "info"
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true, Workspace: cfg.Agents.Defaults.Workspace}}
	cfg.ModelList, cfg.Channels = nil, make(config.ChannelsConfig)
	cfg.Heartbeat.Enabled, cfg.Devices.Enabled, cfg.Tools.MCP.Enabled = false, false, false
	configPath := filepath.Join(home, "config.json")
	if err = saveTestConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewayJSONConsoleHelper$")
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, config.EnvHome + "=" + home,
		config.EnvConfig + "=" + configPath, "MINTCLAW_LOG_FORMAT=json", "MINTCLAW_TEST_GATEWAY_JSON=1",
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = writer, writer
	if err = command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	ready, reloadObserved, captureDone := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var captureErr error
	statuses := 0
	go func() {
		defer close(captureDone)
		limit := &io.LimitedReader{R: reader, N: 1024*1024 + 1}
		decoder := json.NewDecoder(limit)
		for {
			var record map[string]any
			err := decoder.Decode(&record)
			if err == io.EOF && limit.N > 0 {
				return
			}
			if err != nil || limit.N <= 0 {
				captureErr = fmt.Errorf("native console JSON is invalid or exceeds its test bound")
				return
			}
			if record["event"] == "status" {
				statuses++
			}
			if record["message"] == "Press Ctrl+C to stop\n" {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if record["message"] == "Manual reload triggered via /reload endpoint" {
				select {
				case reloadObserved <- struct{}{}:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-done
		_ = reader.Close()
		<-captureDone
	})
	select {
	case <-ready:
	case <-captureDone:
		t.Fatal("native console ended before startup status", captureErr)
	case <-ctx.Done():
		t.Fatal("native console startup control expired")
	}
	process := pid.ReadPidFileWithCheck(home)
	if process == nil || process.PID != command.Process.Pid {
		t.Fatal("native startup did not publish its exact child identity")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/reload", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+process.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("native reload control unavailable")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("native reload was not admitted", response.StatusCode)
	}
	// This native main-loop event occurs after signal registration. Do not use
	// a sleep or mere listener readiness as permission to interrupt startup.
	select {
	case <-reloadObserved:
	case <-captureDone:
		t.Fatal("native console ended before its main-loop control", captureErr)
	case <-ctx.Done():
		t.Fatal("native main-loop control expired")
	}
	if command.Process.Signal(os.Interrupt) != nil {
		t.Fatal("cannot stop native test child")
	}
	<-done
	<-captureDone
	if waitErr != nil || captureErr != nil || statuses < 3 {
		t.Fatal("native JSON console lost status records or clean shutdown", waitErr, captureErr, statuses)
	}
}

func TestGatewayJSONConsoleHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_TEST_GATEWAY_JSON") != "1" {
		return
	}
	if err := Run(false, config.GetHome(), os.Getenv(config.EnvConfig), true); err != nil {
		logger.ErrorCF("console-fixture", "Native startup failed", map[string]any{"error": err.Error()})
		os.Exit(2)
	}
	os.Exit(0)
}
