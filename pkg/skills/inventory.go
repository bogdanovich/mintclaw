package skills

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ManagedSkillOriginLocal      = "local"
	ManagedSkillOriginThirdParty = OriginKindThirdParty
	ManagedSkillOriginMalformed  = "malformed"

	defaultManagedSkillMaxEntries = 1024
	defaultManagedSkillMaxFiles   = 512
	defaultManagedSkillMaxBytes   = 16 * 1024 * 1024
	defaultManagedSkillMaxPath    = 1024

	managedSkillRevisionModeMask = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
)

var (
	ErrManagedSkillNotFound = errors.New("managed skill not found")
	ErrInvalidManagedSkill  = errors.New("invalid managed skill")
)

type ManagedSkillLimits struct {
	MaxEntries int
	MaxFiles   int
	MaxBytes   int64
	MaxPath    int
}

func DefaultManagedSkillLimits() ManagedSkillLimits {
	return ManagedSkillLimits{
		MaxEntries: defaultManagedSkillMaxEntries,
		MaxFiles:   defaultManagedSkillMaxFiles,
		MaxBytes:   defaultManagedSkillMaxBytes,
		MaxPath:    defaultManagedSkillMaxPath,
	}
}

type ManagedSkill struct {
	Name          string
	Description   string
	RelativePath  string
	Revision      string
	OriginKind    string
	Origin        *OriginMetadata
	FileCount     int
	TotalBytes    int64
	Valid         bool
	ValidationErr string
}

type WorkspaceSkillInventory struct {
	workspaceRoot string
	skillsRoot    string
	limits        ManagedSkillLimits
}

func NewWorkspaceSkillInventory(workspace string) *WorkspaceSkillInventory {
	return NewWorkspaceSkillInventoryWithLimits(workspace, DefaultManagedSkillLimits())
}

func NewWorkspaceSkillInventoryWithLimits(
	workspace string,
	limits ManagedSkillLimits,
) *WorkspaceSkillInventory {
	workspaceRoot, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		workspaceRoot = filepath.Clean(workspace)
	}
	return &WorkspaceSkillInventory{
		workspaceRoot: workspaceRoot,
		skillsRoot:    filepath.Join(workspaceRoot, "skills"),
		limits:        normalizeManagedSkillLimits(limits),
	}
}

// ValidateRoot rejects a skill root that is not a real directory. A missing
// root is valid because installers may create it after this preflight.
func (inventory *WorkspaceSkillInventory) ValidateRoot() error {
	if inventory == nil {
		return errors.New("workspace skill inventory is required")
	}
	if err := validateRealDirectoryAncestors(inventory.workspaceRoot); err != nil {
		return fmt.Errorf("inspect workspace root: %w", err)
	}
	workspaceInfo, err := os.Lstat(inventory.workspaceRoot)
	if err != nil {
		return fmt.Errorf("inspect workspace root: %w", err)
	}
	if workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return errors.New("workspace root must be a real directory")
	}
	rootInfo, err := os.Lstat(inventory.skillsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect workspace skills root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("workspace skills root must be a real directory")
	}
	return nil
}

// List returns every canonically named entry under the workspace skill root.
// Invalid entries remain visible with Valid=false instead of disappearing.
func (inventory *WorkspaceSkillInventory) List() ([]ManagedSkill, error) {
	if err := inventory.ValidateRoot(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(inventory.skillsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []ManagedSkill{}, nil
		}
		return nil, fmt.Errorf("list workspace skills: %w", err)
	}

	result := make([]ManagedSkill, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if err := ValidateSkillName(name); err != nil {
			result = append(result, invalidManagedSkill(name, "invalid skill directory name: "+err.Error()))
			continue
		}
		managed, inspectErr := inventory.Inspect(name)
		if inspectErr != nil {
			return nil, inspectErr
		}
		result = append(result, managed)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result, nil
}

// Inspect validates and fingerprints one workspace skill without following
// links. Structural or metadata validation failures are returned in the view;
// invalid requests, absent skills, and filesystem I/O failures return errors.
func (inventory *WorkspaceSkillInventory) Inspect(name string) (ManagedSkill, error) {
	if inventory == nil {
		return ManagedSkill{}, errors.New("workspace skill inventory is required")
	}
	if err := ValidateSkillName(name); err != nil {
		return ManagedSkill{}, fmt.Errorf("invalid managed skill name %q: %w", name, err)
	}
	if err := inventory.ValidateRoot(); err != nil {
		return ManagedSkill{}, err
	}

	managed := ManagedSkill{
		Name:         name,
		RelativePath: filepath.ToSlash(filepath.Join("skills", name)),
		OriginKind:   ManagedSkillOriginLocal,
	}
	targetDir := filepath.Join(inventory.skillsRoot, name)
	targetInfo, err := os.Lstat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ManagedSkill{}, fmt.Errorf("%w: %s", ErrManagedSkillNotFound, name)
		}
		return ManagedSkill{}, fmt.Errorf("inspect skill directory: %w", err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
		managed.ValidationErr = "skill path must be a real directory"
		return managed, nil
	}

	revision, fileCount, totalBytes, scanErr := inventory.scanTree(targetDir)
	managed.FileCount = fileCount
	managed.TotalBytes = totalBytes
	if scanErr != nil {
		return managedSkillFromScan(managed, scanErr)
	}
	managed.Revision = revision

	metadata := (&SkillsLoader{}).getSkillMetadata(filepath.Join(targetDir, "SKILL.md"))
	if metadata == nil {
		managed.ValidationErr = "skill metadata is unavailable"
		return managed, nil
	}
	managed.Description = metadata.Description
	if metadata.Name != name {
		managed.ValidationErr = fmt.Sprintf(
			"skill metadata name %q does not match directory %q",
			metadata.Name,
			name,
		)
		return managed, nil
	}
	if err := (SkillInfo{Name: metadata.Name, Description: metadata.Description}).validate(); err != nil {
		managed.ValidationErr = fmt.Sprintf("invalid skill metadata: %v", err)
		return managed, nil
	}

	origin, originErr := ReadSkillOrigin(targetDir)
	switch {
	case originErr == nil:
		managed.OriginKind = ManagedSkillOriginThirdParty
		managed.Origin = &origin
	case errors.Is(originErr, ErrOriginMetadataNotFound):
		managed.OriginKind = ManagedSkillOriginLocal
	case errors.Is(originErr, ErrInvalidOriginMetadata):
		managed.OriginKind = ManagedSkillOriginMalformed
		managed.ValidationErr = originErr.Error()
		return managed, nil
	default:
		return ManagedSkill{}, fmt.Errorf("inspect skill origin for %q: %w", name, originErr)
	}

	confirmedRevision, confirmedFiles, confirmedBytes, confirmErr := inventory.scanTree(targetDir)
	if confirmErr != nil {
		return managedSkillFromScan(managed, confirmErr)
	}
	if confirmedRevision != managed.Revision || confirmedFiles != managed.FileCount ||
		confirmedBytes != managed.TotalBytes {
		managed.ValidationErr = invalidManagedSkillError("skill tree changed during inventory").Error()
		return managed, nil
	}

	managed.Valid = true
	return managed, nil
}

type managedTreeEntry struct {
	relative  string
	info      fs.FileInfo
	directory bool
}

func (inventory *WorkspaceSkillInventory) scanTree(root string) (string, int, int64, error) {
	entries := make([]managedTreeEntry, 0)
	fileCount := 0
	var totalBytes int64

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if len(entries) >= inventory.limits.MaxEntries {
			return invalidManagedSkillError("skill tree exceeds %d entries", inventory.limits.MaxEntries)
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve skill-relative path: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if !validManagedRelativePath(relative, inventory.limits.MaxPath) {
			return invalidManagedSkillError("invalid skill-relative path %q", relative)
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect skill entry %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return invalidManagedSkillError("skill entry %q is a symlink", relative)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return invalidManagedSkillError("skill entry %q is not a regular file or directory", relative)
		}

		item := managedTreeEntry{
			relative:  relative,
			info:      info,
			directory: info.IsDir(),
		}
		entries = append(entries, item)
		if item.directory {
			return nil
		}

		fileCount++
		if fileCount > inventory.limits.MaxFiles {
			return invalidManagedSkillError("skill tree exceeds %d files", inventory.limits.MaxFiles)
		}
		if item.info.Size() < 0 || item.info.Size() > inventory.limits.MaxBytes-totalBytes {
			return invalidManagedSkillError("skill tree exceeds %d bytes", inventory.limits.MaxBytes)
		}
		totalBytes += item.info.Size()
		return nil
	})
	if err != nil {
		return "", fileCount, totalBytes, err
	}

	sort.Slice(entries, func(left, right int) bool {
		return entries[left].relative < entries[right].relative
	})

	hash := sha256.New()
	_, _ = hash.Write([]byte("mintclaw-managed-skill-tree-v1\x00"))
	for _, entry := range entries {
		kind := byte('f')
		if entry.directory {
			kind = 'd'
		}
		_, _ = hash.Write([]byte{kind})
		writeManagedHashString(hash, entry.relative)
		var mode [4]byte
		binary.BigEndian.PutUint32(mode[:], uint32(entry.info.Mode()&managedSkillRevisionModeMask))
		_, _ = hash.Write(mode[:])
		if entry.directory {
			continue
		}

		file, err := os.Open(filepath.Join(root, filepath.FromSlash(entry.relative)))
		if err != nil {
			return "", fileCount, totalBytes, fmt.Errorf("open skill entry %q: %w", entry.relative, err)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return "", fileCount, totalBytes, fmt.Errorf("inspect opened skill entry %q: %w", entry.relative, statErr)
		}
		if !openedInfo.Mode().IsRegular() || !os.SameFile(entry.info, openedInfo) {
			_ = file.Close()
			return "", fileCount, totalBytes, invalidManagedSkillError(
				"skill entry %q changed identity during inventory",
				entry.relative,
			)
		}
		content, readErr := io.ReadAll(io.LimitReader(file, entry.info.Size()+1))
		closeErr := file.Close()
		if readErr != nil {
			return "", fileCount, totalBytes, fmt.Errorf("read skill entry %q: %w", entry.relative, readErr)
		}
		if closeErr != nil {
			return "", fileCount, totalBytes, fmt.Errorf("close skill entry %q: %w", entry.relative, closeErr)
		}
		if int64(len(content)) != entry.info.Size() {
			return "", fileCount, totalBytes, invalidManagedSkillError(
				"skill entry %q changed during inventory",
				entry.relative,
			)
		}
		writeManagedHashBytes(hash, content)
	}

	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), fileCount, totalBytes, nil
}

func normalizeManagedSkillLimits(limits ManagedSkillLimits) ManagedSkillLimits {
	defaults := DefaultManagedSkillLimits()
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxPath <= 0 {
		limits.MaxPath = defaults.MaxPath
	}
	return limits
}

func validManagedRelativePath(path string, maxLength int) bool {
	return path != "" && path != "." && len(path) <= maxLength && utf8.ValidString(path) &&
		!strings.HasPrefix(path, "/") && !strings.Contains(path, "\\") &&
		!strings.ContainsAny(path, "\x00\r\n") && path != ".." && !strings.HasPrefix(path, "../")
}

func invalidManagedSkill(name, validationErr string) ManagedSkill {
	return ManagedSkill{
		Name:          name,
		RelativePath:  filepath.ToSlash(filepath.Join("skills", name)),
		OriginKind:    ManagedSkillOriginLocal,
		ValidationErr: validationErr,
	}
}

func managedSkillFromScan(managed ManagedSkill, scanErr error) (ManagedSkill, error) {
	if errors.Is(scanErr, ErrInvalidManagedSkill) {
		managed.ValidationErr = scanErr.Error()
		return managed, nil
	}
	return ManagedSkill{}, fmt.Errorf("scan managed skill %q: %w", managed.Name, scanErr)
}

func invalidManagedSkillError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManagedSkill, fmt.Sprintf(format, args...))
}

func validateRealDirectoryAncestors(path string) error {
	absoluteParent, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	volume := filepath.VolumeName(absoluteParent)
	current := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(absoluteParent, volume)
	remainder = strings.TrimLeft(remainder, string(filepath.Separator))

	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace path contains a symlink component")
		}
		if !info.IsDir() {
			return errors.New("workspace path contains a non-directory component")
		}
	}
	return nil
}

type managedHashWriter interface {
	Write([]byte) (int, error)
}

func writeManagedHashString(hash managedHashWriter, value string) {
	writeManagedHashBytes(hash, []byte(value))
}

func writeManagedHashBytes(hash managedHashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write(value)
}
