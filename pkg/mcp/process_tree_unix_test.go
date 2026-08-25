//go:build !windows

package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestIsolatedManagerCloseRetriesOwnedCleanupAfterSDKCloseFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lockPath := t.TempDir() + "/server.lock"
	mgr := NewManager()
	err := mgr.ConnectServer(ctx, "retry-helper", config.MCPServerConfig{
		Enabled:           true,
		Type:              "stdio",
		Command:           os.Args[0],
		Args:              []string{"-test.run=TestIsolatedCleanupRetryHelperProcess"},
		Env:               map[string]string{"MINTCLAW_MCP_CLEANUP_RETRY_HELPER": "1"},
		ExclusiveLockFile: lockPath,
	})
	if err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	conn, ok := mgr.GetServer("retry-helper")
	if !ok {
		t.Fatal("connected server not found")
	}
	pipe, ok := conn.cleanup.(*isolatedPipeRWC)
	if !ok {
		t.Fatalf("cleanup = %T, want *isolatedPipeRWC", conn.cleanup)
	}
	realStop := pipe.stopProcessTree
	attempts := 0
	pipe.stopProcessTree = func(timeout time.Duration) error {
		attempts++
		if attempts == 1 {
			return errors.New("injected transient process-tree failure")
		}
		return realStop(timeout)
	}

	if err = mgr.Close(); err == nil {
		t.Fatal("first Close() error = nil, want injected cleanup failure")
	}
	if _, ok = mgr.GetServer("retry-helper"); !ok {
		t.Fatal("failed cleanup was not retained for retry")
	}
	if err = mgr.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("process-tree cleanup attempts = %d, want 2", attempts)
	}
	lease, err := acquireExclusiveServerLease("contender", lockPath)
	if err != nil {
		t.Fatalf("exclusive lease remained held after successful retry: %v", err)
	}
	lease.release()
}

func TestIsolatedCleanupRetryHelperProcess(t *testing.T) {
	if os.Getenv("MINTCLAW_MCP_CLEANUP_RETRY_HELPER") != "1" {
		return
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name: "cleanup-retry-helper", Version: "1.0.0",
	}, nil)
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCanceledConnectionContextLeavesCleanupToIsolatedTransport(t *testing.T) {
	childPIDPath := t.TempDir() + "/detached-child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	mgr := NewManager()
	err := mgr.ConnectServer(ctx, "context-cleanup-helper", config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestIsolatedContextCleanupHelperProcess"},
		Env: map[string]string{
			"MINTCLAW_MCP_CONTEXT_CLEANUP_HELPER": "1",
			"MINTCLAW_MCP_DETACHED_CHILD_PID":     childPIDPath,
		},
	})
	if err != nil {
		cancel()
		t.Fatalf("ConnectServer() error = %v", err)
	}
	childPID := waitForChildPID(t, childPIDPath)
	t.Cleanup(func() {
		cancel()
		_ = syscall.Kill(-childPID, syscall.SIGKILL)
		_ = mgr.Close()
	})

	cancel()
	if err = mgr.Close(); err != nil {
		t.Fatalf("Close() after connection context cancellation error = %v", err)
	}
	if !waitForProcessExit(childPID, 2*time.Second) {
		t.Fatalf("detached descendant process %d remains alive", childPID)
	}
}

func TestIsolatedContextCleanupHelperProcess(t *testing.T) {
	if os.Getenv("MINTCLAW_MCP_CONTEXT_CLEANUP_HELPER") != "1" {
		return
	}
	child := exec.Command("sh", "-c", "exec sleep 60")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(
		os.Getenv("MINTCLAW_MCP_DETACHED_CHILD_PID"),
		[]byte(strconv.Itoa(child.Process.Pid)),
		0o600,
	); err != nil {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		os.Exit(2)
	}
	defer func() {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	}()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name: "context-cleanup-helper", Version: "1.0.0",
	}, nil)
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
}

func TestStopIsolatedCommandProcessTreeTerminatesDescendants(t *testing.T) {
	childPIDPath := t.TempDir() + "/child.pid"
	command := exec.Command(
		"sh",
		"-c",
		`sleep 60 & child=$!; printf '%s' "$child" > "$1"; wait`,
		"sh",
		childPIDPath,
	)
	processTree, err := prepareIsolatedCommandProcessTree(command)
	if err != nil {
		t.Fatalf("prepareIsolatedCommandProcessTree() error = %v", err)
	}
	if err = command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- command.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		select {
		case <-waitCh:
		default:
		}
	})

	childPID := waitForChildPID(t, childPIDPath)
	processGroup, err := syscall.Getpgid(command.Process.Pid)
	if err != nil || processGroup != command.Process.Pid {
		t.Fatalf("process group = %d, %v; want %d", processGroup, err, command.Process.Pid)
	}
	if err = processTree.stop(2 * time.Second); err != nil {
		t.Fatalf("processTree.stop() error = %v", err)
	}
	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("direct process was not reaped")
	}
	if err = syscall.Kill(childPID, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("descendant process %d remains alive: %v", childPID, err)
	}
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(encoded)) != "" {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(encoded)))
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child PID was not written")
	return 0
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
