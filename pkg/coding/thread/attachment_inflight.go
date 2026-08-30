package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	attachmentInflightVersion         = 1
	attachmentInflightDirectory       = "attachment-inflight"
	attachmentInflightSHA256Directory = "sha256"
	maxAttachmentInflightMarkerBytes  = 4 * 1024
)

type attachmentInflightMarker struct {
	Version  int    `json:"version"`
	ThreadID string `json:"thread_id"`
	Digest   string `json:"sha256"`
	BatchID  string `json:"batch_id"`
}

type attachmentInflightBatch struct {
	store    *Store
	root     *os.Root
	threadID string
	batchID  string
	digests  []string
}

func (v *attachmentStoreView) beginAttachmentInflight(
	prepared []preparedAttachment,
) (*attachmentInflightBatch, error) {
	if v == nil || v.store == nil || v.root == nil {
		return nil, fmt.Errorf("coding attachment in-flight authority: store view is incomplete")
	}
	digestSet := make(map[string]struct{}, len(prepared))
	for _, candidate := range prepared {
		digestSet[candidate.attachment.SHA256] = struct{}{}
	}
	digests := make([]string, 0, len(digestSet))
	for digest := range digestSet {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	batch := &attachmentInflightBatch{
		store: v.store, root: v.root, threadID: v.threadID, batchID: uuid.NewString(),
		digests: make([]string, 0, len(digests)),
	}
	for _, digest := range digests {
		published, err := batch.publish(digest)
		if published {
			batch.digests = append(batch.digests, digest)
		}
		if err != nil {
			return nil, errors.Join(err, batch.Remove())
		}
	}
	return batch, nil
}

func (b *attachmentInflightBatch) publish(digest string) (bool, error) {
	if b == nil || b.store == nil || b.root == nil {
		return false, fmt.Errorf("coding attachment in-flight authority: batch is incomplete")
	}
	if err := validateAttachmentDigest(digest); err != nil {
		return false, err
	}
	hierarchy, openErr := b.store.openAttachmentHierarchy(
		b.root,
		true,
		attachmentInflightDirectory,
		attachmentInflightSHA256Directory,
		digest[:2],
		digest,
	)
	if openErr != nil {
		return false, fmt.Errorf("coding attachment in-flight authority: open digest directory: %w", openErr)
	}
	defer func() { _ = hierarchy.Close() }()
	name := attachmentInflightMarkerName(b.batchID)
	if _, statErr := hierarchy.Leaf().Lstat(name); statErr == nil {
		return false, fmt.Errorf("coding attachment in-flight authority: marker already exists")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("coding attachment in-flight authority: inspect marker: %w", statErr)
	}
	marker := attachmentInflightMarker{
		Version: attachmentInflightVersion, ThreadID: b.threadID, Digest: digest, BatchID: b.batchID,
	}
	data, encodeErr := encodeAttachmentInflightMarker(marker)
	if encodeErr != nil {
		return false, encodeErr
	}
	if writeErr := b.store.writeRoot(hierarchy.Leaf(), name, data, 0o600); writeErr != nil {
		return fileutil.IsCommittedWriteError(writeErr), fmt.Errorf(
			"coding attachment in-flight authority: publish marker: %w",
			writeErr,
		)
	}
	loaded, readErr := readAttachmentInflightMarker(hierarchy.Leaf(), name)
	if readErr != nil {
		return true, readErr
	}
	if loaded != marker {
		return true, fmt.Errorf("coding attachment in-flight authority: published marker changed")
	}
	if validateErr := hierarchy.validate(); validateErr != nil {
		return true, fmt.Errorf("coding attachment in-flight authority: hierarchy changed: %w", validateErr)
	}
	return true, nil
}

func (b *attachmentInflightBatch) Remove() error {
	if b == nil {
		return nil
	}
	var result error
	for _, digest := range b.digests {
		result = errors.Join(result, b.remove(digest))
	}
	return result
}

func (b *attachmentInflightBatch) remove(digest string) error {
	hierarchy, err := b.store.openAttachmentHierarchy(
		b.root,
		false,
		attachmentInflightDirectory,
		attachmentInflightSHA256Directory,
		digest[:2],
		digest,
	)
	if err != nil {
		return fmt.Errorf("coding attachment in-flight authority: reopen digest directory: %w", err)
	}
	defer func() { _ = hierarchy.Close() }()
	name := attachmentInflightMarkerName(b.batchID)
	marker, err := readAttachmentInflightMarker(hierarchy.Leaf(), name)
	if err != nil {
		return err
	}
	if marker.ThreadID != b.threadID || marker.Digest != digest || marker.BatchID != b.batchID {
		return fmt.Errorf("coding attachment in-flight authority: marker identity changed")
	}
	if err := hierarchy.Leaf().Remove(name); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: remove marker: %w", err)
	}
	if err := b.store.syncRoot(hierarchy.Leaf()); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: sync marker removal: %w", err)
	}
	if err := hierarchy.validate(); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: revalidate marker removal: %w", err)
	}
	return pruneAttachmentInflightDigest(b.store, hierarchy)
}

func pruneAttachmentInflightDigest(store *Store, hierarchy *pinnedAttachmentHierarchy) error {
	if store == nil || hierarchy == nil || len(hierarchy.directories) == 0 {
		return fmt.Errorf("coding attachment in-flight authority: prune hierarchy is incomplete")
	}
	empty, err := attachmentInflightDirectoryEmpty(hierarchy.Leaf())
	if err != nil || !empty {
		return err
	}
	last := len(hierarchy.directories) - 1
	digest := hierarchy.directories[last]
	if err := digest.root.Close(); err != nil {
		return err
	}
	hierarchy.directories = hierarchy.directories[:last]
	if err := digest.parent.Remove(digest.name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("coding attachment in-flight authority: remove empty digest directory: %w", err)
	}
	if err := store.syncRoot(digest.parent); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: sync digest-directory removal: %w", err)
	}
	if err := hierarchy.validate(); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: validate digest-directory removal: %w", err)
	}
	return nil
}

func attachmentInflightDirectoryEmpty(root *os.Root) (bool, error) {
	reader, err := root.Open(".")
	if err != nil {
		return false, err
	}
	entries, readErr := reader.ReadDir(1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func attachmentInflightMarkerName(batchID string) string {
	return batchID + ".json"
}

func parseAttachmentInflightMarkerName(name string) (string, error) {
	if !strings.HasSuffix(name, ".json") {
		return "", fmt.Errorf("coding attachment in-flight authority: invalid marker name %q", name)
	}
	batchID := strings.TrimSuffix(name, ".json")
	parsed, err := uuid.Parse(batchID)
	if err != nil || parsed.String() != batchID {
		return "", fmt.Errorf("coding attachment in-flight authority: invalid marker name %q", name)
	}
	return batchID, nil
}

func encodeAttachmentInflightMarker(marker attachmentInflightMarker) ([]byte, error) {
	if err := validateAttachmentInflightMarker(marker); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maxAttachmentInflightMarkerBytes {
		return nil, fmt.Errorf(
			"coding attachment in-flight authority: marker exceeds %d bytes",
			maxAttachmentInflightMarkerBytes,
		)
	}
	return data, nil
}

func readAttachmentInflightMarker(root *os.Root, name string) (attachmentInflightMarker, error) {
	batchID, parseErr := parseAttachmentInflightMarkerName(name)
	if parseErr != nil {
		return attachmentInflightMarker{}, parseErr
	}
	entry, statErr := root.Lstat(name)
	if statErr != nil {
		return attachmentInflightMarker{}, statErr
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker is not a direct regular file",
		)
	}
	file, openErr := root.OpenFile(name, os.O_RDONLY, 0)
	if openErr != nil {
		return attachmentInflightMarker{}, openErr
	}
	defer func() { _ = file.Close() }()
	info, infoErr := file.Stat()
	if infoErr != nil {
		return attachmentInflightMarker{}, infoErr
	}
	if !os.SameFile(entry, info) {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker changed while opening",
		)
	}
	if metadataErr := validateCatalogMetadataFile(file, info); metadataErr != nil {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: unsafe marker: %w",
			metadataErr,
		)
	}
	if info.Size() < 0 || info.Size() > maxAttachmentInflightMarkerBytes {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker exceeds %d bytes",
			maxAttachmentInflightMarkerBytes,
		)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxAttachmentInflightMarkerBytes+1))
	if readErr != nil {
		return attachmentInflightMarker{}, readErr
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var marker attachmentInflightMarker
	if decodeErr := decoder.Decode(&marker); decodeErr != nil {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: decode marker: %w",
			decodeErr,
		)
	}
	if trailingErr := decoder.Decode(new(any)); !errors.Is(trailingErr, io.EOF) {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker has trailing data",
		)
	}
	if markerErr := validateAttachmentInflightMarker(marker); markerErr != nil {
		return attachmentInflightMarker{}, markerErr
	}
	if marker.BatchID != batchID {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker filename identity changed",
		)
	}
	after, afterErr := file.Stat()
	if afterErr != nil {
		return attachmentInflightMarker{}, afterErr
	}
	current, currentErr := root.Lstat(name)
	if currentErr != nil {
		return attachmentInflightMarker{}, currentErr
	}
	if !os.SameFile(entry, after) || !os.SameFile(entry, current) || info.Size() != after.Size() ||
		info.ModTime() != after.ModTime() {
		return attachmentInflightMarker{}, fmt.Errorf(
			"coding attachment in-flight authority: marker changed while reading",
		)
	}
	return marker, nil
}

func validateAttachmentInflightMarker(marker attachmentInflightMarker) error {
	if marker.Version != attachmentInflightVersion {
		return fmt.Errorf("coding attachment in-flight authority: unsupported marker version")
	}
	if err := validateThreadID(marker.ThreadID); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: invalid thread: %w", err)
	}
	if err := validateAttachmentDigest(marker.Digest); err != nil {
		return fmt.Errorf("coding attachment in-flight authority: invalid digest: %w", err)
	}
	parsed, err := uuid.Parse(marker.BatchID)
	if err != nil || parsed.String() != marker.BatchID {
		return fmt.Errorf("coding attachment in-flight authority: invalid batch ID")
	}
	return nil
}
