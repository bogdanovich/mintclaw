package agent

import (
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

var machineYoloPrivilegeDenyPatterns = []string{
	`(^|[^a-z0-9_])(sudo|doas|pkexec|runas(?:\.exe)?|gsudo|su)([^a-z0-9_]|$)`,
	`\b(systemd-run|setpriv)\b[^\r\n]*(--uid|--reuid)(=|[[:space:]]+)(0|root)\b`,
	`\bwith[[:space:]]+administrator[[:space:]]+privileges\b`,
}

// codingExecConfig deliberately ignores personal-agent exec policy. Coding
// authority is selected by the immutable runtime profile. machine-yolo keeps
// unrestricted user-account execution while retaining an unbypassable deny
// layer for common privilege-elevation frontends; machine-yolo-root will use a
// separate backend rather than weakening this profile.
func codingExecConfig(timeoutSeconds int, profile codingscope.Profile) config.ExecConfig {
	result := config.ExecConfig{TimeoutSeconds: timeoutSeconds}
	if profile != codingscope.ProfileMachineYolo {
		return result
	}
	result.EnableDenyPatterns = true
	result.CustomDenyPatterns = append([]string(nil), machineYoloPrivilegeDenyPatterns...)
	// Custom denies are evaluated before allows. This catch-all bypasses the
	// generic conservative deny list while leaving the privilege layer intact.
	result.CustomAllowPatterns = []string{`(?s).*`}
	return result
}
