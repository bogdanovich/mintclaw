package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func TestRemoteCodingScopeForRequiresExactRequesterAndProfile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"companion": {Type: "node", Node: "developer-mac"},
	}
	cfg.Execution.RemoteCodingScopes = map[string]RemoteCodingScope{
		"mintclaw": {
			Target: "companion", Scope: "mintclaw", Revision: "project-v1",
			Profiles: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
			Requesters: []RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: "owner-42",
			}},
		},
	}

	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	scope, allowed := cfg.RemoteCodingScopeFor(
		"mintclaw",
		"main",
		"telegram",
		"owner-42",
		codingtask.TaskModeInvestigate,
	)
	if !allowed || scope.Target != "companion" || scope.Scope != "mintclaw" {
		t.Fatalf("RemoteCodingScopeFor() = %#v, %v", scope, allowed)
	}
	for _, request := range []struct {
		alias, agent, channel, sender string
		profile                       codingtask.TaskMode
	}{
		{alias: "other", agent: "main", channel: "telegram", sender: "owner-42", profile: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "other", channel: "telegram", sender: "owner-42", profile: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "slack", sender: "owner-42", profile: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "telegram", sender: "owner-43", profile: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "telegram", sender: "owner-42", profile: codingtask.TaskModeMutate},
	} {
		if _, allowed := cfg.RemoteCodingScopeFor(
			request.alias,
			request.agent,
			request.channel,
			request.sender,
			request.profile,
		); allowed {
			t.Fatalf("unexpected grant for %#v", request)
		}
	}
	projectYolo := cfg.Execution.RemoteCodingScopes["mintclaw"]
	projectYolo.Profiles = []codingtask.TaskMode{codingtask.TaskModeProjectYolo}
	cfg.Execution.RemoteCodingScopes["mintclaw"] = projectYolo
	if _, allowed := cfg.RemoteCodingScopeFor(
		"mintclaw", "main", "telegram", "owner-42", codingtask.TaskModeProjectYolo,
	); !allowed {
		t.Fatal("exact project-yolo grant was rejected")
	}
	for _, profile := range []codingtask.TaskMode{
		codingtask.TaskModeMachineYolo,
		codingtask.TaskModeMachineYoloRoot,
	} {
		crafted := cfg.Execution.RemoteCodingScopes["mintclaw"]
		crafted.Profiles = []codingtask.TaskMode{profile}
		cfg.Execution.RemoteCodingScopes["mintclaw"] = crafted
		if _, allowed := cfg.RemoteCodingScopeFor(
			"mintclaw",
			"main",
			"telegram",
			"owner-42",
			profile,
		); allowed {
			t.Fatalf("crafted in-memory config granted deferred profile %q", profile)
		}
	}
}

func TestValidateExecutionTargetsRejectsInvalidRemoteCodingScopes(t *testing.T) {
	valid := RemoteCodingScope{
		Target: "companion", Scope: "mintclaw", Revision: "project-v1",
		Profiles: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
		Requesters: []RemoteCodingRequester{{
			Agent: "main", Channel: "telegram", Sender: "owner-42",
		}},
	}
	tests := map[string]struct {
		alias  string
		mutate func(*RemoteCodingScope)
		want   string
	}{
		"numeric gateway alias": {
			alias:  "1mintclaw",
			mutate: func(*RemoteCodingScope) {},
			want:   "invalid alias",
		},
		"numeric node scope alias": {
			mutate: func(scope *RemoteCodingScope) { scope.Scope = "1mintclaw" },
			want:   "invalid node scope alias",
		},
		"unknown target": {
			mutate: func(project *RemoteCodingScope) { project.Target = "missing" },
			want:   "unknown target",
		},
		"empty profiles": {
			mutate: func(project *RemoteCodingScope) { project.Profiles = nil },
			want:   "non-empty profile set",
		},
		"duplicate profiles": {
			mutate: func(project *RemoteCodingScope) {
				project.Profiles = []codingtask.TaskMode{
					codingtask.TaskModeInvestigate,
					codingtask.TaskModeInvestigate,
				}
			},
			want: "duplicate profile",
		},
		"unadmitted profile": {
			mutate: func(scope *RemoteCodingScope) {
				scope.Profiles = []codingtask.TaskMode{codingtask.TaskModeMachineYolo}
			},
			want: "unadmitted profile",
		},
		"no requesters": {
			mutate: func(project *RemoteCodingScope) { project.Requesters = nil },
			want:   "explicit requesters",
		},
		"wildcard requester": {
			mutate: func(project *RemoteCodingScope) { project.Requesters[0].Sender = "*" },
			want:   "invalid requester",
		},
		"duplicate requester": {
			mutate: func(project *RemoteCodingScope) {
				project.Requesters = append(project.Requesters, project.Requesters[0])
			},
			want: "duplicate requester",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Execution.Targets = map[string]ExecutionTarget{
				"companion": {Type: "node", Node: "developer-mac"},
			}
			project := valid
			project.Profiles = append([]codingtask.TaskMode(nil), valid.Profiles...)
			project.Requesters = append([]RemoteCodingRequester(nil), valid.Requesters...)
			test.mutate(&project)
			alias := test.alias
			if alias == "" {
				alias = "mintclaw"
			}
			cfg.Execution.RemoteCodingScopes = map[string]RemoteCodingScope{alias: project}
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigRejectsLegacyRemoteCodingProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"version":4,"execution":{"remote_coding_projects":{}}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "execution.remote_coding_projects") {
		t.Fatalf("LoadConfig() error = %v, want rejected legacy config key", err)
	}
}
