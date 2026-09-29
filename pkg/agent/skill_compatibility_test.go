package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestSkillCompatibilityEnvironmentUsesLiveAdmissionReport(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.UpdatePlan.Enabled = true
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		nil,
	)
	require.NoError(t, err)
	environment := newSkillCompatibilityEnvironment(
		cfg,
		skills.SkillRuntimeGateway,
		nil,
		composer.CapabilityReport,
	)

	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("update_plan"))
	require.NoError(t, composer.PutTool("test.update-plan", tools.NewUpdatePlanTool(), false))
	assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("update_plan"))
}

func TestSkillCompatibilityEnvironmentPreservesCapabilityReason(t *testing.T) {
	report := runtimecap.NewReport(
		runtimecap.KindCoding,
		runtimecap.DependencyUnavailable(
			runtimecap.CapabilityBrowserObserve,
			runtimecap.CapabilityBrowserClient,
		),
	)
	environment := newSkillCompatibilityEnvironment(
		config.DefaultConfig(),
		skills.SkillRuntimeCoding,
		nil,
		func() runtimecap.Report { return report },
	)

	state := environment.CapabilityState("browser.observe")
	assert.Equal(t, skills.SkillRequirementMissing, state.State)
	assert.Equal(t, runtimecap.ReasonDependencyMissing, state.Reason)
	assert.Equal(t, runtimecap.CapabilityBrowserClient, state.Dependency)
}

func TestSkillCompatibilityEnvironmentUsesAdmittedPolicyReason(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		func(name string) bool { return name != "update_plan" },
		newRuntimeToolSetContributor(
			"test.plan",
			runtimeToolCandidate{tool: tools.NewUpdatePlanTool()},
		),
	)
	require.NoError(t, err)
	environment := newSkillCompatibilityEnvironment(
		config.DefaultConfig(),
		skills.SkillRuntimeGateway,
		nil,
		composer.CapabilityReport,
	)

	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.ToolState("update_plan"))
}

func TestCapabilityBackedSkillFollowsFinalToolAdmission(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		func(name string) bool { return name != "document" },
		newRuntimeToolSetContributor(
			"document.feature",
			runtimeToolCandidate{tool: &runtimeComposerTestTool{name: "document", value: "document"}},
		).withCapability(runtimecap.CapabilityDocumentInspect, "document"),
	)
	require.NoError(t, err)
	environment := newSkillCompatibilityEnvironment(
		config.DefaultConfig(),
		skills.SkillRuntimeCoding,
		nil,
		composer.CapabilityReport,
	)
	state := environment.CapabilityState("document.inspect")
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, state.State)
	assert.Equal(t, runtimecap.ReasonPolicyDisabled, state.Reason)

	root := filepath.Join(t.TempDir(), "skills")
	directory := filepath.Join(root, "document-reader")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "agents"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(directory, "SKILL.md"),
		[]byte("---\nname: document-reader\ndescription: document reader\n---\n\n# Document reader\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(directory, "agents", "mintclaw.yaml"),
		[]byte("schema_version: 1\nproducts: [coding]\nrequirements:\n  capabilities: [document.inspect]\n"),
		0o644,
	))
	loader := skills.NewSkillsLoader([]skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeUser, Runtime: skills.SkillRuntimeCoding,
	}}).WithCompatibilityEnvironment(environment)
	report := loader.Compatibility(skills.SkillRuntimeCoding)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, skills.SkillCompatibilityPolicyDisabled, report.Skills[0].Status)
	assert.Empty(t, loader.ListCompatibleSkills(skills.SkillRuntimeCoding))
	require.Len(t, report.Skills[0].Checks, 2)
	assert.Equal(t, runtimecap.ReasonPolicyDisabled, report.Skills[0].Checks[1].Reason)
}

func TestConfiguredSkillCompatibilityEnvironmentKeepsCodingSurfaceIsolated(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}

	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)

	assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("exec"))
	if documentToolAvailable() {
		assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState("document"))
		assert.Equal(
			t,
			skills.SkillRequirementAvailable,
			environment.CapabilityState("document.inspect").State,
		)
		for _, capability := range []string{"document.fields", "document.fill", "document.verify"} {
			assert.Equal(
				t,
				skills.SkillRequirementAvailable,
				environment.CapabilityState(capability).State,
				capability,
			)
		}
	} else {
		assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("document"))
	}
	assert.Equal(
		t,
		skills.SkillRequirementIncompatible,
		environment.CapabilityState("document.form").State,
	)
	for _, name := range []string{
		"browser_act",
		"browser_capture",
		"browser_contexts",
		"browser_diagnostics",
		"browser_execute",
		"browser_observe",
		"browser_session",
		"browser_targets",
	} {
		assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState(name), name)
	}
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.MCPServerState("github"))

	disabled := *cfg
	disabled.Coding = cfg.Coding
	disabled.Coding.Capabilities.Document = false
	disabled.Coding.Capabilities.Browser = false
	disabledEnvironment := ConfiguredSkillCompatibilityEnvironment(&disabled, skills.SkillRuntimeCoding)
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, disabledEnvironment.ToolState("document"))
	assert.Equal(
		t,
		skills.SkillRequirementPolicyDisabled,
		disabledEnvironment.CapabilityState("document.inspect").State,
	)
	for _, capability := range []string{"document.fields", "document.fill", "document.verify"} {
		assert.Equal(
			t,
			skills.SkillRequirementPolicyDisabled,
			disabledEnvironment.CapabilityState(capability).State,
			capability,
		)
	}
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, disabledEnvironment.ToolState("browser_targets"))
	assert.Equal(
		t,
		skills.SkillRequirementPolicyDisabled,
		disabledEnvironment.CapabilityState("browser.observe").State,
	)
}

func TestConfiguredCodingSkillCompatibilityProjectsOnlyGrantedBrowserOperations(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Coding.Remote.Enabled = true
	cfg.Coding.Remote.Grant = "local-development"
	cfg.Execution.CodingRemoteGrants = make(map[string]config.CodingRemoteClientGrant)
	cfg.Execution.CodingRemoteCapabilities = make(map[string]config.CodingRemoteCapability)
	cfg.Execution.CodingRemoteGrants["local-development"] = config.CodingRemoteClientGrant{
		Revision: "grant-v1",
		Agent:    "main",
		Capabilities: []string{
			"browser-personal",
		},
	}
	cfg.Execution.CodingRemoteCapabilities["browser-personal"] = config.CodingRemoteCapability{
		Revision:       "browser-v1",
		Kind:           config.CodingRemoteCapabilityBrowser,
		Target:         "companion",
		BrowserProfile: "personal",
		Operations: []string{
			"browser_open",
			"browser_status",
			"browser_close",
			"browser_observe",
			"browser_act",
		},
	}

	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)

	for _, name := range []string{"browser_targets", "browser_session", "browser_observe", "browser_act"} {
		assert.Equal(t, skills.SkillRequirementAvailable, environment.ToolState(name), name)
	}
	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("browser_capture"))
	assert.Equal(t, skills.SkillRequirementAvailable, environment.CapabilityState("browser.observe").State)
	assert.Equal(t, skills.SkillRequirementAvailable, environment.CapabilityState("browser.act").State)
	assert.Equal(t, skills.SkillRequirementAvailable, environment.CapabilityState("browser.workflow").State)
	assert.Equal(t, skills.SkillRequirementMissing, environment.CapabilityState("browser.capture").State)
	assert.Equal(t, skills.SkillRequirementMissing, environment.CapabilityState("browser.download").State)

	cfg.Execution.CodingRemoteCapabilities["browser-personal"] = config.CodingRemoteCapability{
		Revision:       "browser-v2",
		Kind:           config.CodingRemoteCapabilityBrowser,
		Target:         "companion",
		BrowserProfile: "personal",
		Operations: []string{
			"browser_close",
			"browser_context_list",
			"browser_observe",
			"browser_act",
		},
	}
	partial := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)
	assert.Equal(t, skills.SkillRequirementAvailable, partial.ToolState("browser_targets"))
	assert.Equal(t, skills.SkillRequirementMissing, partial.ToolState("browser_session"))
	assert.Equal(t, skills.SkillRequirementMissing, partial.ToolState("browser_contexts"))
	assert.Equal(t, skills.SkillRequirementAvailable, partial.ToolState("browser_observe"))
	assert.Equal(t, skills.SkillRequirementAvailable, partial.ToolState("browser_act"))
	assert.Equal(t, skills.SkillRequirementMissing, partial.CapabilityState("browser.workflow").State)

	cfg.Execution.CodingRemoteCapabilities["browser-personal"] = config.CodingRemoteCapability{
		Revision:       "browser-v3",
		Kind:           config.CodingRemoteCapabilityBrowser,
		Target:         "companion",
		BrowserProfile: "personal",
		Operations: []string{
			"browser_open",
			"browser_status",
			"browser_close",
		},
	}
	cfg.Execution.CodingRemoteCapabilities["browser-actions"] = config.CodingRemoteCapability{
		Revision:       "browser-actions-v1",
		Kind:           config.CodingRemoteCapabilityBrowser,
		Target:         "companion",
		BrowserProfile: "work",
		Operations: []string{
			"browser_observe",
			"browser_act",
		},
	}
	grant := cfg.Execution.CodingRemoteGrants["local-development"]
	grant.Capabilities = []string{"browser-personal", "browser-actions"}
	cfg.Execution.CodingRemoteGrants["local-development"] = grant

	split := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeCoding)
	assert.Equal(t, skills.SkillRequirementAvailable, split.ToolState("browser_targets"))
	assert.Equal(t, skills.SkillRequirementAvailable, split.ToolState("browser_session"))
	assert.Equal(t, skills.SkillRequirementAvailable, split.ToolState("browser_observe"))
	assert.Equal(t, skills.SkillRequirementAvailable, split.ToolState("browser_act"))
	assert.Equal(t, skills.SkillRequirementMissing, split.CapabilityState("browser.workflow").State)
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
	assert.Equal(t, skills.SkillRequirementAvailable, environment.CapabilityState("browser.observe").State)
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.CapabilityState("browser.act").State)
}

func TestConfiguredSkillCompatibilityReportsUnknownDependenciesBeforeDefaultDenyPolicy(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}
	cfg.Agents.List[0].ToolPolicy = &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultDeny,
	}
	cfg.Agents.List[0].MCPServerPolicy = &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultDeny,
	}

	environment := ConfiguredSkillCompatibilityEnvironment(cfg, skills.SkillRuntimeGateway)

	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("exec_typo"))
	assert.Equal(t, skills.SkillRequirementMissing, environment.ToolState("browser_typo"))
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.ToolState("exec"))
	assert.Equal(t, skills.SkillRequirementMissing, environment.MCPServerState("github_typo"))
	assert.Equal(t, skills.SkillRequirementPolicyDisabled, environment.MCPServerState("github"))
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
