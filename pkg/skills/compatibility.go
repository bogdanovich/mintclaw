package skills

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

type SkillCompatibilityStatus string

const (
	SkillCompatibilityReady               SkillCompatibilityStatus = "ready"
	SkillCompatibilityMissingDependency   SkillCompatibilityStatus = "missing_dependency"
	SkillCompatibilityPolicyDisabled      SkillCompatibilityStatus = "policy_disabled"
	SkillCompatibilityRuntimeIncompatible SkillCompatibilityStatus = "runtime_incompatible"
	SkillCompatibilityMalformed           SkillCompatibilityStatus = "malformed"
	SkillCompatibilityShadowed            SkillCompatibilityStatus = "shadowed"
)

type SkillRequirementKind string

const (
	SkillRequirementOS         SkillRequirementKind = "os"
	SkillRequirementExecutable SkillRequirementKind = "executable"
	SkillRequirementTool       SkillRequirementKind = "tool"
	SkillRequirementMCPServer  SkillRequirementKind = "mcp_server"
	SkillRequirementProduct    SkillRequirementKind = "product"
)

type SkillRequirementState string

const (
	SkillRequirementAvailable      SkillRequirementState = "available"
	SkillRequirementMissing        SkillRequirementState = "missing"
	SkillRequirementPolicyDisabled SkillRequirementState = "policy_disabled"
	SkillRequirementIncompatible   SkillRequirementState = "incompatible"
)

type SkillRequirements struct {
	OperatingSystems []string       `json:"operating_systems,omitempty" yaml:"os,omitempty"`
	Executables      []string       `json:"executables,omitempty"       yaml:"executables,omitempty"`
	Tools            []string       `json:"tools,omitempty"             yaml:"tools,omitempty"`
	MCPServers       []string       `json:"mcp_servers,omitempty"       yaml:"mcp_servers,omitempty"`
	Products         []SkillRuntime `json:"products,omitempty"          yaml:"-"`
}

func (requirements SkillRequirements) Empty() bool {
	return len(requirements.OperatingSystems) == 0 && len(requirements.Executables) == 0 &&
		len(requirements.Tools) == 0 && len(requirements.MCPServers) == 0 && len(requirements.Products) == 0
}

type SkillInteroperabilityDependency struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SkillInteroperability is inspectable product-specific metadata. It is never
// used to grant tools, select a runtime, or change MintClaw policy.
type SkillInteroperability struct {
	OpenAIManifest                bool                              `json:"openai_manifest,omitempty"`
	OpenAIDependencies            []SkillInteroperabilityDependency `json:"openai_dependencies,omitempty"`
	OpenAIAllowImplicitInvocation *bool                             `json:"openai_allow_implicit_invocation,omitempty"`
	OpenAIHasInterface            bool                              `json:"openai_has_interface,omitempty"`
}

type SkillRequirementCheck struct {
	Kind  SkillRequirementKind  `json:"kind"`
	Name  string                `json:"name"`
	State SkillRequirementState `json:"state"`
}

type SkillCompatibility struct {
	Name              string                   `json:"name"`
	Path              string                   `json:"path"`
	Scope             SkillScope               `json:"scope"`
	DeclaredRuntime   SkillRuntime             `json:"declared_runtime"`
	Runtime           SkillRuntime             `json:"runtime"`
	Status            SkillCompatibilityStatus `json:"status"`
	RequirementSource string                   `json:"requirement_source,omitempty"`
	Requirements      SkillRequirements        `json:"requirements,omitempty"`
	Checks            []SkillRequirementCheck  `json:"checks,omitempty"`
	WinnerPath        string                   `json:"winner_path,omitempty"`
	Message           string                   `json:"message,omitempty"`
	Interoperability  SkillInteroperability    `json:"interoperability,omitempty"`
}

type SkillCompatibilityReport struct {
	Runtime     SkillRuntime         `json:"runtime"`
	Skills      []SkillCompatibility `json:"skills"`
	Diagnostics []CatalogDiagnostic  `json:"diagnostics,omitempty"`
}

// SkillCompatibilityEnvironment describes current read-only runtime facts.
// Resolvers must inspect state only: compatibility checks never install a
// binary, connect to an MCP server, or mutate policy.
type SkillCompatibilityEnvironment struct {
	Runtime             SkillRuntime
	OperatingSystem     string
	ExecutableAvailable func(string) bool
	ToolState           func(string) SkillRequirementState
	MCPServerState      func(string) SkillRequirementState
}

func NewSkillCompatibilityEnvironment(runtimeProduct SkillRuntime) SkillCompatibilityEnvironment {
	return SkillCompatibilityEnvironment{
		Runtime:         runtimeProduct,
		OperatingSystem: runtime.GOOS,
		ExecutableAvailable: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
	}
}

func (sl *SkillsLoader) WithCompatibilityEnvironment(environment SkillCompatibilityEnvironment) *SkillsLoader {
	if sl == nil {
		return sl
	}
	copy := environment
	if strings.TrimSpace(copy.OperatingSystem) == "" {
		copy.OperatingSystem = runtime.GOOS
	}
	if copy.ExecutableAvailable == nil {
		copy.ExecutableAvailable = NewSkillCompatibilityEnvironment(copy.Runtime).ExecutableAvailable
	}
	sl.compatibility = &copy
	return sl
}

func (sl *SkillsLoader) Compatibility(runtimeProduct SkillRuntime) SkillCompatibilityReport {
	runtimeProduct = sl.compatibilityRuntime(runtimeProduct)
	catalog := sl.Discover()
	report := SkillCompatibilityReport{
		Runtime:     runtimeProduct,
		Skills:      make([]SkillCompatibility, 0, len(catalog.Skills)),
		Diagnostics: append([]CatalogDiagnostic(nil), catalog.Diagnostics...),
	}
	for _, info := range catalog.Skills {
		report.Skills = append(report.Skills, sl.evaluateCompatibility(info, runtimeProduct))
	}
	for _, diagnostic := range catalog.Diagnostics {
		var status SkillCompatibilityStatus
		switch diagnostic.Kind {
		case CatalogDiagnosticShadowed:
			status = SkillCompatibilityShadowed
		case CatalogDiagnosticInvalidSkill, CatalogDiagnosticCompatibilityMalformed:
			status = SkillCompatibilityMalformed
		default:
			continue
		}
		if compatibilityReportContainsPath(report.Skills, diagnostic.Path) ||
			diagnostic.Kind == CatalogDiagnosticCompatibilityMalformed &&
				compatibilityReportContainsName(report.Skills, diagnostic.Name, diagnostic.Scope) {
			continue
		}
		report.Skills = append(report.Skills, SkillCompatibility{
			Name:       diagnostic.Name,
			Path:       diagnostic.Path,
			Scope:      diagnostic.Scope,
			Runtime:    runtimeProduct,
			Status:     status,
			WinnerPath: diagnostic.WinnerPath,
			Message:    diagnostic.Message,
		})
	}
	return report
}

func compatibilityReportContainsName(items []SkillCompatibility, name string, scope SkillScope) bool {
	for _, item := range items {
		if strings.EqualFold(item.Name, name) && item.Scope == scope {
			return true
		}
	}
	return false
}

func compatibilityReportContainsPath(items []SkillCompatibility, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, item := range items {
		if item.Path == path {
			return true
		}
	}
	return false
}

func (sl *SkillsLoader) evaluateCompatibility(info SkillInfo, runtimeProduct SkillRuntime) SkillCompatibility {
	runtimeProduct = sl.compatibilityRuntime(runtimeProduct)
	result := SkillCompatibility{
		Name:              info.Name,
		Path:              info.Path,
		Scope:             info.Scope,
		DeclaredRuntime:   info.Runtime,
		Runtime:           runtimeProduct,
		Status:            SkillCompatibilityReady,
		RequirementSource: info.RequirementSource,
		Requirements:      info.Requirements,
		Interoperability:  info.Interoperability,
	}
	if info.compatibilityError != "" {
		result.Status = SkillCompatibilityMalformed
		result.Message = info.compatibilityError
		return result
	}
	if !skillRuntimeCompatible(info.Runtime, runtimeProduct) {
		result.Status = SkillCompatibilityRuntimeIncompatible
		result.Message = "skill is not available to this runtime product"
		return result
	}
	if sl == nil || sl.compatibility == nil {
		return result
	}

	environment := *sl.compatibility
	if runtimeProduct != "" {
		environment.Runtime = runtimeProduct
	}
	if len(info.Requirements.Products) > 0 {
		state := SkillRequirementIncompatible
		if slices.Contains(info.Requirements.Products, environment.Runtime) {
			state = SkillRequirementAvailable
		}
		result.Checks = append(result.Checks, SkillRequirementCheck{
			Kind: SkillRequirementProduct, Name: string(environment.Runtime), State: state,
		})
		if state != SkillRequirementAvailable {
			result.Status = SkillCompatibilityRuntimeIncompatible
		}
	}
	if len(info.Requirements.OperatingSystems) > 0 {
		state := SkillRequirementIncompatible
		if slices.Contains(info.Requirements.OperatingSystems, strings.ToLower(environment.OperatingSystem)) {
			state = SkillRequirementAvailable
		}
		result.Checks = append(result.Checks, SkillRequirementCheck{
			Kind: SkillRequirementOS, Name: environment.OperatingSystem, State: state,
		})
		if state != SkillRequirementAvailable {
			result.Status = SkillCompatibilityRuntimeIncompatible
		}
	}
	for _, name := range info.Requirements.Executables {
		state := SkillRequirementMissing
		if environment.ExecutableAvailable != nil && environment.ExecutableAvailable(name) {
			state = SkillRequirementAvailable
		}
		result.Checks = append(result.Checks, SkillRequirementCheck{
			Kind: SkillRequirementExecutable, Name: name, State: state,
		})
		result.Status = combineCompatibilityStatus(result.Status, state)
	}
	for _, name := range info.Requirements.Tools {
		state := requirementState(environment.ToolState, name)
		result.Checks = append(result.Checks, SkillRequirementCheck{
			Kind: SkillRequirementTool, Name: name, State: state,
		})
		result.Status = combineCompatibilityStatus(result.Status, state)
	}
	for _, name := range info.Requirements.MCPServers {
		state := requirementState(environment.MCPServerState, name)
		result.Checks = append(result.Checks, SkillRequirementCheck{
			Kind: SkillRequirementMCPServer, Name: name, State: state,
		})
		result.Status = combineCompatibilityStatus(result.Status, state)
	}
	return result
}

func requirementState(
	resolver func(string) SkillRequirementState,
	name string,
) SkillRequirementState {
	if resolver == nil {
		return SkillRequirementMissing
	}
	switch state := resolver(name); state {
	case SkillRequirementAvailable, SkillRequirementPolicyDisabled:
		return state
	default:
		return SkillRequirementMissing
	}
}

func combineCompatibilityStatus(
	current SkillCompatibilityStatus,
	state SkillRequirementState,
) SkillCompatibilityStatus {
	if current == SkillCompatibilityRuntimeIncompatible || current == SkillCompatibilityMalformed {
		return current
	}
	switch state {
	case SkillRequirementPolicyDisabled:
		if current != SkillCompatibilityMissingDependency {
			return SkillCompatibilityPolicyDisabled
		}
	case SkillRequirementMissing:
		return SkillCompatibilityMissingDependency
	}
	return current
}

func (sl *SkillsLoader) compatibleCatalog(runtimeProduct SkillRuntime) SkillCatalog {
	runtimeProduct = sl.compatibilityRuntime(runtimeProduct)
	catalog := sl.Discover()
	if sl == nil || sl.compatibility == nil {
		return catalog
	}
	compatible := make([]SkillInfo, 0, len(catalog.Skills))
	for _, info := range catalog.Skills {
		result := sl.evaluateCompatibility(info, runtimeProduct)
		if result.Status == SkillCompatibilityReady {
			compatible = append(compatible, info)
			continue
		}
		catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticIncompatible,
			Scope:   info.Scope,
			Name:    info.Name,
			Path:    info.Path,
			Message: string(result.Status),
		})
	}
	catalog.Skills = compatible
	return catalog
}

func (sl *SkillsLoader) compatibilityRuntime(runtimeProduct SkillRuntime) SkillRuntime {
	if runtimeProduct == "" && sl != nil && sl.compatibility != nil {
		return sl.compatibility.Runtime
	}
	return runtimeProduct
}

func (sl *SkillsLoader) CompatibleCatalog(runtimeProduct SkillRuntime) SkillCatalog {
	return sl.compatibleCatalog(runtimeProduct)
}

func (sl *SkillsLoader) ListCompatibleSkills(runtimeProduct SkillRuntime) []SkillInfo {
	return sl.compatibleCatalog(runtimeProduct).Skills
}

func (sl *SkillsLoader) ensureCompatible(info SkillInfo, runtimeProduct SkillRuntime) error {
	if sl == nil || sl.compatibility == nil {
		return nil
	}
	result := sl.evaluateCompatibility(info, runtimeProduct)
	if result.Status == SkillCompatibilityReady {
		return nil
	}
	return &SkillSelectionError{
		Kind:     SkillSelectionIncompatible,
		Selector: SkillSelector{Name: info.Name, Path: info.Path},
	}
}
