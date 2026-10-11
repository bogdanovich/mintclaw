//go:build !windows

package mcp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func TestPrivateCommandDrainsWithoutLoggingStderr(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.DisableFileLogging)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.Command(os.Args[0], "-test.run=TestPrivateCommandHelper")
	command.Env = []string{"MINTCLAW_PRIVATE_COMMAND_HELPER=1"}
	pipe, err := StartPrivateIsolatedCommand(ctx, "private-fixture", command, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Abort() })
	data, err := io.ReadAll(pipe)
	if err != nil || string(data) != "public-ready\n" {
		t.Fatal("private stderr drain blocked stdio", err)
	}
	if err = pipe.Close(); err != nil {
		t.Fatal(err)
	}
	logger.DisableFileLogging()
	logs, err := os.ReadFile(logPath)
	if err != nil || strings.Contains(string(logs), "PROTECTED_STDERR") {
		t.Fatal("private worker stderr entered diagnostics")
	}
}

func TestPrivateCommandHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_PRIVATE_COMMAND_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stderr, strings.Repeat("PROTECTED_STDERR\n", 16*1024))
	_, _ = io.WriteString(os.Stdout, "public-ready\n")
	os.Exit(0)
}
