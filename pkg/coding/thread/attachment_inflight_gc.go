package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

const attachmentGCInflightDigestLimit = attachmentGCMarkedBlobLimit

func (s *Store) scanAttachmentInflight(
	ctx context.Context,
	root *os.Root,
	recoverMarkers bool,
	manifestLimit int,
	markedBlobLimit int,
	marked map[string]struct{},
) (int, error) {
	inflight, exists, err := openOptionalPinnedAttachmentChild(root, attachmentInflightDirectory)
	if err != nil || !exists {
		return 0, err
	}
	defer func() { _ = inflight.Close() }()
	shaRoot, exists, err := openOptionalPinnedAttachmentChild(inflight, attachmentInflightSHA256Directory)
	if err != nil || !exists {
		return 0, err
	}
	defer func() { _ = shaRoot.Close() }()
	shards, err := readBoundedRootEntries(shaRoot, 256)
	if err != nil {
		return 0, fmt.Errorf("coding attachment garbage collection: list in-flight shards: %w", err)
	}
	count := 0
	digestCount := 0
	for _, shardEntry := range shards {
		shardName := shardEntry.Name()
		if len(shardName) != 2 || !isHex(shardName) || strings.ToLower(shardName) != shardName {
			return count, fmt.Errorf(
				"coding attachment garbage collection: invalid in-flight shard %q",
				shardName,
			)
		}
		shard, err := openPinnedAttachmentChild(shaRoot, shardName)
		if err != nil {
			return count, err
		}
		remainingDigests := attachmentGCInflightDigestLimit - digestCount
		digests, readErr := readBoundedRootEntries(shard, remainingDigests)
		if readErr != nil {
			_ = shard.Close()
			return count, fmt.Errorf(
				"coding attachment garbage collection: list in-flight digests: %w",
				readErr,
			)
		}
		digestCount += len(digests)
		for _, digestEntry := range digests {
			if err := ctx.Err(); err != nil {
				_ = shard.Close()
				return count, err
			}
			digest := digestEntry.Name()
			if validateAttachmentDigest(digest) != nil || digest[:2] != shardName {
				_ = shard.Close()
				return count, fmt.Errorf(
					"coding attachment garbage collection: invalid in-flight digest %q",
					digest,
				)
			}
			digestRoot, err := openPinnedAttachmentChild(shard, digest)
			if err != nil {
				_ = shard.Close()
				return count, err
			}
			remainingMarkers := manifestLimit - count
			markers, readErr := readBoundedRootEntries(digestRoot, remainingMarkers)
			if readErr != nil {
				_ = digestRoot.Close()
				_ = shard.Close()
				return count, fmt.Errorf(
					"coding attachment garbage collection: list in-flight markers: %w",
					readErr,
				)
			}
			for _, markerEntry := range markers {
				marker, readErr := readAttachmentInflightMarker(digestRoot, markerEntry.Name())
				if readErr != nil {
					_ = digestRoot.Close()
					_ = shard.Close()
					return count, readErr
				}
				count++
				if marker.Digest != digest {
					_ = digestRoot.Close()
					_ = shard.Close()
					return count, fmt.Errorf(
						"coding attachment garbage collection: in-flight digest authority mismatch",
					)
				}
				var referenced bool
				var reconcileErr error
				if recoverMarkers {
					referenced, reconcileErr = s.reconcileAttachmentInflightMarker(
						ctx,
						digestRoot,
						markerEntry.Name(),
						marker,
					)
				} else {
					referenced, reconcileErr = s.inspectAttachmentInflightMarker(ctx, marker)
				}
				if reconcileErr != nil {
					_ = digestRoot.Close()
					_ = shard.Close()
					return count, reconcileErr
				}
				if referenced {
					if _, exists := marked[digest]; !exists && len(marked) >= markedBlobLimit {
						_ = digestRoot.Close()
						_ = shard.Close()
						return count, fmt.Errorf(
							"coding attachment garbage collection: referenced blobs exceed %d entries",
							markedBlobLimit,
						)
					}
					marked[digest] = struct{}{}
				}
			}
			empty, emptyErr := attachmentInflightDirectoryEmpty(digestRoot)
			if emptyErr != nil {
				_ = digestRoot.Close()
				_ = shard.Close()
				return count, emptyErr
			}
			if !empty {
				if err := validatePinnedAttachmentDirectory(shard, digest, digestRoot); err != nil {
					_ = digestRoot.Close()
					_ = shard.Close()
					return count, err
				}
				if err := digestRoot.Close(); err != nil {
					_ = shard.Close()
					return count, err
				}
				continue
			}
			if err := digestRoot.Close(); err != nil {
				_ = shard.Close()
				return count, err
			}
			if err := shard.Remove(digest); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = shard.Close()
				return count, fmt.Errorf(
					"coding attachment garbage collection: remove empty in-flight digest: %w",
					err,
				)
			}
			if err := s.syncRoot(shard); err != nil {
				_ = shard.Close()
				return count, fmt.Errorf(
					"coding attachment garbage collection: sync in-flight digest removal: %w",
					err,
				)
			}
		}
		if err := validatePinnedAttachmentDirectory(shaRoot, shardName, shard); err != nil {
			_ = shard.Close()
			return count, err
		}
		if err := shard.Close(); err != nil {
			return count, err
		}
	}
	if err := validatePinnedAttachmentDirectory(inflight, attachmentInflightSHA256Directory, shaRoot); err != nil {
		return count, err
	}
	if err := validatePinnedAttachmentDirectory(root, attachmentInflightDirectory, inflight); err != nil {
		return count, err
	}
	return count, nil
}

func (s *Store) inspectAttachmentInflightMarker(
	ctx context.Context,
	marker attachmentInflightMarker,
) (bool, error) {
	inspection, err := s.InspectLease(marker.ThreadID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf(
			"coding attachment garbage collection: inspect in-flight writer: %w",
			err,
		)
	}
	if inspection.Busy {
		return true, nil
	}
	attachments, err := s.ListAttachments(marker.ThreadID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, attachment := range attachments {
		if attachment.SHA256 == marker.Digest {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) reconcileAttachmentInflightMarker(
	ctx context.Context,
	root *os.Root,
	name string,
	marker attachmentInflightMarker,
) (bool, error) {
	lease, err := s.AcquireLease(marker.ThreadID)
	if errors.Is(err, ErrLeaseBusy) {
		return true, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf(
			"coding attachment garbage collection: inspect in-flight thread authority: %w",
			err,
		)
	}
	referenced := false
	if lease != nil {
		attachments, listErr := s.ListAttachmentsWithLease(ctx, lease, marker.ThreadID)
		if listErr == nil {
			for _, attachment := range attachments {
				if attachment.SHA256 == marker.Digest {
					referenced = true
					break
				}
			}
		}
		if listErr != nil {
			_ = lease.Release()
			return false, listErr
		}
	}
	current, readErr := readAttachmentInflightMarker(root, name)
	if readErr != nil || current != marker {
		if lease != nil {
			_ = lease.Release()
		}
		return false, errors.Join(
			readErr,
			fmt.Errorf("coding attachment garbage collection: in-flight marker changed before recovery"),
		)
	}
	removeErr := root.Remove(name)
	if removeErr == nil {
		removeErr = s.syncRoot(root)
	}
	if lease != nil {
		removeErr = errors.Join(removeErr, lease.Release())
	}
	if removeErr != nil {
		return false, fmt.Errorf(
			"coding attachment garbage collection: recover in-flight marker: %w",
			removeErr,
		)
	}
	return referenced, nil
}

func (s *Store) attachmentInflightDigestPresent(
	ctx context.Context,
	root *os.Root,
	digest string,
) (bool, error) {
	if err := validateAttachmentDigest(digest); err != nil {
		return false, err
	}
	hierarchy, err := s.openAttachmentHierarchy(
		root,
		false,
		attachmentInflightDirectory,
		attachmentInflightSHA256Directory,
		digest[:2],
		digest,
	)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"coding attachment garbage collection: open final in-flight authority: %w",
			err,
		)
	}
	defer func() { _ = hierarchy.Close() }()
	entries, err := readBoundedRootEntries(hierarchy.Leaf(), attachmentGCManifestLimit)
	if err != nil {
		return false, fmt.Errorf(
			"coding attachment garbage collection: list final in-flight authority: %w",
			err,
		)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		marker, err := readAttachmentInflightMarker(hierarchy.Leaf(), entry.Name())
		if err != nil {
			return false, err
		}
		if marker.Digest != digest {
			return false, fmt.Errorf(
				"coding attachment garbage collection: final in-flight digest authority mismatch",
			)
		}
	}
	if err := hierarchy.validate(); err != nil {
		return false, fmt.Errorf(
			"coding attachment garbage collection: final in-flight hierarchy changed: %w",
			err,
		)
	}
	return len(entries) > 0, nil
}
