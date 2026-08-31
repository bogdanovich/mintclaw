package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	repositoryDirectory        = "repository"
	repositoryBaselineFileName = "baseline.json"
	MaxRepositoryBaselineBytes = 256 << 10
)

var ErrRepositoryBaselineExists = errors.New("coding thread repository baseline already exists")

func (s *Store) PublishRepositoryBaseline(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	baseline workspace.RepositoryBaseline,
) error {
	if s == nil {
		return fmt.Errorf("coding thread repository baseline store is nil")
	}
	if ctx == nil {
		return fmt.Errorf("coding thread repository baseline: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	if err := validateRepositoryBaseline(metadata, baseline); err != nil {
		return err
	}
	data, err := encodeRepositoryBaseline(baseline)
	if err != nil {
		return err
	}
	return lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return fmt.Errorf("coding thread repository baseline: pin thread: %w", err)
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return fmt.Errorf("coding thread repository baseline: validate writer: %w", writerErr)
		}
		hierarchy, err := s.openAttachmentHierarchy(view.thread, true, repositoryDirectory)
		if err != nil {
			return fmt.Errorf("coding thread repository baseline: create repository directory: %w", err)
		}
		defer func() { _ = hierarchy.Close() }()
		if err := view.validateHierarchy(hierarchy); err != nil {
			return fmt.Errorf("coding thread repository baseline: validate directory: %w", err)
		}
		root := hierarchy.Leaf()
		if _, err := root.Lstat(repositoryBaselineFileName); err == nil {
			return ErrRepositoryBaselineExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("coding thread repository baseline: inspect destination: %w", err)
		}
		if err := writeRootFileExclusiveAtomic(root, repositoryBaselineFileName, data, 0o600); err != nil {
			return fmt.Errorf("coding thread repository baseline: publish: %w", err)
		}
		if err := errors.Join(view.validateWriter(lease), view.validateHierarchy(hierarchy)); err != nil {
			return &fileutil.CommittedWriteError{Err: fmt.Errorf("validate published baseline authority: %w", err)}
		}
		return nil
	})
}

func (s *Store) LoadRepositoryBaseline(threadID string) (workspace.RepositoryBaseline, error) {
	if s == nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("coding thread repository baseline store is nil")
	}
	view, err := s.openAttachmentStoreView(threadID)
	if err != nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("coding thread repository baseline: pin thread: %w", err)
	}
	defer func() { _ = view.Close() }()
	metadata, err := loadRepositoryBaselineMetadata(view.thread, threadID)
	if err != nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("coding thread repository baseline: load metadata: %w", err)
	}
	baseline, err := loadRepositoryBaselineFromRoot(view.thread, threadID)
	if err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	if err := validateRepositoryBaseline(metadata, baseline); err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	return baseline, nil
}

func loadRepositoryBaselineMetadata(threadRoot *os.Root, threadID string) (Metadata, error) {
	entry, err := threadRoot.Lstat(metadataFileName)
	if err != nil {
		return Metadata{}, err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return Metadata{}, fmt.Errorf("metadata path is not a direct regular file")
	}
	file, err := threadRoot.OpenFile(metadataFileName, os.O_RDONLY, 0)
	if err != nil {
		return Metadata{}, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Metadata{}, err
	}
	if err := validateCatalogMetadataFile(file, opened); err != nil {
		_ = file.Close()
		return Metadata{}, err
	}
	if !os.SameFile(entry, opened) {
		_ = file.Close()
		return Metadata{}, fmt.Errorf("metadata path changed while opening")
	}
	return loadMetadataFile(threadID, file)
}

func (s *Store) LoadRepositoryBaselineWithLease(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
) (workspace.RepositoryBaseline, error) {
	if s == nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("coding thread repository baseline store is nil")
	}
	if ctx == nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("coding thread repository baseline: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	var baseline workspace.RepositoryBaseline
	err := lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return err
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return writerErr
		}
		baseline, err = loadRepositoryBaselineFromRoot(view.thread, metadata.ThreadID)
		if err != nil {
			return err
		}
		return validateRepositoryBaseline(metadata, baseline)
	})
	return baseline, err
}

func loadRepositoryBaselineFromRoot(
	threadRoot *os.Root,
	threadID string,
) (workspace.RepositoryBaseline, error) {
	repositoryRoot, err := openPinnedAttachmentChild(threadRoot, repositoryDirectory)
	if err != nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("open repository directory: %w", err)
	}
	defer func() { _ = repositoryRoot.Close() }()
	entry, err := repositoryRoot.Lstat(repositoryBaselineFileName)
	if err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return workspace.RepositoryBaseline{}, fmt.Errorf("baseline path is not a direct regular file")
	}
	file, err := repositoryRoot.OpenFile(repositoryBaselineFileName, os.O_RDONLY, 0)
	if err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxRepositoryBaselineBytes+1))
	opened, statErr := file.Stat()
	var validationErr error
	if statErr == nil {
		validationErr = validateCatalogMetadataFile(file, opened)
	}
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, validationErr, closeErr); err != nil {
		return workspace.RepositoryBaseline{}, err
	}
	if !os.SameFile(entry, opened) {
		return workspace.RepositoryBaseline{}, fmt.Errorf("baseline path changed while opening")
	}
	if len(data) > MaxRepositoryBaselineBytes {
		return workspace.RepositoryBaseline{}, fmt.Errorf("baseline exceeds %d bytes", MaxRepositoryBaselineBytes)
	}
	var baseline workspace.RepositoryBaseline
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&baseline); err != nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("decode baseline for thread %q: %w", threadID, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return workspace.RepositoryBaseline{}, fmt.Errorf(
			"decode baseline for thread %q: trailing JSON content",
			threadID,
		)
	}
	if err := baseline.Validate(); err != nil {
		return workspace.RepositoryBaseline{}, fmt.Errorf("validate baseline for thread %q: %w", threadID, err)
	}
	return baseline, nil
}

func validateRepositoryBaseline(metadata Metadata, baseline workspace.RepositoryBaseline) error {
	if err := baseline.Validate(); err != nil {
		return err
	}
	if baseline.ProjectKey != metadata.Project.ProjectKey {
		return fmt.Errorf("coding thread repository baseline: project key does not match thread")
	}
	if metadata.Project.Kind == ProjectKindGitWorktree &&
		(baseline.TopLevel != metadata.Project.GitWorktreeRoot || baseline.CommonDir != metadata.Project.GitCommonDir) {
		return fmt.Errorf("coding thread repository baseline: Git authority does not match thread")
	}
	return nil
}

func encodeRepositoryBaseline(baseline workspace.RepositoryBaseline) ([]byte, error) {
	if err := baseline.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > MaxRepositoryBaselineBytes {
		return nil, fmt.Errorf("coding thread repository baseline exceeds %d bytes", MaxRepositoryBaselineBytes)
	}
	return data, nil
}

func writeRootFileExclusiveAtomic(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if root == nil || !filepath.IsLocal(name) {
		return fmt.Errorf("pinned root and local file name are required")
	}
	temporary := ".tmp-baseline-" + NewThreadID()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = file.Close()
			_ = root.Remove(temporary)
		}
	}()
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Link(temporary, name); err != nil {
		return err
	}
	removeErr := root.Remove(temporary)
	if removeErr == nil {
		cleanup = false
	}
	syncErr := syncRootDirectory(root)
	if err := errors.Join(removeErr, syncErr); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	return nil
}
