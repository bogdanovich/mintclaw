package config

import (
	"strings"
	"testing"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
)

func TestValidateCodingRemoteAcceptsExactBoundedGrant(t *testing.T) {
	cfg := validCodingRemoteConfig()
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}
}

func TestValidateCodingRemoteAcceptsClosedServiceCommands(t *testing.T) {
	cfg := validCodingRemoteConfig()
	capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
	capability.Operations = []string{"service.status.v1", "service.logs.v1", "service.action.v1"}
	cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}
}

func TestValidateCodingRemoteDefaultsDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Coding.Remote.Enabled || cfg.Gateway.CodingRemote.Enabled ||
		len(cfg.Execution.CodingRemoteCapabilities) != 0 || len(cfg.Execution.CodingRemoteGrants) != 0 {
		t.Fatalf("default coding remote authority = %#v / %#v", cfg.Coding.Remote, cfg.Gateway.CodingRemote)
	}
	if err := cfg.ValidateCodingRemote(); err != nil {
		t.Fatalf("ValidateCodingRemote() default error = %v", err)
	}
}

func TestValidateCodingRemoteAcceptsOwnedWorkspaceJobLifecycle(t *testing.T) {
	cfg := validCodingRemoteConfig()
	target := cfg.Execution.Targets["laptop"]
	target.JobProfile = "project-jobs"
	cfg.Execution.Targets["laptop"] = target
	workspace := cfg.Execution.RemoteWorkspaces["laptop-build"]
	workspace.Tools = append(workspace.Tools, "jobs")
	cfg.Execution.RemoteWorkspaces["laptop-build"] = workspace
	capability := cfg.Execution.CodingRemoteCapabilities["build-workspace"]
	capability.Operations = []string{
		"workspace_exec", "job_status", "job_logs", "job_artifacts", "job_cancel",
	}
	cfg.Execution.CodingRemoteCapabilities["build-workspace"] = capability
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}
}

func TestValidateCodingRemoteRejectsInvalidAuthority(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*Config)
	}{
		{name: "listener without nodes", want: "requires nodes.enabled", mutate: func(cfg *Config) {
			cfg.Nodes.Enabled = false
		}},
		{name: "relative listener", want: "clean absolute path", mutate: func(cfg *Config) {
			cfg.Gateway.CodingRemote.SocketPath = "run/broker.sock"
		}},
		{name: "different endpoints", want: "socket paths must match", mutate: func(cfg *Config) {
			cfg.Coding.Remote.SocketPath = "/tmp/mintclaw-other.sock"
		}},
		{name: "unknown client grant", want: "is not configured", mutate: func(cfg *Config) {
			cfg.Coding.Remote.Grant = "missing"
		}},
		{name: "unknown workspace", want: "unknown remote workspace", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["build-workspace"]
			capability.RemoteWorkspace = "missing"
			cfg.Execution.CodingRemoteCapabilities["build-workspace"] = capability
		}},
		{name: "workspace operation", want: "is not granted by remote workspace", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["build-workspace"]
			capability.Operations = append(capability.Operations, "write_file")
			cfg.Execution.CodingRemoteCapabilities["build-workspace"] = capability
		}},
		{
			name: "job operation without job grant",
			want: "is not granted by remote workspace",
			mutate: func(cfg *Config) {
				capability := cfg.Execution.CodingRemoteCapabilities["build-workspace"]
				capability.Operations = append(capability.Operations, "job_status")
				cfg.Execution.CodingRemoteCapabilities["build-workspace"] = capability
			},
		},
		{name: "job operation without workspace exec", want: "requires workspace_exec", mutate: func(cfg *Config) {
			target := cfg.Execution.Targets["laptop"]
			target.JobProfile = "project-jobs"
			cfg.Execution.Targets["laptop"] = target
			workspace := cfg.Execution.RemoteWorkspaces["laptop-build"]
			workspace.Tools = []string{"read_file", "jobs", "workspace_exec"}
			cfg.Execution.RemoteWorkspaces["laptop-build"] = workspace
			capability := cfg.Execution.CodingRemoteCapabilities["build-workspace"]
			capability.Operations = []string{"read_file", "job_status"}
			cfg.Execution.CodingRemoteCapabilities["build-workspace"] = capability
		}},
		{name: "unknown target", want: "unknown target", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Target = "missing"
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "invalid command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"sudo sh"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "generic read command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"node.info.v1"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "coding control command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"coding.task.start.v5"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "raw shell command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"shell.exec.v1"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "future raw shell command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"shell.exec.v2"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "future node update command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"node.update.v7"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "future artifact transport command", want: "invalid node command", mutate: func(cfg *Config) {
			capability := cfg.Execution.CodingRemoteCapabilities["system-status"]
			capability.Operations = []string{"job.artifact.download.v3"}
			cfg.Execution.CodingRemoteCapabilities["system-status"] = capability
		}},
		{name: "unknown agent", want: "unknown agent", mutate: func(cfg *Config) {
			grant := cfg.Execution.CodingRemoteGrants["local-development"]
			grant.Agent = "missing"
			cfg.Execution.CodingRemoteGrants["local-development"] = grant
		}},
		{name: "privileged local profile", want: "unsupported local profile", mutate: func(cfg *Config) {
			grant := cfg.Execution.CodingRemoteGrants["local-development"]
			grant.LocalProfiles = []codingscope.Profile{codingscope.ProfileMachineYoloRoot}
			cfg.Execution.CodingRemoteGrants["local-development"] = grant
		}},
		{name: "duplicate capability", want: "duplicate capability", mutate: func(cfg *Config) {
			grant := cfg.Execution.CodingRemoteGrants["local-development"]
			grant.Capabilities = []string{"build-workspace", "build-workspace"}
			cfg.Execution.CodingRemoteGrants["local-development"] = grant
		}},
		{name: "target outside policy", want: "outside agent target policy", mutate: func(cfg *Config) {
			cfg.Agents.List[0].TargetPolicy = &TargetPolicy{AllowedTargets: []string{"other"}}
			cfg.Execution.Targets["other"] = ExecutionTarget{Type: "node", Node: "other-node"}
		}},
		{name: "unknown task scope", want: "unknown task scope", mutate: func(cfg *Config) {
			grant := cfg.Execution.CodingRemoteGrants["local-development"]
			grant.Tasks[0].Scope = "missing"
			cfg.Execution.CodingRemoteGrants["local-development"] = grant
		}},
		{name: "task profile broader than scope", want: "unavailable profile", mutate: func(cfg *Config) {
			grant := cfg.Execution.CodingRemoteGrants["local-development"]
			grant.Tasks[0].Profiles = []codingscope.Profile{codingscope.ProfileMachineYolo}
			cfg.Execution.CodingRemoteGrants["local-development"] = grant
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validCodingRemoteConfig()
			test.mutate(cfg)
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateCodingRemoteBrowserProfileCapabilityIsClosed(t *testing.T) {
	cfg := validCodingRemoteConfig()
	cfg.Tools.Browser = BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"main"},
		Targets: map[string]BrowserTargetConfig{
			"laptop-browser": {
				Enabled: true, Placement: BrowserPlacementNode, NodeTarget: "laptop",
				Profiles: map[string]BrowserProfileConfig{
					"automation": {
						Enabled: true, Revision: "browser-v1", Mode: BrowserProfileManaged,
						AllowedAgents: []string{"main"}, AllowedActors: []string{"coding:local:operator"},
						NetworkMode: BrowserNetworkPublicWeb, CapabilityMode: BrowserCapabilityFullAccess,
						ApprovalMode: BrowserApprovalNone, AllowApprovedActions: true,
					},
				},
			},
		},
	}
	cfg.Execution.CodingRemoteCapabilities["browser"] = CodingRemoteCapability{
		Revision: "browser-capability-v1", Kind: CodingRemoteCapabilityBrowser,
		Target: "laptop-browser", BrowserProfile: "automation",
		Operations: []string{"browser_open", "browser_observe", "browser_act"},
	}
	grant := cfg.Execution.CodingRemoteGrants["local-development"]
	grant.Capabilities = append(grant.Capabilities, "browser")
	cfg.Execution.CodingRemoteGrants["local-development"] = grant
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}

	target := cfg.Tools.Browser.Targets["laptop-browser"]
	profile := target.Profiles["automation"]
	profile.ApprovalMode = BrowserApprovalAlwaysCommit
	target.Profiles["automation"] = profile
	cfg.Tools.Browser.Targets["laptop-browser"] = target
	if err := cfg.ValidateCodingRemote(); err == nil ||
		!strings.Contains(err.Error(), "requires approval_mode none") {
		t.Fatalf("approval-requiring browser capability error = %v", err)
	}

	profile.ApprovalMode = BrowserApprovalNone
	profile.Mode = BrowserProfileAttachedUser
	target.Profiles["automation"] = profile
	cfg.Tools.Browser.Targets["laptop-browser"] = target
	if err := cfg.ValidateCodingRemote(); err == nil ||
		!strings.Contains(err.Error(), "unavailable browser profile") {
		t.Fatalf("attached browser capability error = %v", err)
	}
}

func validCodingRemoteConfig() *Config {
	cfg := DefaultConfig()
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"laptop": {Type: "node", Node: "paired-laptop", FileProfile: "workspace-files"},
	}
	cfg.Agents.List[0].TargetPolicy = &TargetPolicy{AllowedTargets: []string{"laptop"}, DefaultTarget: "laptop"}
	cfg.Execution.RemoteWorkspaces = map[string]RemoteWorkspace{
		"laptop-build": {
			Target: "laptop", WorkingScope: "mintclaw", Tools: []string{"read_file", "workspace_exec"},
			Revision: "workspace-v1",
		},
	}
	cfg.Execution.RemoteCodingScopes = map[string]RemoteCodingScope{
		"mintclaw-dev": {
			Target: "laptop", Scope: "mintclaw", Revision: "scope-v1",
			Profiles:   []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Requesters: []RemoteCodingRequester{{Agent: "main", Channel: "telegram", Sender: "owner"}},
		},
	}
	cfg.Execution.CodingRemoteCapabilities = map[string]CodingRemoteCapability{
		"build-workspace": {
			Revision: "capability-v1", Kind: CodingRemoteCapabilityWorkspace,
			RemoteWorkspace: "laptop-build", Operations: []string{"read_file", "workspace_exec"},
		},
		"system-status": {
			Revision: "capability-v1", Kind: CodingRemoteCapabilityNode,
			Target: "laptop", Operations: []string{"service.status.v1"},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"build-workspace", "system-status"},
			Tasks: []CodingRemoteTaskGrant{{
				Scope: "mintclaw-dev", Profiles: []codingscope.Profile{
					codingscope.ProfileInvestigate, codingscope.ProfileMutate,
				},
			}},
		},
	}
	cfg.Gateway.CodingRemote = GatewayCodingRemoteListener{Enabled: true, SocketPath: "/tmp/mintclaw-broker.sock"}
	cfg.Coding.Remote = CodingRemoteClient{
		Enabled: true, SocketPath: "/tmp/mintclaw-broker.sock", Grant: "local-development",
	}
	return cfg
}
