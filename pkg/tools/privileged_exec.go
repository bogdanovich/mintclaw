package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	defaultPrivilegedExecTimeout = 300
	maxPrivilegedLiveOutputBytes = 24 << 10
	privilegedCommandLabel       = "[privileged command redacted]"
)

type PrivilegedExecTool struct {
	executor         privilege.Executor
	binding          privilege.Binding
	mu               sync.Mutex
	outcomeUncertain bool
}

func NewPrivilegedExecTool(executor privilege.Executor) (*PrivilegedExecTool, error) {
	binding, err := privilege.Require(executor)
	if err != nil {
		return nil, err
	}
	return &PrivilegedExecTool{executor: executor, binding: binding}, nil
}

func (*PrivilegedExecTool) Name() string { return "privileged_exec" }

func (*PrivilegedExecTool) Description() string {
	return "Execute one non-interactive shell script through the root-owned coding authority broker. " +
		"The backend profile, working scope, environment, timeout ceiling, and output ceiling are immutable. " +
		"Use only when root authority is necessary. Command text and bounded output are available to the current " +
		"turn but are omitted from durable history and terminal reports; do not repeat secrets in the final answer."
}

func (tool *PrivilegedExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"script": map[string]any{
				"type": "string", "minLength": 1, "maxLength": privilege.MaxScriptBytes,
				"description": "Non-interactive shell script to execute with the configured root profile.",
			},
			"timeout_seconds": map[string]any{
				"type": "integer", "minimum": 1, "maximum": tool.binding.TimeoutSecondsMax,
				"description": "Execution timeout; omitted uses the smaller of 300 seconds and the configured ceiling.",
			},
		},
		"required": []string{"script"},
	}
}

func (*PrivilegedExecTool) DurableArguments(args map[string]any) (map[string]any, error) {
	projected := make(map[string]any, len(args))
	for key, value := range args {
		projected[key] = value
	}
	value, present := projected["script"]
	if present {
		script, _ := value.(string)
		digest := sha256.Sum256([]byte(script))
		projected["script"] = "sha256:" + hex.EncodeToString(digest[:])
	}
	return projected, nil
}

func (*PrivilegedExecTool) ProtectedDurableArguments(map[string]any) bool { return true }
func (*PrivilegedExecTool) ProtectedDurableResult(map[string]any) bool    { return true }
func (*PrivilegedExecTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*PrivilegedExecTool) CodingStartObservation(map[string]any) *toolshared.ToolObservation {
	return toolshared.SanitizeToolObservation(&toolshared.ToolObservation{
		Command: &toolshared.CommandObservation{
			Action: "run", Command: privilegedCommandLabel, Source: "agent",
			Status: "running", OwnsProcess: true,
		},
	})
}

func (tool *PrivilegedExecTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.outcomeUncertain {
		return privilegedExecFailure(
			"unknown",
			"privileged_exec is disabled because a prior command outcome is uncertain; inspect machine state before starting a new coding task",
			privilege.ErrOutcomeUnknown,
		)
	}
	script, ok := args["script"].(string)
	if !ok || strings.TrimSpace(script) == "" {
		return privilegedExecFailure("failed", "privileged_exec requires a non-empty script", nil)
	}
	timeout := min(defaultPrivilegedExecTimeout, tool.binding.TimeoutSecondsMax)
	if configured, present := args["timeout_seconds"]; present {
		value, valid := integerArgument(configured)
		if !valid || value <= 0 || value > tool.binding.TimeoutSecondsMax {
			return privilegedExecFailure("failed", "privileged_exec timeout is outside the configured bound", nil)
		}
		timeout = value
	}
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	callID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	if executionID == "" || callID == "" {
		return privilegedExecFailure("failed", "privileged_exec requires bound execution identities", nil)
	}
	digest := sha256.Sum256([]byte(executionID + "\x00" + callID))
	invocationID := "coding-root-" + hex.EncodeToString(digest[:12])
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	started := time.Now()
	result, err := tool.executor.Execute(execCtx, privilege.Request{
		InvocationID: invocationID, Script: script, TimeoutSeconds: timeout,
	})
	duration := time.Since(started)
	if err != nil {
		status := "failed"
		message := "privileged_exec failed before a verified terminal result"
		switch {
		case errors.Is(err, privilege.ErrCancellationConfirmed) && errors.Is(execCtx.Err(), context.DeadlineExceeded):
			status = "timed_out"
			message = "privileged_exec timed out; the broker confirmed descendant cleanup"
		case errors.Is(err, privilege.ErrCancellationConfirmed):
			status = "canceled"
			message = "privileged_exec was canceled; the broker confirmed descendant cleanup"
		case errors.Is(err, privilege.ErrOutcomeUnknown):
			status = "unknown"
			message = "privileged_exec outcome is uncertain; inspect machine state before retrying"
			tool.outcomeUncertain = true
		}
		return privilegedExecFailure(status, message, err).WithObservation(
			privilegedCommandObservation(status, duration, nil, false),
		)
	}
	status := "succeeded"
	isError := result.ExitCode != 0
	if isError {
		status = "failed"
	}
	summary := fmt.Sprintf(
		"privileged_exec %s (exit_code=%d, signal=%q, output_truncated=%t); raw bounded output is available only in the current turn",
		status,
		result.ExitCode,
		result.Signal,
		result.Truncated,
	)
	liveOutput, liveTruncated := boundedPrivilegedLiveOutput(result.Stdout, result.Stderr)
	exitCode := result.ExitCode
	return (&toolshared.ToolResult{
		ForLLM: summary, ContextText: liveOutput, IsError: isError,
		Observation: &toolshared.ToolObservation{Command: pointerCommandObservation(
			privilegedCommandObservation(status, duration, &exitCode, result.Truncated || liveTruncated),
		)},
	})
}

func privilegedExecFailure(status string, message string, err error) *toolshared.ToolResult {
	return &toolshared.ToolResult{
		ForLLM: message, IsError: true, Err: err,
		Observation: &toolshared.ToolObservation{Command: pointerCommandObservation(
			privilegedCommandObservation(status, 0, nil, false),
		)},
	}
}

func privilegedCommandObservation(
	status string,
	duration time.Duration,
	exitCode *int,
	truncated bool,
) toolshared.CommandObservation {
	return toolshared.CommandObservation{
		Action: "run", Command: privilegedCommandLabel, Source: "agent",
		Status: status, OwnsProcess: true, Duration: duration,
		ExitCode: exitCode, Truncated: truncated,
		Canceled: status == "canceled", TimedOut: status == "timed_out",
	}
}

func pointerCommandObservation(value toolshared.CommandObservation) *toolshared.CommandObservation {
	return &value
}

func integerArgument(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		converted := int(typed)
		return converted, float64(converted) == typed
	default:
		return 0, false
	}
}

func boundedPrivilegedLiveOutput(stdout string, stderr string) (string, bool) {
	text := "STDOUT:\n" + stdout + "\nSTDERR:\n" + stderr
	if len(text) <= maxPrivilegedLiveOutputBytes {
		return text, false
	}
	headBytes := maxPrivilegedLiveOutputBytes / 2
	tailBytes := maxPrivilegedLiveOutputBytes - headBytes - len("\n[… privileged output omitted …]\n")
	head := validPrivilegedUTF8Prefix(text, headBytes)
	tail := validPrivilegedUTF8Suffix(text, tailBytes)
	return head + "\n[… privileged output omitted …]\n" + tail, true
}

func validPrivilegedUTF8Prefix(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.ValidString(value[:maximum]) {
		maximum--
	}
	return value[:maximum]
}

func validPrivilegedUTF8Suffix(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	start := len(value) - maximum
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}
