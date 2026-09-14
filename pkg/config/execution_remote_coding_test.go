package config

import (
	"strings"
	"testing"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func TestRemoteCodingProjectForRequiresExactRequesterAndMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Targets = map[string]ExecutionTarget{
		"companion": {Type: "node", Node: "developer-mac"},
	}
	cfg.Execution.RemoteCodingProjects = map[string]RemoteCodingProject{
		"mintclaw": {
			Target: "companion", Project: "mintclaw", Revision: "project-v1",
			Modes: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
			Requesters: []RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: "owner-42",
			}},
		},
	}

	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	project, allowed := cfg.RemoteCodingProjectFor(
		"mintclaw",
		"main",
		"telegram",
		"owner-42",
		codingtask.TaskModeInvestigate,
	)
	if !allowed || project.Target != "companion" || project.Project != "mintclaw" {
		t.Fatalf("RemoteCodingProjectFor() = %#v, %v", project, allowed)
	}
	for _, request := range []struct {
		alias, agent, channel, sender string
		mode                          codingtask.TaskMode
	}{
		{alias: "other", agent: "main", channel: "telegram", sender: "owner-42", mode: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "other", channel: "telegram", sender: "owner-42", mode: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "slack", sender: "owner-42", mode: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "telegram", sender: "owner-43", mode: codingtask.TaskModeInvestigate},
		{alias: "mintclaw", agent: "main", channel: "telegram", sender: "owner-42", mode: codingtask.TaskModeMutate},
	} {
		if _, allowed := cfg.RemoteCodingProjectFor(
			request.alias,
			request.agent,
			request.channel,
			request.sender,
			request.mode,
		); allowed {
			t.Fatalf("unexpected grant for %#v", request)
		}
	}
}

func TestValidateExecutionTargetsRejectsInvalidRemoteCodingProjects(t *testing.T) {
	valid := RemoteCodingProject{
		Target: "companion", Project: "mintclaw", Revision: "project-v1",
		Modes: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
		Requesters: []RemoteCodingRequester{{
			Agent: "main", Channel: "telegram", Sender: "owner-42",
		}},
	}
	tests := map[string]struct {
		mutate func(*RemoteCodingProject)
		want   string
	}{
		"unknown target": {
			mutate: func(project *RemoteCodingProject) { project.Target = "missing" },
			want:   "unknown target",
		},
		"empty modes": {
			mutate: func(project *RemoteCodingProject) { project.Modes = nil },
			want:   "non-empty mode set",
		},
		"duplicate modes": {
			mutate: func(project *RemoteCodingProject) {
				project.Modes = []codingtask.TaskMode{
					codingtask.TaskModeInvestigate,
					codingtask.TaskModeInvestigate,
				}
			},
			want: "duplicate mode",
		},
		"no requesters": {
			mutate: func(project *RemoteCodingProject) { project.Requesters = nil },
			want:   "explicit requesters",
		},
		"wildcard requester": {
			mutate: func(project *RemoteCodingProject) { project.Requesters[0].Sender = "*" },
			want:   "invalid requester",
		},
		"duplicate requester": {
			mutate: func(project *RemoteCodingProject) {
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
			project.Modes = append([]codingtask.TaskMode(nil), valid.Modes...)
			project.Requesters = append([]RemoteCodingRequester(nil), valid.Requesters...)
			test.mutate(&project)
			cfg.Execution.RemoteCodingProjects = map[string]RemoteCodingProject{"mintclaw": project}
			err := cfg.ValidateExecutionTargets()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionTargets() error = %v, want %q", err, test.want)
			}
		})
	}
}
