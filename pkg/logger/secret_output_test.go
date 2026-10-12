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
	for name, value := range map[string]string{
		"html":    "logger-canary-91&\"secret",
		"control": "logger-canary-92&\x00secret",
	} {
		t.Run(name, func(t *testing.T) { testCredentialLogFanout(t, value) })
	}
}

func testCredentialLogFanout(t *testing.T, value string) {
	t.Helper()
	originalLevel := GetLevel()
	SetLevel(INFO)
	defer SetLevel(originalLevel)
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
	embedded, marshalErr := json.Marshal(map[string]string{"detail": value})
	if marshalErr != nil {
		t.Fatal("nested JSON fixture unavailable")
	}
	InfoCF("credentials", "login finished", map[string]any{
		"alias": "ebay", "outcome": "failed", "detail": value,
		"nested":   map[string]string{"note": base64.StdEncoding.EncodeToString([]byte(value))},
		"response": string(embedded),
	})
	SetTimeFormat(defaultTimeFormat)
	InfoCF(
		"credentials",
		"second record",
		map[string]any{"alias": "ebay", "detail": value, "response": string(embedded)},
	)
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
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			t.Fatal("guarded log record is not valid JSON")
		}
		if record["detail"] != secretvalues.Marker {
			t.Fatal("JSON-encoded log field exposed a registered value")
		}
		response, ok := record["response"].(string)
		var inner map[string]string
		if !ok || json.Unmarshal([]byte(response), &inner) != nil || inner["detail"] != secretvalues.Marker {
			t.Fatal("JSON nested inside a log string exposed a registered value")
		}
	}
}
