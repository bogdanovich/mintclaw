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
	"sort"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	DefaultAttachmentGCBlobLimit      = 100_000
	DefaultAttachmentGCCandidateLimit = 10_000
	attachmentGCManifestLimit         = 30_000
)

// AttachmentGCOptions bounds one mark-and-sweep pass. Before is an explicit
// retention cutoff. Delete=false performs the same fail-closed scan without
// removing bytes.
type AttachmentGCOptions struct {
	Before         time.Time
	BlobLimit      int
	CandidateLimit int
	Delete         bool
}

// AttachmentGCCandidate is one exact immutable blob selected for deletion.
type AttachmentGCCandidate struct {
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

// AttachmentGCResult is the bounded, machine-readable outcome of one pass.
type AttachmentGCResult struct {
	Before           time.Time               `json:"before"`
	ScannedManifests int                     `json:"scanned_manifests"`
	ReferencedBlobs  int                     `json:"referenced_blobs"`
	ScannedBlobs     int                     `json:"scanned_blobs"`
	Candidates       []AttachmentGCCandidate `json:"candidates"`
	CandidateBytes   int64                   `json:"candidate_bytes"`
	DeletedBlobs     int                     `json:"deleted_blobs"`
	DeletedBytes     int64                   `json:"deleted_bytes"`
}

// CommittedAttachmentGCError reports a pass which removed at least one blob
// before a later deletion or durability failure.
type CommittedAttachmentGCError struct {
	Result AttachmentGCResult
	Err    error
}

func (e *CommittedAttachmentGCError) Error() string {
	return fmt.Sprintf("coding attachment garbage collection committed partially: %v", e.Err)
}

func (e *CommittedAttachmentGCError) Unwrap() error { return e.Err }

// IsCommittedAttachmentGCError distinguishes a partial committed sweep from
// a fail-closed planning error which removed nothing.
func IsCommittedAttachmentGCError(err error) bool {
	var committed *CommittedAttachmentGCError
	return errors.As(err, &committed)
}

// CollectAttachmentGarbage marks every active and recoverable-trash manifest
// under one maintenance lease. It deletes only bounded, old, unreferenced
// blobs when Delete is true.
func (s *Store) CollectAttachmentGarbage(
	ctx context.Context,
	options AttachmentGCOptions,
) (result AttachmentGCResult, resultErr error) {
	if s == nil {
		return AttachmentGCResult{}, fmt.Errorf("coding attachment garbage collection: store is nil")
	}
	if ctx == nil {
		return AttachmentGCResult{}, fmt.Errorf("coding attachment garbage collection: context is required")
	}
	resolved, err := resolveAttachmentGCOptions(options)
	if err != nil {
		return AttachmentGCResult{}, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return AttachmentGCResult{}, contextErr
	}
	maintenance, err := s.acquireAttachmentMaintenanceLease()
	if err != nil {
		return AttachmentGCResult{}, err
	}
	defer func() {
		releaseErr := maintenance.Release()
		if releaseErr == nil {
			return
		}
		if result.DeletedBlobs > 0 {
			resultErr = &CommittedAttachmentGCError{
				Result: result,
				Err:    errors.Join(resultErr, fmt.Errorf("release maintenance lock: %w", releaseErr)),
			}
			return
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()

	marked, scannedManifests, err := s.markAttachmentGarbage(ctx)
	if err != nil {
		return AttachmentGCResult{}, err
	}
	result, err = s.planAttachmentGarbage(ctx, resolved, marked)
	result.ScannedManifests = scannedManifests
	result.ReferencedBlobs = len(marked)
	if err != nil || !resolved.Delete || len(result.Candidates) == 0 {
		return result, err
	}
	return s.sweepAttachmentGarbage(ctx, result)
}

func resolveAttachmentGCOptions(options AttachmentGCOptions) (AttachmentGCOptions, error) {
	if options.Before.IsZero() {
		return AttachmentGCOptions{}, fmt.Errorf("coding attachment garbage collection: retention cutoff is required")
	}
	options.Before = options.Before.UTC()
	if options.BlobLimit == 0 {
		options.BlobLimit = DefaultAttachmentGCBlobLimit
	}
	if options.CandidateLimit == 0 {
		options.CandidateLimit = DefaultAttachmentGCCandidateLimit
	}
	if options.BlobLimit < 1 || options.BlobLimit > DefaultAttachmentGCBlobLimit ||
		options.CandidateLimit < 1 || options.CandidateLimit > DefaultAttachmentGCCandidateLimit ||
		options.CandidateLimit > options.BlobLimit {
		return AttachmentGCOptions{}, fmt.Errorf("coding attachment garbage collection: invalid scan limits")
	}
	return options, nil
}

func (s *Store) markAttachmentGarbage(ctx context.Context) (map[string]struct{}, int, error) {
	root, err := openPinnedCatalogRoot(s.root)
	if err != nil {
		return nil, 0, fmt.Errorf("coding attachment garbage collection: pin store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	marked := make(map[string]struct{})
	count := 0
	for _, tree := range [][]string{
		{"threads"},
		{"trash", "threads"},
		{"trash", "fork-preparations"},
	} {
		treeCount, scanErr := scanAttachmentManifestTree(ctx, s, root, tree, marked)
		count += treeCount
		if scanErr != nil {
			return nil, count, scanErr
		}
		if count > attachmentGCManifestLimit {
			return nil, count, fmt.Errorf(
				"coding attachment garbage collection: manifest scan exceeds %d entries",
				attachmentGCManifestLimit,
			)
		}
	}
	if err := validatePinnedAttachmentRoot(s.root, root); err != nil {
		return nil, count, fmt.Errorf("coding attachment garbage collection: revalidate store root: %w", err)
	}
	return marked, count, nil
}

func scanAttachmentManifestTree(
	ctx context.Context,
	store *Store,
	storeRoot *os.Root,
	components []string,
	marked map[string]struct{},
) (int, error) {
	current := storeRoot
	opened := make([]*os.Root, 0, len(components))
	defer func() {
		for index := len(opened) - 1; index >= 0; index-- {
			_ = opened[index].Close()
		}
	}()
	for _, component := range components {
		child, err := openPinnedAttachmentChild(current, component)
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf(
				"coding attachment garbage collection: open manifest tree %q: %w",
				filepath.Join(components...),
				err,
			)
		}
		opened = append(opened, child)
		current = child
	}
	entries, err := readBoundedRootEntries(current, attachmentGCManifestLimit)
	if err != nil {
		return 0, fmt.Errorf("coding attachment garbage collection: list manifest tree: %w", err)
	}
	count := 0
	activeTree := len(components) == 1 && components[0] == "threads"
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if !filepath.IsLocal(entry.Name()) || entry.Name() == "." {
			return count, fmt.Errorf("coding attachment garbage collection: unsafe manifest directory name")
		}
		if activeTree {
			if err := validateThreadID(entry.Name()); err != nil {
				return count, fmt.Errorf("coding attachment garbage collection: invalid active thread entry: %w", err)
			}
		}
		threadRoot, err := openPinnedAttachmentChild(current, entry.Name())
		if err != nil {
			return count, fmt.Errorf("coding attachment garbage collection: open manifest owner: %w", err)
		}
		manifest, exists, loadErr := loadAttachmentGCManifest(store, threadRoot)
		closeErr := threadRoot.Close()
		if err := errors.Join(loadErr, closeErr); err != nil {
			return count, err
		}
		if !exists {
			continue
		}
		count++
		if activeTree && manifest.ThreadID != entry.Name() {
			return count, fmt.Errorf("coding attachment garbage collection: active manifest owner mismatch")
		}
		for _, attachment := range manifest.Entries {
			marked[attachment.SHA256] = struct{}{}
		}
	}
	for index, component := range components {
		parent := storeRoot
		if index > 0 {
			parent = opened[index-1]
		}
		if err := validatePinnedAttachmentDirectory(parent, component, opened[index]); err != nil {
			return count, fmt.Errorf("coding attachment garbage collection: manifest tree changed: %w", err)
		}
	}
	return count, nil
}

func loadAttachmentGCManifest(
	store *Store,
	threadRoot *os.Root,
) (attachmentManifestFile, bool, error) {
	hierarchy, err := store.openAttachmentHierarchy(threadRoot, false, attachmentDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return attachmentManifestFile{}, false, nil
	}
	if err != nil {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: open manifest directory: %w",
			err,
		)
	}
	defer func() { _ = hierarchy.Close() }()
	data, err := readAttachmentManifestData(hierarchy.Leaf())
	if errors.Is(err, os.ErrNotExist) {
		return attachmentManifestFile{}, false, nil
	}
	if err != nil {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: read manifest: %w",
			err,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest attachmentManifestFile
	if err := decoder.Decode(&manifest); err != nil {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: decode manifest: %w",
			err,
		)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: manifest has trailing data",
		)
	}
	if err := validateAttachmentManifest(manifest.ThreadID, manifest); err != nil {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: invalid manifest: %w",
			err,
		)
	}
	if err := hierarchy.validate(); err != nil {
		return attachmentManifestFile{}, false, fmt.Errorf(
			"coding attachment garbage collection: manifest hierarchy changed: %w",
			err,
		)
	}
	return manifest, true, nil
}

func (s *Store) planAttachmentGarbage(
	ctx context.Context,
	options AttachmentGCOptions,
	marked map[string]struct{},
) (AttachmentGCResult, error) {
	result := AttachmentGCResult{Before: options.Before, Candidates: []AttachmentGCCandidate{}}
	root, err := openPinnedCatalogRoot(s.root)
	if err != nil {
		return result, fmt.Errorf("coding attachment garbage collection: pin blob root: %w", err)
	}
	defer func() { _ = root.Close() }()
	blobs, exists, err := openOptionalPinnedAttachmentChild(root, "blobs")
	if err != nil || !exists {
		return result, err
	}
	defer func() { _ = blobs.Close() }()
	shaRoot, exists, err := openOptionalPinnedAttachmentChild(blobs, "sha256")
	if err != nil || !exists {
		return result, err
	}
	defer func() { _ = shaRoot.Close() }()
	shards, err := readBoundedRootEntries(shaRoot, 256)
	if err != nil {
		return result, fmt.Errorf("coding attachment garbage collection: list blob shards: %w", err)
	}
	for _, shardEntry := range shards {
		shard := shardEntry.Name()
		if len(shard) != 2 || !isHex(shard) || strings.ToLower(shard) != shard || shard != filepath.Base(shard) {
			return result, fmt.Errorf("coding attachment garbage collection: invalid blob shard %q", shard)
		}
		shardRoot, err := openPinnedAttachmentChild(shaRoot, shard)
		if err != nil {
			return result, fmt.Errorf("coding attachment garbage collection: open blob shard %q: %w", shard, err)
		}
		remaining := options.BlobLimit - result.ScannedBlobs
		entries, listErr := readBoundedRootEntries(shardRoot, remaining)
		if listErr != nil {
			_ = shardRoot.Close()
			return result, fmt.Errorf("coding attachment garbage collection: list blob shard %q: %w", shard, listErr)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				_ = shardRoot.Close()
				return result, err
			}
			digest := entry.Name()
			if validateAttachmentDigest(digest) != nil || digest[:2] != shard {
				_ = shardRoot.Close()
				return result, fmt.Errorf("coding attachment garbage collection: invalid blob entry %q", digest)
			}
			info, err := validateAttachmentGCBlob(shardRoot, digest)
			if err != nil {
				_ = shardRoot.Close()
				return result, err
			}
			result.ScannedBlobs++
			if _, referenced := marked[digest]; referenced || !info.ModTime().Before(options.Before) {
				continue
			}
			if len(result.Candidates) >= options.CandidateLimit {
				_ = shardRoot.Close()
				return result, fmt.Errorf(
					"coding attachment garbage collection: candidates exceed %d entries",
					options.CandidateLimit,
				)
			}
			result.Candidates = append(result.Candidates, AttachmentGCCandidate{
				SHA256: digest, Size: info.Size(), ModifiedAt: info.ModTime().UTC(),
			})
			result.CandidateBytes += info.Size()
		}
		if err := validatePinnedAttachmentDirectory(shaRoot, shard, shardRoot); err != nil {
			_ = shardRoot.Close()
			return result, fmt.Errorf("coding attachment garbage collection: blob shard changed: %w", err)
		}
		if err := shardRoot.Close(); err != nil {
			return result, err
		}
	}
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return result, fmt.Errorf("coding attachment garbage collection: SHA root changed: %w", err)
	}
	if err := validatePinnedAttachmentDirectory(root, "blobs", blobs); err != nil {
		return result, fmt.Errorf("coding attachment garbage collection: blob root changed: %w", err)
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		return result.Candidates[left].SHA256 < result.Candidates[right].SHA256
	})
	return result, nil
}

func (s *Store) sweepAttachmentGarbage(
	ctx context.Context,
	result AttachmentGCResult,
) (AttachmentGCResult, error) {
	root, err := openPinnedCatalogRoot(s.root)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	blobs, exists, err := openOptionalPinnedAttachmentChild(root, "blobs")
	if err != nil || !exists {
		return classifyAttachmentGCSweep(result, errors.Join(err, os.ErrNotExist))
	}
	defer func() { _ = blobs.Close() }()
	shaRoot, exists, err := openOptionalPinnedAttachmentChild(blobs, "sha256")
	if err != nil || !exists {
		return classifyAttachmentGCSweep(result, errors.Join(err, os.ErrNotExist))
	}
	defer func() { _ = shaRoot.Close() }()
	touched := make(map[string]*os.Root)
	defer func() {
		for _, shard := range touched {
			_ = shard.Close()
		}
	}()
	for _, candidate := range result.Candidates {
		if contextErr := ctx.Err(); contextErr != nil {
			return classifyAttachmentGCSweep(result, contextErr)
		}
		shardName := candidate.SHA256[:2]
		shard := touched[shardName]
		if shard == nil {
			shard, err = openPinnedAttachmentChild(shaRoot, shardName)
			if err != nil {
				return classifyAttachmentGCSweep(result, err)
			}
			touched[shardName] = shard
		}
		info, err := validateAttachmentGCBlob(shard, candidate.SHA256)
		if err != nil {
			return classifyAttachmentGCSweep(result, err)
		}
		if info.Size() != candidate.Size || !info.ModTime().UTC().Equal(candidate.ModifiedAt) {
			return classifyAttachmentGCSweep(
				result,
				fmt.Errorf("coding attachment garbage collection: candidate changed before deletion"),
			)
		}
		if err := shard.Remove(candidate.SHA256); err != nil {
			return classifyAttachmentGCSweep(result, err)
		}
		result.DeletedBlobs++
		result.DeletedBytes += candidate.Size
	}
	for name, shard := range touched {
		if err := validatePinnedAttachmentDirectory(shaRoot, name, shard); err != nil {
			return classifyAttachmentGCSweep(result, err)
		}
		if err := s.syncRoot(shard); err != nil {
			return classifyAttachmentGCSweep(
				result,
				&fileutil.CommittedWriteError{Err: fmt.Errorf("sync blob deletion: %w", err)},
			)
		}
	}
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return classifyAttachmentGCSweep(result, err)
	}
	if err := validatePinnedAttachmentDirectory(root, "blobs", blobs); err != nil {
		return classifyAttachmentGCSweep(result, err)
	}
	return result, nil
}

func classifyAttachmentGCSweep(result AttachmentGCResult, err error) (AttachmentGCResult, error) {
	if result.DeletedBlobs == 0 {
		return result, err
	}
	return result, &CommittedAttachmentGCError{Result: result, Err: err}
}

func openOptionalPinnedAttachmentChild(parent *os.Root, name string) (*os.Root, bool, error) {
	child, err := openPinnedAttachmentChild(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("coding attachment garbage collection: open %q: %w", name, err)
	}
	return child, true, nil
}

func readBoundedRootEntries(root *os.Root, limit int) ([]os.DirEntry, error) {
	if root == nil || limit < 0 {
		return nil, fmt.Errorf("bounded directory reader requires a root and non-negative limit")
	}
	reader, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	before, statErr := reader.Stat()
	entries, readErr := reader.ReadDir(limit + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	after, restatErr := reader.Stat()
	closeErr := reader.Close()
	if operationErr := errors.Join(statErr, readErr, restatErr, closeErr); operationErr != nil {
		return nil, operationErr
	}
	if len(entries) > limit {
		return nil, fmt.Errorf("directory exceeds %d entries", limit)
	}
	current, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !os.SameFile(before, current) || before.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("directory changed while reading")
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	return entries, nil
}

func validateAttachmentGCBlob(root *os.Root, name string) (os.FileInfo, error) {
	entry, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return nil, fmt.Errorf("coding attachment garbage collection: blob %q is not a direct regular file", name)
	}
	file, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if validationErr := validateCatalogMetadataFile(file, info); validationErr != nil {
		return nil, fmt.Errorf(
			"coding attachment garbage collection: unsafe blob %q: %w",
			name,
			validationErr,
		)
	}
	if info.Size() < 0 || info.Size() > MaxAttachmentBytes {
		return nil, fmt.Errorf(
			"coding attachment garbage collection: blob %q exceeds %d bytes",
			name,
			MaxAttachmentBytes,
		)
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(entry, info) || !os.SameFile(entry, current) || entry.Size() != info.Size() ||
		entry.ModTime() != info.ModTime() {
		return nil, fmt.Errorf("coding attachment garbage collection: blob %q changed while opening", name)
	}
	return info, nil
}
