package cron

import "github.com/spf13/cobra"

func newListCommand(storePath func() string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all scheduled jobs",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cronListCmd(storePath())
		},
	}

	return cmd
}
