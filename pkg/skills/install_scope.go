package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SkillInstallScope string

const (
	SkillInstallScopeUser       SkillInstallScope = "user"
	SkillInstallScopeRepository SkillInstallScope = "repository"
	SkillInstallScopeWorkspace  SkillInstallScope = "workspace"
	SkillInstallScopeSystem     SkillInstallScope = "system"
)

type SkillInstallContext struct {
	UserHome       string
	RepositoryRoot string
	Workspace      string
}

type SkillInstallTarget struct {
	Scope     SkillInstallScope `json:"scope"`
	Root      string            `json:"root"`
	OwnerRoot string            `json:"owner_root"`
	Boundary  string            `json:"-"`
}

func ParseSkillInstallScope(value string, defaultScope SkillInstallScope) (SkillInstallScope, error) {
	scope := SkillInstallScope(strings.ToLower(strings.TrimSpace(value)))
	if scope == "" {
		scope = defaultScope
	}
	switch scope {
	case SkillInstallScopeUser, SkillInstallScopeRepository, SkillInstallScopeWorkspace:
		return scope, nil
	case SkillInstallScopeSystem:
		return "", errors.New("system skills are immutable and cannot be an installation target")
	default:
		return "", fmt.Errorf("skill install scope must be user, repository, or workspace")
	}
}

func ResolveSkillInstallTarget(
	scope SkillInstallScope,
	installContext SkillInstallContext,
) (SkillInstallTarget, error) {
	parsed, err := ParseSkillInstallScope(string(scope), "")
	if err != nil {
		return SkillInstallTarget{}, err
	}

	var boundary string
	switch parsed {
	case SkillInstallScopeUser:
		boundary = installContext.UserHome
	case SkillInstallScopeRepository:
		boundary = installContext.RepositoryRoot
	case SkillInstallScopeWorkspace:
		boundary = installContext.Workspace
	}
	if strings.TrimSpace(boundary) == "" {
		return SkillInstallTarget{}, fmt.Errorf("%s skill install scope is unavailable in this runtime", parsed)
	}

	boundary, err = canonicalInstallBoundary(boundary)
	if err != nil {
		return SkillInstallTarget{}, fmt.Errorf("resolve %s skill install boundary: %w", parsed, err)
	}
	ownerRoot := boundary
	if parsed == SkillInstallScopeUser || parsed == SkillInstallScopeRepository {
		ownerRoot = filepath.Join(boundary, ".agents")
	}
	target := SkillInstallTarget{
		Scope:     parsed,
		Root:      filepath.Join(ownerRoot, "skills"),
		OwnerRoot: ownerRoot,
		Boundary:  boundary,
	}
	if err := target.Validate(); err != nil {
		return SkillInstallTarget{}, err
	}
	return target, nil
}

func (target SkillInstallTarget) Validate() error {
	if _, err := ParseSkillInstallScope(string(target.Scope), ""); err != nil {
		return err
	}
	boundary, err := canonicalInstallBoundary(target.Boundary)
	if err != nil {
		return fmt.Errorf("validate %s skill install boundary: %w", target.Scope, err)
	}
	ownerRoot, err := filepath.Abs(filepath.Clean(target.OwnerRoot))
	if err != nil {
		return fmt.Errorf("resolve skill owner root: %w", err)
	}
	root, err := filepath.Abs(filepath.Clean(target.Root))
	if err != nil {
		return fmt.Errorf("resolve skill install root: %w", err)
	}
	if !pathWithin(ownerRoot, boundary) || !pathWithin(root, ownerRoot) || root == ownerRoot {
		return errors.New("skill install target escapes its selected scope")
	}
	if err := validateProspectiveInstallPath(boundary, ownerRoot); err != nil {
		return fmt.Errorf("validate skill owner root: %w", err)
	}
	if err := validateProspectiveInstallPath(boundary, root); err != nil {
		return fmt.Errorf("validate skill install root: %w", err)
	}
	return nil
}

func (target SkillInstallTarget) RuntimeProducts() []SkillRuntime {
	switch target.Scope {
	case SkillInstallScopeRepository:
		return []SkillRuntime{SkillRuntimeCoding}
	case SkillInstallScopeWorkspace:
		return []SkillRuntime{SkillRuntimeGateway}
	case SkillInstallScopeUser:
		return []SkillRuntime{SkillRuntimeCoding, SkillRuntimeGateway}
	default:
		return nil
	}
}

func EnsureSkillInstallRoot(target SkillInstallTarget) error {
	if err := target.Validate(); err != nil {
		return err
	}
	for _, directory := range []string{target.OwnerRoot, target.Root} {
		if err := createPhysicalDirectoryWithin(target.Boundary, directory); err != nil {
			return err
		}
	}
	return target.Validate()
}

func canonicalInstallBoundary(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("install boundary is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	if validationErr := validateRealDirectoryAncestors(absolute); validationErr != nil {
		return "", validationErr
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("install boundary must be a real directory")
	}
	return absolute, nil
}

func validateProspectiveInstallPath(boundary, target string) error {
	relative, err := filepath.Rel(boundary, target)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return errors.New("install path escapes its boundary")
	}
	if relative == "." {
		return nil
	}
	current := boundary
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%q must be a real directory", current)
		}
	}
	return nil
}

func createPhysicalDirectoryWithin(boundary, target string) error {
	relative, err := filepath.Rel(boundary, target)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return errors.New("skill install directory escapes its boundary")
	}
	if relative == "." {
		return nil
	}
	current := boundary
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case os.IsNotExist(statErr):
			if mkdirErr := os.Mkdir(current, 0o755); mkdirErr != nil && !os.IsExist(mkdirErr) {
				return fmt.Errorf("create skill install directory %q: %w", current, mkdirErr)
			}
			info, statErr = os.Lstat(current)
		case statErr != nil:
			return fmt.Errorf("inspect skill install directory %q: %w", current, statErr)
		}
		if statErr != nil {
			return fmt.Errorf("inspect created skill install directory %q: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("skill install directory %q must be a real directory", current)
		}
	}
	return nil
}
