package openai_compat

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func TestSensitiveStreamSuppressesUntrustedToolDiagnostics(t *testing.T) {
	const sentinel = "MINTCLAW_SENSITIVE_TOOL_ECHO_859d"
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "synthetic", "type": "function",
					"function": map[string]any{"name": sentinel, "arguments": "{"},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sse := "data: " + string(payload) + "\n\ndata: [DONE]\n\n"
	previousLevel := logger.GetLevel()
	logger.SetLevel(logger.WARN)
	defer func() {
		logger.DisableFileLogging()
		logger.SetLevel(previousLevel)
	}()
	for _, sensitive := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected", false: "ordinary"}[sensitive], func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "stream.log")
			if err := logger.EnableFileLogging(logPath); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if sensitive {
				ctx = protocoltypes.WithSensitiveRequest(ctx)
			}
			response, parseErr := parseStreamResponse(ctx, io.NopCloser(strings.NewReader(sse)), nil)
			logger.DisableFileLogging()
			if parseErr != nil || response == nil || len(response.ToolCalls) != 1 {
				t.Fatal("provider parsing changed instead of scoping diagnostics")
			}
			data, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			logs := string(data)
			if !strings.Contains(logs, "failed to decode tool call arguments") {
				t.Fatal("safe warning disappeared")
			}
			if sensitive {
				if strings.Contains(logs, sentinel) || strings.Contains(logs, `"error"`) ||
					strings.Contains(logs, `"tool"`) {
					t.Fatal("untrusted tool diagnostics escaped the sensitive request")
				}
			} else if !strings.Contains(logs, sentinel) || !strings.Contains(logs, `"error"`) {
				t.Fatal("ordinary tool diagnostics were globally suppressed")
			}
		})
	}
}
