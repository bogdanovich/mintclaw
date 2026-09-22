package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	agentruntime "github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/project"
	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

type skillMutationOptions struct {
	scope      string
	project    string
	dryRun     bool
	jsonOutput bool
}

func (options *skillMutationOptions) bind(cmd *cobra.Command, defaultScope runtimeskills.SkillInstallScope) {
	cmd.Flags().StringVar(
		&options.scope,
		"scope",
		string(defaultScope),
		"Ownership scope: user, repository, or workspace",
	)
	cmd.Flags().StringVar(
		&options.project,
		"project",
		"",
		"Directory used to resolve repository scope (defaults to current directory)",
	)
	cmd.Flags().BoolVar(&options.dryRun, "dry-run", false, "Validate and print the plan without changing files")
	cmd.Flags().BoolVar(&options.jsonOutput, "json", false, "Emit the stable JSON plan")
}

func (d *deps) scopedSkillManager(
	ctx context.Context,
	scopeName string,
	projectPath string,
) (*runtimeskills.ScopedSkillManager, runtimeskills.SkillInstallTarget, error) {
	manager, target, _, err := d.scopedSkillManagerWithContext(ctx, scopeName, projectPath)
	return manager, target, err
}

func (d *deps) scopedSkillManagerWithContext(
	ctx context.Context,
	scopeName string,
	projectPath string,
) (*runtimeskills.ScopedSkillManager, runtimeskills.SkillInstallTarget, runtimeskills.SkillInstallContext, error) {
	if d == nil || d.cfg == nil {
		return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{},
			errors.New("skills configuration is not initialized")
	}
	scope, err := runtimeskills.ParseSkillInstallScope(scopeName, runtimeskills.SkillInstallScopeUser)
	if err != nil {
		return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{}, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{},
			fmt.Errorf("resolve user home for skill scope: %w", err)
	}
	installContext := runtimeskills.SkillInstallContext{UserHome: userHome, Workspace: d.workspace}
	if scope == runtimeskills.SkillInstallScopeRepository || strings.TrimSpace(projectPath) != "" {
		if strings.TrimSpace(projectPath) == "" {
			projectPath, err = os.Getwd()
			if err != nil {
				return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{},
					fmt.Errorf("resolve current directory for repository scope: %w", err)
			}
		}
		identity, resolveErr := project.ResolveProject(ctx, projectPath)
		if resolveErr != nil {
			return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{}, resolveErr
		}
		installContext.RepositoryRoot = identity.ProjectRoot
	}
	target, err := runtimeskills.ResolveSkillInstallTarget(scope, installContext)
	if err != nil {
		return nil, runtimeskills.SkillInstallTarget{}, runtimeskills.SkillInstallContext{}, err
	}
	environments := map[runtimeskills.SkillRuntime]runtimeskills.SkillCompatibilityEnvironment{
		runtimeskills.SkillRuntimeCoding: agentruntime.ConfiguredSkillCompatibilityEnvironment(
			d.cfg,
			runtimeskills.SkillRuntimeCoding,
		),
		runtimeskills.SkillRuntimeGateway: agentruntime.ConfiguredSkillCompatibilityEnvironment(
			d.cfg,
			runtimeskills.SkillRuntimeGateway,
		),
	}
	manager := runtimeskills.NewScopedSkillManager(
		runtimeskills.NewRegistryManagerFromToolsConfig(d.cfg.Tools.Skills),
		environments,
	)
	return manager, target, installContext, nil
}

func contextWithSkillMutationTimeout(
	parent context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func renderSkillMutationPlan(w io.Writer, plan runtimeskills.SkillMutationPlan, jsonOutput bool) error {
	if jsonOutput {
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	state := "Applied"
	if plan.DryRun {
		state = "Dry run"
	}
	fmt.Fprintf(w, "%s: %s %s\n", state, plan.Operation, plan.SkillName)
	if plan.Source != "" {
		fmt.Fprintf(w, "  source: %s (%s)\n", plan.Source, plan.SourceScope)
	}
	fmt.Fprintf(w, "  action: %s\n", plan.Action)
	fmt.Fprintf(w, "  target: %s (%s)\n", plan.Target, plan.Scope)
	if plan.Origin != nil {
		fmt.Fprintf(w, "  origin: %s:%s\n", plan.Origin.Registry, plan.Origin.Slug)
	}
	for _, compatibility := range plan.Compatibility {
		fmt.Fprintf(w, "  %s: %s\n", compatibility.Runtime, compatibility.Status)
	}
	for _, gap := range plan.DependencyGaps {
		fmt.Fprintf(w, "    missing %s %s: %s\n", gap.Kind, gap.Name, gap.State)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	return nil
}
