package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/config"
	nodepkg "github.com/bogdanovich/mintclaw/pkg/nodes"
)

func newInvocationStoreCommand(loadConfig func() (*config.Config, error)) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:    "invocation-store",
		Short:  "Inspect the gateway invocation database",
		Hidden: true,
		Args:   cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(
		&workspace,
		"workspace",
		"",
		"Workspace containing state/node_invocations.db (default: configured workspace)",
	)
	databasePath := func() (string, error) {
		selected := strings.TrimSpace(workspace)
		if selected == "" {
			cfg, err := loadConfig()
			if err != nil {
				return "", err
			}
			selected = strings.TrimSpace(cfg.WorkspacePath())
		}
		if selected == "" {
			return "", errors.New("workspace is required for gateway invocation maintenance")
		}
		return nodepkg.GatewayInvocationStorePath(selected), nil
	}
	cmd.AddCommand(newInvocationStoreInspectCommand(databasePath))
	return cmd
}

func newInvocationStoreInspectCommand(databasePath func() (string, error)) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Validate and summarize the gateway invocation database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := databasePath()
			if err != nil {
				return err
			}
			report, err := nodepkg.InspectGatewayInvocationStore(path)
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"schema=%d records=%d prepared=%d dispatched=%d db_bytes=%d wal_bytes=%d free_page_bytes=%d maximum_bytes=%d oldest_updated_at=%d retention_seconds=%d\n",
				report.SchemaVersion,
				report.Records,
				report.Prepared,
				report.Dispatched,
				report.DatabaseBytes,
				report.WALBytes,
				report.FreePageBytes,
				report.MaximumBytes,
				report.OldestUpdatedAt,
				report.RetentionSeconds,
			)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	return cmd
}
