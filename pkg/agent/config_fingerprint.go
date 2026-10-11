package agent

import "github.com/bogdanovich/mintclaw/pkg/config"

// PublicConfigFingerprint observes the active configuration under its native
// lock, rather than reading a file that may not have been successfully reloaded.
func (al *AgentLoop) PublicConfigFingerprint() (string, error) {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return config.PublicFingerprint(al.cfg)
}
