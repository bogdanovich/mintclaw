package integrationtools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/skills"
)

const defaultSkillRegistryName = "github"

const installSkillUsageGuidance = "Use the exact owner/repo[/path] slug from the request or search result; " +
	"never shorten a nested path. Scope user means self/personal/shared/both runtimes; " +
	"workspace is only for an explicitly requested gateway-only workspace."

// InstallSkillTool installs one registry package into an explicitly selected
// mutable scope. The same resolver and mutation manager back the CLI.
type InstallSkillTool struct {
	manager        *skills.ScopedSkillManager
	installContext skills.SkillInstallContext
	mu             sync.Mutex
}

func NewInstallSkillTool(
	registryManager *skills.RegistryManager,
	installContext skills.SkillInstallContext,
	environments map[skills.SkillRuntime]skills.SkillCompatibilityEnvironment,
) *InstallSkillTool {
	return &InstallSkillTool{
		manager:        skills.NewScopedSkillManager(registryManager, environments),
		installContext: installContext,
	}
}

func (tool *InstallSkillTool) Name() string {
	return "install_skill"
}

func (tool *InstallSkillTool) Description() string {
	return "Install or dry-run one registry skill. " + installSkillUsageGuidance
}

func (tool *InstallSkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"slug": map[string]any{
				"type":        "string",
				"description": "Exact owner/repo[/path] registry slug; preserve any nested path.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"user", "workspace"},
				"description": "user=self/personal/shared/both runtimes; workspace=explicit gateway-only workspace.",
			},
			"version": map[string]any{
				"type": "string",
			},
			"registry": map[string]any{
				"type": "string",
			},
			"force": map[string]any{
				"type": "boolean",
			},
			"dry_run": map[string]any{
				"type": "boolean",
			},
		},
		"required": []string{"slug", "scope"},
	}
}

func (tool *InstallSkillTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	tool.mu.Lock()
	defer tool.mu.Unlock()

	slug, _ := args["slug"].(string)
	if strings.TrimSpace(slug) == "" {
		return ErrorResult("identifier is required and must be a non-empty string")
	}
	scopeValue, _ := args["scope"].(string)
	scope, err := skills.ParseSkillInstallScope(scopeValue, "")
	if err != nil {
		return ErrorResult(err.Error())
	}
	target, err := skills.ResolveSkillInstallTarget(scope, tool.installContext)
	if err != nil {
		return ErrorResult(err.Error())
	}
	registry, _ := args["registry"].(string)
	if registry == "" {
		registry = defaultSkillRegistryName
	}
	version, _ := args["version"].(string)
	force, _ := args["force"].(bool)
	dryRun, _ := args["dry_run"].(bool)

	plan, err := tool.manager.Install(ctx, skills.SkillInstallRequest{
		Target: target, Registry: registry, Slug: slug, Version: version, Replace: force, DryRun: dryRun,
	})
	if err != nil {
		return ErrorResult(err.Error() + ". " + installSkillUsageGuidance)
	}
	return SilentResult(renderInstallSkillPlan(plan))
}

func renderInstallSkillPlan(plan skills.SkillMutationPlan) string {
	state := "planned"
	if plan.Applied {
		state = "completed"
	}
	var output strings.Builder
	fmt.Fprintf(
		&output,
		"Skill %s %s.\nScope: %s\nAction: %s\nTarget: %s\nOrigin: %s:%s@%s\n",
		plan.Operation,
		state,
		plan.Scope,
		plan.Action,
		plan.Target,
		plan.Registry,
		plan.Slug,
		plan.ResolvedVersion,
	)
	switch plan.Scope {
	case skills.SkillInstallScopeUser:
		output.WriteString("Ownership: shared by the coding and gateway runtimes for this user.\n")
	case skills.SkillInstallScopeWorkspace:
		output.WriteString(
			"Ownership: gateway-only workspace; self/personal/shared/both requests require scope=user.\n",
		)
	}
	for _, compatibility := range plan.Compatibility {
		fmt.Fprintf(&output, "Compatibility (%s): %s\n", compatibility.Runtime, compatibility.Status)
	}
	if len(plan.DependencyGaps) > 0 {
		output.WriteString("Dependency gaps:\n")
		for _, gap := range plan.DependencyGaps {
			fmt.Fprintf(&output, "- %s %s: %s\n", gap.Kind, gap.Name, gap.State)
		}
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(&output, "Warning: %s\n", warning)
	}
	if plan.DryRun {
		output.WriteString("Dry run: no selected-scope files were changed.\n")
	}
	return output.String()
}
