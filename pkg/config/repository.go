package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const configTransactionVersion = 1

// Revision identifies one complete public/security configuration pair.
type Revision string

// Snapshot is a configuration value and the durable revision it was loaded from.
type Snapshot struct {
	Config   *Config
	Revision Revision
}

// ErrConfigConflict identifies an optimistic configuration revision conflict.
var ErrConfigConflict = errors.New("configuration revision conflict")

// ConflictError reports that a replacement was based on a stale revision.
type ConflictError struct {
	Expected Revision
	Actual   Revision
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: expected %s, found %s", ErrConfigConflict, e.Expected, e.Actual)
}

func (e *ConflictError) Unwrap() error {
	return ErrConfigConflict
}

// Repository serializes and versions mutations for one canonical config path.
// Separate Repository values and separate processes coordinate through the same
// cross-platform file lock.
type Repository struct {
	path  string
	hooks repositoryHooks
}

type repositoryHooks struct {
	checkpoint func(string) error
}

type configTransactionManifest struct {
	Version    int                       `json:"version"`
	PublicPath string                    `json:"public_path"`
	Public     configTransactionDocument `json:"public"`
	Security   configTransactionDocument `json:"security"`
}

type configTransactionDocument struct {
	PreviousExists bool   `json:"previous_exists"`
	PreviousHash   string `json:"previous_hash,omitempty"`
	NextHash       string `json:"next_hash"`
}

type configDocumentPaths struct {
	target   string
	previous string
	next     string
}

// NewRepository returns the single-writer repository for path.
func NewRepository(path string) *Repository {
	if path == "" {
		return &Repository{}
	}
	cleanPath := filepath.Clean(path)
	if absolutePath, err := filepath.Abs(cleanPath); err == nil {
		cleanPath = absolutePath
	}
	return &Repository{path: cleanPath}
}

// ReadOnly returns a coherent public/security pair without running or
// persisting configuration migrations. It may finish recovery of a transaction
// interrupted by an earlier writer.
func (r *Repository) ReadOnly() (Snapshot, error) {
	var snapshot Snapshot
	err := r.withLock(func() error {
		if _, err := r.recoverLocked(); err != nil {
			return err
		}
		cfg, err := LoadConfigReadOnly(r.path)
		if err != nil {
			return err
		}
		revision, err := revisionForPaths(r.path, securityPath(r.path))
		if err != nil {
			return err
		}
		snapshot = Snapshot{Config: cfg, Revision: revision}
		return nil
	})
	return snapshot, err
}

// Update holds the repository lease across load, mutation, and commit. This is
// the preferred API for partial mutations because disjoint writers cannot lose
// one another's accepted changes.
func (r *Repository) Update(mutate func(*Config) error) (Snapshot, error) {
	if mutate == nil {
		return Snapshot{}, errors.New("configuration mutation is nil")
	}
	var snapshot Snapshot
	err := r.withLock(func() error {
		if _, err := r.recoverLocked(); err != nil {
			return err
		}
		cfg, err := loadConfigForUpdate(r.path)
		if err != nil {
			return err
		}
		if err = mutate(cfg); err != nil {
			return err
		}
		snapshot, err = r.saveLocked(cfg)
		return err
	})
	return snapshot, err
}

// Replace commits cfg only when expected still identifies the current durable
// pair. Full-document APIs should expose the returned conflict to callers.
func (r *Repository) Replace(expected Revision, cfg *Config) (Snapshot, error) {
	if cfg == nil {
		return Snapshot{}, errors.New("configuration is nil")
	}
	var snapshot Snapshot
	err := r.withLock(func() error {
		if _, err := r.recoverLocked(); err != nil {
			return err
		}
		actual, err := revisionForPaths(r.path, securityPath(r.path))
		if err != nil {
			return err
		}
		if actual != expected {
			return &ConflictError{Expected: expected, Actual: actual}
		}
		snapshot, err = r.saveLocked(cfg)
		return err
	})
	return snapshot, err
}

// Save performs an unconditional serialized replacement. It exists for
// compatibility while callers migrate to Update or revision-checked Replace.
func (r *Repository) Save(cfg *Config) (Snapshot, error) {
	if cfg == nil {
		return Snapshot{}, errors.New("configuration is nil")
	}
	var snapshot Snapshot
	err := r.withLock(func() error {
		if _, err := r.recoverLocked(); err != nil {
			return err
		}
		var err error
		snapshot, err = r.saveLocked(cfg)
		return err
	})
	return snapshot, err
}

func (r *Repository) withLock(fn func() error) error {
	if r == nil || r.path == "" || r.path == "." {
		return errors.New("configuration repository path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	release, err := acquireConfigFileLock(securityPath(r.path) + ".lock")
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func (r *Repository) saveLocked(cfg *Config) (Snapshot, error) {
	documents, err := marshalConfigDocuments(cfg)
	if err != nil {
		return Snapshot{}, err
	}
	return r.saveDocumentsLocked(documents)
}

func (r *Repository) saveDocumentsLocked(documents configDocuments) (Snapshot, error) {
	if err := r.commitLocked(documents.public, documents.security); err != nil {
		_, recoveryErr := r.recoverLocked()
		if recoveryErr != nil {
			return Snapshot{}, errors.Join(err, fmt.Errorf("recover configuration transaction: %w", recoveryErr))
		}
		actualRevision, revisionErr := revisionForPaths(r.path, securityPath(r.path))
		if revisionErr != nil {
			return Snapshot{}, errors.Join(err, fmt.Errorf("read recovered configuration revision: %w", revisionErr))
		}
		if actualRevision != documents.revision {
			return Snapshot{}, uncommittedTransactionError(err)
		}
		if fileutil.IsCommittedWriteError(err) {
			return Snapshot{Config: documents.config, Revision: documents.revision}, err
		}
	}
	return Snapshot{
		Config:   documents.config,
		Revision: documents.revision,
	}, nil
}

type rolledBackTransactionError struct {
	message string
	cause   error
}

func (e *rolledBackTransactionError) Error() string {
	return e.message
}

func (e *rolledBackTransactionError) Unwrap() error {
	return e.cause
}

func uncommittedTransactionError(err error) error {
	var committedErr *fileutil.CommittedWriteError
	if !errors.As(err, &committedErr) {
		return err
	}
	return &rolledBackTransactionError{message: err.Error(), cause: committedErr.Unwrap()}
}

type configDocuments struct {
	config   *Config
	public   []byte
	security []byte
	revision Revision
}

func marshalConfigDocuments(cfg *Config) (configDocuments, error) {
	copyCfg := *cfg
	if copyCfg.Version < CurrentVersion {
		copyCfg.Version = CurrentVersion
	}
	copyCfg.Channels = make(ChannelsConfig, len(cfg.Channels))
	for name, channel := range cfg.Channels {
		if channel == nil {
			continue
		}
		channelCopy := *channel
		copyCfg.Channels[name] = &channelCopy
	}
	if err := initUndecodedChannelList(copyCfg.Channels); err != nil {
		return configDocuments{}, err
	}
	copyCfg.ModelList = make([]*ModelConfig, 0, len(cfg.ModelList))
	for _, model := range cfg.ModelList {
		if model != nil && !model.isVirtual {
			copyCfg.ModelList = append(copyCfg.ModelList, model)
		}
	}
	securityData, err := marshalSecurityConfig(&copyCfg)
	if err != nil {
		return configDocuments{}, err
	}
	publicData, err := json.MarshalIndent(&copyCfg, "", "  ")
	if err != nil {
		return configDocuments{}, err
	}
	return configDocuments{
		config:   &copyCfg,
		public:   publicData,
		security: securityData,
		revision: revisionForData(publicData, true, securityData, true),
	}, nil
}

func (r *Repository) commitLocked(publicData, securityData []byte) error {
	publicPaths, securityPaths := r.documentPaths()
	manifest := configTransactionManifest{
		Version:    configTransactionVersion,
		PublicPath: r.path,
	}
	var err error
	manifest.Public, err = stageConfigDocument(publicPaths, publicData)
	if err != nil {
		return fmt.Errorf("stage public configuration: %w", err)
	}
	manifest.Security, err = stageConfigDocument(securityPaths, securityData)
	if err != nil {
		return fmt.Errorf("stage security configuration: %w", err)
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = fileWriteAtomic(r.manifestPath(), manifestData); err != nil {
		return fmt.Errorf("publish configuration transaction: %w", err)
	}
	if err = r.checkpoint("before_security_commit"); err != nil {
		return err
	}
	if err = replaceConfigDocument(securityPaths.target, securityData); err != nil {
		return fmt.Errorf("commit security configuration: %w", err)
	}
	if err = r.checkpoint("after_security_commit"); err != nil {
		return err
	}
	if err = r.checkpoint("before_public_commit"); err != nil {
		return err
	}
	if err = replaceConfigDocument(publicPaths.target, publicData); err != nil {
		return fmt.Errorf("commit public configuration: %w", err)
	}
	if err = r.checkpoint("after_public_commit"); err != nil {
		return err
	}
	return r.cleanupTransactionLocked()
}

func (r *Repository) recoverLocked() (bool, error) {
	manifestData, err := os.ReadFile(r.manifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, r.cleanupTransactionArtifactsLocked()
	}
	if err != nil {
		return false, fmt.Errorf("read configuration transaction: %w", err)
	}
	var manifest configTransactionManifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return false, fmt.Errorf("decode configuration transaction: %w", err)
	}
	if manifest.Version != configTransactionVersion {
		return false, fmt.Errorf("unsupported configuration transaction version %d", manifest.Version)
	}
	owner := NewRepository(manifest.PublicPath)
	if owner.path == "" || securityPath(owner.path) != securityPath(r.path) {
		return false, errors.New("configuration transaction public path is outside the locked directory")
	}
	publicPaths, securityPaths := owner.documentPaths()
	publicNext, err := documentMatches(publicPaths.target, manifest.Public.NextHash, true)
	if err != nil {
		return false, err
	}
	securityNext, err := documentMatches(securityPaths.target, manifest.Security.NextHash, true)
	if err != nil {
		return false, err
	}
	if publicNext && securityNext {
		return true, owner.cleanupTransactionLocked()
	}
	if err = restoreConfigDocument(securityPaths, manifest.Security); err != nil {
		return false, fmt.Errorf("restore security configuration: %w", err)
	}
	if err = restoreConfigDocument(publicPaths, manifest.Public); err != nil {
		return false, fmt.Errorf("restore public configuration: %w", err)
	}
	return false, owner.cleanupTransactionLocked()
}

func (r *Repository) checkpoint(name string) error {
	if r.hooks.checkpoint == nil {
		return nil
	}
	if err := r.hooks.checkpoint(name); err != nil {
		return fmt.Errorf("configuration transaction %s: %w", name, err)
	}
	return nil
}

func (r *Repository) manifestPath() string {
	return securityPath(r.path) + ".transaction"
}

func (r *Repository) documentPaths() (configDocumentPaths, configDocumentPaths) {
	transactionPath := r.manifestPath()
	return configDocumentPaths{
			target:   r.path,
			previous: transactionPath + ".public.previous",
			next:     transactionPath + ".public.next",
		}, configDocumentPaths{
			target:   securityPath(r.path),
			previous: transactionPath + ".security.previous",
			next:     transactionPath + ".security.next",
		}
}

func stageConfigDocument(paths configDocumentPaths, next []byte) (configTransactionDocument, error) {
	previous, previousExists, err := readOptionalFile(paths.target)
	if err != nil {
		return configTransactionDocument{}, err
	}
	if previousExists {
		if err = fileWriteAtomic(paths.previous, previous); err != nil {
			return configTransactionDocument{}, err
		}
	} else if err = removeFileDurable(paths.previous); err != nil {
		return configTransactionDocument{}, err
	}
	if err = fileWriteAtomic(paths.next, next); err != nil {
		return configTransactionDocument{}, err
	}
	return configTransactionDocument{
		PreviousExists: previousExists,
		PreviousHash:   hashBytes(previous),
		NextHash:       hashBytes(next),
	}, nil
}

func restoreConfigDocument(paths configDocumentPaths, document configTransactionDocument) error {
	if !document.PreviousExists {
		return removeFileDurable(paths.target)
	}
	previous, err := os.ReadFile(paths.previous)
	if err != nil {
		return err
	}
	if hashBytes(previous) != document.PreviousHash {
		return errors.New("staged previous document checksum mismatch")
	}
	return replaceConfigDocument(paths.target, previous)
}

func replaceConfigDocument(path string, data []byte) error {
	return fileWriteAtomic(path, data)
}

func fileWriteAtomic(path string, data []byte) error {
	return configWriteFileAtomic(path, data, 0o600)
}

func (r *Repository) cleanupTransactionLocked() error {
	if err := removeFileDurable(r.manifestPath()); err != nil {
		return err
	}
	if err := r.cleanupTransactionArtifactsLocked(); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	return nil
}

func (r *Repository) cleanupTransactionArtifactsLocked() error {
	publicPaths, securityPaths := r.documentPaths()
	var cleanupErrors []error
	for _, path := range []string{
		publicPaths.previous, publicPaths.next,
		securityPaths.previous, securityPaths.next,
	} {
		if err := removeFileDurable(path); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s: %w", filepath.Base(path), err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func removeFileDurable(path string) error {
	err := configRemoveFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = configSyncDirectory(filepath.Dir(path)); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	return nil
}

func revisionForPaths(publicPath, securityPath string) (Revision, error) {
	publicData, publicExists, err := readOptionalFile(publicPath)
	if err != nil {
		return "", err
	}
	securityData, securityExists, err := readOptionalFile(securityPath)
	if err != nil {
		return "", err
	}
	return revisionForData(publicData, publicExists, securityData, securityExists), nil
}

func revisionForData(publicData []byte, publicExists bool, securityData []byte, securityExists bool) Revision {
	hash := sha256.New()
	writeRevisionDocument := func(exists bool, data []byte) {
		if exists {
			_, _ = hash.Write([]byte{1})
		} else {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write(data)
	}
	writeRevisionDocument(publicExists, publicData)
	writeRevisionDocument(securityExists, securityData)
	return Revision(hex.EncodeToString(hash.Sum(nil)))
}

func documentMatches(path, expectedHash string, expectedExists bool) (bool, error) {
	data, exists, err := readOptionalFile(path)
	if err != nil {
		return false, err
	}
	return exists == expectedExists && hashBytes(data) == expectedHash, nil
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
