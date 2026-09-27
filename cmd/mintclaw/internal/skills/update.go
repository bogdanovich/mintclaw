package skills

import (
	"time"

	"github.com/spf13/cobra"

	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

func newUpdateCommand(d *deps) *cobra.Command {
	var options skillMutationOptions
	var version string
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a registry skill from its immutable recorded origin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, target, err := d.scopedSkillManager(cmd.Context(), options.scope, options.project)
			if err != nil {
				return err
			}
			ctx, cancel := contextWithSkillMutationTimeout(cmd.Context(), time.Minute)
			defer cancel()
			plan, err := manager.Update(ctx, target, args[0], version, options.dryRun)
			if err != nil {
				return err
			}
			return renderSkillMutationPlan(cmd.OutOrStdout(), plan, options.jsonOutput)
		},
	}
	options.bind(cmd, runtimeskills.SkillInstallScopeUser)
	cmd.Flags().StringVar(&version, "version", "", "Registry version or revision")
	return cmd
}
