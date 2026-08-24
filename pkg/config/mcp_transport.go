package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

const maxMCPExclusiveLockFilePathBytes = 4096

// Validate requires one explicit transport contract and rejects fields that
// belong to another transport. Config conversion belongs outside the runtime.
func (server MCPServerConfig) Validate() error {
	switch server.Type {
	case "stdio":
		if server.Command == "" {
			return fmt.Errorf("stdio transport requires command")
		}
		if server.URL != "" {
			return fmt.Errorf("stdio transport does not support url")
		}
		if len(server.Headers) > 0 {
			return fmt.Errorf("stdio transport does not support headers")
		}
	case "sse", "http":
		if server.URL == "" {
			return fmt.Errorf("%s transport requires url", server.Type)
		}
		parsed, err := url.ParseRequestURI(server.URL)
		if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("%s transport requires a valid absolute HTTP(S) url", server.Type)
		}
		if server.Command != "" {
			return fmt.Errorf("%s transport does not support command", server.Type)
		}
		if len(server.Args) > 0 {
			return fmt.Errorf("%s transport does not support args", server.Type)
		}
		if len(server.Env) > 0 {
			return fmt.Errorf("%s transport does not support env", server.Type)
		}
		if server.EnvFile != "" {
			return fmt.Errorf("%s transport does not support env_file", server.Type)
		}
		if server.ExclusiveLockFile != "" {
			return fmt.Errorf("%s transport does not support exclusive_lock_file", server.Type)
		}
	default:
		return fmt.Errorf("unsupported MCP transport type %q (supported: stdio, sse, http)", server.Type)
	}
	return ValidateMCPExclusiveLockFile(server)
}

// ValidateMCPConfig validates every configured server, including disabled
// templates that another subsystem may start directly.
func (c *Config) ValidateMCPConfig() error {
	names := make([]string, 0, len(c.Tools.MCP.Servers))
	for name := range c.Tools.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := c.Tools.MCP.Servers[name].Validate(); err != nil {
			return fmt.Errorf("tools.mcp.servers.%s: %w", name, err)
		}
	}
	return nil
}

// ValidateMCPExclusiveLockFile validates an optional cross-process lease path
// before an MCP subprocess is started.
func ValidateMCPExclusiveLockFile(server MCPServerConfig) error {
	path := server.ExclusiveLockFile
	if path == "" {
		return nil
	}
	if server.Type != "stdio" {
		return fmt.Errorf("exclusive_lock_file is supported only for stdio MCP servers")
	}
	if len(path) > maxMCPExclusiveLockFilePathBytes {
		return fmt.Errorf("exclusive_lock_file exceeds %d bytes", maxMCPExclusiveLockFilePathBytes)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("exclusive_lock_file must be absolute")
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("exclusive_lock_file must be clean")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("exclusive_lock_file parent is unavailable")
	}
	if !parent.IsDir() {
		return fmt.Errorf("exclusive_lock_file parent is not a directory")
	}
	return nil
}
