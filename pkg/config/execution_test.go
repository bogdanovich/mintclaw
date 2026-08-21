package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateExecutionTargetsAcceptsBoundedNodePolicies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"build": {
			Type: "node", Node: "linux-builder", FileProfile: "project-files",
			ServiceProfile: "server-services", UpdateProfile: "stable-node", JobProfile: "build-jobs",
		},
		"vpn": {Type: "node", Node: "node_0123456789abcdef", Executor: "local"},
	}
	cfg.Agents.Defaults.TargetPolicy = &TargetPolicy{
		DefaultTarget:  "build",
		AllowedTargets: []string{"build"},
	}
	cfg.Agents.List = []AgentConfig{{
		ID: "ops",
		TargetPolicy: &TargetPolicy{
			AllowedTargets: []string{"vpn"},
		},
	}}

	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateExecutionTargetsAcceptsRemoteWorkspace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"build": {
			Type: "node", Node: "node-build", FileProfile: "project-files", JobProfile: "project-jobs",
		},
	}
	cfg.Execution.RemoteWorkspaces = map[string]RemoteWorkspace{
		"mintclaw-build": {
			Target: "build", WorkingScope: "mintclaw", Revision: "mintclaw-v1",
			Tools: []string{"read_file", "search_files", "write_file", "apply_patch", "workspace_exec", "jobs"},
		},
	}

	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}
	workspace, allowed := cfg.RemoteWorkspaceAllows("mintclaw-build", "search_files")
	if !allowed || workspace.Target != "build" {
		t.Fatalf("RemoteWorkspaceAllows() = %#v, %v", workspace, allowed)
	}
	if _, allowed := cfg.RemoteWorkspaceAllows("mintclaw-build", "browser_act"); allowed {
		t.Fatal("RemoteWorkspaceAllows() allowed unsupported tool")
	}
}

func TestValidateExecutionTargetsRejectsInvalidRemoteWorkspaces(t *testing.T) {
	tests := []struct {
		name      string
		workspace RemoteWorkspace
		want      string
	}{
		{name: "unknown target", workspace: RemoteWorkspace{
			Target: "missing", WorkingScope: "project", Revision: "v1", Tools: []string{"read_file"},
		}, want: "unknown target"},
		{name: "missing scope", workspace: RemoteWorkspace{
			Target: "build", Revision: "v1", Tools: []string{"read_file"},
		}, want: "invalid working scope"},
		{name: "missing revision", workspace: RemoteWorkspace{
			Target: "build", WorkingScope: "project", Tools: []string{"read_file"},
		}, want: "invalid revision"},
		{name: "empty tools", workspace: RemoteWorkspace{
			Target: "build", WorkingScope: "project", Revision: "v1",
		}, want: "non-empty tool set"},
		{name: "unsupported tool", workspace: RemoteWorkspace{
			Target: "build", WorkingScope: "project", Revision: "v1", Tools: []string{"browser_act"},
		}, want: "unsupported tool"},
		{name: "duplicate tool", workspace: RemoteWorkspace{
			Target: "build", WorkingScope: "project", Revision: "v1",
			Tools: []string{"read_file", "read_file"},
		}, want: "duplicate tool"},
		{name: "missing file profile", workspace: RemoteWorkspace{
			Target: "exec-only", WorkingScope: "project", Revision: "v1", Tools: []string{"read_file"},
		}, want: "target file profile"},
		{name: "missing job profile", workspace: RemoteWorkspace{
			Target: "build", WorkingScope: "project", Revision: "v1", Tools: []string{"jobs"},
		}, want: "target job profile"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Execution.Targets = map[string]ExecutionTarget{
				"build": {
					Type: "node", Node: "node-build", FileProfile: "project-files",
				},
				"exec-only": {Type: "node", Node: "node-exec"},
			}
			cfg.Execution.RemoteWorkspaces = map[string]RemoteWorkspace{"workspace": test.workspace}
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExecutionTargetsRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		target string
		value  ExecutionTarget
		want   string
	}{
		{
			name:   "invalid name",
			target: "Build Server",
			value:  ExecutionTarget{Type: "node", Node: "builder"},
			want:   "invalid name",
		},
		{
			name:   "unsupported type",
			target: "build",
			value:  ExecutionTarget{Type: "ssh", Node: "builder"},
			want:   `unsupported type "ssh"`,
		},
		{
			name:   "invalid node",
			target: "build",
			value:  ExecutionTarget{Type: "node", Node: " builder"},
			want:   "invalid node reference",
		},
		{
			name:   "unsupported executor",
			target: "build",
			value:  ExecutionTarget{Type: "node", Node: "builder", Executor: "docker"},
			want:   `unsupported executor "docker"`,
		},
		{
			name:   "invalid file profile",
			target: "build",
			value:  ExecutionTarget{Type: "node", Node: "builder", FileProfile: "Root Files"},
			want:   "invalid file profile",
		},
		{
			name:   "invalid service profile",
			target: "build",
			value: ExecutionTarget{
				Type: "node", Node: "builder", ServiceProfile: "Root Services",
			},
			want: "invalid service profile",
		},
		{
			name:   "invalid update profile",
			target: "build",
			value: ExecutionTarget{
				Type: "node", Node: "builder", UpdateProfile: "Stable Releases",
			},
			want: "invalid update profile",
		},
		{
			name:   "invalid job profile",
			target: "build",
			value: ExecutionTarget{
				Type: "node", Node: "builder", JobProfile: "Build Jobs",
			},
			want: "invalid job profile",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Execution.Targets = map[string]ExecutionTarget{test.target: test.value}
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestValidateExecutionTargetsRejectsInvalidAgentPolicies(t *testing.T) {
	tests := []struct {
		name   string
		policy TargetPolicy
		want   string
	}{
		{
			name:   "unknown target",
			policy: TargetPolicy{AllowedTargets: []string{"missing"}},
			want:   `references unknown target "missing"`,
		},
		{
			name:   "duplicate target",
			policy: TargetPolicy{AllowedTargets: []string{"build", "build"}},
			want:   `contains duplicate target "build"`,
		},
		{
			name:   "default not allowed",
			policy: TargetPolicy{DefaultTarget: "build"},
			want:   `default target "build" is not allowed`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Execution.Targets = map[string]ExecutionTarget{
				"build": {Type: "node", Node: "builder"},
			}
			cfg.Agents.List = []AgentConfig{{ID: "ops", TargetPolicy: &test.policy}}
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestValidateExecutionTargetsRejectsOversizedTargetSet(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = make(map[string]ExecutionTarget, MaxExecutionTargets+1)
	for index := 0; index <= MaxExecutionTargets; index++ {
		cfg.Execution.Targets[fmt.Sprintf("target_%d", index)] = ExecutionTarget{
			Type: "node",
			Node: "builder",
		}
	}
	if err := cfg.ValidateExecutionTargets(); err == nil ||
		!strings.Contains(err.Error(), "target limit") {
		t.Fatalf("ValidateExecutionTargets() error = %v", err)
	}
}

func TestLoadConfigRejectsUnknownExecutionTargetReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"version": 3,
		"agents": {
			"defaults": {
				"target_policy": {"allowed_targets": ["missing"]}
			}
		},
		"execution": {
			"targets": {
				"build": {"type": "node", "node": "builder"}
			}
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil ||
		!strings.Contains(err.Error(), `references unknown target "missing"`) {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestConfigLoadersPreserveExecutionTargetPolicy(t *testing.T) {
	loaders := map[string]func(string) (*Config, error){
		"repository": LoadConfig,
		"read-only":  LoadConfigReadOnly,
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{
				"version": 3,
				"agents": {
					"defaults": {
						"target_policy": {
							"default_target": "build",
							"allowed_targets": ["build"]
						}
					}
				},
				"execution": {
					"targets": {
						"build": {"type": "node", "node": "linux-builder"}
					}
				},
				"nodes": {"enabled": true}
			}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := load(path)
			if err != nil {
				t.Fatal(err)
			}
			target, exists := cfg.Execution.Targets["build"]
			if !exists || target.Type != "node" || target.Node != "linux-builder" {
				t.Fatalf("execution target = %#v, exists %v", target, exists)
			}
			policy := cfg.Agents.Defaults.TargetPolicy
			if policy == nil || policy.DefaultTarget != "build" ||
				len(policy.AllowedTargets) != 1 || policy.AllowedTargets[0] != "build" {
				t.Fatalf("target policy = %#v", policy)
			}
			if !cfg.Nodes.Enabled {
				t.Fatal("nodes.enabled was not preserved")
			}
		})
	}
}

func TestInvalidTargetPolicyDoesNotRewriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{
		"version": 3,
		"agents": {
			"defaults": {
				"target_policy": {"allowed_targets": ["missing"]}
			}
		},
		"execution": {
			"targets": {
				"build": {"type": "node", "node": "linux-builder"}
			}
		},
		"nodes": {"enabled": true}
	}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil ||
		!strings.Contains(err.Error(), `references unknown target "missing"`) {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("failed load rewrote config:\n%s", after)
	}
	backups, err := filepath.Glob(path + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("failed load created backups: %v", backups)
	}
}
