package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestSkillCompatibilityEnvironmentUsesLiveToolRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.UpdatePlan.Enabled = true
	registry := tools.NewToolRegistry()
	environment := newSkillCompatibilityEnvironment(
		cfg,
		skills.SkillRuntimeGateway,
		nil,
		nil,
		registry,
	)

	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("update_plan"))
	registry.Register(tools.NewUpdatePlanTool())
	assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("update_plan"))
}

func TestConfiguredSkillCompatibilityEnvironmentKeepsCodingSurfaceIsolated(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}

	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)

	assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("exec"))
	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("document"))
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.MCPServerState("github"))
}

func TestConfiguredGatewaySkillCompatibilityHonorsAgentPolicyAndBrowserGrant(t *testing.T) {
	cfg := config.DefaultConfig()
	disabledEnvironment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeGateway)
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, disabledEnvironment.ToolState("browser_targets"))

	cfg.Tools.Browser.Enabled = true
	cfg.Tools.Browser.Agents = []string{"main"}
	cfg.Agents.List[0].ToolPolicy = &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultAllow,
		Deny:    []string{"browser_act"},
	}

	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeGateway)

	assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("browser_targets"))
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.ToolState("browser_act"))
}

func TestContextBuilderPublishesOnlySkillsCompatibleWithItsRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	for name, product := range map[string]string{"gateway-skill": "gateway", "coding-skill": "coding"} {
		directory := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Join(directory, "agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(
			"---\nname: "+name+"\ndescription: fixture\n---\n\n# Fixture\n",
		), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "agents", "mintclaw.yaml"), []byte(
			"schema_version: 1\nproducts: ["+product+"]\nrequirements: {}\n",
		), 0o644))
	}
	builder := newContextBuilderWithMemoryStoreAndSkills(t.TempDir(), NewMemoryStore(t.TempDir()), []skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeUser, Runtime: skills.SkillRuntimeShared,
	}})
	builder.WithSkillCompatibilityEnvironment(skills.SkillCompatibilityEnvironment{
		Runtime:         skills.SkillRuntimeGateway,
		OperatingSystem: "linux",
	})

	assert.Equal(t, []string{"gateway-skill"}, builder.ListSkillNames())
	info := builder.GetSkillsInfo()
	assert.Equal(t, 2, info["total"])
	assert.Equal(t, 1, info["available"])
	assert.Equal(t, []string{"gateway-skill"}, info["names"])
}
