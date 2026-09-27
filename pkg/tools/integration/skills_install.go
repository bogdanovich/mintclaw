package integrationtools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/skills"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const defaultSkillRegistryName = "github"

const installSkillUsageGuidance = "Use the exact owner/repo[/path] slug from the request or search result; " +
	"never shorten a nested path. Scope user means self/personal/shared/both runtimes; " +
	"workspace is only for an explicitly requested gateway-only workspace."

type skillInstallOwnershipIntent uint8

const (
	skillInstallOwnershipUnspecified skillInstallOwnershipIntent = iota
	skillInstallOwnershipUser
	skillInstallOwnershipWorkspace
	skillInstallOwnershipConflicting
)

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
	userMessage := toolshared.ToolUserMessage(ctx)
	if toolshared.ToolApprovalContinuation(ctx) && strings.TrimSpace(userMessage) == "" {
		return ErrorResult(
			"originating user request is unavailable for this approved install; refuse mutation and ask the user to retry",
		)
	}
	if intentErr := validateInstallSkillOwnershipIntent(userMessage, scope); intentErr != nil {
		return ErrorResult(intentErr.Error())
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

func validateInstallSkillOwnershipIntent(message string, scope skills.SkillInstallScope) error {
	switch detectInstallSkillOwnershipIntent(message) {
	case skillInstallOwnershipUser:
		if scope != skills.SkillInstallScopeUser {
			return fmt.Errorf(
				"scope %s conflicts with the current self/personal/shared request; retry with scope=user",
				scope,
			)
		}
	case skillInstallOwnershipWorkspace:
		if scope != skills.SkillInstallScopeWorkspace {
			return fmt.Errorf(
				"scope %s conflicts with the current explicit gateway-workspace request; retry with scope=workspace",
				scope,
			)
		}
	case skillInstallOwnershipConflicting:
		return fmt.Errorf(
			"current request contains conflicting skill ownership intent; clarify user or workspace scope",
		)
	}
	return nil
}

func detectInstallSkillOwnershipIntent(message string) skillInstallOwnershipIntent {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return skillInstallOwnershipUnspecified
	}
	userIntent := containsAny(normalized,
		"for yourself", "for myself", "personal skill", "personal install", "user scope", "scope=user",
		"make it personal", "make this personal", "make it shared", "make this shared",
		"both agents", "both runtimes", "available to both", "shared skill", "shared between", "shared by both",
		"себе", "для себя", "личный скилл", "личного скилла", "обоим агент", "обоих агент",
		"обоим рантайм", "обоих рантайм", "общий скилл", "общего скилла", "сделай общ", "сделай личн",
		"сделай его общ", "сделай его личн", "сделай этот общ", "сделай этот личн",
	)
	workspaceIntent := containsAny(normalized,
		"gateway workspace", "gateway-only", "workspace-only", "workspace scope", "scope=workspace",
		"воркспейс гейтвея", "воркспейс gateway", "рабочую область gateway", "рабочей области gateway",
	)
	switch {
	case userIntent && workspaceIntent:
		return skillInstallOwnershipConflicting
	case userIntent:
		return skillInstallOwnershipUser
	case workspaceIntent:
		return skillInstallOwnershipWorkspace
	default:
		return skillInstallOwnershipUnspecified
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
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
