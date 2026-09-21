package skills

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

type deps struct {
	workspace    string
	skillsLoader *skills.SkillsLoader
}

func NewSkillsCommand() *cobra.Command {
	var d deps

	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage skills",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := internal.LoadConfig()
			if err != nil {
				return fmt.Errorf("error loading config: %w", err)
			}

			d.workspace = cfg.WorkspacePath()

			userHome, _ := os.UserHomeDir()
			systemRoot, _ := skills.RuntimeSystemBundleRoot(config.GetHome())
			d.skillsLoader = skills.NewSkillsLoader(skills.GatewaySkillRoots(
				d.workspace,
				userHome,
				systemRoot,
			))

			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	loaderFn := func() (*skills.SkillsLoader, error) {
		if d.skillsLoader == nil {
			return nil, fmt.Errorf("skills loader is not initialized")
		}
		return d.skillsLoader, nil
	}

	cmd.AddCommand(
		newListCommand(loaderFn),
		newInstallCommand(),
		newRemoveCommand(),
		newSearchCommand(),
		newShowCommand(loaderFn),
	)

	return cmd
}
