package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestResolveAgentPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	defaults := config.AgentDefaults{Workspace: "~/mintclaw/workspace"}
	tests := []struct {
		name  string
		agent *config.AgentConfig
		want  string
	}{
		{
			name:  "default",
			agent: &config.AgentConfig{ID: "main", Default: true},
			want:  filepath.Join(home, "mintclaw", "workspace"),
		},
		{
			name:  "normalized main",
			agent: &config.AgentConfig{ID: " MAIN "},
			want:  filepath.Join(home, "mintclaw", "workspace"),
		},
		{
			name:  "inherited named",
			agent: &config.AgentConfig{ID: "Deep Research"},
			want:  filepath.Join(home, "mintclaw", "workspace-deep-research"),
		},
		{
			name:  "explicit tilde",
			agent: &config.AgentConfig{ID: "custom", Workspace: "~/custom"},
			want:  filepath.Join(home, "custom"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveAgentPath(test.agent, &defaults); got != test.want {
				t.Fatalf("ResolveAgentPath() = %q, want %q", got, test.want)
			}
		})
	}
	if got := ResolveAgentPath(nil, nil); got != "" {
		t.Fatalf("ResolveAgentPath(nil, nil) = %q", got)
	}
}
