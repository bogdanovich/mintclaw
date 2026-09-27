package skills

import (
	"time"

	"github.com/spf13/cobra"

	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

func newInstallCommand(d *deps) *cobra.Command {
	var options skillMutationOptions
	var registry, version string
	var replace bool

	cmd := &cobra.Command{
		Use:   "install <slug>",
		Short: "Install a skill into an explicit ownership scope",
		Example: `mintclaw skills install owner/repository/skills/weather
mintclaw skills install --scope repository --project . owner/repository/skills/pr-review
mintclaw skills install --scope workspace --registry clawhub github
mintclaw skills install --dry-run --json owner/repository/skills/weather`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, target, err := d.scopedSkillManager(cmd.Context(), options.scope, options.project)
			if err != nil {
				return err
			}
			ctx, cancel := contextWithSkillMutationTimeout(cmd.Context(), time.Minute)
			defer cancel()
			plan, err := manager.Install(ctx, runtimeskills.SkillInstallRequest{
				Target: target, Registry: registry, Slug: args[0], Version: version,
				Replace: replace, DryRun: options.dryRun,
			})
			if err != nil {
				return err
			}
			return renderSkillMutationPlan(cmd.OutOrStdout(), plan, options.jsonOutput)
		},
	}

	options.bind(cmd, runtimeskills.SkillInstallScopeUser)
	cmd.Flags().StringVar(&registry, "registry", "github", "Configured skill registry")
	cmd.Flags().StringVar(&version, "version", "", "Registry version or revision")
	cmd.Flags().BoolVar(&replace, "replace", false, "Replace only when immutable origin matches")
	return cmd
}
