package companion

// LocalUserShellConfig enables shell.exec.v1 as the companion service account.
// The working-scope paths and shell path remain node-local; discovery exposes
// only their aliases and the bounded profile facts in ShellBrokerSnapshot.
type LocalUserShellConfig struct {
	Revision                  string            `json:"revision"`
	Profile                   string            `json:"profile"`
	ShellPath                 string            `json:"shell_path"`
	Login                     bool              `json:"login"`
	WorkingScopes             map[string]string `json:"working_scopes"`
	FixedEnvironment          map[string]string `json:"fixed_environment,omitempty"`
	PermittedEnvironmentNames []string          `json:"permitted_environment_names,omitempty"`
	TimeoutSecondsMax         int               `json:"timeout_seconds_max"`
	OutputBytesMax            int               `json:"output_bytes_max"`
	ConcurrentCommands        int               `json:"concurrent_commands"`

	ready *normalizedAuthorityBrokerProfile
}
