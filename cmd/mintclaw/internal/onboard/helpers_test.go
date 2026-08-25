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

	agentPath := filepath.Join(targetDir, "AGENTS.md")
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

	for _, legacyName := range []string{"AGENT.md", "IDENTITY.md"} {
		legacyPath := filepath.Join(targetDir, legacyName)
		if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
			t.Fatalf("expected legacy file %s to be absent, got err=%v", legacyPath, err)
		}
	}
}

func TestCopyEmbeddedToTargetPreservesExistingAgentInstructions(t *testing.T) {
	targetDir := t.TempDir()
	agentPath := filepath.Join(targetDir, "AGENTS.md")
	const instructions = "# Personal instructions\n\nKeep this content.\n"
	if err := os.WriteFile(agentPath, []byte(instructions), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := copyEmbeddedToTarget(targetDir); err != nil {
		t.Fatalf("copyEmbeddedToTarget() error = %v", err)
	}
	got, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != instructions {
		t.Fatalf("AGENTS.md = %q, want existing instructions preserved", got)
	}
	if _, err = os.Stat(filepath.Join(targetDir, "SOUL.md")); err != nil {
		t.Fatalf("missing template was not created: %v", err)
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

	err = saveOnboardConfig(repository, stale.Config, stale.Revision)
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

	reset, expectedRevision, err := prepareOnboardConfig(repository, false)
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

	err = saveOnboardConfig(repository, reset, expectedRevision)
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

func TestFreshOnboardingRejectsConfigCreatedAfterPreparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository := config.NewRepository(path)
	fresh, expectedRevision, err := prepareOnboardConfig(repository, false)
	if err != nil {
		t.Fatalf("prepareOnboardConfig() error = %v", err)
	}

	created := config.DefaultConfig()
	created.Gateway.Port = 45678
	if _, err = config.NewRepository(path).Save(created); err != nil {
		t.Fatalf("concurrent Save() error = %v", err)
	}
	err = saveOnboardConfig(repository, fresh, expectedRevision)
	if !errors.Is(err, config.ErrConfigConflict) {
		t.Fatalf("saveOnboardConfig() error = %v, want config conflict", err)
	}
	current, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() after conflict error = %v", err)
	}
	if current.Config.Gateway.Port != 45678 {
		t.Fatalf("gateway port after conflict = %d, want concurrently created value", current.Config.Gateway.Port)
	}
}
