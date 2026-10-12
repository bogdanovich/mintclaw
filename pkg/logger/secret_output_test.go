package logger

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestRuntimeCredentialGuardAppliesBeforeConsoleAndFileFanout(t *testing.T) {
	originalLevel := GetLevel()
	SetLevel(INFO)
	defer SetLevel(originalLevel)
	value := "logger-canary-91&\"secret"
	release, err := secretvalues.Register([]byte(value))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	var console bytes.Buffer
	mu.Lock()
	originalConsole := consoleWriter
	originalWriters := writers
	originalLogger := logger
	consoleWriter.Out = &console
	writers = []io.Writer{consoleWriter}
	logger = logger.Output(loggerOutput())
	mu.Unlock()
	defer func() {
		DisableFileLogging()
		mu.Lock()
		consoleWriter, writers, logger = originalConsole, originalWriters, originalLogger
		mu.Unlock()
	}()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if EnableFileLogging(path) != nil {
		t.Fatal("private test log unavailable")
	}
	InfoCF("credentials", "login finished", map[string]any{
		"alias": "ebay", "outcome": "failed", "detail": value,
		"nested": map[string]string{"note": base64.StdEncoding.EncodeToString([]byte(value))},
	})
	SetTimeFormat(defaultTimeFormat)
	InfoCF("credentials", "second record", map[string]any{"alias": "ebay", "detail": value})
	DisableFileLogging()
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal("test log read unavailable")
	}
	for _, output := range []string{console.String(), string(data)} {
		if strings.Contains(output, value) ||
			strings.Contains(output, base64.StdEncoding.EncodeToString([]byte(value))) {
			t.Fatal("runtime log exposed a registered value")
		}
		if !strings.Contains(output, "ebay") || !strings.Contains(output, "failed") {
			t.Fatal("safe alias or outcome was lost")
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("guarded log record is not valid JSON")
		}
	}
}
