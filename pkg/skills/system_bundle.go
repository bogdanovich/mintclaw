package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	systemBundleSchemaVersion = 2
	systemBundleSourceRoot    = "bundled"
	systemBundleManifestName  = ".manifest.json"
	systemBundleActiveName    = "active.json"
	skillProvenanceName       = "MINTCLAW_PROVENANCE.json"
	skillProvenanceVersion    = 1
)

var ErrSystemBundleUnavailable = errors.New("system skill bundle is unavailable")

var ensuredSystemBundles sync.Map

//go:embed all:bundled
var embeddedSystemSkills embed.FS

type SystemBundle struct {
	Fingerprint string
	Root        string
}

type systemBundleFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	data   []byte
}

type systemBundleManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Fingerprint   string             `json:"fingerprint"`
	Files         []systemBundleFile `json:"files"`
}

type systemBundleActive struct {
	SchemaVersion int    `json:"schema_version"`
	Fingerprint   string `json:"fingerprint"`
	Generation    string `json:"generation"`
}

type skillProvenance struct {
	SchemaVersion    int      `json:"schema_version"`
	SourceRepository string   `json:"source_repository"`
	SourceRevision   string   `json:"source_revision"`
	SourcePath       string   `json:"source_path"`
	License          string   `json:"license"`
	Decision         string   `json:"decision"`
	Adaptations      []string `json:"adaptations,omitempty"`
}

type systemBundleWriter func(path string, data []byte, mode os.FileMode) error

// EnsureSystemBundle materializes the skills shipped with this binary under a
// fingerprinted, release-owned directory. An active marker is replaced only
// after the complete generation has been written and verified.
func EnsureSystemBundle(mintclawHome string) (SystemBundle, error) {
	bundle, err := ensureSystemBundleFromFS(
		mintclawHome,
		embeddedSystemSkills,
		systemBundleSourceRoot,
		writeSystemBundleFile,
	)
	if err != nil {
		return SystemBundle{}, err
	}
	ensuredSystemBundles.Store(systemBundleCacheKey(mintclawHome), bundle)
	return bundle, nil
}

// RuntimeSystemBundleRoot returns the generation verified by this process at
// startup. Pinning the process prevents an older and newer binary sharing one
// MintClaw home from changing each other's live catalog through active.json.
func RuntimeSystemBundleRoot(mintclawHome string) (string, error) {
	if cached, ok := ensuredSystemBundles.Load(systemBundleCacheKey(mintclawHome)); ok {
		bundle, valid := cached.(SystemBundle)
		if valid {
			if err := validateGenerationIdentity(bundle.Root, bundle.Fingerprint); err != nil {
				return "", fmt.Errorf("validate process system skill bundle: %w", err)
			}
			return bundle.Root, nil
		}
	}
	return ActiveSystemBundleRoot(mintclawHome)
}

// ActiveSystemBundleRoot resolves the immutable system generation selected by
// active.json. It never falls back to the process working directory.
func ActiveSystemBundleRoot(mintclawHome string) (string, error) {
	systemRoot := systemBundleRoot(mintclawHome)
	generationsRoot := filepath.Join(systemRoot, "generations")
	for _, directory := range []string{filepath.Join(mintclawHome, "skills"), systemRoot, generationsRoot} {
		if err := validateSystemBundleDirectory(directory); err != nil {
			if os.IsNotExist(err) {
				return "", ErrSystemBundleUnavailable
			}
			return "", fmt.Errorf("validate system skill bundle directory: %w", err)
		}
	}
	bundle, err := readActiveSystemBundle(systemRoot, generationsRoot)
	if err != nil {
		return "", err
	}
	return bundle.Root, nil
}

func readActiveSystemBundle(systemRoot, generationsRoot string) (SystemBundle, error) {
	activePath := filepath.Join(systemRoot, systemBundleActiveName)
	activeInfo, err := os.Lstat(activePath)
	if err != nil {
		if os.IsNotExist(err) {
			return SystemBundle{}, ErrSystemBundleUnavailable
		}
		return SystemBundle{}, fmt.Errorf("stat system skill bundle marker: %w", err)
	}
	if !activeInfo.Mode().IsRegular() || activeInfo.Mode()&os.ModeSymlink != 0 {
		return SystemBundle{}, fmt.Errorf("system skill bundle marker is not a regular file")
	}
	activeData, err := os.ReadFile(activePath)
	if err != nil {
		if os.IsNotExist(err) {
			return SystemBundle{}, ErrSystemBundleUnavailable
		}
		return SystemBundle{}, fmt.Errorf("read system skill bundle marker: %w", err)
	}

	var active systemBundleActive
	if err = decodeStrictJSON(activeData, &active); err != nil {
		return SystemBundle{}, fmt.Errorf("decode system skill bundle marker: %w", err)
	}
	if active.SchemaVersion != systemBundleSchemaVersion ||
		!validBundleGenerationName(active.Generation, active.Fingerprint) {
		return SystemBundle{}, fmt.Errorf("invalid system skill bundle marker")
	}

	generationRoot := filepath.Join(generationsRoot, active.Generation)
	if err = validateGenerationIdentity(generationRoot, active.Fingerprint); err != nil {
		return SystemBundle{}, fmt.Errorf("validate active system skill bundle: %w", err)
	}
	return SystemBundle{Fingerprint: active.Fingerprint, Root: generationRoot}, nil
}

func ensureSystemBundleFromFS(
	mintclawHome string,
	source fs.FS,
	sourceRoot string,
	writeFile systemBundleWriter,
) (SystemBundle, error) {
	manifest, err := snapshotSystemBundle(source, sourceRoot)
	if err != nil {
		return SystemBundle{}, err
	}

	systemRoot, generationsRoot, err := ensureSystemBundleDirectories(mintclawHome)
	if err != nil {
		return SystemBundle{}, err
	}

	bundle := SystemBundle{Fingerprint: manifest.Fingerprint}
	activeBundle, activeErr := readActiveSystemBundle(systemRoot, generationsRoot)
	if activeErr == nil && activeBundle.Fingerprint == manifest.Fingerprint {
		if validationErr := validateSystemBundleGeneration(activeBundle.Root, manifest); validationErr == nil {
			bundle.Root = activeBundle.Root
		}
	}
	if bundle.Root == "" {
		generationRoot := filepath.Join(generationsRoot, manifest.Fingerprint)
		if validationErr := validateSystemBundleGeneration(generationRoot, manifest); validationErr == nil {
			bundle.Root = generationRoot
		} else {
			replaceExisting, statErr := systemBundleGenerationExists(generationRoot)
			if statErr != nil {
				return SystemBundle{}, statErr
			}
			bundle.Root, err = publishSystemBundleGeneration(
				generationsRoot,
				generationRoot,
				manifest,
				writeFile,
				replaceExisting,
			)
			if err != nil {
				return SystemBundle{}, err
			}
		}
	}

	active := systemBundleActive{
		SchemaVersion: systemBundleSchemaVersion,
		Fingerprint:   manifest.Fingerprint,
		Generation:    filepath.Base(bundle.Root),
	}
	activeData, err := json.MarshalIndent(active, "", "  ")
	if err != nil {
		return SystemBundle{}, fmt.Errorf("encode system skill bundle marker: %w", err)
	}
	activeData = append(activeData, '\n')
	activePath := filepath.Join(systemRoot, systemBundleActiveName)
	if !systemBundleMarkerMatches(activePath, activeData) {
		if err = fileutil.WriteFileAtomic(activePath, activeData, 0o644); err != nil {
			return SystemBundle{}, fmt.Errorf("activate system skill bundle: %w", err)
		}
	}

	return bundle, nil
}

func systemBundleGenerationExists(generationRoot string) (bool, error) {
	if _, err := os.Lstat(generationRoot); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat system skill generation: %w", err)
	}
	return true, nil
}

func snapshotSystemBundle(source fs.FS, sourceRoot string) (systemBundleManifest, error) {
	files := make([]systemBundleFile, 0)
	err := fs.WalkDir(source, sourceRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported bundled skill entry %q", filePath)
		}
		relativePath := strings.TrimPrefix(filePath, strings.TrimSuffix(sourceRoot, "/")+"/")
		if relativePath == filePath || relativePath == "." || !fs.ValidPath(relativePath) {
			return fmt.Errorf("invalid bundled skill path %q", filePath)
		}
		data, err := fs.ReadFile(source, filePath)
		if err != nil {
			return fmt.Errorf("read bundled skill %q: %w", filePath, err)
		}
		mode := uint32(0o444)
		if strings.Contains(relativePath, "/scripts/") {
			mode = 0o555
		}
		sum := sha256.Sum256(data)
		files = append(files, systemBundleFile{
			Path:   relativePath,
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(data)),
			Mode:   mode,
			data:   data,
		})
		return nil
	})
	if err != nil {
		return systemBundleManifest{}, fmt.Errorf("snapshot bundled system skills: %w", err)
	}
	if len(files) == 0 {
		return systemBundleManifest{}, fmt.Errorf("snapshot bundled system skills: bundle is empty")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if err = validateBundledSkillOwnership(files); err != nil {
		return systemBundleManifest{}, fmt.Errorf("snapshot bundled system skills: %w", err)
	}

	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "mintclaw-system-skills-v%d\n", systemBundleSchemaVersion)
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%s\n", file.Path, file.Mode, file.Size, file.SHA256)
	}
	return systemBundleManifest{
		SchemaVersion: systemBundleSchemaVersion,
		Fingerprint:   hex.EncodeToString(hash.Sum(nil)),
		Files:         files,
	}, nil
}

func validateBundledSkillOwnership(files []systemBundleFile) error {
	byPath := make(map[string]systemBundleFile, len(files))
	skillDirectories := make(map[string]struct{})
	for _, file := range files {
		byPath[file.Path] = file
		skillDirectory, _, found := strings.Cut(file.Path, "/")
		if !found || skillDirectory == "" {
			return fmt.Errorf("bundled skill entry %q is not inside a skill directory", file.Path)
		}
		skillDirectories[skillDirectory] = struct{}{}
		if path.Base(file.Path) == skillProvenanceName && path.Dir(file.Path) != skillDirectory {
			return fmt.Errorf("import provenance %q must be at the skill root", file.Path)
		}
	}

	directories := make([]string, 0, len(skillDirectories))
	for skillDirectory := range skillDirectories {
		directories = append(directories, skillDirectory)
	}
	sort.Strings(directories)
	for _, skillDirectory := range directories {
		if _, ok := byPath[path.Join(skillDirectory, "SKILL.md")]; !ok {
			return fmt.Errorf("bundled skill %q has no SKILL.md", skillDirectory)
		}
		provenancePath := path.Join(skillDirectory, skillProvenanceName)
		provenanceFile, hasProvenance := byPath[provenancePath]
		if isMintClawAuthoredBundledSkill(skillDirectory) {
			if hasProvenance {
				return fmt.Errorf("MintClaw-authored skill %q must not declare import provenance", skillDirectory)
			}
			continue
		}
		if !hasProvenance {
			return fmt.Errorf(
				"bundled skill %q is not declared MintClaw-authored and has no import provenance",
				skillDirectory,
			)
		}
		if _, ok := byPath[path.Join(skillDirectory, "LICENSE")]; !ok {
			return fmt.Errorf("imported skill %q has provenance without LICENSE", skillDirectory)
		}
		var provenance skillProvenance
		if err := decodeStrictJSON(provenanceFile.data, &provenance); err != nil {
			return fmt.Errorf("decode imported skill provenance %q: %w", provenancePath, err)
		}
		if err := validateSkillProvenance(provenance); err != nil {
			return fmt.Errorf("validate imported skill provenance %q: %w", provenancePath, err)
		}
	}
	return nil
}

func isMintClawAuthoredBundledSkill(name string) bool {
	switch name {
	case "agent-browser", "github", "hardware", "mintclaw-agent", "mintclaw-trace-debug", "pdf", "skill-creator",
		"summarize", "tmux", "weather":
		return true
	default:
		return false
	}
}

func validateSkillProvenance(provenance skillProvenance) error {
	if provenance.SchemaVersion != skillProvenanceVersion {
		return fmt.Errorf("schema_version must be %d", skillProvenanceVersion)
	}
	repository, err := url.ParseRequestURI(provenance.SourceRepository)
	if err != nil || repository.Scheme != "https" || repository.Host == "" || repository.User != nil ||
		repository.Fragment != "" {
		return fmt.Errorf("source_repository must be an absolute HTTPS URL without credentials or fragment")
	}
	if !validLowerHex(provenance.SourceRevision, 20) {
		return fmt.Errorf("source_revision must be a lowercase 40-character Git revision")
	}
	if provenance.SourcePath == "." || !fs.ValidPath(provenance.SourcePath) {
		return fmt.Errorf("source_path must be a safe relative slash path")
	}
	if provenance.License == "" || strings.TrimSpace(provenance.License) != provenance.License ||
		len(provenance.License) > 128 {
		return fmt.Errorf("license must be a non-empty bounded identifier without surrounding whitespace")
	}
	if provenance.Decision != "port" && provenance.Decision != "adapt" {
		return fmt.Errorf("decision must be port or adapt")
	}
	if provenance.Decision == "adapt" && len(provenance.Adaptations) == 0 {
		return fmt.Errorf("adapt provenance must record at least one adaptation")
	}
	if provenance.Decision == "port" && len(provenance.Adaptations) != 0 {
		return fmt.Errorf("port provenance must not record adaptations")
	}
	seen := make(map[string]struct{}, len(provenance.Adaptations))
	for _, adaptation := range provenance.Adaptations {
		if adaptation == "" || strings.TrimSpace(adaptation) != adaptation || len(adaptation) > 240 {
			return fmt.Errorf("adaptations must be non-empty bounded strings without surrounding whitespace")
		}
		if _, duplicate := seen[adaptation]; duplicate {
			return fmt.Errorf("adaptations must be unique")
		}
		seen[adaptation] = struct{}{}
	}
	return nil
}

func validLowerHex(value string, size int) bool {
	if len(value) != size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size
}

func publishSystemBundleGeneration(
	generationsRoot string,
	generationRoot string,
	manifest systemBundleManifest,
	writeFile systemBundleWriter,
	replaceExisting bool,
) (string, error) {
	stagingRoot, err := os.MkdirTemp(generationsRoot, ".staging-")
	if err != nil {
		return "", fmt.Errorf("create staged system skill bundle: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingRoot) }()

	for _, file := range manifest.Files {
		target := filepath.Join(stagingRoot, filepath.FromSlash(file.Path))
		if err = writeFile(target, file.data, os.FileMode(file.Mode)); err != nil {
			return "", fmt.Errorf("write staged system skill %q: %w", file.Path, err)
		}
	}
	manifestData, err := marshalSystemBundleManifest(manifest)
	if err != nil {
		return "", err
	}
	if err = writeFile(filepath.Join(stagingRoot, systemBundleManifestName), manifestData, 0o444); err != nil {
		return "", fmt.Errorf("write staged system skill manifest: %w", err)
	}
	if err = validateSystemBundleGenerationAt(stagingRoot, manifest, false); err != nil {
		return "", fmt.Errorf("verify staged system skill bundle: %w", err)
	}
	if replaceExisting {
		generationRoot = replacementSystemBundleGenerationRoot(
			generationsRoot,
			manifest.Fingerprint,
			stagingRoot,
		)
	}

	if err = os.Rename(stagingRoot, generationRoot); err != nil {
		if validationErr := validateSystemBundleGeneration(generationRoot, manifest); validationErr == nil {
			return generationRoot, nil
		}
		return "", fmt.Errorf("publish system skill bundle: %w", err)
	}
	if err = fileutil.SyncDirectory(generationsRoot); err != nil {
		return "", fmt.Errorf("sync published system skill bundle: %w", err)
	}
	return generationRoot, nil
}

func replacementSystemBundleGenerationRoot(generationsRoot, fingerprint, stagingRoot string) string {
	suffix := strings.TrimPrefix(filepath.Base(stagingRoot), ".staging-")
	return filepath.Join(generationsRoot, fingerprint+"-"+suffix)
}

func validateSystemBundleGeneration(root string, expected systemBundleManifest) error {
	return validateSystemBundleGenerationAt(root, expected, true)
}

func validateSystemBundleGenerationAt(root string, expected systemBundleManifest, requireFingerprintName bool) error {
	if err := validateGenerationDirectory(root, expected.Fingerprint, requireFingerprintName); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, systemBundleManifestName))
	if err != nil {
		return err
	}
	var actual systemBundleManifest
	if err = decodeStrictJSON(data, &actual); err != nil {
		return err
	}
	if actual.SchemaVersion != expected.SchemaVersion || actual.Fingerprint != expected.Fingerprint ||
		len(actual.Files) != len(expected.Files) {
		return fmt.Errorf("system skill manifest does not match embedded bundle")
	}

	for i, file := range expected.Files {
		actualFile := actual.Files[i]
		if actualFile.Path != file.Path || actualFile.SHA256 != file.SHA256 || actualFile.Size != file.Size ||
			actualFile.Mode != file.Mode {
			return fmt.Errorf("system skill manifest entry %d does not match embedded bundle", i)
		}
		filePath := filepath.Join(root, filepath.FromSlash(file.Path))
		info, statErr := os.Lstat(filePath)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("system skill entry %q is not a regular file", file.Path)
		}
		if !systemBundleFileModeMatches(runtime.GOOS, info.Mode(), os.FileMode(file.Mode)) {
			return fmt.Errorf(
				"system skill entry %q has permissions %04o, expected %04o",
				file.Path,
				info.Mode().Perm(),
				os.FileMode(file.Mode).Perm(),
			)
		}
		contents, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(contents)
		if int64(len(contents)) != file.Size || hex.EncodeToString(sum[:]) != file.SHA256 {
			return fmt.Errorf("system skill entry %q failed integrity validation", file.Path)
		}
	}
	wantPaths := make(map[string]struct{}, len(expected.Files)+1)
	wantDirectories := make(map[string]struct{})
	wantPaths[systemBundleManifestName] = struct{}{}
	for _, file := range expected.Files {
		localPath := filepath.FromSlash(file.Path)
		wantPaths[localPath] = struct{}{}
		for directory := filepath.Dir(localPath); directory != "."; directory = filepath.Dir(directory) {
			wantDirectories[directory] = struct{}{}
		}
	}
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath == root {
				return nil
			}
			relativePath, relErr := filepath.Rel(root, filePath)
			if relErr != nil {
				return relErr
			}
			if _, ok := wantDirectories[relativePath]; !ok {
				return fmt.Errorf("unexpected system skill bundle directory %q", relativePath)
			}
			return nil
		}
		relativePath, relErr := filepath.Rel(root, filePath)
		if relErr != nil {
			return relErr
		}
		if _, ok := wantPaths[relativePath]; !ok {
			return fmt.Errorf("unexpected system skill bundle entry %q", relativePath)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func systemBundleFileModeMatches(goos string, actual, expected os.FileMode) bool {
	// Windows does not preserve POSIX execute bits. Integrity there is defined
	// by regular-file identity and content; Unix-like hosts also enforce the
	// exact read/execute permissions declared by the embedded manifest.
	return goos == "windows" || actual.Perm() == expected.Perm()
}

func validateGenerationIdentity(root, fingerprint string) error {
	return validateGenerationDirectory(root, fingerprint, true)
}

func validateGenerationDirectory(root, fingerprint string, requireFingerprintName bool) error {
	if !validBundleFingerprint(fingerprint) ||
		requireFingerprintName && !validBundleGenerationName(filepath.Base(root), fingerprint) {
		return fmt.Errorf("invalid system skill generation identity")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("system skill generation is not a directory")
	}
	manifestPath := filepath.Join(root, systemBundleManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("system skill manifest is not a regular file")
	}
	return nil
}

func marshalSystemBundleManifest(manifest systemBundleManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode system skill manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("unexpected trailing JSON value")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing JSON data")
	}
	return nil
}

func validBundleFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validBundleGenerationName(value, fingerprint string) bool {
	if !validBundleFingerprint(fingerprint) {
		return false
	}
	if value == fingerprint {
		return true
	}
	prefix := fingerprint + "-"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(value, prefix)
	if suffix == "" || len(suffix) > 32 {
		return false
	}
	for _, character := range suffix {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func systemBundleRoot(mintclawHome string) string {
	return filepath.Join(mintclawHome, "skills", ".system")
}

func systemBundleCacheKey(mintclawHome string) string {
	absolute, err := filepath.Abs(mintclawHome)
	if err != nil {
		return filepath.Clean(mintclawHome)
	}
	return filepath.Clean(absolute)
}

func ensureSystemBundleDirectories(mintclawHome string) (string, string, error) {
	if err := os.MkdirAll(mintclawHome, 0o755); err != nil {
		return "", "", fmt.Errorf("create MintClaw home for system skills: %w", err)
	}
	skillsRoot := filepath.Join(mintclawHome, "skills")
	systemRoot := systemBundleRoot(mintclawHome)
	generationsRoot := filepath.Join(systemRoot, "generations")
	for _, directory := range []string{skillsRoot, systemRoot, generationsRoot} {
		if err := os.Mkdir(directory, 0o755); err != nil && !os.IsExist(err) {
			return "", "", fmt.Errorf("create system skill bundle directory: %w", err)
		}
		if err := validateSystemBundleDirectory(directory); err != nil {
			return "", "", fmt.Errorf("validate system skill bundle directory: %w", err)
		}
	}
	return systemRoot, generationsRoot, nil
}

func validateSystemBundleDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a physical directory", directory)
	}
	return nil
}

func systemBundleMarkerMatches(activePath string, expected []byte) bool {
	info, err := os.Lstat(activePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	actual, err := os.ReadFile(activePath)
	return err == nil && bytes.Equal(actual, expected)
}

func writeSystemBundleFile(path string, data []byte, mode os.FileMode) error {
	return fileutil.WriteFileAtomic(path, data, mode)
}
