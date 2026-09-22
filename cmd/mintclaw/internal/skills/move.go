package skills

import (
	"github.com/spf13/cobra"

	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

func newMoveCommand(d *deps) *cobra.Command {
	var options skillMutationOptions
	var fromScope string
	cmd := &cobra.Command{
		Use:     "move <name>",
		Short:   "Move a validated skill between explicit ownership scopes",
		Args:    cobra.ExactArgs(1),
		Example: `mintclaw skills move weather --from-scope workspace --scope user --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceScope, err := runtimeskills.ParseSkillInstallScope(fromScope, "")
			if err != nil {
				return err
			}
			projectPath := options.project
			if sourceScope == runtimeskills.SkillInstallScopeRepository && projectPath == "" {
				projectPath = "."
			}
			manager, target, installContext, err := d.scopedSkillManagerWithContext(
				cmd.Context(),
				options.scope,
				projectPath,
			)
			if err != nil {
				return err
			}
			source, err := runtimeskills.ResolveSkillInstallTarget(sourceScope, installContext)
			if err != nil {
				return err
			}
			plan, err := manager.Move(runtimeskills.SkillMoveRequest{
				Source: source, Target: target, Name: args[0], DryRun: options.dryRun,
			})
			if err != nil {
				return err
			}
			return renderSkillMutationPlan(cmd.OutOrStdout(), plan, options.jsonOutput)
		},
	}
	options.bind(cmd, runtimeskills.SkillInstallScopeUser)
	cmd.Flags().StringVar(
		&fromScope,
		"from-scope",
		"",
		"Required source scope: user, repository, or workspace",
	)
	_ = cmd.MarkFlagRequired("from-scope")
	return cmd
}
