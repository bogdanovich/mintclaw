package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

const consoleFormatSecret = "console-format-registered-secret-47"

func TestConsoleFormatSelection(t *testing.T) {
	for _, format := range []string{"", "json", "unsupported"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "records.jsonl")
			command := exec.Command(os.Args[0], "-test.run=^TestConsoleFormatHelper$")
			command.Env = append(os.Environ(),
				"MINTCLAW_TEST_CONSOLE_FORMAT=1", "MINTCLAW_LOG_FORMAT="+format,
				"MINTCLAW_TEST_CONSOLE_FILE="+path,
			)
			output, err := command.CombinedOutput()
			if err != nil || bytes.Contains(output, []byte("must-not-appear")) ||
				bytes.Contains(output, []byte(consoleFormatSecret)) {
				t.Fatal("console format helper failed or re-enabled disabled output", err)
			}
			if format == "json" {
				assertConsoleJSONRecords(t, output)
			} else if !bytes.Contains(output, []byte("first-record")) ||
				!bytes.Contains(output, []byte("last-record")) ||
				!bytes.Contains(output, []byte("startup fixture "+secretvalues.Marker+"\n")) ||
				json.Valid(bytes.TrimSpace(output)) {
				t.Fatal("default or unknown format changed existing console output")
			}
			data, err := os.ReadFile(path)
			if err != nil || bytes.Contains(data, []byte(consoleFormatSecret)) {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			count := 0
			for {
				var record map[string]any
				if err := decoder.Decode(&record); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal("file logger lost its native JSON format", err)
				}
				count++
			}
			want := 4
			if format == "json" {
				want++
			}
			if count != want {
				t.Fatal("console mode dropped native file records", count)
			}
		})
	}
}

func assertConsoleJSONRecords(t *testing.T, data []byte) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var status map[string]any
	if err := decoder.Decode(&status); err != nil || status["event"] != "status" ||
		status["message"] != "startup fixture "+secretvalues.Marker+"\n" {
		t.Fatal("gateway status escaped the structured console", err)
	}
	for _, message := range []string{"first-record", "last-record"} {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil || record["message"] != message ||
			record["component"] != "console-fixture" || record["value"] != "line\nquoted\"value" ||
			record["secret"] != secretvalues.Marker {
			t.Fatal("structured console is not lossless native JSON", err)
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatal("console format emitted extra or malformed output", err)
	}
}

func TestConsoleFormatHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_TEST_CONSOLE_FORMAT") != "1" {
		return
	}
	if EnableFileLogging(os.Getenv("MINTCLAW_TEST_CONSOLE_FILE")) != nil {
		os.Exit(2)
	}
	release, err := secretvalues.Register([]byte(consoleFormatSecret))
	if err != nil {
		os.Exit(2)
	}
	fields := map[string]any{"value": "line\nquoted\"value", "secret": consoleFormatSecret}
	SetLevel(FATAL)
	Statusf("startup fixture %s\n", consoleFormatSecret)
	SetLevel(INFO)
	WarnCF("console-fixture", "first-record", fields)
	DisableConsole()
	WarnCF("console-fixture", "must-not-appear", fields)
	SetTimeFormat("2006-01-02T15:04:05")
	WarnCF("console-fixture", "must-not-appear-after-time-change", fields)
	EnableConsole()
	WarnCF("console-fixture", "last-record", fields)
	DisableFileLogging()
	release()
	os.Exit(0)
}
