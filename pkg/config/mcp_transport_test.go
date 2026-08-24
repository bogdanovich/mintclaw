package config

import (
	"strings"
	"testing"
)

func TestMCPServerConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		server  MCPServerConfig
		wantErr string
	}{
		{name: "stdio", server: MCPServerConfig{Type: "stdio", Command: "example"}},
		{name: "http", server: MCPServerConfig{Type: "http", URL: "https://example.invalid/mcp"}},
		{name: "sse", server: MCPServerConfig{Type: "sse", URL: "http://127.0.0.1:8080/mcp"}},
		{name: "missing type", server: MCPServerConfig{Command: "example"}, wantErr: "transport type"},
		{
			name:    "unknown type",
			server:  MCPServerConfig{Type: "HTTP", URL: "https://example.invalid/mcp"},
			wantErr: "transport type",
		},
		{name: "stdio missing command", server: MCPServerConfig{Type: "stdio"}, wantErr: "requires command"},
		{
			name: "stdio with remote fields",
			server: MCPServerConfig{
				Type: "stdio", Command: "example", URL: "https://example.invalid/mcp",
			},
			wantErr: "does not support url",
		},
		{name: "http missing url", server: MCPServerConfig{Type: "http"}, wantErr: "requires url"},
		{
			name:    "http invalid scheme",
			server:  MCPServerConfig{Type: "http", URL: "ftp://example.invalid/mcp"},
			wantErr: "HTTP(S)",
		},
		{
			name: "http with process fields",
			server: MCPServerConfig{
				Type: "http", URL: "https://example.invalid/mcp", Command: "example",
			},
			wantErr: "does not support command",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.server.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateMCPConfigReportsServerPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tools.MCP.Servers["example"] = MCPServerConfig{Command: "example"}
	err := cfg.ValidateMCPConfig()
	if err == nil || !strings.Contains(err.Error(), "tools.mcp.servers.example") {
		t.Fatalf("ValidateMCPConfig() error = %v, want server path", err)
	}
}
