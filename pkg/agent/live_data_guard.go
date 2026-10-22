package agent

import (
	"slices"
	"strings"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func (p *Pipeline) liveDataFailureMessage(tool string, result *toolshared.ToolResult) string {
	if p == nil || p.Cfg == nil || (result != nil && !result.IsError && result.Err == nil) {
		return ""
	}
	for _, guard := range p.Cfg.Tools.LiveDataGuards {
		if !slices.Contains(guard.Tools, tool) {
			continue
		}
		if message := strings.TrimSpace(guard.FailureMessage); message != "" {
			return message
		}
		return "The required live data could not be loaded. No report was created or sent using cached data."
	}
	return ""
}
