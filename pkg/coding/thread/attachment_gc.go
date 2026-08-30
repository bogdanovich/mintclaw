package thread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	DefaultAttachmentGCBlobLimit      = 100_000
	DefaultAttachmentGCCandidateLimit = 10_000
	attachmentGCManifestLimit         = 30_000
	attachmentGCMarkedBlobLimit       = DefaultAttachmentGCBlobLimit
	attachmentGCQuarantinePrefix      = ".gc-"
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
	empty := AttachmentGCResult{Before: resolved.Before, Candidates: []AttachmentGCCandidate{}}
	if _, statErr := os.Lstat(s.root); errors.Is(statErr, os.ErrNotExist) {
		return empty, nil
	} else if statErr != nil {
		return AttachmentGCResult{}, fmt.Errorf(
			"coding attachment garbage collection: inspect store root: %w",
			statErr,
		)
	}
	session, err := s.acquireAttachmentGCSession()
	if err != nil {
		return AttachmentGCResult{}, err
	}
	defer func() {
		closeErr := session.Close()
		if closeErr == nil {
			return
		}
		if result.DeletedBlobs > 0 {
			resultErr = &CommittedAttachmentGCError{
				Result: result,
				Err:    errors.Join(resultErr, fmt.Errorf("close garbage-collection session: %w", closeErr)),
			}
			return
		}
		resultErr = errors.Join(resultErr, closeErr)
	}()

	if recoveryErr := s.recoverAttachmentGCQuarantine(ctx, session); recoveryErr != nil {
		return AttachmentGCResult{}, recoveryErr
	}
	marked, scannedManifests, err := s.markAttachmentGarbage(ctx, session.root, resolved.Delete)
	if err != nil {
		return AttachmentGCResult{}, err
	}
	result, err = s.planAttachmentGarbage(ctx, session.root, resolved, marked)
	result.ScannedManifests = scannedManifests
	result.ReferencedBlobs = len(marked)
	if err != nil || !resolved.Delete || len(result.Candidates) == 0 {
		return result, err
	}
	return s.sweepAttachmentGarbage(ctx, session, result)
}

type attachmentGCSession struct {
	root            *os.Root
	maintenanceRoot *os.Root
	maintenance     *attachmentMaintenanceLease
}

func (s *Store) acquireAttachmentGCSession() (*attachmentGCSession, error) {
	maintenance, err := s.acquireAttachmentMaintenanceLease()
	if err != nil {
		return nil, err
	}
	session := &attachmentGCSession{
		root:            maintenance.root,
		maintenanceRoot: maintenance.directory,
		maintenance:     maintenance,
	}
	if err := session.validate(s.root); err != nil {
		return nil, errors.Join(err, session.Close())
	}
	return session, nil
}

func (s *attachmentGCSession) validate(storeRoot string) error {
	if s == nil || s.root == nil || s.maintenanceRoot == nil || s.maintenance == nil {
		return fmt.Errorf("coding attachment garbage collection: session is incomplete")
	}
	if s.maintenance.storeRoot != storeRoot {
		return fmt.Errorf("coding attachment garbage collection: maintenance store identity changed")
	}
	if err := s.maintenance.Validate(); err != nil {
		return fmt.Errorf("coding attachment garbage collection: validate maintenance authority: %w", err)
	}
	return nil
}

func (s *attachmentGCSession) Close() error {
	if s == nil {
		return nil
	}
	return s.maintenance.Release()
}

func resolveAttachmentGCOptions(options AttachmentGCOptions) (AttachmentGCOptions, error) {
	if options.Before.IsZero() {
		return AttachmentGCOptions{}, fmt.Errorf("coding attachment garbage collection: retention cutoff is required")
	}
	options.Before = options.Before.UTC()
	if options.Before.After(time.Now().UTC()) {
		return AttachmentGCOptions{}, fmt.Errorf(
			"coding attachment garbage collection: retention cutoff cannot be future",
		)
	}
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

func (s *Store) markAttachmentGarbage(
	ctx context.Context,
	root *os.Root,
	recoverInflight bool,
) (map[string]struct{}, int, error) {
	requireActive, err := attachmentGCBlobStoreExists(root)
	if err != nil {
		return nil, 0, err
	}
	marked := make(map[string]struct{})
	count := 0
	for index, tree := range [][]string{
		{"threads"},
		{"trash", "threads"},
		{"trash", "fork-preparations"},
	} {
		treeCount, scanErr := scanAttachmentManifestTree(
			ctx,
			s,
			root,
			tree,
			index == 0 && requireActive,
			attachmentGCManifestLimit-count,
			attachmentGCMarkedBlobLimit,
			marked,
		)
		count += treeCount
		if scanErr != nil {
			return nil, count, scanErr
		}
	}
	inflightCount, err := s.scanAttachmentInflight(
		ctx,
		root,
		recoverInflight,
		attachmentGCManifestLimit-count,
		attachmentGCMarkedBlobLimit,
		marked,
	)
	count += inflightCount
	if err != nil {
		return nil, count, err
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
	required bool,
	manifestLimit int,
	markedBlobLimit int,
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
			if required {
				return 0, fmt.Errorf(
					"coding attachment garbage collection: required manifest authority %q is missing",
					filepath.Join(components...),
				)
			}
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
	entries, err := readBoundedRootEntries(current, manifestLimit)
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
			if _, exists := marked[attachment.SHA256]; !exists && len(marked) >= markedBlobLimit {
				return count, fmt.Errorf(
					"coding attachment garbage collection: referenced blobs exceed %d entries",
					markedBlobLimit,
				)
			}
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

func attachmentGCBlobStoreExists(root *os.Root) (bool, error) {
	blobs, exists, err := openOptionalPinnedAttachmentChild(root, "blobs")
	if err != nil || !exists {
		return false, err
	}
	defer func() { _ = blobs.Close() }()
	shaRoot, exists, err := openOptionalPinnedAttachmentChild(blobs, "sha256")
	if err != nil || !exists {
		return false, err
	}
	defer func() { _ = shaRoot.Close() }()
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return false, err
	}
	if err := validatePinnedAttachmentDirectory(root, "blobs", blobs); err != nil {
		return false, err
	}
	return true, nil
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
	root *os.Root,
	options AttachmentGCOptions,
	marked map[string]struct{},
) (AttachmentGCResult, error) {
	result := AttachmentGCResult{Before: options.Before, Candidates: []AttachmentGCCandidate{}}
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
			info, err := validateAttachmentGCBlob(ctx, shardRoot, digest, digest)
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
	if err := validatePinnedAttachmentRoot(s.root, root); err != nil {
		return result, fmt.Errorf("coding attachment garbage collection: store root changed: %w", err)
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		return result.Candidates[left].SHA256 < result.Candidates[right].SHA256
	})
	return result, nil
}

func (s *Store) sweepAttachmentGarbage(
	ctx context.Context,
	session *attachmentGCSession,
	result AttachmentGCResult,
) (AttachmentGCResult, error) {
	if err := session.validate(s.root); err != nil {
		return result, err
	}
	root := session.root
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
		if validationErr := session.validate(s.root); validationErr != nil {
			return classifyAttachmentGCSweep(result, validationErr)
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
		deleted, deleteErr := s.deleteAttachmentGCCandidate(
			ctx,
			session,
			blobs,
			shaRoot,
			shardName,
			shard,
			candidate,
		)
		if deleted {
			result.DeletedBlobs++
			result.DeletedBytes += candidate.Size
		}
		if deleteErr != nil {
			return classifyAttachmentGCSweep(result, deleteErr)
		}
	}
	for name, shard := range touched {
		if err := validatePinnedAttachmentDirectory(shaRoot, name, shard); err != nil {
			return classifyAttachmentGCSweep(result, err)
		}
	}
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return classifyAttachmentGCSweep(result, err)
	}
	if err := validatePinnedAttachmentDirectory(root, "blobs", blobs); err != nil {
		return classifyAttachmentGCSweep(result, err)
	}
	if err := session.validate(s.root); err != nil {
		return classifyAttachmentGCSweep(result, err)
	}
	return result, nil
}

func (s *Store) deleteAttachmentGCCandidate(
	ctx context.Context,
	session *attachmentGCSession,
	blobs *os.Root,
	shaRoot *os.Root,
	shardName string,
	shard *os.Root,
	candidate AttachmentGCCandidate,
) (deleted bool, resultErr error) {
	owner, blobErr := acquireAttachmentGCQuarantineOwnership(
		ctx,
		shard,
		candidate.SHA256,
		candidate.SHA256,
		false,
	)
	if blobErr != nil {
		return false, blobErr
	}
	defer func() {
		closeErr := owner.Release()
		if closeErr == nil {
			return
		}
		wrapped := fmt.Errorf("release coding attachment garbage-collection ownership: %w", closeErr)
		if deleted {
			resultErr = &fileutil.CommittedWriteError{Err: errors.Join(resultErr, wrapped)}
			return
		}
		resultErr = errors.Join(resultErr, wrapped)
	}()
	info := owner.info
	if info.Size() != candidate.Size || !info.ModTime().UTC().Equal(candidate.ModifiedAt) {
		return false, fmt.Errorf("coding attachment garbage collection: candidate changed before deletion")
	}
	if authorityErr := validateAttachmentGCCommitAuthority(
		s,
		session,
		blobs,
		shaRoot,
		shardName,
		shard,
		candidate.SHA256,
		info,
	); authorityErr != nil {
		return false, authorityErr
	}
	if s.afterAttachmentGCCommitValidation != nil {
		s.afterAttachmentGCCommitValidation()
	}
	quarantineName := attachmentGCQuarantineName(candidate.SHA256)
	if linkErr := shard.Link(candidate.SHA256, quarantineName); linkErr != nil {
		return false, linkErr
	}
	quarantined, quarantineErr := validateAttachmentGCLinkedBlob(ctx, shard, quarantineName, candidate.SHA256)
	validationErr := quarantineErr
	if validationErr == nil && !os.SameFile(info, quarantined) {
		validationErr = fmt.Errorf("coding attachment garbage collection: detached candidate changed identity")
	}
	if validationErr != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(
			fmt.Errorf("coding attachment garbage collection: validate detached candidate: %w", validationErr),
			restoreErr,
		)
	}
	if err := s.syncRoot(shard); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(fmt.Errorf("sync detached candidate: %w", err), restoreErr)
	}
	if err := validateAttachmentGCCandidateIdentity(shard, candidate.SHA256, info); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(err, restoreErr)
	}
	if err := shard.Remove(candidate.SHA256); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(err, restoreErr)
	}
	if err := s.syncRoot(shard); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(fmt.Errorf("sync detached candidate: %w", err), restoreErr)
	}
	if s.afterAttachmentGCQuarantinePublish != nil {
		s.afterAttachmentGCQuarantinePublish()
	}
	if _, err := validateAttachmentGCBlob(ctx, shard, quarantineName, candidate.SHA256); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(err, restoreErr)
	}
	inflight, inflightErr := s.attachmentInflightDigestPresent(ctx, session.root, candidate.SHA256)
	if inflightErr != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(inflightErr, restoreErr)
	}
	if inflight {
		return false, s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
	}
	if err := session.validate(s.root); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(err, restoreErr)
	}
	if err := shard.Remove(quarantineName); err != nil {
		restoreErr := s.restoreAttachmentGCQuarantine(
			ctx,
			shard,
			candidate.SHA256,
			quarantineName,
		)
		return false, errors.Join(err, restoreErr)
	}
	if err := s.syncRoot(shard); err != nil {
		return true, &fileutil.CommittedWriteError{Err: fmt.Errorf("sync quarantined blob deletion: %w", err)}
	}
	return true, nil
}

// attachmentGCQuarantineOwnership binds a live collector to the blob inode it
// may publish under a .gc-* name. The OS releases the lock if the process
// exits, allowing a later collector to distinguish crash residue from live
// quarantine without trusting a pathname or process identifier.
type attachmentGCQuarantineOwnership struct {
	file *os.File
	info os.FileInfo
}

func acquireAttachmentGCQuarantineOwnership(
	ctx context.Context,
	root *os.Root,
	name string,
	expectedDigest string,
	allowMultipleLinks bool,
) (*attachmentGCQuarantineOwnership, error) {
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
	closeOnError := func(operationErr error) (*attachmentGCQuarantineOwnership, error) {
		return nil, errors.Join(operationErr, file.Close())
	}
	if lockErr := tryAcquireThreadLeaseFile(file); lockErr != nil {
		return closeOnError(fmt.Errorf("coding attachment garbage-collection quarantine is busy: %w", lockErr))
	}
	releaseOnError := func(operationErr error) (*attachmentGCQuarantineOwnership, error) {
		return nil, errors.Join(operationErr, releaseThreadLeaseFile(file), file.Close())
	}
	info, err := validateAttachmentGCOpenedBlob(
		ctx,
		root,
		name,
		expectedDigest,
		allowMultipleLinks,
		entry,
		file,
	)
	if err != nil {
		return releaseOnError(err)
	}
	return &attachmentGCQuarantineOwnership{file: file, info: info}, nil
}

func (o *attachmentGCQuarantineOwnership) Release() error {
	if o == nil || o.file == nil {
		return nil
	}
	file := o.file
	o.file = nil
	return errors.Join(releaseThreadLeaseFile(file), file.Close())
}

func validateAttachmentGCCommitAuthority(
	store *Store,
	session *attachmentGCSession,
	blobs *os.Root,
	shaRoot *os.Root,
	shardName string,
	shard *os.Root,
	digest string,
	info os.FileInfo,
) error {
	if err := session.validate(store.root); err != nil {
		return err
	}
	if err := validatePinnedAttachmentDirectory(session.root, "blobs", blobs); err != nil {
		return err
	}
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return err
	}
	if err := validatePinnedAttachmentDirectory(shaRoot, shardName, shard); err != nil {
		return err
	}
	return validateAttachmentGCCandidateIdentity(shard, digest, info)
}

func attachmentGCQuarantineName(digest string) string {
	return attachmentGCQuarantinePrefix + digest + "-" + uuid.NewString()
}

func parseAttachmentGCQuarantineName(name string) (string, error) {
	if len(name) != len(attachmentGCQuarantinePrefix)+sha256.Size*2+1+36 ||
		!strings.HasPrefix(name, attachmentGCQuarantinePrefix) {
		return "", fmt.Errorf("coding attachment garbage collection: invalid quarantine entry %q", name)
	}
	digestStart := len(attachmentGCQuarantinePrefix)
	digestEnd := digestStart + sha256.Size*2
	digest := name[digestStart:digestEnd]
	if validateAttachmentDigest(digest) != nil || name[digestEnd] != '-' {
		return "", fmt.Errorf("coding attachment garbage collection: invalid quarantine entry %q", name)
	}
	idText := name[digestEnd+1:]
	id, err := uuid.Parse(idText)
	if err != nil || id.String() != idText {
		return "", fmt.Errorf("coding attachment garbage collection: invalid quarantine entry %q", name)
	}
	return digest, nil
}

func (s *Store) recoverAttachmentGCQuarantine(ctx context.Context, session *attachmentGCSession) error {
	blobs, exists, err := openOptionalPinnedAttachmentChild(session.root, "blobs")
	if err != nil || !exists {
		return err
	}
	defer func() { _ = blobs.Close() }()
	shaRoot, exists, err := openOptionalPinnedAttachmentChild(blobs, "sha256")
	if err != nil || !exists {
		return err
	}
	defer func() { _ = shaRoot.Close() }()
	shardEntries, err := readBoundedRootEntries(shaRoot, 256)
	if err != nil {
		return fmt.Errorf("coding attachment garbage collection: list recovery shards: %w", err)
	}
	quarantineCount := 0
	totalEntries := 0
	for _, shardEntry := range shardEntries {
		shardName := shardEntry.Name()
		if len(shardName) != 2 || !isHex(shardName) || strings.ToLower(shardName) != shardName {
			return fmt.Errorf("coding attachment garbage collection: invalid blob shard %q", shardName)
		}
		shard, err := openPinnedAttachmentChild(shaRoot, shardName)
		if err != nil {
			return err
		}
		remaining := DefaultAttachmentGCBlobLimit + DefaultAttachmentGCCandidateLimit - totalEntries
		entries, readErr := readBoundedRootEntries(shard, remaining)
		if readErr != nil {
			_ = shard.Close()
			return fmt.Errorf("coding attachment garbage collection: list recovery shard: %w", readErr)
		}
		totalEntries += len(entries)
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), attachmentGCQuarantinePrefix) {
				continue
			}
			if err := ctx.Err(); err != nil {
				_ = shard.Close()
				return err
			}
			quarantineCount++
			if quarantineCount > DefaultAttachmentGCCandidateLimit {
				_ = shard.Close()
				return fmt.Errorf(
					"coding attachment garbage collection: recovery quarantine exceeds %d entries",
					DefaultAttachmentGCCandidateLimit,
				)
			}
			digest, parseErr := parseAttachmentGCQuarantineName(entry.Name())
			if parseErr != nil || digest[:2] != shardName {
				_ = shard.Close()
				return errors.Join(
					parseErr,
					fmt.Errorf("coding attachment garbage collection: quarantine shard mismatch"),
				)
			}
			if err := session.validate(s.root); err != nil {
				_ = shard.Close()
				return err
			}
			owner, ownershipErr := acquireAttachmentGCQuarantineOwnership(
				ctx,
				shard,
				entry.Name(),
				digest,
				true,
			)
			if ownershipErr != nil {
				_ = shard.Close()
				return fmt.Errorf(
					"coding attachment garbage collection: claim recovery quarantine: %w",
					ownershipErr,
				)
			}
			if err := s.restoreAttachmentGCQuarantine(
				ctx,
				shard,
				digest,
				entry.Name(),
			); err != nil {
				releaseErr := owner.Release()
				_ = shard.Close()
				return fmt.Errorf(
					"coding attachment garbage collection: recover detached blob: %w",
					errors.Join(err, releaseErr),
				)
			}
			if releaseErr := owner.Release(); releaseErr != nil {
				_ = shard.Close()
				return fmt.Errorf(
					"coding attachment garbage collection: release recovery quarantine: %w",
					releaseErr,
				)
			}
		}
		if err := validatePinnedAttachmentDirectory(shaRoot, shardName, shard); err != nil {
			_ = shard.Close()
			return err
		}
		if err := shard.Close(); err != nil {
			return err
		}
	}
	if err := validatePinnedAttachmentDirectory(blobs, "sha256", shaRoot); err != nil {
		return err
	}
	if err := validatePinnedAttachmentDirectory(session.root, "blobs", blobs); err != nil {
		return err
	}
	return session.validate(s.root)
}

func (s *Store) restoreAttachmentGCQuarantine(
	ctx context.Context,
	shard *os.Root,
	digest string,
	quarantineName string,
) error {
	quarantined, err := validateAttachmentGCLinkedBlob(ctx, shard, quarantineName, digest)
	if err != nil {
		return err
	}
	canonical, canonicalErr := shard.Lstat(digest)
	linked := false
	if errors.Is(canonicalErr, os.ErrNotExist) {
		linkErr := shard.Link(quarantineName, digest)
		if linkErr == nil {
			linked = true
			canonical, canonicalErr = shard.Lstat(digest)
		} else if errors.Is(linkErr, os.ErrExist) {
			canonical, canonicalErr = shard.Lstat(digest)
		} else {
			return linkErr
		}
	}
	if canonicalErr != nil {
		return canonicalErr
	}
	if canonical.Mode()&os.ModeSymlink != 0 || !canonical.Mode().IsRegular() {
		return fmt.Errorf("coding attachment garbage collection: recovery target is not a direct regular file")
	}
	sameFile := os.SameFile(quarantined, canonical)
	if linked && !sameFile {
		return fmt.Errorf("coding attachment garbage collection: recovery link changed identity")
	}
	if !sameFile {
		if _, validationErr := validateAttachmentGCBlob(ctx, shard, digest, digest); validationErr != nil {
			return validationErr
		}
	}
	if syncErr := s.syncRoot(shard); syncErr != nil {
		return fmt.Errorf("sync restored canonical blob before quarantine cleanup: %w", syncErr)
	}
	if identityErr := validateAttachmentGCCandidateIdentity(shard, quarantineName, quarantined); identityErr != nil {
		return identityErr
	}
	if removeErr := shard.Remove(quarantineName); removeErr != nil {
		return removeErr
	}
	if syncErr := s.syncRoot(shard); syncErr != nil {
		return syncErr
	}
	_, err = validateAttachmentGCBlob(ctx, shard, digest, digest)
	return err
}

func validateAttachmentGCCandidateIdentity(root *os.Root, name string, pinned os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(pinned, current) || pinned.Size() != current.Size() ||
		pinned.ModTime() != current.ModTime() {
		return fmt.Errorf("coding attachment garbage collection: candidate changed before deletion")
	}
	return nil
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

func validateAttachmentGCBlob(
	ctx context.Context,
	root *os.Root,
	name string,
	expectedDigest string,
) (os.FileInfo, error) {
	return validateAttachmentGCBlobLinks(ctx, root, name, expectedDigest, false)
}

func validateAttachmentGCLinkedBlob(
	ctx context.Context,
	root *os.Root,
	name string,
	expectedDigest string,
) (os.FileInfo, error) {
	return validateAttachmentGCBlobLinks(ctx, root, name, expectedDigest, true)
}

func validateAttachmentGCBlobLinks(
	ctx context.Context,
	root *os.Root,
	name string,
	expectedDigest string,
	allowMultipleLinks bool,
) (os.FileInfo, error) {
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
	return validateAttachmentGCOpenedBlob(
		ctx,
		root,
		name,
		expectedDigest,
		allowMultipleLinks,
		entry,
		file,
	)
}

func validateAttachmentGCOpenedBlob(
	ctx context.Context,
	root *os.Root,
	name string,
	expectedDigest string,
	allowMultipleLinks bool,
	entry os.FileInfo,
	file *os.File,
) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var validationErr error
	if allowMultipleLinks {
		validationErr = validateAttachmentGCLinkedMetadataFile(file, info)
	} else {
		validationErr = validateCatalogMetadataFile(file, info)
	}
	if validationErr != nil {
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
	hash := sha256.New()
	reader := io.LimitReader(file, MaxAttachmentBytes+1)
	buffer := make([]byte, 64*1024)
	var size int64
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			size += int64(count)
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if size != info.Size() || hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return nil, fmt.Errorf("coding attachment garbage collection: blob %q content identity changed", name)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(entry, info) || !os.SameFile(entry, after) || !os.SameFile(entry, current) ||
		entry.Size() != info.Size() || info.Size() != after.Size() || entry.ModTime() != info.ModTime() ||
		info.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("coding attachment garbage collection: blob %q changed while opening", name)
	}
	return info, nil
}
