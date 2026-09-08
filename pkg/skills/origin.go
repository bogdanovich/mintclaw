package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

const (
	OriginMetadataFilename = ".skill-origin.json"
	OriginMetadataVersion  = 1
	OriginKindThirdParty   = "third_party"
	maxOriginMetadataBytes = 64 * 1024
)

var (
	ErrOriginMetadataNotFound = errors.New("skill origin metadata not found")
	ErrInvalidOriginMetadata  = errors.New("invalid skill origin metadata")
)

// OriginMetadata identifies the immutable registry source of an installed skill.
type OriginMetadata struct {
	Version          int    `json:"version"`
	OriginKind       string `json:"origin_kind,omitempty"`
	Registry         string `json:"registry"`
	Slug             string `json:"slug"`
	RegistryURL      string `json:"registry_url,omitempty"`
	InstalledVersion string `json:"installed_version"`
	InstalledAt      int64  `json:"installed_at"`
}

// WriteInstalledSkillOrigin records the registry source after an install has
// completed. The on-disk shape remains compatible with the original version-1
// metadata written by install_skill.
func WriteInstalledSkillOrigin(
	targetDir string,
	registry SkillRegistry,
	slug string,
	version string,
) error {
	if registry == nil {
		return errors.New("skill origin registry is required")
	}

	normalizedSlug, registryURL := BuildInstallMetadataForRegistryInstance(registry, slug, version)
	metadata := OriginMetadata{
		Version:          OriginMetadataVersion,
		OriginKind:       OriginKindThirdParty,
		Registry:         registry.Name(),
		Slug:             normalizedSlug,
		RegistryURL:      registryURL,
		InstalledVersion: version,
		InstalledAt:      time.Now().UnixMilli(),
	}
	if err := metadata.Validate(); err != nil {
		return err
	}

	targetInfo, err := os.Lstat(targetDir)
	if err != nil {
		return fmt.Errorf("inspect skill directory: %w", err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
		return errors.New("skill origin target must be a real directory")
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode skill origin metadata: %w", err)
	}
	return fileutil.WriteFileAtomic(filepath.Join(targetDir, OriginMetadataFilename), data, 0o600)
}

// ReadSkillOrigin reads and strictly validates versioned registry metadata.
func ReadSkillOrigin(targetDir string) (OriginMetadata, error) {
	path := filepath.Join(targetDir, OriginMetadataFilename)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return OriginMetadata{}, ErrOriginMetadataNotFound
		}
		return OriginMetadata{}, fmt.Errorf("inspect skill origin metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return OriginMetadata{}, invalidOriginMetadataError("skill origin metadata must be a regular file")
	}
	if info.Size() > maxOriginMetadataBytes {
		return OriginMetadata{}, invalidOriginMetadataError(
			"skill origin metadata exceeds %d bytes",
			maxOriginMetadataBytes,
		)
	}

	file, err := os.Open(path)
	if err != nil {
		return OriginMetadata{}, fmt.Errorf("open skill origin metadata: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return OriginMetadata{}, fmt.Errorf("inspect opened skill origin metadata: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return OriginMetadata{}, invalidOriginMetadataError("skill origin metadata changed identity while opening")
	}

	data, err := io.ReadAll(io.LimitReader(file, maxOriginMetadataBytes+1))
	if err != nil {
		return OriginMetadata{}, fmt.Errorf("read skill origin metadata: %w", err)
	}
	if len(data) > maxOriginMetadataBytes {
		return OriginMetadata{}, invalidOriginMetadataError(
			"skill origin metadata exceeds %d bytes",
			maxOriginMetadataBytes,
		)
	}

	var metadata OriginMetadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return OriginMetadata{}, invalidOriginMetadataError("decode skill origin metadata: %v", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return OriginMetadata{}, invalidOriginMetadataError("%v", err)
	}
	if err := metadata.Validate(); err != nil {
		return OriginMetadata{}, invalidOriginMetadataError("%v", err)
	}
	return metadata, nil
}

func (metadata OriginMetadata) Validate() error {
	if metadata.Version != OriginMetadataVersion {
		return fmt.Errorf("unsupported skill origin metadata version %d", metadata.Version)
	}
	if metadata.OriginKind != OriginKindThirdParty {
		return fmt.Errorf("unsupported skill origin kind %q", metadata.OriginKind)
	}
	if strings.TrimSpace(metadata.Registry) != metadata.Registry || len(metadata.Registry) > 256 ||
		strings.ContainsAny(metadata.Registry, "\x00\r\n") {
		return errors.New("invalid skill origin registry name")
	}
	if err := utils.ValidateSkillIdentifier(metadata.Registry); err != nil {
		return fmt.Errorf("invalid skill origin registry: %w", err)
	}
	if strings.TrimSpace(metadata.Slug) == "" {
		return errors.New("skill origin slug is required")
	}
	if len(metadata.Slug) > 4096 {
		return errors.New("skill origin slug exceeds 4096 bytes")
	}
	if strings.ContainsAny(metadata.Slug, "\x00\r\n") {
		return errors.New("skill origin slug contains control characters")
	}
	if len(metadata.RegistryURL) > 8192 {
		return errors.New("skill origin registry URL exceeds 8192 bytes")
	}
	if strings.ContainsAny(metadata.RegistryURL, "\x00\r\n") {
		return errors.New("skill origin registry URL contains control characters")
	}
	if len(metadata.InstalledVersion) > 1024 {
		return errors.New("skill installed version exceeds 1024 bytes")
	}
	if strings.ContainsAny(metadata.InstalledVersion, "\x00\r\n") {
		return errors.New("skill installed version contains control characters")
	}
	if metadata.InstalledAt <= 0 {
		return errors.New("skill origin install time is required")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("skill origin metadata contains multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode trailing skill origin metadata: %w", err)
	}
	return nil
}

func invalidOriginMetadataError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOriginMetadata, fmt.Sprintf(format, args...))
}
