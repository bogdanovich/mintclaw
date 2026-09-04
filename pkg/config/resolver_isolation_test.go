package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRepositorySaveRejectsUnresolvedFileReferenceBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	repository := NewRepository(configPath)
	cfg := DefaultConfig()
	if _, err := repository.Save(cfg); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	publicBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	securityPath := filepath.Join(dir, SecurityConfigFile)
	securityBefore, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg.ModelList[0].APIKeys = SimpleSecureStrings("placeholder")
	cfg.ModelList[0].APIKeys[0].Set("file://missing.key")
	if _, err = repository.Save(cfg); err == nil {
		t.Fatal("Repository.Save() accepted an unresolved file reference")
	}
	publicAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	securityAfter, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicBefore, publicAfter) || !bytes.Equal(securityBefore, securityAfter) {
		t.Fatal("failed credential resolution changed the durable config pair")
	}
}

func TestRepositorySaveResolvesFileReferencesAgainstRepository(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(filepath.Join(dir, "shared.key"), []byte("saved-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.ModelList[0].APIKeys = SimpleSecureStrings("placeholder")
	cfg.ModelList[0].APIKeys[0].Set("file://shared.key")
	snapshot, err := NewRepository(configPath).Save(cfg)
	if err != nil {
		t.Fatalf("Repository.Save() error = %v", err)
	}
	if got := snapshot.Config.ModelList[0].APIKey(); got != "saved-secret" {
		t.Fatalf("saved snapshot key = %q, want saved-secret", got)
	}
	if got := cfg.ModelList[0].APIKey(); got != "file://shared.key" {
		t.Fatalf("Repository.Save() mutated caller key to %q", got)
	}
	security, err := os.ReadFile(filepath.Join(dir, SecurityConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(security), "file://shared.key") {
		t.Fatalf("security config did not preserve file reference:\n%s", security)
	}
}

func TestLoadConfigConcurrentFileReferencesStayRepositoryLocal(t *testing.T) {
	const workersPerRepository = 8

	firstPath := writeFileReferenceConfig(t, "first-secret")
	secondPath := writeFileReferenceConfig(t, "second-secret")
	start := make(chan struct{})
	errors := make(chan error, workersPerRepository*2)
	var wait sync.WaitGroup

	for _, test := range []struct {
		path string
		want string
	}{
		{path: firstPath, want: "first-secret"},
		{path: secondPath, want: "second-secret"},
	} {
		for range workersPerRepository {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				for range 10 {
					cfg, err := LoadConfig(test.path)
					if err != nil {
						errors <- err
						return
					}
					if got := cfg.ModelList[0].APIKey(); got != test.want {
						errors <- fmt.Errorf("LoadConfig(%s) key = %q, want %q", test.path, got, test.want)
						return
					}
				}
			}()
		}
	}

	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func writeFileReferenceConfig(t *testing.T, secret string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		configPath,
		[]byte(`{"version":4,"model_list":[{"model_name":"test","provider":"openai","model":"gpt-4"}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shared.key"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, SecurityConfigFile),
		[]byte("model_list:\n  test:0:\n    api_keys:\n      - file://shared.key\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return configPath
}
