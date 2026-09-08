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
	if err := os.WriteFile(filepath.Join(dir, "registry.key"), []byte("registry-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.ModelList[0].APIKeys = SimpleSecureStrings("placeholder")
	cfg.ModelList[0].APIKeys[0].Set("file://shared.key")
	cfg.Tools.Skills.Registries.Set("custom", SkillRegistryConfig{
		Enabled:   true,
		BaseURL:   "https://skills.example.com",
		AuthToken: *NewSecureString("file://registry.key"),
	})
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
	savedRegistry, exists := snapshot.Config.Tools.Skills.Registries.Get("custom")
	if !exists || savedRegistry.AuthToken.String() != "registry-secret" {
		t.Fatalf("saved registry = %#v, want resolved registry token", savedRegistry)
	}
	security, err := os.ReadFile(filepath.Join(dir, SecurityConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(security), "file://shared.key") {
		t.Fatalf("security config did not preserve file reference:\n%s", security)
	}
	if !strings.Contains(string(security), "file://registry.key") {
		t.Fatalf("security config did not preserve registry file reference:\n%s", security)
	}
}

func TestRepositorySaveValidatesReconstructedSnapshotBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	repository := NewRepository(configPath)
	if _, err := repository.Save(DefaultConfig()); err != nil {
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

	invalid := DefaultConfig()
	invalid.Tools.RequestUserInput.DefaultTimeoutSeconds = 59
	if _, err = repository.Save(invalid); err == nil ||
		!strings.Contains(err.Error(), "default_timeout_seconds") {
		t.Fatalf("Repository.Save() error = %v, want request_user_input validation error", err)
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
		t.Fatal("failed snapshot validation changed the durable config pair")
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

func TestRepositoriesUseIndependentPassphraseSources(t *testing.T) {
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "")
	mustSetupSSHKey(t)

	type repositoryCase struct {
		path       string
		passphrase string
		secret     string
		repository *Repository
	}
	newCase := func(passphrase, secret string) repositoryCase {
		path := filepath.Join(t.TempDir(), "config.json")
		return repositoryCase{
			path:       path,
			passphrase: passphrase,
			secret:     secret,
			repository: NewRepositoryWithPassphraseSource(path, func() string { return passphrase }),
		}
	}
	cases := []repositoryCase{
		newCase("first-repository-passphrase", "sk-first-repository"),
		newCase("second-repository-passphrase", "sk-second-repository"),
	}

	for _, test := range cases {
		cfg := DefaultConfig()
		cfg.ModelList[0].APIKeys = SimpleSecureStrings(test.secret)
		if _, err := test.repository.Save(cfg); err != nil {
			t.Fatalf("Save(%s): %v", test.path, err)
		}
		security, err := os.ReadFile(securityPath(test.path))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", securityPath(test.path), err)
		}
		if !strings.Contains(string(security), "enc://") || strings.Contains(string(security), test.secret) {
			t.Fatalf("security document for %s exposed plaintext or omitted ciphertext:\n%s", test.path, security)
		}
	}

	start := make(chan struct{})
	errors := make(chan error, len(cases))
	var wait sync.WaitGroup
	for _, test := range cases {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for range 10 {
				snapshot, err := test.repository.ReadOnly()
				if err != nil {
					errors <- err
					return
				}
				if got := snapshot.Config.ModelList[0].APIKey(); got != test.secret {
					errors <- fmt.Errorf("ReadOnly(%s) key = %q, want %q", test.path, got, test.secret)
					return
				}
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	for index, test := range cases {
		wrongPassphrase := cases[(index+1)%len(cases)].passphrase
		_, err := NewRepositoryWithPassphraseSource(
			test.path,
			func() string { return wrongPassphrase },
		).ReadOnly()
		if err == nil {
			t.Errorf("ReadOnly(%s) succeeded with another repository's passphrase", test.path)
		}
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
