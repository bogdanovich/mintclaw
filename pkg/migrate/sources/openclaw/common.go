package openclaw

import "github.com/bogdanovich/mintclaw/pkg/migrate/internal"

var workspaceFiles = []internal.WorkspaceFile{
	{Source: "AGENTS.md", Target: "AGENTS.md"},
	{Source: "SOUL.md", Target: "SOUL.md"},
	{Source: "USER.md", Target: "USER.md"},
	{Source: "HEARTBEAT.md", Target: "HEARTBEAT.md"},
}

var workspaceDirs = []string{
	"memory",
	"skills",
}

var supportedChannels = map[string]bool{
	"whatsapp": true,
	"telegram": true,
	"feishu":   true,
	"discord":  true,
	"maixcam":  true,
	"qq":       true,
	"dingtalk": true,
	"slack":    true,
	"matrix":   true,
	"line":     true,
	"onebot":   true,
	"wecom":    true,
}
