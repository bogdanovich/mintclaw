package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakePrivilegedExecutor struct {
	binding privilege.Binding
	result  privilege.Result
	err     error
	request privilege.Request
	calls   int
}

func (executor *fakePrivilegedExecutor) Binding() privilege.Binding { return executor.binding }

func (executor *fakePrivilegedExecutor) Execute(
	_ context.Context,
	request privilege.Request,
) (privilege.Result, error) {
	executor.calls++
	executor.request = request
	return executor.result, executor.err
}

func TestPrivilegedExecProtectsCommandAndOutputFromDurableState(t *testing.T) {
	executor := &fakePrivilegedExecutor{
		binding: testPrivilegeBinding(),
		result: privilege.Result{
			ExitCode: 0, Stdout: "secret-output", StartedAt: 1, CompletedAt: 2,
		},
	}
	tool, err := NewPrivilegedExecTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"script": "printf secret-output", "timeout_seconds": 12.0}
	durable, err := tool.DurableArguments(arguments)
	if err != nil {
		t.Fatal(err)
	}
	encoded := durable["script"].(string)
	if !strings.HasPrefix(encoded, "sha256:") || strings.Contains(encoded, "secret-output") ||
		!tool.ProtectedDurableArguments(arguments) || !tool.ProtectedDurableResult(arguments) {
		t.Fatalf("durable privileged arguments = %#v", durable)
	}
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "/workspace", "execution-secret")
	ctx = toolshared.WithToolCallID(ctx, "call-secret")
	result := tool.Execute(ctx, arguments)
	if result.IsError || !strings.Contains(result.ContextText, "secret-output") ||
		strings.Contains(result.ForLLM, "secret-output") || executor.request.Script != "printf secret-output" ||
		result.Observation == nil || result.Observation.Command == nil ||
		result.Observation.Command.Command != privilegedCommandLabel ||
		strings.Contains(result.Observation.Command.Output, "secret-output") {
		t.Fatalf("privileged result = %#v, request = %#v", result, executor.request)
	}
}

func TestPrivilegedExecReportsUnknownOutcomeWithoutReplayableContent(t *testing.T) {
	executor := &fakePrivilegedExecutor{binding: testPrivilegeBinding(), err: privilege.ErrOutcomeUnknown}
	tool, err := NewPrivilegedExecTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(
		toolshared.WithToolCallID(
			toolshared.WithToolExecutionIdentity(t.Context(), "/workspace", "execution-unknown"),
			"call-unknown",
		),
		map[string]any{"script": "dangerous-secret"},
	)
	if !result.IsError || !errors.Is(result.Err, privilege.ErrOutcomeUnknown) ||
		strings.Contains(result.ForLLM, "dangerous-secret") || result.Observation.Command.Status != "unknown" {
		t.Fatalf("unknown privileged result = %#v", result)
	}
	executor.err = nil
	second := tool.Execute(
		toolshared.WithToolCallID(
			toolshared.WithToolExecutionIdentity(t.Context(), "/workspace", "execution-after-unknown"),
			"call-after-unknown",
		),
		map[string]any{"script": "must-not-run"},
	)
	if !second.IsError || !errors.Is(second.Err, privilege.ErrOutcomeUnknown) || executor.calls != 1 ||
		executor.request.Script != "dangerous-secret" || !strings.Contains(second.ForLLM, "disabled") {
		t.Fatalf("latched privileged result = %#v, executor = %#v", second, executor)
	}
}

func TestPrivilegedExecRejectsMissingToolCallIdentity(t *testing.T) {
	executor := &fakePrivilegedExecutor{binding: testPrivilegeBinding()}
	tool, err := NewPrivilegedExecTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(t.Context(), map[string]any{"script": "id -u"})
	if !result.IsError || executor.request.Script != "" ||
		!strings.Contains(result.ForLLM, "execution identities") {
		t.Fatalf("missing-identity privileged result = %#v, request = %#v", result, executor.request)
	}
}

func TestPrivilegedExecInvocationIdentitySeparatesTurns(t *testing.T) {
	executor := &fakePrivilegedExecutor{
		binding: testPrivilegeBinding(),
		result:  privilege.Result{ExitCode: 0, StartedAt: 1, CompletedAt: 2},
	}
	tool, err := NewPrivilegedExecTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "/workspace", "execution-one")
	ctx = toolshared.WithToolCallID(ctx, "call-shared")
	if result := tool.Execute(ctx, map[string]any{"script": "id -u"}); result.IsError {
		t.Fatalf("first privileged result = %#v", result)
	}
	first := executor.request.InvocationID
	ctx = toolshared.WithToolExecutionIdentity(t.Context(), "/workspace", "execution-two")
	ctx = toolshared.WithToolCallID(ctx, "call-shared")
	if result := tool.Execute(ctx, map[string]any{"script": "id -u"}); result.IsError {
		t.Fatalf("second privileged result = %#v", result)
	}
	if first == "" || first == executor.request.InvocationID {
		t.Fatalf("privileged invocation identities = %q and %q", first, executor.request.InvocationID)
	}
}

func testPrivilegeBinding() privilege.Binding {
	return privilege.Binding{
		Backend: privilege.BackendAuthorityBroker, Endpoint: "/run/mintclaw/root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine", TimeoutSecondsMax: 60, OutputBytesMax: 4096,
	}
}
