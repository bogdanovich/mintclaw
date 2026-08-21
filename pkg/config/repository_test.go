package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestRepositorySerializesDisjointUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository := NewRepository(path)
	if _, err := repository.Save(DefaultConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	errorsCh := make(chan error, 2)
	go func() {
		_, err := NewRepository(path).Update(func(cfg *Config) error {
			close(firstEntered)
			<-releaseFirst
			cfg.Gateway.LogLevel = "debug"
			return nil
		})
		errorsCh <- err
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := NewRepository(path).Update(func(cfg *Config) error {
			cfg.Heartbeat.Enabled = false
			return nil
		})
		errorsCh <- err
	}()
	<-secondStarted
	close(releaseFirst)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatalf("Update() error = %v", err)
		}
	}

	snapshot, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if snapshot.Config.Gateway.LogLevel != "debug" || snapshot.Config.Heartbeat.Enabled {
		t.Fatalf(
			"disjoint updates = log_level %q, heartbeat %t",
			snapshot.Config.Gateway.LogLevel,
			snapshot.Config.Heartbeat.Enabled,
		)
	}
}

func TestRepositoryRejectsStaleReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository := NewRepository(path)
	initial, err := repository.Save(DefaultConfig())
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err = repository.Update(func(cfg *Config) error {
		cfg.Gateway.LogLevel = "debug"
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stale := DefaultConfig()
	stale.Heartbeat.Enabled = false
	_, err = repository.Replace(initial.Revision, stale)
	if !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("Replace() error = %v, want conflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Expected != initial.Revision || conflict.Actual == initial.Revision {
		t.Fatalf("Replace() conflict = %#v", conflict)
	}

	current, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if current.Config.Gateway.LogLevel != "debug" || !current.Config.Heartbeat.Enabled {
		t.Fatalf("stale replacement changed current config: %#v", current.Config)
	}
}

func TestRepositoryRevisionIncludesSecurityDocument(t *testing.T) {
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "repository-test-passphrase")
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	repository := NewRepository(path)
	initial := DefaultConfig()
	initial.Tools.Web.Gemini.APIKey = *NewSecureString("first-secret")
	first, err := repository.Save(initial)
	if err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	second, err := repository.Update(func(cfg *Config) error {
		cfg.Tools.Web.Gemini.APIKey = *NewSecureString("second-secret")
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if first.Revision == second.Revision {
		t.Fatal("security-only update did not advance revision")
	}
}

func TestRepositoryUpdateDoesNotPersistRuntimeEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.LogLevel = "warn"
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	t.Setenv("MINTCLAW_LOG_LEVEL", "debug")
	if _, err := NewRepository(path).Update(func(cfg *Config) error {
		cfg.Heartbeat.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if err := os.Unsetenv("MINTCLAW_LOG_LEVEL"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := NewRepository(path).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if snapshot.Config.Gateway.LogLevel != "warn" || snapshot.Config.Heartbeat.Enabled {
		t.Fatalf(
			"persisted config = log_level %q, heartbeat %t",
			snapshot.Config.Gateway.LogLevel,
			snapshot.Config.Heartbeat.Enabled,
		)
	}
}

func TestRepositoryUpdateDoesNotPersistChannelEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	telegram := &Channel{
		Enabled:  true,
		Type:     ChannelTelegram,
		Settings: []byte(`{"token":"durable-token","base_url":"https://durable.example"}`),
	}
	if err := telegram.Decode(&TelegramSettings{}); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	baseline.Channels[ChannelTelegram] = telegram
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	t.Setenv("MINTCLAW_CHANNELS_TELEGRAM_TOKEN", "runtime-token")
	t.Setenv("MINTCLAW_CHANNELS_TELEGRAM_BASE_URL", "https://runtime.example")
	if _, err := NewRepository(path).Update(func(cfg *Config) error {
		cfg.Heartbeat.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if err := os.Unsetenv("MINTCLAW_CHANNELS_TELEGRAM_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("MINTCLAW_CHANNELS_TELEGRAM_BASE_URL"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := NewRepository(path).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	decoded, err := snapshot.Config.Channels.Get(ChannelTelegram).GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings := decoded.(*TelegramSettings)
	if settings.Token.String() != "durable-token" || settings.BaseURL != "https://durable.example" {
		t.Fatalf("persisted Telegram settings = token %q, base_url %q",
			settings.Token.String(), settings.BaseURL)
	}
}

func TestRepositorySeparatesRawChannelCredentials(t *testing.T) {
	tests := []struct {
		name   string
		commit func(*Repository, *Config) (Snapshot, error)
	}{
		{
			name: "save",
			commit: func(repository *Repository, cfg *Config) (Snapshot, error) {
				return repository.Save(cfg)
			},
		},
		{
			name: "update",
			commit: func(repository *Repository, cfg *Config) (Snapshot, error) {
				if _, err := repository.Save(DefaultConfig()); err != nil {
					return Snapshot{}, err
				}
				return repository.Update(func(current *Config) error {
					current.Channels[ChannelTelegram] = cfg.Channels[ChannelTelegram]
					return nil
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			repository := NewRepository(path)
			candidate := DefaultConfig()
			token := "raw-" + tt.name + "-token"
			candidate.Channels[ChannelTelegram] = &Channel{
				Enabled:  true,
				Type:     ChannelTelegram,
				Settings: []byte(fmt.Sprintf(`{"token":%q,"base_url":"https://durable.example"}`, token)),
			}

			if _, err := tt.commit(repository, candidate); err != nil {
				t.Fatalf("commit error = %v", err)
			}
			publicData, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if strings.Contains(string(publicData), token) {
				t.Fatalf("public config contains channel credential: %s", publicData)
			}
			snapshot, err := repository.ReadOnly()
			if err != nil {
				t.Fatalf("ReadOnly() error = %v", err)
			}
			decoded, err := snapshot.Config.Channels.Get(ChannelTelegram).GetDecoded()
			if err != nil {
				t.Fatalf("GetDecoded() error = %v", err)
			}
			if got := decoded.(*TelegramSettings).Token.String(); got != token {
				t.Fatalf("persisted channel credential = %q, want %q", got, token)
			}
		})
	}
}

func TestRepositoryRollsBackCommittedStagingErrorClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.LogLevel = "warn"
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save(baseline) error = %v", err)
	}

	injected := errors.New("injected staging sync failure")
	originalWrite := configWriteFileAtomic
	configWriteFileAtomic = func(target string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(target, ".transaction.public.next") {
			return &fileutil.CommittedWriteError{Err: injected}
		}
		return fileutil.WriteFileAtomic(target, data, perm)
	}
	t.Cleanup(func() { configWriteFileAtomic = originalWrite })

	candidate := DefaultConfig()
	candidate.Gateway.LogLevel = "debug"
	_, err := NewRepository(path).Save(candidate)
	if !errors.Is(err, injected) || fileutil.IsCommittedWriteError(err) {
		t.Fatalf("Save() error = %v, want uncommitted error wrapping %v", err, injected)
	}
	snapshot, readErr := NewRepository(path).ReadOnly()
	if readErr != nil {
		t.Fatalf("ReadOnly() error = %v", readErr)
	}
	if snapshot.Config.Gateway.LogLevel != "warn" {
		t.Fatalf("rolled-back log level = %q, want warn", snapshot.Config.Gateway.LogLevel)
	}
	assertNoConfigTransactionArtifacts(t, path)
}

func TestRepositoryReportsCommittedManifestRemovalSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.LogLevel = "warn"
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save(baseline) error = %v", err)
	}

	injected := errors.New("injected manifest removal sync failure")
	originalRemove := configRemoveFile
	originalSync := configSyncDirectory
	manifestRemoved := false
	configRemoveFile = func(target string) error {
		err := os.Remove(target)
		if target == NewRepository(path).manifestPath() && err == nil {
			manifestRemoved = true
		}
		return err
	}
	configSyncDirectory = func(dir string) error {
		if manifestRemoved {
			manifestRemoved = false
			return injected
		}
		return fileutil.SyncDirectory(dir)
	}
	t.Cleanup(func() {
		configRemoveFile = originalRemove
		configSyncDirectory = originalSync
	})

	candidate := DefaultConfig()
	candidate.Gateway.LogLevel = "debug"
	snapshot, err := NewRepository(path).Save(candidate)
	if !errors.Is(err, injected) || !fileutil.IsCommittedWriteError(err) {
		t.Fatalf("Save() error = %v, want committed error wrapping %v", err, injected)
	}
	if snapshot.Revision == "" {
		t.Fatal("Save() returned empty revision for committed pair")
	}
	current, readErr := NewRepository(path).ReadOnly()
	if readErr != nil {
		t.Fatalf("ReadOnly() error = %v", readErr)
	}
	if current.Config.Gateway.LogLevel != "debug" || current.Revision != snapshot.Revision {
		t.Fatalf(
			"committed config = level %q, revision %q; want debug, %q",
			current.Config.Gateway.LogLevel,
			current.Revision,
			snapshot.Revision,
		)
	}
	assertNoConfigTransactionArtifacts(t, path)
}

func TestRepositoryRecoversCompletePairAtEveryCommitBoundary(t *testing.T) {
	tests := []struct {
		checkpoint    string
		wantNew       bool
		wantSaveError bool
	}{
		{checkpoint: "before_security_commit", wantSaveError: true},
		{checkpoint: "after_security_commit", wantSaveError: true},
		{checkpoint: "before_public_commit", wantSaveError: true},
		{checkpoint: "after_public_commit", wantNew: true},
	}
	for _, tt := range tests {
		t.Run(tt.checkpoint, func(t *testing.T) {
			t.Setenv("MINTCLAW_KEY_PASSPHRASE", "repository-test-passphrase")
			mustSetupSSHKey(t)
			path := filepath.Join(t.TempDir(), "config.json")
			baseline := DefaultConfig()
			baseline.Gateway.LogLevel = "warn"
			baseline.Tools.Web.Gemini.APIKey = *NewSecureString("old-secret")
			if _, err := NewRepository(path).Save(baseline); err != nil {
				t.Fatalf("Save(baseline) error = %v", err)
			}

			candidate := DefaultConfig()
			candidate.Gateway.LogLevel = "debug"
			candidate.Tools.Web.Gemini.APIKey = *NewSecureString("new-secret")
			injected := errors.New("injected interruption")
			var once sync.Once
			repository := NewRepository(path)
			repository.hooks.checkpoint = func(checkpoint string) error {
				var err error
				if checkpoint == tt.checkpoint {
					once.Do(func() { err = injected })
				}
				return err
			}
			_, saveErr := repository.Save(candidate)
			if tt.wantSaveError && !errors.Is(saveErr, injected) {
				t.Fatalf("Save() error = %v, want %v", saveErr, injected)
			}
			if !tt.wantSaveError && saveErr != nil {
				t.Fatalf("Save() error = %v", saveErr)
			}

			snapshot, err := NewRepository(path).ReadOnly()
			if err != nil {
				t.Fatalf("ReadOnly() error = %v", err)
			}
			wantLevel, wantSecret := "warn", "old-secret"
			if tt.wantNew {
				wantLevel, wantSecret = "debug", "new-secret"
			}
			if snapshot.Config.Gateway.LogLevel != wantLevel ||
				snapshot.Config.Tools.Web.Gemini.APIKey.String() != wantSecret {
				t.Fatalf(
					"recovered pair = level %q, secret %q; want %q, %q",
					snapshot.Config.Gateway.LogLevel,
					snapshot.Config.Tools.Web.Gemini.APIKey.String(),
					wantLevel,
					wantSecret,
				)
			}
			assertNoConfigTransactionArtifacts(t, path)
		})
	}
}

func TestRepositoriesSharingSecurityDocumentUseOneLease(t *testing.T) {
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "repository-test-passphrase")
	mustSetupSSHKey(t)
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.json")
	secondPath := filepath.Join(dir, "second.json")
	if _, err := NewRepository(firstPath).Save(DefaultConfig()); err != nil {
		t.Fatalf("Save(first baseline) error = %v", err)
	}
	if _, err := NewRepository(secondPath).Save(DefaultConfig()); err != nil {
		t.Fatalf("Save(second baseline) error = %v", err)
	}

	first := DefaultConfig()
	first.Gateway.LogLevel = "debug"
	first.Tools.Web.Gemini.APIKey = *NewSecureString("shared-secret")
	securityCommitted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstRepository := NewRepository(firstPath)
	firstRepository.hooks.checkpoint = func(checkpoint string) error {
		if checkpoint == "after_security_commit" {
			close(securityCommitted)
			<-releaseFirst
		}
		return nil
	}
	firstErr := make(chan error, 1)
	go func() {
		_, err := firstRepository.Save(first)
		firstErr <- err
	}()
	<-securityCommitted

	secondEntered := make(chan struct{})
	secondErr := make(chan error, 1)
	go func() {
		_, err := NewRepository(secondPath).Update(func(cfg *Config) error {
			close(secondEntered)
			cfg.Heartbeat.Enabled = false
			return nil
		})
		secondErr <- err
	}()
	select {
	case <-secondEntered:
		t.Fatal("second repository entered mutation while shared security transaction was active")
	case <-time.After(250 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstErr; err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("Update(second) error = %v", err)
	}
	firstSnapshot, err := NewRepository(firstPath).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly(first) error = %v", err)
	}
	secondSnapshot, err := NewRepository(secondPath).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly(second) error = %v", err)
	}
	if firstSnapshot.Config.Gateway.LogLevel != "debug" ||
		firstSnapshot.Config.Tools.Web.Gemini.APIKey.String() != "shared-secret" ||
		secondSnapshot.Config.Heartbeat.Enabled {
		t.Fatalf("serialized snapshots = first %#v, second %#v", firstSnapshot.Config, secondSnapshot.Config)
	}
	assertNoConfigTransactionArtifacts(t, firstPath)
}

func TestRepositorySnapshotMatchesNormalizedCommittedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	candidate := DefaultConfig()
	candidate.Version = CurrentVersion - 1
	candidate.ModelList = append(candidate.ModelList, &ModelConfig{
		ModelName: "virtual",
		Model:     "virtual",
		isVirtual: true,
	})

	snapshot, err := NewRepository(path).Save(candidate)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if snapshot.Config == candidate {
		t.Fatal("Save() returned the unnormalized input pointer")
	}
	if snapshot.Config.Version != CurrentVersion {
		t.Fatalf("snapshot version = %d, want %d", snapshot.Config.Version, CurrentVersion)
	}
	for _, model := range snapshot.Config.ModelList {
		if model != nil && model.isVirtual {
			t.Fatal("snapshot retained a virtual model")
		}
	}
	reloaded, err := NewRepository(path).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if reloaded.Revision != snapshot.Revision || reloaded.Config.Version != snapshot.Config.Version {
		t.Fatalf("reloaded snapshot = revision %q, version %d; saved = %q, %d",
			reloaded.Revision, reloaded.Config.Version, snapshot.Revision, snapshot.Config.Version)
	}
}

func TestLoadConfigRecoversInterruptedConfigurationTransaction(t *testing.T) {
	t.Setenv("MINTCLAW_KEY_PASSPHRASE", "repository-test-passphrase")
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.LogLevel = "warn"
	baseline.Tools.Web.Gemini.APIKey = *NewSecureString("old-secret")
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save(baseline) error = %v", err)
	}

	candidate := DefaultConfig()
	candidate.Gateway.LogLevel = "debug"
	candidate.Tools.Web.Gemini.APIKey = *NewSecureString("new-secret")
	documents, err := marshalConfigDocuments(candidate)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("simulated process interruption")
	repository := NewRepository(path)
	repository.hooks.checkpoint = func(checkpoint string) error {
		if checkpoint == "after_security_commit" {
			return injected
		}
		return nil
	}
	err = repository.withLock(func() error {
		return repository.commitLocked(documents.public, documents.security)
	})
	if !errors.Is(err, injected) {
		t.Fatalf("commitLocked() error = %v, want %v", err, injected)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() recovery error = %v", err)
	}
	if loaded.Gateway.LogLevel != "warn" || loaded.Tools.Web.Gemini.APIKey.String() != "old-secret" {
		t.Fatalf(
			"LoadConfig() recovered pair = level %q, secret %q",
			loaded.Gateway.LogLevel,
			loaded.Tools.Web.Gemini.APIKey.String(),
		)
	}
	assertNoConfigTransactionArtifacts(t, path)
}

func TestRepositoryReadOnlyRejectsOldVersionWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	old := []byte(
		`{"version":2,"agents":{"defaults":{}},"channel_list":{},"model_list":[],` +
			`"gateway":{},"tools":{},"heartbeat":{},"devices":{},"voice":{}}`,
	)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRepository(
		path,
	).ReadOnly(); err == nil ||
		!strings.Contains(err.Error(), "unsupported config version") {
		t.Fatalf("ReadOnly() error = %v, want old-version rejection", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ReadOnly() rewrote config: %q", after)
	}
	if _, err = os.Stat(securityPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadOnly() security file error = %v", err)
	}
	backups, err := filepath.Glob(path + ".*.bak")
	if err != nil || len(backups) != 0 {
		t.Fatalf("ReadOnly() backups = %v, %v", backups, err)
	}
	assertNoConfigTransactionArtifacts(t, path)
}

func TestRepositoryReadDurableDoesNotApplyRuntimeEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.LogLevel = "warn"
	repository := NewRepository(path)
	if _, err := repository.Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	t.Setenv("MINTCLAW_LOG_LEVEL", "debug")

	runtimeSnapshot, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	durableSnapshot, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() error = %v", err)
	}
	if runtimeSnapshot.Config.Gateway.LogLevel != "debug" {
		t.Fatalf("ReadOnly() log level = %q, want runtime override", runtimeSnapshot.Config.Gateway.LogLevel)
	}
	if durableSnapshot.Config.Gateway.LogLevel != "warn" {
		t.Fatalf("ReadDurable() log level = %q, want durable value", durableSnapshot.Config.Gateway.LogLevel)
	}
	if durableSnapshot.Revision != runtimeSnapshot.Revision {
		t.Fatalf("revision mismatch: durable %q, runtime %q", durableSnapshot.Revision, runtimeSnapshot.Revision)
	}
}

func TestRepositoryUpdatePreservesDurableMultiKeyModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.ModelList = []*ModelConfig{{
		ModelName: "multi-key",
		Model:     "openai/multi-key",
		APIKeys:   SimpleSecureStrings("key-one", "key-two"),
		Enabled:   true,
	}}
	repository := NewRepository(path)
	if _, err := repository.Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := repository.Update(func(cfg *Config) error {
		cfg.Gateway.Port = 23456
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	durable, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() error = %v", err)
	}
	if len(durable.Config.ModelList) != 1 {
		t.Fatalf("durable models = %d, want 1", len(durable.Config.ModelList))
	}
	model := durable.Config.ModelList[0]
	if got := model.APIKeys.Values(); !slices.Equal(got, []string{"key-one", "key-two"}) {
		t.Fatalf("durable API keys = %q, want both original keys", got)
	}
	if len(model.Fallbacks) != 0 {
		t.Fatalf("durable fallbacks = %q, want no generated names", model.Fallbacks)
	}
	runtimeSnapshot, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if len(runtimeSnapshot.Config.ModelList) != 2 {
		t.Fatalf("runtime models = %d, want expanded key models", len(runtimeSnapshot.Config.ModelList))
	}
}

func TestRepositoryResetToDefaultsBacksUpAndPreservesDefaultModelCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.Port = 23456
	for _, model := range baseline.ModelList {
		if model.ModelName == "gpt-5.4" {
			model.APIKeys = SimpleSecureStrings("reset-secret")
		}
	}
	repository := NewRepository(path)
	if _, err := repository.Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	snapshot, err := repository.ResetToDefaults()
	if err != nil {
		t.Fatalf("ResetToDefaults() error = %v", err)
	}
	if snapshot.Config.Gateway.Port == 23456 {
		t.Fatal("ResetToDefaults() retained non-default gateway port")
	}
	var preserved *ModelConfig
	for _, model := range snapshot.Config.ModelList {
		if model.ModelName == "gpt-5.4" {
			preserved = model
			break
		}
	}
	if preserved == nil || !slices.Equal(preserved.APIKeys.Values(), []string{"reset-secret"}) {
		t.Fatalf("ResetToDefaults() credential = %#v, want preserved key", preserved)
	}

	backupSuffix := time.Now().Format(".20060102.bak")
	if _, err = os.Stat(path + backupSuffix); err != nil {
		t.Fatalf("public backup: %v", err)
	}
	if _, err = os.Stat(securityPath(path) + backupSuffix); err != nil {
		t.Fatalf("security backup: %v", err)
	}
	backup, err := loadConfigReadOnly(path+backupSuffix, false)
	if err != nil {
		t.Fatalf("load public backup: %v", err)
	}
	if backup.Gateway.Port != 23456 {
		t.Fatalf("backup gateway port = %d, want 23456", backup.Gateway.Port)
	}
}

func TestRepositoryResetSerializesConcurrentUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.Port = 23456
	if _, err := NewRepository(path).Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	resetEnteredCommit := make(chan struct{})
	releaseReset := make(chan struct{})
	resetRepository := NewRepository(path)
	resetRepository.hooks.checkpoint = func(name string) error {
		if name == "before_security_commit" {
			close(resetEnteredCommit)
			<-releaseReset
		}
		return nil
	}
	resetErr := make(chan error, 1)
	go func() {
		_, err := resetRepository.ResetToDefaults()
		resetErr <- err
	}()
	<-resetEnteredCommit

	updateStarted := make(chan struct{})
	updateErr := make(chan error, 1)
	go func() {
		close(updateStarted)
		_, err := NewRepository(path).Update(func(cfg *Config) error {
			cfg.Gateway.LogLevel = "debug"
			return nil
		})
		updateErr <- err
	}()
	<-updateStarted
	close(releaseReset)
	if err := <-resetErr; err != nil {
		t.Fatalf("ResetToDefaults() error = %v", err)
	}
	if err := <-updateErr; err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	current, err := NewRepository(path).ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() error = %v", err)
	}
	if current.Config.Gateway.Port == 23456 {
		t.Fatal("reset retained non-default gateway port")
	}
	if current.Config.Gateway.LogLevel != "debug" {
		t.Fatalf("concurrent update log level = %q, want debug", current.Config.Gateway.LogLevel)
	}
}

func TestRepositoryResetRejectsUnreadableSecurityWithoutChangingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := DefaultConfig()
	baseline.Gateway.Port = 23456
	repository := NewRepository(path)
	if _, err := repository.Save(baseline); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	malformedSecurity := []byte("model_list: [")
	if err := os.WriteFile(securityPath(path), malformedSecurity, 0o600); err != nil {
		t.Fatalf("write malformed security: %v", err)
	}
	publicBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read public before reset: %v", err)
	}

	if _, err = repository.ResetToDefaults(); err == nil {
		t.Fatal("ResetToDefaults() error = nil, want security preservation failure")
	}
	publicAfter, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read public after reset: %v", readErr)
	}
	securityAfter, readErr := os.ReadFile(securityPath(path))
	if readErr != nil {
		t.Fatalf("read security after reset: %v", readErr)
	}
	if !slices.Equal(publicAfter, publicBefore) {
		t.Fatal("reset changed public config after security preservation failure")
	}
	if !slices.Equal(securityAfter, malformedSecurity) {
		t.Fatal("reset changed malformed security config after preservation failure")
	}
}

func TestLoadConfigDoesNotPersistRuntimeEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	baseline := []byte(
		`{"version":3,"agents":{"defaults":{}},"channel_list":{},"model_list":[],` +
			`"gateway":{"log_level":"warn"},"tools":{},"heartbeat":{},"devices":{},"voice":{}}`,
	)
	if err := os.WriteFile(path, baseline, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("MINTCLAW_LOG_LEVEL", "debug")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.Gateway.LogLevel != "debug" {
		t.Fatalf("runtime log level = %q, want debug", loaded.Gateway.LogLevel)
	}
	if err = os.Unsetenv("MINTCLAW_LOG_LEVEL"); err != nil {
		t.Fatal(err)
	}
	durable, err := NewRepository(path).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if durable.Config.Gateway.LogLevel != "warn" {
		t.Fatalf("durable log level = %q, want warn", durable.Config.Gateway.LogLevel)
	}
}

func assertNoConfigTransactionArtifacts(t *testing.T, path string) {
	t.Helper()
	security := securityPath(path)
	for _, artifact := range []string{
		security + ".transaction",
		security + ".transaction.public.previous",
		security + ".transaction.public.next",
		security + ".transaction.security.previous",
		security + ".transaction.security.next",
	} {
		if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction artifact %q stat error = %v", artifact, err)
		}
	}
}
