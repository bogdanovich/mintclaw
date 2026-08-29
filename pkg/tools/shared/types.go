package toolshared

type ExecRequest struct {
	Action     string            `json:"action"`
	Command    string            `json:"command,omitempty"`
	PTY        bool              `json:"pty,omitempty"`
	Background bool              `json:"background,omitempty"`
	Timeout    int               `json:"timeout,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	SessionID  string            `json:"sessionId,omitempty"`
	Data       string            `json:"data,omitempty"`
}

type ExecResponse struct {
	SessionID    string        `json:"sessionId,omitempty"`
	SessionScope string        `json:"sessionScope,omitempty"`
	RestartSafe  *bool         `json:"restartSafe,omitempty"`
	Status       string        `json:"status,omitempty"`
	ExitCode     int           `json:"exitCode,omitempty"`
	Output       string        `json:"output,omitempty"`
	Error        string        `json:"error,omitempty"`
	Sessions     []SessionInfo `json:"sessions,omitempty"`
}

type SessionInfo struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"`
}
