package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SkillScope string

const (
	SkillScopeWorkspace  SkillScope = "workspace"
	SkillScopeRepository SkillScope = "repository"
	SkillScopeUser       SkillScope = "user"
	SkillScopeGlobal     SkillScope = "global"
	SkillScopeBuiltin    SkillScope = "builtin"
	SkillScopeSystem     SkillScope = "system"
)

type SkillRuntime string

const (
	SkillRuntimeShared  SkillRuntime = "shared"
	SkillRuntimeCoding  SkillRuntime = "coding"
	SkillRuntimeGateway SkillRuntime = "gateway"
)

type SkillTrust string

const (
	SkillTrustProject SkillTrust = "project"
	SkillTrustUser    SkillTrust = "user"
	SkillTrustSystem  SkillTrust = "system"
)

// SkillRoot is one ordered discovery root. Earlier roots shadow later roots.
// Boundary defaults to Path and identifies the owning trust boundary. The
// resolved root must remain inside it; skill directories and SKILL.md files
// are then confined to the resolved root itself.
type SkillRoot struct {
	Path     string       `json:"path"`
	Scope    SkillScope   `json:"scope"`
	Runtime  SkillRuntime `json:"runtime"`
	Trust    SkillTrust   `json:"trust"`
	Priority int          `json:"priority"`
	Boundary string       `json:"boundary,omitempty"`
}

type CatalogDiagnosticKind string

const (
	CatalogDiagnosticRootUnreadable     CatalogDiagnosticKind = "root_unreadable"
	CatalogDiagnosticPathEscape         CatalogDiagnosticKind = "path_escape"
	CatalogDiagnosticMetadataUnreadable CatalogDiagnosticKind = "metadata_unreadable"
	CatalogDiagnosticMetadataTruncated  CatalogDiagnosticKind = "metadata_truncated"
	CatalogDiagnosticInvalidSkill       CatalogDiagnosticKind = "invalid_skill"
	CatalogDiagnosticShadowed           CatalogDiagnosticKind = "shadowed"
	CatalogDiagnosticCatalogOmitted     CatalogDiagnosticKind = "catalog_omitted"
)

type CatalogDiagnostic struct {
	Kind       CatalogDiagnosticKind `json:"kind"`
	Scope      SkillScope            `json:"scope,omitempty"`
	Name       string                `json:"name,omitempty"`
	Path       string                `json:"path,omitempty"`
	WinnerPath string                `json:"winner_path,omitempty"`
	Message    string                `json:"message"`
}

type SkillCatalog struct {
	Skills      []SkillInfo         `json:"skills"`
	Diagnostics []CatalogDiagnostic `json:"diagnostics,omitempty"`
}

func WorkspaceSkillRoot(workspace string) SkillRoot {
	if strings.TrimSpace(workspace) == "" {
		return SkillRoot{}
	}
	return SkillRoot{
		Path:     filepath.Join(workspace, "skills"),
		Scope:    SkillScopeWorkspace,
		Runtime:  SkillRuntimeGateway,
		Trust:    SkillTrustProject,
		Boundary: workspace,
	}
}

func StandardUserSkillRoot(home string) SkillRoot {
	if strings.TrimSpace(home) == "" {
		return SkillRoot{}
	}
	return SkillRoot{
		Path:    filepath.Join(home, ".agents", "skills"),
		Scope:   SkillScopeUser,
		Runtime: SkillRuntimeShared,
		Trust:   SkillTrustUser,
	}
}

func LegacyGlobalSkillRoot(mintclawHome string) SkillRoot {
	if strings.TrimSpace(mintclawHome) == "" {
		return SkillRoot{}
	}
	return SkillRoot{
		Path:    filepath.Join(mintclawHome, "skills"),
		Scope:   SkillScopeGlobal,
		Runtime: SkillRuntimeShared,
		Trust:   SkillTrustUser,
	}
}

func BuiltinSkillRoot(path string) SkillRoot {
	if strings.TrimSpace(path) == "" {
		return SkillRoot{}
	}
	return SkillRoot{
		Path:    path,
		Scope:   SkillScopeBuiltin,
		Runtime: SkillRuntimeShared,
		Trust:   SkillTrustSystem,
	}
}

func GatewaySkillRoots(workspace, mintclawHome, userHome, builtin string) []SkillRoot {
	return normalizeSkillRoots([]SkillRoot{
		WorkspaceSkillRoot(workspace),
		StandardUserSkillRoot(userHome),
		LegacyGlobalSkillRoot(mintclawHome),
		BuiltinSkillRoot(builtin),
	})
}

func CodingSkillRoots(projectRoot, workingDirectory, mintclawHome, userHome, builtin string) ([]SkillRoot, error) {
	projectRoot, err := canonicalProspectivePath(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve coding skill project root: %w", err)
	}
	workingDirectory, err = canonicalProspectivePath(workingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve coding skill working directory: %w", err)
	}
	if !pathWithin(workingDirectory, projectRoot) {
		return nil, fmt.Errorf("coding skill working directory must be inside project root")
	}

	roots := make([]SkillRoot, 0, 8)
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		rootPath := filepath.Join(directory, ".agents", "skills")
		roots = append(roots, SkillRoot{
			Path:     rootPath,
			Scope:    SkillScopeRepository,
			Runtime:  SkillRuntimeCoding,
			Trust:    SkillTrustProject,
			Boundary: projectRoot,
		})
		if directory == projectRoot {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory || !pathWithin(parent, projectRoot) {
			return nil, fmt.Errorf("coding skill directory traversal escaped project root")
		}
	}
	roots = append(
		roots,
		StandardUserSkillRoot(userHome),
		LegacyGlobalSkillRoot(mintclawHome),
		BuiltinSkillRoot(builtin),
	)
	return normalizeSkillRoots(roots), nil
}

func normalizeSkillRoots(roots []SkillRoot) []SkillRoot {
	out := make([]SkillRoot, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root.Path = strings.TrimSpace(root.Path)
		if root.Path == "" || root.Path == "." {
			continue
		}
		root.Path = filepath.Clean(root.Path)
		if root.Boundary == "" {
			root.Boundary = root.Path
		} else {
			root.Boundary = filepath.Clean(strings.TrimSpace(root.Boundary))
		}
		key := filepath.Clean(root.Path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if root.Runtime == "" {
			root.Runtime = SkillRuntimeShared
		}
		root.Priority = len(out)
		out = append(out, root)
	}
	return out
}

func (sl *SkillsLoader) Discover() SkillCatalog {
	catalog := SkillCatalog{Skills: make([]SkillInfo, 0)}
	winners := make(map[string]SkillInfo)
	for _, root := range sl.roots {
		sl.discoverRoot(root, &catalog, winners)
	}
	return catalog
}

func (sl *SkillsLoader) discoverRoot(root SkillRoot, catalog *SkillCatalog, winners map[string]SkillInfo) {
	resolvedRoot, err := canonicalExistingPath(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticRootUnreadable,
			Scope:   root.Scope,
			Path:    root.Path,
			Message: "skill root could not be read",
		})
		return
	}

	ownerBoundary, err := canonicalExistingPath(root.Boundary)
	if err != nil {
		catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticRootUnreadable,
			Scope:   root.Scope,
			Path:    root.Path,
			Message: "skill root boundary could not be resolved",
		})
		return
	}
	if !pathWithin(resolvedRoot, ownerBoundary) {
		catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticPathEscape,
			Scope:   root.Scope,
			Path:    root.Path,
			Message: "skill root resolves outside its owning trust boundary",
		})
		return
	}

	entries, err := os.ReadDir(resolvedRoot)
	if err != nil {
		catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
			Kind:    CatalogDiagnosticRootUnreadable,
			Scope:   root.Scope,
			Path:    root.Path,
			Message: "skill root could not be read",
		})
		return
	}

	for _, entry := range entries {
		displayDirectory := filepath.Join(root.Path, entry.Name())
		skillDirectory := filepath.Join(resolvedRoot, entry.Name())
		resolvedDirectory, ok := resolveCatalogDirectory(skillDirectory, resolvedRoot)
		if !ok {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
					Kind:    CatalogDiagnosticPathEscape,
					Scope:   root.Scope,
					Path:    displayDirectory,
					Message: "skill directory resolves outside its discovery root or is not a readable directory",
				})
			}
			continue
		}

		skillCandidate := filepath.Join(resolvedDirectory, "SKILL.md")
		skillFile, ok := resolveCatalogFile(skillCandidate, resolvedRoot)
		if !ok {
			if _, statErr := os.Lstat(skillCandidate); statErr == nil {
				catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
					Kind:    CatalogDiagnosticPathEscape,
					Scope:   root.Scope,
					Path:    skillCandidate,
					Message: "SKILL.md resolves outside its discovery root or is not a regular file",
				})
			} else if !os.IsNotExist(statErr) {
				catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
					Kind:    CatalogDiagnosticMetadataUnreadable,
					Scope:   root.Scope,
					Path:    skillCandidate,
					Message: "SKILL.md could not be inspected",
				})
			}
			continue
		}
		metadata, truncated, metadataErr := sl.readSkillMetadata(skillFile)
		if metadataErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
				Kind:    CatalogDiagnosticMetadataUnreadable,
				Scope:   root.Scope,
				Path:    skillFile,
				Message: "skill metadata could not be read",
			})
			continue
		}
		if truncated {
			catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
				Kind:    CatalogDiagnosticMetadataTruncated,
				Scope:   root.Scope,
				Name:    metadata.Name,
				Path:    skillFile,
				Message: fmt.Sprintf("skill metadata scan was limited to %d bytes", MaxMetadataBytes),
			})
		}
		info := SkillInfo{
			Name:        metadata.Name,
			Path:        skillFile,
			Source:      string(root.Scope),
			Scope:       root.Scope,
			Runtime:     root.Runtime,
			Trust:       root.Trust,
			Priority:    root.Priority,
			Description: metadata.Description,
		}
		if validateErr := info.validate(); validateErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
				Kind:    CatalogDiagnosticInvalidSkill,
				Scope:   root.Scope,
				Name:    info.Name,
				Path:    skillFile,
				Message: validateErr.Error(),
			})
			continue
		}

		key := strings.ToLower(info.Name)
		if winner, exists := winners[key]; exists {
			catalog.Diagnostics = append(catalog.Diagnostics, CatalogDiagnostic{
				Kind:       CatalogDiagnosticShadowed,
				Scope:      root.Scope,
				Name:       info.Name,
				Path:       info.Path,
				WinnerPath: winner.Path,
				Message:    "skill is shadowed by a higher-priority root",
			})
			continue
		}
		winners[key] = info
		catalog.Skills = append(catalog.Skills, info)
	}
}

func resolveCatalogDirectory(path, boundary string) (string, bool) {
	resolved, err := canonicalExistingPath(path)
	if err != nil || !pathWithin(resolved, boundary) {
		return "", false
	}
	info, err := os.Stat(resolved)
	return resolved, err == nil && info.IsDir()
}

func resolveCatalogFile(path, boundary string) (string, bool) {
	resolved, err := canonicalExistingPath(path)
	if err != nil || !pathWithin(resolved, boundary) {
		return "", false
	}
	info, err := os.Stat(resolved)
	return resolved, err == nil && info.Mode().IsRegular()
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// canonicalProspectivePath resolves every existing ancestor while preserving
// a possibly nonexistent suffix. Coding layout construction is intentionally
// side-effect free, so catalog setup must not require or create the repository.
func canonicalProspectivePath(path string) (string, error) {
	absolute, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", resolveErr
			}
			relative, relativeErr := filepath.Rel(current, absolute)
			if relativeErr != nil {
				return "", relativeErr
			}
			return filepath.Clean(filepath.Join(resolved, relative)), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		if filepath.Dir(current) == current {
			return "", fmt.Errorf("path has no resolvable ancestor: %q", absolute)
		}
	}
}

func absoluteCleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}
