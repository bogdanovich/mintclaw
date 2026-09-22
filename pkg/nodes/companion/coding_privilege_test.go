package companion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
)

type fakeCodingPrivilegeBroker struct {
	snapshot ShellBrokerSnapshot
	result   ShellBrokerResult
	err      error
	request  ShellBrokerRequest
}

func (broker *fakeCodingPrivilegeBroker) Snapshot(context.Context) (ShellBrokerSnapshot, error) {
	return broker.snapshot, nil
}

func (broker *fakeCodingPrivilegeBroker) Execute(
	_ context.Context,
	request ShellBrokerRequest,
) (ShellBrokerResult, error) {
	broker.request = request
	return broker.result, broker.err
}

func TestNormalizeCodingPrivilegePolicyIsExplicitAndLinuxOnly(t *testing.T) {
	policy := &CodingPrivilegePolicy{
		Backend: privilege.BackendAuthorityBroker, BrokerSocket: "root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine",
	}
	ready, err := normalizeCodingPrivilegePolicy(policy, t.TempDir(), "linux", true)
	if err != nil || ready == nil || !strings.HasSuffix(ready.BrokerSocket, "/root.sock") {
		t.Fatalf("normalized privilege policy = %#v, %v", ready, err)
	}
	if _, err = normalizeCodingPrivilegePolicy(policy, t.TempDir(), "darwin", true); err == nil ||
		!strings.Contains(err.Error(), "containment is not proven") {
		t.Fatalf("darwin root policy error = %v", err)
	}
	if _, err = normalizeCodingPrivilegePolicy(policy, t.TempDir(), "linux", false); err == nil {
		t.Fatal("unused privileged executor was accepted")
	}
	if _, err = normalizeCodingPrivilegePolicy(nil, t.TempDir(), "linux", true); err == nil {
		t.Fatal("root profile without privileged executor was accepted")
	}
}

func TestCodingPrivilegeExecutorRevalidatesExactSnapshot(t *testing.T) {
	binding := privilege.Binding{
		Backend: privilege.BackendAuthorityBroker, Endpoint: "/run/mintclaw/root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine", TimeoutSecondsMax: 60, OutputBytesMax: 4096,
	}
	broker := &fakeCodingPrivilegeBroker{
		snapshot: ShellBrokerSnapshot{Revision: "broker-one", Profiles: []ShellBrokerProfile{{
			Alias: "root", Revision: "profile-one", WorkingScopes: []string{"machine"},
			TimeoutSecondsMax: 60, OutputBytesMax: 4096, ConcurrentCommands: 1,
			ConcurrentTerminals: 1, TerminalIdleSeconds: DefaultTerminalIdleSeconds,
			TerminalLifetimeSeconds: MaxTerminalLifetimeSeconds, TerminalBufferBytes: DefaultTerminalBufferBytes,
		}}},
		result: ShellBrokerResult{ExitCode: 0, StartedAt: 1, CompletedAt: 2},
	}
	executor := &codingPrivilegeExecutor{binding: binding, client: broker}
	result, err := executor.Execute(t.Context(), privilege.Request{
		InvocationID: "call-one", Script: "id -u", TimeoutSeconds: 30,
	})
	if err != nil || result.ExitCode != 0 || broker.request.Profile != "root" ||
		broker.request.WorkingScope != "machine" || len(broker.request.Environment) != 0 ||
		broker.request.Script != "id -u" {
		t.Fatalf("privileged execution = %#v, %v; request %#v", result, err, broker.request)
	}
	broker.snapshot.Revision = "broker-two"
	broker.request = ShellBrokerRequest{}
	if _, err = executor.Execute(t.Context(), privilege.Request{
		InvocationID: "call-two", Script: "id -u", TimeoutSeconds: 30,
	}); err == nil || broker.request.Script != "" {
		t.Fatalf("stale broker execution error = %v, request %#v", err, broker.request)
	}
}

func TestCodingPrivilegeExecutorMapsUnknownOutcome(t *testing.T) {
	binding := privilege.Binding{
		Backend: privilege.BackendAuthorityBroker, Endpoint: "/run/mintclaw/root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine", TimeoutSecondsMax: 60, OutputBytesMax: 4096,
	}
	broker := &fakeCodingPrivilegeBroker{
		snapshot: ShellBrokerSnapshot{Revision: "broker-one", Profiles: []ShellBrokerProfile{{
			Alias: "root", Revision: "profile-one", WorkingScopes: []string{"machine"},
			TimeoutSecondsMax: 60, OutputBytesMax: 4096, ConcurrentCommands: 1,
			ConcurrentTerminals: 1, TerminalIdleSeconds: DefaultTerminalIdleSeconds,
			TerminalLifetimeSeconds: MaxTerminalLifetimeSeconds, TerminalBufferBytes: DefaultTerminalBufferBytes,
		}}},
		err: ErrShellBrokerOutcomeUnknown,
	}
	executor := &codingPrivilegeExecutor{binding: binding, client: broker}
	_, err := executor.Execute(t.Context(), privilege.Request{
		InvocationID: "call-one", Script: "id -u", TimeoutSeconds: 30,
	})
	if !errors.Is(err, privilege.ErrOutcomeUnknown) {
		t.Fatalf("unknown outcome error = %v", err)
	}
}

func TestCodingPrivilegeReportFailsClosedWithoutLivePolicy(t *testing.T) {
	host := &CodingTaskHost{}
	report := host.codingPrivilegeReport("missing", "stale", true)
	if report.Backend != privilege.BackendAuthorityBroker || report.Profile != "unavailable" ||
		report.ProfileRevision != "unavailable" || report.Usage != "uncertain" ||
		report.Outcome != "uncertain" || report.Commands != 0 {
		t.Fatalf("fallback coding privilege report = %#v", report)
	}
}
