package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	mintclawAgentManifestPath = "agents/mintclaw.yaml"
	openAIAgentManifestPath   = "agents/openai.yaml"
	mintclawManifestVersion   = 1
)

var requirementIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]*$`)

type rawSkillFrontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	Compatibility string         `yaml:"compatibility"`
	Metadata      map[string]any `yaml:"metadata"`
}

type legacyNanobotMetadata struct {
	OS       []string `json:"os"       yaml:"os"`
	Requires struct {
		Bins  []string `json:"bins"  yaml:"bins"`
		Tools []string `json:"tools" yaml:"tools"`
	} `json:"requires" yaml:"requires"`
}

type rawMintClawManifest struct {
	SchemaVersion int      `yaml:"schema_version"`
	Products      []string `yaml:"products"`
	Requirements  struct {
		OperatingSystems []string `yaml:"os"`
		Executables      []string `yaml:"executables"`
		Tools            []string `yaml:"tools"`
		MCPServers       []string `yaml:"mcp_servers"`
	} `yaml:"requirements"`
}

type rawOpenAIManifest struct {
	Interface    map[string]any `yaml:"interface"`
	Dependencies struct {
		Tools []struct {
			Type  string `yaml:"type"`
			Value string `yaml:"value"`
		} `yaml:"tools"`
	} `yaml:"dependencies"`
	Policy struct {
		AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
	} `yaml:"policy"`
}

type runtimeMetadataResult struct {
	Requirements       SkillRequirements
	RequirementSource  string
	Interoperability   SkillInteroperability
	CompatibilityError string
	Diagnostics        []CatalogDiagnostic
}

func parseSkillMetadataContent(skillPath, content string) (*SkillMetadata, error) {
	frontmatter, bodyContent := splitFrontmatter(content)
	dirName := filepath.Base(filepath.Dir(skillPath))
	title, bodyDescription := extractMarkdownMetadata(bodyContent)

	metadata := &SkillMetadata{
		Name:        dirName,
		Description: bodyDescription,
	}
	if title != "" && namePattern.MatchString(title) && len(title) <= MaxNameLength {
		metadata.Name = title
	}
	if frontmatter == "" {
		return metadata, nil
	}

	var parsed rawSkillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return nil, fmt.Errorf("parse YAML frontmatter: %w", err)
	}
	if parsed.Name != "" {
		metadata.Name = strings.TrimSpace(parsed.Name)
	}
	if parsed.Description != "" {
		metadata.Description = strings.TrimSpace(parsed.Description)
	}
	metadata.Compatibility = strings.TrimSpace(parsed.Compatibility)
	if len(metadata.Compatibility) > 500 {
		return nil, fmt.Errorf("compatibility exceeds 500 characters")
	}

	legacy, ok, err := parseLegacyNanobotMetadata(parsed.Metadata["nanobot"])
	if err != nil {
		return nil, fmt.Errorf("parse legacy metadata.nanobot: %w", err)
	}
	if ok {
		requirements := SkillRequirements{
			OperatingSystems: legacy.OS,
			Executables:      legacy.Requires.Bins,
			Tools:            legacy.Requires.Tools,
		}
		metadata.LegacyRequirements, err = normalizeSkillRequirements(requirements)
		if err != nil {
			return nil, fmt.Errorf("normalize legacy metadata.nanobot: %w", err)
		}
		metadata.LegacyRequirementSource = "legacy:nanobot"
	}
	return metadata, nil
}

func parseLegacyNanobotMetadata(value any) (legacyNanobotMetadata, bool, error) {
	if value == nil {
		return legacyNanobotMetadata{}, false, nil
	}
	var data []byte
	var err error
	switch typed := value.(type) {
	case string:
		data = []byte(typed)
		var parsed legacyNanobotMetadata
		if err = json.Unmarshal(data, &parsed); err != nil {
			return legacyNanobotMetadata{}, true, err
		}
		return parsed, true, nil
	default:
		data, err = yaml.Marshal(value)
		if err != nil {
			return legacyNanobotMetadata{}, true, err
		}
	}
	var parsed legacyNanobotMetadata
	if err = yaml.Unmarshal(data, &parsed); err != nil {
		return legacyNanobotMetadata{}, true, err
	}
	return parsed, true, nil
}

func readRuntimeMetadata(skillDirectory string, scope SkillScope) runtimeMetadataResult {
	result := runtimeMetadataResult{}
	manifest, manifestPath, found, err := readOptionalSkillMetadata(skillDirectory, mintclawAgentManifestPath)
	if err != nil {
		result.CompatibilityError = "MintClaw compatibility metadata is unreadable"
		result.Diagnostics = append(result.Diagnostics, CatalogDiagnostic{
			Kind: CatalogDiagnosticCompatibilityMalformed, Scope: scope, Path: manifestPath,
			Message: result.CompatibilityError,
		})
	} else if found {
		result.Requirements, err = parseMintClawManifest(manifest)
		if err != nil {
			result.CompatibilityError = "MintClaw compatibility metadata is malformed"
			result.Diagnostics = append(result.Diagnostics, CatalogDiagnostic{
				Kind: CatalogDiagnosticCompatibilityMalformed, Scope: scope, Path: manifestPath,
				Message: result.CompatibilityError,
			})
		} else {
			result.RequirementSource = "agents/mintclaw.yaml"
		}
	}

	openAI, openAIPath, openAIFound, openAIErr := readOptionalSkillMetadata(skillDirectory, openAIAgentManifestPath)
	if openAIErr != nil {
		result.Diagnostics = append(result.Diagnostics, CatalogDiagnostic{
			Kind: CatalogDiagnosticInteroperabilityMalformed, Scope: scope, Path: openAIPath,
			Message: "OpenAI interoperability metadata is unreadable",
		})
	} else if openAIFound {
		result.Interoperability, openAIErr = parseOpenAIManifest(openAI)
		if openAIErr != nil {
			result.Diagnostics = append(result.Diagnostics, CatalogDiagnostic{
				Kind: CatalogDiagnosticInteroperabilityMalformed, Scope: scope, Path: openAIPath,
				Message: "OpenAI interoperability metadata is malformed",
			})
		}
	}
	return result
}

func readOptionalSkillMetadata(skillDirectory, relativePath string) ([]byte, string, bool, error) {
	candidate := filepath.Join(skillDirectory, filepath.FromSlash(relativePath))
	if _, err := os.Lstat(candidate); err != nil {
		if os.IsNotExist(err) {
			return nil, candidate, false, nil
		}
		return nil, candidate, true, err
	}
	resolved, ok := resolveCatalogFile(candidate, skillDirectory)
	if !ok {
		return nil, candidate, true, fmt.Errorf("metadata file is outside the skill directory or is not regular")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, candidate, true, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, MaxMetadataBytes+1))
	if err != nil {
		return nil, candidate, true, err
	}
	if len(content) > MaxMetadataBytes {
		return nil, candidate, true, fmt.Errorf("metadata file exceeds %d bytes", MaxMetadataBytes)
	}
	return content, candidate, true, nil
}

func parseMintClawManifest(content []byte) (SkillRequirements, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var manifest rawMintClawManifest
	if err := decoder.Decode(&manifest); err != nil {
		return SkillRequirements{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return SkillRequirements{}, fmt.Errorf("multiple YAML documents are not allowed")
		}
		return SkillRequirements{}, fmt.Errorf("decode trailing YAML document: %w", err)
	}
	if manifest.SchemaVersion != mintclawManifestVersion {
		return SkillRequirements{}, fmt.Errorf("unsupported schema_version %d", manifest.SchemaVersion)
	}
	requirements := SkillRequirements{
		OperatingSystems: manifest.Requirements.OperatingSystems,
		Executables:      manifest.Requirements.Executables,
		Tools:            manifest.Requirements.Tools,
		MCPServers:       manifest.Requirements.MCPServers,
	}
	for _, product := range manifest.Products {
		switch SkillRuntime(strings.ToLower(strings.TrimSpace(product))) {
		case SkillRuntimeCoding:
			requirements.Products = append(requirements.Products, SkillRuntimeCoding)
		case SkillRuntimeGateway:
			requirements.Products = append(requirements.Products, SkillRuntimeGateway)
		default:
			return SkillRequirements{}, fmt.Errorf("unsupported product %q", product)
		}
	}
	return normalizeSkillRequirements(requirements)
}

func parseOpenAIManifest(content []byte) (SkillInteroperability, error) {
	var manifest rawOpenAIManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return SkillInteroperability{}, err
	}
	result := SkillInteroperability{
		OpenAIManifest:                true,
		OpenAIAllowImplicitInvocation: manifest.Policy.AllowImplicitInvocation,
		OpenAIHasInterface:            len(manifest.Interface) > 0,
	}
	for _, dependency := range manifest.Dependencies.Tools {
		typeName := strings.ToLower(strings.TrimSpace(dependency.Type))
		value := strings.TrimSpace(dependency.Value)
		if typeName == "" || value == "" || len(typeName) > 64 || len(value) > 256 {
			return SkillInteroperability{}, fmt.Errorf("dependency type and value must be bounded non-empty strings")
		}
		result.OpenAIDependencies = append(result.OpenAIDependencies, SkillInteroperabilityDependency{
			Type: typeName, Value: value,
		})
	}
	return result, nil
}

func normalizeSkillRequirements(requirements SkillRequirements) (SkillRequirements, error) {
	var err error
	requirements.OperatingSystems, err = normalizeRequirementNames(
		requirements.OperatingSystems,
		true,
	)
	if err != nil {
		return SkillRequirements{}, fmt.Errorf("os: %w", err)
	}
	requirements.Executables, err = normalizeRequirementNames(requirements.Executables, false)
	if err != nil {
		return SkillRequirements{}, fmt.Errorf("executables: %w", err)
	}
	requirements.Tools, err = normalizeRequirementNames(requirements.Tools, true)
	if err != nil {
		return SkillRequirements{}, fmt.Errorf("tools: %w", err)
	}
	requirements.MCPServers, err = normalizeRequirementNames(requirements.MCPServers, true)
	if err != nil {
		return SkillRequirements{}, fmt.Errorf("mcp_servers: %w", err)
	}
	requirements.Products = normalizeProducts(requirements.Products)
	return requirements, nil
}

func normalizeRequirementNames(names []string, lowercase bool) ([]string, error) {
	seen := make(map[string]struct{}, len(names))
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if lowercase {
			name = strings.ToLower(name)
		}
		if name == "" || len(name) > 128 || !requirementIdentifierPattern.MatchString(name) {
			return nil, fmt.Errorf("invalid requirement %q", name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeProducts(products []SkillRuntime) []SkillRuntime {
	seen := make(map[SkillRuntime]struct{}, len(products))
	normalized := make([]SkillRuntime, 0, len(products))
	for _, product := range products {
		if _, ok := seen[product]; ok {
			continue
		}
		seen[product] = struct{}{}
		normalized = append(normalized, product)
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left] < normalized[right] })
	return normalized
}
