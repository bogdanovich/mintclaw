package onboard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCopyEmbeddedToTargetUsesStructuredAgentFiles(t *testing.T) {
	targetDir := t.TempDir()

	if err := copyEmbeddedToTarget(targetDir); err != nil {
		t.Fatalf("copyEmbeddedToTarget() error = %v", err)
	}

	agentPath := filepath.Join(targetDir, "AGENT.md")
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("expected %s to exist: %v", agentPath, err)
	}

	soulPath := filepath.Join(targetDir, "SOUL.md")
	if _, err := os.Stat(soulPath); err != nil {
		t.Fatalf("expected %s to exist: %v", soulPath, err)
	}

	userPath := filepath.Join(targetDir, "USER.md")
	if _, err := os.Stat(userPath); err != nil {
		t.Fatalf("expected %s to exist: %v", userPath, err)
	}

	for _, legacyName := range []string{"AGENTS.md", "IDENTITY.md"} {
		legacyPath := filepath.Join(targetDir, legacyName)
		if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
			t.Fatalf("expected legacy file %s to be absent, got err=%v", legacyPath, err)
		}
	}
}

func TestSaveOnboardConfigRejectsStaleExistingSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository := config.NewRepository(path)
	if _, err := repository.Save(config.DefaultConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stale, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() error = %v", err)
	}
	if _, err = repository.Update(func(cfg *config.Config) error {
		cfg.Gateway.Port = 23456
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	err = saveOnboardConfig(repository, stale.Config, true, stale.Revision)
	if !errors.Is(err, config.ErrConfigConflict) {
		t.Fatalf("saveOnboardConfig() error = %v, want config conflict", err)
	}
	current, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() after conflict error = %v", err)
	}
	if current.Config.Gateway.Port != 23456 {
		t.Fatalf("gateway port after conflict = %d, want concurrent value", current.Config.Gateway.Port)
	}
}

func TestConfirmedResetRejectsStaleExistingSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository := config.NewRepository(path)
	existing := config.DefaultConfig()
	existing.Gateway.Port = 23456
	if _, err := repository.Save(existing); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reset, expectedRevision, err := prepareOnboardConfig(repository, true, true)
	if err != nil {
		t.Fatalf("prepareOnboardConfig() error = %v", err)
	}
	if reset.Gateway.Port == 23456 {
		t.Fatal("prepareOnboardConfig() retained existing config for confirmed reset")
	}
	if _, err = repository.Update(func(cfg *config.Config) error {
		cfg.Gateway.Port = 34567
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	err = saveOnboardConfig(repository, reset, true, expectedRevision)
	if !errors.Is(err, config.ErrConfigConflict) {
		t.Fatalf("saveOnboardConfig() error = %v, want config conflict", err)
	}
	current, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() after conflict error = %v", err)
	}
	if current.Config.Gateway.Port != 34567 {
		t.Fatalf("gateway port after conflict = %d, want concurrent value", current.Config.Gateway.Port)
	}
}
