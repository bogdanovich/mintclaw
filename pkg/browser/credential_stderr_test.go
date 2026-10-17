//go:build linux || darwin

package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func TestCredentialLibraryDriverDiscardsStderrBeforeLogging(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.log")
	if logger.EnableFileLogging(logPath) != nil {
		t.Fatal("private driver log fixture unavailable")
	}
	t.Cleanup(logger.DisableFileLogging)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := &playwrightLibraryClient{}
	lockDirectory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(lockDirectory, 0o700) != nil {
		t.Fatal("private driver lock fixture unavailable")
	}
	_, err = client.Connect(ctx, "private-stderr-fixture", config.MCPServerConfig{
		Type: "stdio", ExclusiveLockFile: filepath.Join(lockDirectory, "driver.lock"),
		Command: os.Args[0], Args: []string{"-test.run=^TestCredentialLibraryStderrHelper$"},
		Env: map[string]string{"MINTCLAW_CREDENTIAL_STDERR_FIXTURE": "1"},
	})
	if err != nil {
		t.Fatal("private driver subprocess did not initialize")
	}
	t.Cleanup(func() { _ = client.Abort() })
	if client.Close() != nil {
		t.Fatal("private driver subprocess cleanup did not acknowledge")
	}
	logger.DisableFileLogging()
	logs, err := os.ReadFile(logPath)
	if err != nil || strings.Contains(string(logs), "private-driver-stderr-canary-P94") {
		t.Fatal("browser driver stderr entered local logging")
	}
}

func TestCredentialLibraryStderrHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_CREDENTIAL_STDERR_FIXTURE") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stderr, strings.Repeat("private-driver-stderr-canary-P94\n", 16*1024))
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request playwrightLibraryWireRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		text := "closed"
		if request.Method == "mintclaw_initialize" {
			text = "MINTCLAW_PLAYWRIGHT_LIBRARY_V1|ready"
		}
		response := playwrightLibraryWireResponse{
			ID: request.ID, Result: &playwrightLibraryWireResult{
				Content: []playwrightLibraryWireContent{{Type: "text", Text: text}},
			},
		}
		if json.NewEncoder(os.Stdout).Encode(response) != nil {
			os.Exit(3)
		}
		if request.Method == "shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(4)
}
