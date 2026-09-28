package companion

// PrivilegedShellHelperConfig selects the operator-installed owner-shell
// authority helper. Linux uses an explicit root-owned Unix socket; macOS gets
// a private inherited endpoint from the supervising LaunchDaemon.
type PrivilegedShellHelperConfig struct {
	Endpoint string `json:"endpoint,omitempty"`
}
