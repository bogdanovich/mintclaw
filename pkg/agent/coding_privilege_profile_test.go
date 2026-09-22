package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type profilePrivilegeExecutor struct{ binding privilege.Binding }

func (executor *profilePrivilegeExecutor) Binding() privilege.Binding { return executor.binding }
func (*profilePrivilegeExecutor) Execute(context.Context, privilege.Request) (privilege.Result, error) {
	return privilege.Result{}, nil
}

func TestCodingRuntimeProfileBindsPrivilegeOnlyToRootProfile(t *testing.T) {
	base := t.TempDir()
	executionRoot := filepath.Join(base, "machine")
	if err := os.Mkdir(executionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	layout, err := NewCodingRuntimeLayout(
		"root-profile", executionRoot, filepath.Join(base, "state"), []string{executionRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	executor := &profilePrivilegeExecutor{binding: privilege.Binding{
		Backend: privilege.BackendAuthorityBroker, Endpoint: "/run/mintclaw/root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine", TimeoutSecondsMax: 60, OutputBytesMax: 4096,
	}}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{
		AgentID: "main", Layout: layout, Profile: codingscope.ProfileMachineYoloRoot,
		Privilege: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if admitted, ok := profile.AgentPrivilegedExecutor("main"); !ok || admitted != executor {
		t.Fatalf("root executor = %#v, %v", admitted, ok)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewCodingAgentLoop(t.Context(), cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)
	agent, ok := loop.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("root coding agent is missing")
	}
	if _, ok = agent.Tools.Get("privileged_exec"); !ok {
		t.Fatal("root coding agent did not register privileged_exec")
	}
	if _, err = NewCodingRuntimeProfile(CodingRuntimeBinding{
		AgentID: "main", Layout: layout, Profile: codingscope.ProfileMachineYoloRoot,
	}); err == nil {
		t.Fatal("root profile without privileged executor was accepted")
	}
	if _, err = NewCodingRuntimeProfile(CodingRuntimeBinding{
		AgentID: "main", Layout: layout, Profile: codingscope.ProfileMachineYolo,
		Privilege: executor,
	}); err == nil {
		t.Fatal("machine-yolo profile carrying a privileged executor was accepted")
	}
}
