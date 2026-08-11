package mcp

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an MCP server from config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := loadConfig(); err != nil {
				return err
			}

			name := args[0]
			if err := removeMCPServer(name); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ MCP server %q removed.\n", name)
			return nil
		},
	}
}

func removeMCPServer(name string) error {
	return updateValidatedConfig(func(cfg *config.Config) error {
		if _, exists := cfg.Tools.MCP.Servers[name]; !exists {
			return fmt.Errorf("MCP server %q not found", name)
		}
		delete(cfg.Tools.MCP.Servers, name)
		if len(cfg.Tools.MCP.Servers) == 0 {
			cfg.Tools.MCP.Servers = nil
			cfg.Tools.MCP.Enabled = false
		}
		return nil
	})
}
