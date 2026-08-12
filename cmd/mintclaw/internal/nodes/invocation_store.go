package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/config"
	nodepkg "github.com/bogdanovich/mintclaw/pkg/nodes"
)

func newInvocationStoreCommand(loadConfig func() (*config.Config, error)) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:    "invocation-store",
		Short:  "Inspect or export the gateway invocation database",
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
	cmd.AddCommand(
		newInvocationStoreInspectCommand(databasePath),
		newInvocationStoreExportCommand(databasePath),
	)
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
			report, err := nodepkg.InspectGatewayInvocationSQLite(path)
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
				"schema=%d records=%d prepared=%d dispatched=%d db_bytes=%d wal_bytes=%d free_page_bytes=%d maximum_bytes=%d oldest_updated_at=%d retention_seconds=%d migration_complete=%t\n",
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
				report.MigrationComplete,
			)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	return cmd
}

func newInvocationStoreExportCommand(databasePath func() (string, error)) *cobra.Command {
	var output string
	var gatewayStopped bool
	var replace bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a validated legacy snapshot for binary downgrade",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !gatewayStopped {
				return errors.New("refusing downgrade export without --gateway-stopped")
			}
			output = strings.TrimSpace(output)
			if output == "" {
				return errors.New("--output is required")
			}
			if !replace {
				if _, err := os.Lstat(output); err == nil {
					return errors.New("export output already exists; use --replace to overwrite it")
				} else if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("inspect export output: %w", err)
				}
			}
			path, err := databasePath()
			if err != nil {
				return err
			}
			if filepath.Clean(output) == strings.TrimSuffix(path, filepath.Ext(path))+".json" {
				return errors.New("export to a staging path, then replace the migration marker only while downgrading")
			}
			report, err := nodepkg.ExportGatewayInvocationSQLite(path, output, replace)
			if err != nil {
				return err
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"exported records=%d bytes_limit=%d output=%s\n",
				report.Records,
				nodepkg.DefaultGatewayInvocationStoreBytes,
				output,
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Staging path for the legacy JSON snapshot")
	cmd.Flags().BoolVar(
		&gatewayStopped,
		"gateway-stopped",
		false,
		"Confirm the gateway is stopped and cannot commit newer authority",
	)
	cmd.Flags().BoolVar(&replace, "replace", false, "Replace an existing staging export")
	return cmd
}
