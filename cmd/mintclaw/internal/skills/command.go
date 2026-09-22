package skills

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	agentruntime "github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/project"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

type deps struct {
	cfg       *config.Config
	workspace string
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

			d.cfg = cfg
			d.workspace = cfg.WorkspacePath()

			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	loaderFn := func(ctx context.Context, runtimeProduct skills.SkillRuntime) (*skills.SkillsLoader, error) {
		return d.newSkillsLoader(ctx, runtimeProduct)
	}
	gatewayLoaderFn := func() (*skills.SkillsLoader, error) {
		return loaderFn(context.Background(), skills.SkillRuntimeGateway)
	}

	cmd.AddCommand(
		newListCommand(loaderFn),
		newDoctorCommand(loaderFn),
		newInstallCommand(),
		newRemoveCommand(),
		newSearchCommand(),
		newShowCommand(gatewayLoaderFn),
	)

	return cmd
}

func (d *deps) newSkillsLoader(
	ctx context.Context,
	runtimeProduct skills.SkillRuntime,
) (*skills.SkillsLoader, error) {
	if d == nil || d.cfg == nil {
		return nil, fmt.Errorf("skills configuration is not initialized")
	}
	userHome, _ := os.UserHomeDir()
	systemRoot, _ := skills.RuntimeSystemBundleRoot(config.GetHome())
	var roots []skills.SkillRoot
	switch runtimeProduct {
	case skills.SkillRuntimeGateway:
		roots = skills.GatewaySkillRoots(d.workspace, userHome, systemRoot)
	case skills.SkillRuntimeCoding:
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve coding skill working directory: %w", err)
		}
		identity, err := project.ResolveProject(ctx, cwd)
		if err != nil {
			return nil, err
		}
		roots, err = skills.CodingSkillRoots(identity.ProjectRoot, identity.InvocationCWD, userHome, systemRoot)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported skills runtime %q", runtimeProduct)
	}
	loader := skills.NewSkillsLoader(roots)
	loader.WithCompatibilityEnvironment(agentruntime.ConfiguredSkillCompatibilityEnvironment(d.cfg, runtimeProduct))
	return loader, nil
}
