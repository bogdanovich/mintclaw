package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeMetadataCanonicalManifestWinsAndOpenAIMetadataIsNonAuthoritative(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	directory := createCompatibilitySkill(t, root, "portable", `
schema_version: 1
products: [gateway]
requirements:
  executables: [gh]
  tools: [exec]
`)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(`---
name: portable
description: portable workflow
metadata: {"nanobot":{"requires":{"bins":["legacy-bin"],"tools":["legacy_tool"]}}}
---

# Portable
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "agents", "openai.yaml"), []byte(`
interface:
  display_name: Portable
dependencies:
  tools:
    - type: mcp
      value: dangerous-server
policy:
  allow_implicit_invocation: false
`), 0o644))

	catalog := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}}).Discover()

	require.Len(t, catalog.Skills, 1)
	info := catalog.Skills[0]
	assert.Equal(t, "agents/mintclaw.yaml", info.RequirementSource)
	assert.Equal(t, []string{"gh"}, info.Requirements.Executables)
	assert.Equal(t, []string{"exec"}, info.Requirements.Tools)
	assert.Empty(t, info.Requirements.MCPServers)
	assert.Equal(t, []SkillRuntime{SkillRuntimeGateway}, info.Requirements.Products)
	assert.True(t, info.Interoperability.OpenAIManifest)
	assert.True(t, info.Interoperability.OpenAIHasInterface)
	require.NotNil(t, info.Interoperability.OpenAIAllowImplicitInvocation)
	assert.False(t, *info.Interoperability.OpenAIAllowImplicitInvocation)
	assert.Equal(t, []SkillInteroperabilityDependency{{Type: "mcp", Value: "dangerous-server"}},
		info.Interoperability.OpenAIDependencies)
}

func TestLegacyNanobotRequirementsAdaptOnlyWhenCanonicalManifestIsAbsent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	directory := filepath.Join(root, "legacy")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(`---
name: legacy
description: legacy workflow
metadata:
  nanobot:
    os: [linux]
    requires:
      bins: [curl]
      tools: [exec]
---

# Legacy
`), 0o644))

	catalog := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}}).Discover()

	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "legacy:nanobot", catalog.Skills[0].RequirementSource)
	assert.Equal(t, []string{"linux"}, catalog.Skills[0].Requirements.OperatingSystems)
	assert.Equal(t, []string{"curl"}, catalog.Skills[0].Requirements.Executables)
	assert.Equal(t, []string{"exec"}, catalog.Skills[0].Requirements.Tools)
}

func TestMintClawManifestRejectsTrailingYAMLDocument(t *testing.T) {
	_, err := parseMintClawManifest([]byte("schema_version: 1\nrequirements: {}\n---\nignored: true\n"))
	assert.Error(t, err)
}

func TestCompatibilityReportClassifiesAndFiltersWithoutLeakingInstructions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	createCompatibilitySkill(t, root, "ready", `
schema_version: 1
products: [coding]
requirements:
  executables: [present-bin]
  tools: [read_file]
`)
	createCompatibilitySkill(t, root, "missing", `
schema_version: 1
requirements:
  executables: [missing-bin]
`)
	createCompatibilitySkill(t, root, "disabled", `
schema_version: 1
requirements:
  tools: [exec]
`)
	createCompatibilitySkill(t, root, "gateway-only", `
schema_version: 1
products: [gateway]
requirements: {}
`)
	malformed := createCompatibilitySkill(t, root, "malformed", `
schema_version: broken
requirements: {}
`)
	require.NoError(t, os.WriteFile(filepath.Join(malformed, "secret.txt"), []byte("must-not-leak"), 0o600))
	brokenDirectory := filepath.Join(root, "broken-frontmatter")
	require.NoError(t, os.MkdirAll(brokenDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(brokenDirectory, "SKILL.md"), []byte(
		"---\nname: [broken\ndescription: nope\n---\n\nsecret instructions\n",
	), 0o644))

	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}}).WithCompatibilityEnvironment(
		SkillCompatibilityEnvironment{
			Runtime:         SkillRuntimeCoding,
			OperatingSystem: "linux",
			ExecutableAvailable: func(name string) bool {
				return name == "present-bin"
			},
			ToolState: func(name string) SkillRequirementState {
				if name == "read_file" {
					return SkillRequirementAvailable
				}
				return SkillRequirementPolicyDisabled
			},
		},
	)

	report := loader.Compatibility(SkillRuntimeCoding)
	require.Len(t, report.Skills, 6)
	statuses := compatibilityStatuses(report)
	assert.Equal(t, SkillCompatibilityReady, statuses["ready"])
	assert.Equal(t, SkillCompatibilityMissingDependency, statuses["missing"])
	assert.Equal(t, SkillCompatibilityPolicyDisabled, statuses["disabled"])
	assert.Equal(t, SkillCompatibilityRuntimeIncompatible, statuses["gateway-only"])
	assert.Equal(t, SkillCompatibilityMalformed, statuses["malformed"])
	assert.Equal(t, SkillCompatibilityMalformed, statuses["broken-frontmatter"])
	assert.Equal(t, []string{"ready"}, skillNames(loader.ListCompatibleSkills(SkillRuntimeCoding)))

	rendered := loader.RenderCatalog(CatalogRenderOptions{Runtime: SkillRuntimeCoding})
	assert.Contains(t, rendered.Text, "<name>ready</name>")
	assert.NotContains(t, rendered.Text, "<name>missing</name>")
	assert.NotContains(t, rendered.Text, "<name>gateway-only</name>")

	_, err := loader.Select(
		[]SkillSelector{{Name: "missing"}},
		SkillSelectionOptions{Runtime: SkillRuntimeCoding},
	)
	var selectionError *SkillSelectionError
	require.ErrorAs(t, err, &selectionError)
	assert.Equal(t, SkillSelectionIncompatible, selectionError.Kind)

	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "must-not-leak")
	assert.NotContains(t, string(encoded), "# malformed")
}

func TestBundledSkillManifestsHaveExpectedGatewayAndCodingCompatibility(t *testing.T) {
	root, err := filepath.Abs("bundled")
	require.NoError(t, err)
	baseEnvironment := SkillCompatibilityEnvironment{
		OperatingSystem:     "linux",
		ExecutableAvailable: func(string) bool { return true },
		ToolState:           func(string) SkillRequirementState { return SkillRequirementAvailable },
		MCPServerState:      func(string) SkillRequirementState { return SkillRequirementAvailable },
	}
	loader := NewSkillsLoader([]SkillRoot{
		{Path: root, Scope: SkillScopeSystem, Runtime: SkillRuntimeShared, Trust: SkillTrustSystem},
	})

	gatewayEnvironment := baseEnvironment
	gatewayEnvironment.Runtime = SkillRuntimeGateway
	loader.WithCompatibilityEnvironment(gatewayEnvironment)
	gateway := compatibilityStatuses(loader.Compatibility(SkillRuntimeGateway))
	for _, name := range []string{"agent-browser", "github", "hardware", "pdf", "tmux", "weather"} {
		assert.Equal(t, SkillCompatibilityReady, gateway[name], name)
	}

	codingEnvironment := baseEnvironment
	codingEnvironment.Runtime = SkillRuntimeCoding
	loader.WithCompatibilityEnvironment(codingEnvironment)
	coding := compatibilityStatuses(loader.Compatibility(SkillRuntimeCoding))
	for _, name := range []string{"github", "tmux", "weather"} {
		assert.Equal(t, SkillCompatibilityReady, coding[name], name)
	}
	for _, name := range []string{"agent-browser", "hardware", "pdf"} {
		assert.Equal(t, SkillCompatibilityRuntimeIncompatible, coding[name], name)
	}
}

func TestCompatibilityReportClassifiesMCPDependencyAndShadowedSkill(t *testing.T) {
	base := t.TempDir()
	userRoot := filepath.Join(base, "user")
	systemRoot := filepath.Join(base, "system")
	createCompatibilitySkill(t, userRoot, "github-mcp", `
schema_version: 1
requirements:
  mcp_servers: [github]
`)
	createCompatibilitySkill(t, systemRoot, "github-mcp", `
schema_version: 1
requirements: {}
`)
	loader := NewSkillsLoader([]SkillRoot{
		{Path: userRoot, Scope: SkillScopeUser, Runtime: SkillRuntimeShared},
		{Path: systemRoot, Scope: SkillScopeSystem, Runtime: SkillRuntimeShared},
	}).WithCompatibilityEnvironment(SkillCompatibilityEnvironment{
		Runtime:         SkillRuntimeGateway,
		OperatingSystem: "linux",
		MCPServerState:  func(string) SkillRequirementState { return SkillRequirementMissing },
	})

	report := loader.Compatibility(SkillRuntimeGateway)
	require.Len(t, report.Skills, 2)
	assert.Equal(t, SkillCompatibilityMissingDependency, report.Skills[0].Status)
	assert.Equal(t, SkillScopeUser, report.Skills[0].Scope)
	assert.Equal(t, SkillCompatibilityShadowed, report.Skills[1].Status)
	assert.Equal(t, SkillScopeSystem, report.Skills[1].Scope)

	loader.compatibility.MCPServerState = func(string) SkillRequirementState {
		return SkillRequirementPolicyDisabled
	}
	assert.Equal(t, SkillCompatibilityPolicyDisabled, loader.Compatibility(SkillRuntimeGateway).Skills[0].Status)
}

func TestCompatibilityReportDoesNotLetPolicyDisabledMaskMissingDependency(t *testing.T) {
	root := t.TempDir()
	createCompatibilitySkill(t, root, "missing-before-policy", `
schema_version: 1
requirements:
  executables: [missing-bin]
  tools: [denied-tool]
`)
	createCompatibilitySkill(t, root, "policy-before-missing", `
schema_version: 1
requirements:
  tools: [denied-tool]
  mcp_servers: [missing-server]
`)
	loader := NewSkillsLoader([]SkillRoot{{Path: root, Scope: SkillScopeUser}}).WithCompatibilityEnvironment(
		SkillCompatibilityEnvironment{
			Runtime:             SkillRuntimeGateway,
			OperatingSystem:     "linux",
			ExecutableAvailable: func(string) bool { return false },
			ToolState:           func(string) SkillRequirementState { return SkillRequirementPolicyDisabled },
			MCPServerState:      func(string) SkillRequirementState { return SkillRequirementMissing },
		},
	)

	statuses := compatibilityStatuses(loader.Compatibility(SkillRuntimeGateway))

	assert.Equal(t, SkillCompatibilityMissingDependency, statuses["missing-before-policy"])
	assert.Equal(t, SkillCompatibilityMissingDependency, statuses["policy-before-missing"])
}

func createCompatibilitySkill(t *testing.T, root, name, manifest string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(
		"---\nname: "+name+"\ndescription: "+name+" workflow\n---\n\n# "+name+"\n\nsecret instructions\n",
	), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "agents", "mintclaw.yaml"), []byte(manifest), 0o644))
	return directory
}

func compatibilityStatuses(report SkillCompatibilityReport) map[string]SkillCompatibilityStatus {
	result := make(map[string]SkillCompatibilityStatus, len(report.Skills))
	for _, skill := range report.Skills {
		result[skill.Name] = skill.Status
	}
	return result
}

func skillNames(infos []SkillInfo) []string {
	result := make([]string, 0, len(infos))
	for _, info := range infos {
		result = append(result, info.Name)
	}
	return result
}
