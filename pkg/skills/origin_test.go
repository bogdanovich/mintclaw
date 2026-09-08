package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type originTestRegistry struct{}

func (originTestRegistry) Name() string { return "github" }

func (originTestRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (originTestRegistry) SkillURL(slug, version string) string {
	return "https://github.example.test/" + slug + "/tree/" + version
}

func (originTestRegistry) Search(context.Context, string, int) ([]SearchResult, error) {
	return nil, nil
}

func (originTestRegistry) GetSkillMeta(context.Context, string) (*SkillMeta, error) {
	return nil, nil
}

func (originTestRegistry) DownloadAndInstall(context.Context, string, string, string) (*InstallResult, error) {
	return nil, errors.New("not implemented")
}

func TestWriteAndReadInstalledSkillOrigin(t *testing.T) {
	targetDir := t.TempDir()
	registry := originTestRegistry{}

	if err := WriteInstalledSkillOrigin(targetDir, registry, "owner/repo/skills/review", "v1.2.3"); err != nil {
		t.Fatalf("WriteInstalledSkillOrigin() error = %v", err)
	}

	metadata, err := ReadSkillOrigin(targetDir)
	if err != nil {
		t.Fatalf("ReadSkillOrigin() error = %v", err)
	}
	if metadata.Version != OriginMetadataVersion || metadata.OriginKind != OriginKindThirdParty ||
		metadata.Registry != "github" || metadata.Slug != "owner/repo/skills/review" ||
		metadata.InstalledVersion != "v1.2.3" || metadata.InstalledAt <= 0 {
		t.Fatalf("origin metadata = %#v", metadata)
	}
	if metadata.RegistryURL != "https://github.example.test/owner/repo/skills/review/tree/v1.2.3" {
		t.Fatalf("registry URL = %q", metadata.RegistryURL)
	}
	info, err := os.Stat(filepath.Join(targetDir, OriginMetadataFilename))
	if err != nil {
		t.Fatalf("stat origin metadata: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("origin metadata mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReadSkillOriginRejectsMalformedMetadata(t *testing.T) {
	tests := map[string]string{
		"unsupported version": `{"version":2}`,
		"unknown field":       `{"version":1,"extra":true}`,
		"missing registry":    `{"version":1,"origin_kind":"third_party"}`,
		"multiple values":     `{} {}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			targetDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(targetDir, OriginMetadataFilename), []byte(body), 0o600); err != nil {
				t.Fatalf("write origin metadata: %v", err)
			}
			if _, err := ReadSkillOrigin(targetDir); !errors.Is(err, ErrInvalidOriginMetadata) {
				t.Fatalf("ReadSkillOrigin() error = %v, want ErrInvalidOriginMetadata", err)
			}
		})
	}
}

func TestReadSkillOriginDistinguishesMissingMetadata(t *testing.T) {
	_, err := ReadSkillOrigin(t.TempDir())
	if !errors.Is(err, ErrOriginMetadataNotFound) {
		t.Fatalf("ReadSkillOrigin() error = %v, want ErrOriginMetadataNotFound", err)
	}
}

func TestWriteInstalledSkillOriginRejectsSymlinkTarget(t *testing.T) {
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "linked-skill")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := WriteInstalledSkillOrigin(linkDir, originTestRegistry{}, "owner/repo", "main")
	if err == nil {
		t.Fatal("WriteInstalledSkillOrigin() unexpectedly accepted a symlink target")
	}
	if _, statErr := os.Stat(filepath.Join(realDir, OriginMetadataFilename)); !os.IsNotExist(statErr) {
		t.Fatalf("origin metadata reached symlink target: %v", statErr)
	}
}
