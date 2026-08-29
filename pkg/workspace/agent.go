package workspace

import (
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// ResolveAgentPath applies the runtime's single workspace-selection rule for
// one configured agent. Named agents without an explicit path use a sibling of
// the default workspace so their durable state remains isolated.
func ResolveAgentPath(agent *config.AgentConfig, defaults *config.AgentDefaults) string {
	if defaults == nil {
		return ""
	}
	if agent != nil && strings.TrimSpace(agent.Workspace) != "" {
		return fileutil.ExpandHome(strings.TrimSpace(agent.Workspace))
	}
	defaultWorkspace := fileutil.ExpandHome(strings.TrimSpace(defaults.Workspace))
	if agent == nil || agent.Default || routing.NormalizeAgentID(agent.ID) == routing.DefaultAgentID {
		return defaultWorkspace
	}
	id := routing.NormalizeAgentID(agent.ID)
	return filepath.Join(defaultWorkspace, "..", "workspace-"+id)
}
