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
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	AttachmentManifestVersion        = 1
	MaxAttachmentBytes         int64 = 32 << 20
	MaxThreadAttachments             = 1024
	MaxAttachmentManifestBytes       = 1 << 20
	MaxAttachmentFilenameBytes       = 255
	MaxAttachmentContentType         = 127

	attachmentDirectory = "attachments"
	attachmentManifest  = "manifest.json"
	attachmentRefPrefix = "media://coding-attachment/"
)

// AttachmentInput describes one local file admitted under a thread writer.
type AttachmentInput struct {
	Path        string
	Filename    string
	ContentType string
	At          time.Time
}

// Attachment is the durable, bounded descriptor stored in a thread manifest.
type Attachment struct {
	Ref         string    `json:"ref"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

type attachmentManifestFile struct {
	Version  int          `json:"version"`
	ThreadID string       `json:"thread_id"`
	Entries  []Attachment `json:"entries"`
}

// AttachmentUnavailableError reports a durable reference whose bytes are no
// longer present with the identity admitted by the thread.
type AttachmentUnavailableError struct {
	Ref    string
	Reason string
}

// CommittedAttachmentError reports an attachment reference that reached the
// canonical manifest even though post-rename durability could not be confirmed.
type CommittedAttachmentError struct {
	Attachment Attachment
	Err        error
}

// committedAttachmentManifestError distinguishes a manifest rename from a
// directory-creation durability warning that did not publish the manifest.
type committedAttachmentManifestError struct {
	Err error
}

func (e *committedAttachmentManifestError) Error() string { return e.Err.Error() }

func (e *committedAttachmentManifestError) Unwrap() error { return e.Err }

func (e *CommittedAttachmentError) Error() string {
	return fmt.Sprintf("coding attachment %q committed with uncertain durability: %v", e.Attachment.Ref, e.Err)
}

func (e *CommittedAttachmentError) Unwrap() error { return e.Err }

// IsCommittedAttachmentError distinguishes a published reference from an
// admission failure that did not update the canonical manifest.
func IsCommittedAttachmentError(err error) bool {
	var committed *CommittedAttachmentError
	return errors.As(err, &committed)
}

func (e *AttachmentUnavailableError) Error() string {
	return fmt.Sprintf("coding attachment %q is unavailable: %s", e.Ref, e.Reason)
}

// IsAttachmentUnavailable distinguishes an honest missing/changed reference
// from corrupt manifest or store state.
func IsAttachmentUnavailable(err error) bool {
	var unavailable *AttachmentUnavailableError
	return errors.As(err, &unavailable)
}

// AdmitAttachment verifies one stable regular file, publishes an immutable
// copy by digest, and commits the thread-local descriptor last.
func (s *Store) AdmitAttachment(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	input AttachmentInput,
) (Attachment, error) {
	if s == nil {
		return Attachment{}, fmt.Errorf("coding attachment store is nil")
	}
	if ctx == nil {
		return Attachment{}, fmt.Errorf("coding attachment admission: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return Attachment{}, err
	}
	input, err := validateAttachmentInput(input)
	if err != nil {
		return Attachment{}, err
	}
	var admitted Attachment
	err = lease.withActive(s.root, metadata.ThreadID, func() error {
		data, canonicalPath, digest, size, readErr := readAttachmentSource(
			ctx,
			input.Path,
			MaxAttachmentBytes,
			true,
		)
		if readErr != nil {
			return fmt.Errorf("coding attachment admission: %w", readErr)
		}
		filename := input.Filename
		if filename == "" {
			filename = filepath.Base(canonicalPath)
		}
		contentType := input.ContentType
		if contentType == "" {
			contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if validationErr := validateAttachmentPresentation(filename, contentType); validationErr != nil {
			return validationErr
		}
		storeRoot, threadRoot, pinErr := s.openPinnedAttachmentRoots(lease, metadata.ThreadID)
		if pinErr != nil {
			return fmt.Errorf("coding attachment: pin active thread: %w", pinErr)
		}
		defer func() { _ = errors.Join(threadRoot.Close(), storeRoot.Close()) }()
		manifest, loadErr := s.loadPinnedAttachmentManifest(threadRoot, metadata.ThreadID)
		if loadErr != nil {
			return loadErr
		}
		candidate := Attachment{
			Filename: filename, ContentType: contentType,
			Size: size, SHA256: digest, CreatedAt: input.At.UTC(),
		}
		if publishErr := s.publishAttachmentBlob(ctx, storeRoot, digest, data); publishErr != nil {
			return publishErr
		}
		if identityErr := s.validatePinnedAttachmentView(
			storeRoot,
			threadRoot,
			metadata.ThreadID,
		); identityErr != nil {
			return identityErr
		}
		if existing, found := equivalentAttachment(manifest.Entries, candidate); found {
			if identityErr := s.validatePinnedAttachmentWriter(
				metadata.ThreadID,
				lease,
				threadRoot,
			); identityErr != nil {
				return fmt.Errorf("coding attachment: revalidate active thread before deduplication: %w", identityErr)
			}
			admitted = existing
			return nil
		}
		if len(manifest.Entries) >= MaxThreadAttachments {
			return fmt.Errorf("coding attachment admission: thread exceeds %d attachments", MaxThreadAttachments)
		}
		candidate.Ref = attachmentRefPrefix + uuid.NewString()
		manifest.Entries = append(manifest.Entries, candidate)
		admitted = candidate
		if identityErr := s.validatePinnedAttachmentWriter(metadata.ThreadID, lease, threadRoot); identityErr != nil {
			admitted = Attachment{}
			return fmt.Errorf("coding attachment: revalidate active thread before manifest publish: %w", identityErr)
		}
		saveErr := s.saveAttachmentManifest(threadRoot, metadata.ThreadID, manifest)
		var committedManifest *committedAttachmentManifestError
		if saveErr == nil || errors.As(saveErr, &committedManifest) {
			if identityErr := s.validatePinnedAttachmentView(
				storeRoot,
				threadRoot,
				metadata.ThreadID,
			); identityErr != nil {
				admitted = Attachment{}
				return fmt.Errorf("coding attachment: revalidate active view after manifest publish: %w", identityErr)
			}
			if identityErr := s.validatePinnedAttachmentWriter(
				metadata.ThreadID,
				lease,
				threadRoot,
			); identityErr != nil {
				admitted = Attachment{}
				return fmt.Errorf("coding attachment: revalidate active thread after manifest publish: %w", identityErr)
			}
		}
		if saveErr != nil {
			if committedManifest != nil {
				return &CommittedAttachmentError{Attachment: candidate, Err: saveErr}
			}
			admitted = Attachment{}
			return saveErr
		}
		return nil
	})
	if err != nil {
		return admitted, err
	}
	return admitted, nil
}

// ListAttachments loads one validated manifest without reading blob payloads.
func (s *Store) ListAttachments(threadID string) ([]Attachment, error) {
	if s == nil {
		return nil, fmt.Errorf("coding attachment store is nil")
	}
	manifest, err := s.loadAttachmentManifest(threadID)
	if err != nil {
		return nil, err
	}
	return append([]Attachment(nil), manifest.Entries...), nil
}

// ResolveAttachment returns verified immutable bytes for a reference owned by
// the selected thread. It never repairs, rewrites, or removes manifest state.
func (s *Store) ResolveAttachment(
	ctx context.Context,
	threadID string,
	ref string,
) ([]byte, Attachment, error) {
	if s == nil {
		return nil, Attachment{}, fmt.Errorf("coding attachment store is nil")
	}
	if ctx == nil {
		return nil, Attachment{}, fmt.Errorf("coding attachment resolve: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, Attachment{}, err
	}
	if err := validateThreadID(threadID); err != nil {
		return nil, Attachment{}, err
	}
	storeRoot, threadRoot, err := s.openAttachmentRoots(threadID)
	if err != nil {
		return nil, Attachment{}, err
	}
	defer func() { _ = errors.Join(threadRoot.Close(), storeRoot.Close()) }()
	manifest, err := s.loadPinnedAttachmentManifest(threadRoot, threadID)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, Attachment{}, contextErr
		}
		return nil, Attachment{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Attachment{}, err
	}
	entry, found := attachmentByRef(manifest.Entries, ref)
	if !found {
		if err := ctx.Err(); err != nil {
			return nil, Attachment{}, err
		}
		return nil, Attachment{}, &AttachmentUnavailableError{Ref: ref, Reason: "reference is not owned by thread"}
	}
	data, readErr := readPinnedAttachmentBlob(ctx, storeRoot, entry)
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, entry, readErr
		}
		return nil, entry, &AttachmentUnavailableError{Ref: ref, Reason: readErr.Error()}
	}
	if err := s.validatePinnedAttachmentView(storeRoot, threadRoot, threadID); err != nil {
		return nil, entry, &AttachmentUnavailableError{Ref: ref, Reason: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return nil, entry, err
	}
	return data, entry, nil
}

func readPinnedAttachmentBlob(
	ctx context.Context,
	storeRoot *os.Root,
	entry Attachment,
) ([]byte, error) {
	blobs, err := openDirectAttachmentDirectory(storeRoot, "blobs")
	if err != nil {
		return nil, err
	}
	defer func() { _ = blobs.Close() }()
	shaRoot, err := openDirectAttachmentDirectory(blobs, "sha256")
	if err != nil {
		return nil, err
	}
	defer func() { _ = shaRoot.Close() }()
	shardName := entry.SHA256[:2]
	shard, err := openDirectAttachmentDirectory(shaRoot, shardName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = shard.Close() }()
	data, digest, size, err := readAttachmentRootFile(ctx, shard, entry.SHA256, MaxAttachmentBytes)
	if err != nil {
		return nil, err
	}
	if digest != entry.SHA256 || size != entry.Size {
		return nil, fmt.Errorf("content identity changed")
	}
	for _, check := range []struct {
		parent *os.Root
		name   string
		pinned *os.Root
	}{
		{parent: shaRoot, name: shardName, pinned: shard},
		{parent: blobs, name: "sha256", pinned: shaRoot},
		{parent: storeRoot, name: "blobs", pinned: blobs},
	} {
		if err := validatePinnedAttachmentDirectory(check.parent, check.name, check.pinned); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func openDirectAttachmentDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("coding attachment: directory %q is not direct", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func validateAttachmentInput(input AttachmentInput) (AttachmentInput, error) {
	input.Filename = strings.TrimSpace(input.Filename)
	input.ContentType = strings.TrimSpace(input.ContentType)
	if input.Path == "" {
		return AttachmentInput{}, fmt.Errorf("coding attachment admission: path is required")
	}
	if input.At.IsZero() {
		return AttachmentInput{}, fmt.Errorf("coding attachment admission: timestamp is required")
	}
	if input.Filename != "" {
		if err := validateAttachmentPresentation(input.Filename, "application/octet-stream"); err != nil {
			return AttachmentInput{}, err
		}
	}
	if input.ContentType != "" &&
		(!utf8.ValidString(input.ContentType) || len(input.ContentType) > MaxAttachmentContentType ||
			strings.ContainsAny(input.ContentType, "\r\n\x00")) {
		return AttachmentInput{}, fmt.Errorf("coding attachment admission: content type is invalid")
	}
	return input, nil
}

func validateAttachmentPresentation(filename, contentType string) error {
	if filename == "" || filename != filepath.Base(filename) || filename == "." || filename == ".." ||
		!utf8.ValidString(filename) || len(filename) > MaxAttachmentFilenameBytes ||
		strings.ContainsAny(filename, "\r\n\x00") {
		return fmt.Errorf("coding attachment admission: filename is invalid")
	}
	if contentType == "" || !utf8.ValidString(contentType) || len(contentType) > MaxAttachmentContentType ||
		strings.ContainsAny(contentType, "\r\n\x00") {
		return fmt.Errorf("coding attachment admission: content type is invalid")
	}
	return nil
}

func readAttachmentSource(
	ctx context.Context,
	path string,
	maxBytes int64,
	resolveInitialSymlink bool,
) ([]byte, string, string, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", "", 0, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", "", 0, err
	}
	canonical := filepath.Clean(absolute)
	if resolveInitialSymlink {
		canonical, err = filepath.EvalSymlinks(canonical)
		if err != nil {
			return nil, "", "", 0, err
		}
		canonical = filepath.Clean(canonical)
	} else {
		entry, lstatErr := os.Lstat(canonical)
		if lstatErr != nil {
			return nil, "", "", 0, lstatErr
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, "", "", 0, fmt.Errorf("source path became a symbolic link")
		}
	}
	file, err := os.Open(canonical)
	if err != nil {
		return nil, "", "", 0, err
	}
	defer func() { _ = file.Close() }()
	data, digest, size, before, err := readAttachmentFile(ctx, file, maxBytes)
	if err != nil {
		return nil, "", "", 0, err
	}
	active, err := os.Lstat(canonical)
	if err != nil {
		return nil, "", "", 0, err
	}
	if !os.SameFile(before, active) {
		return nil, "", "", 0, fmt.Errorf("source identity changed while reading")
	}
	return data, canonical, digest, size, nil
}

func readAttachmentFile(
	ctx context.Context,
	file *os.File,
	maxBytes int64,
) ([]byte, string, int64, os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, "", 0, nil, err
	}
	if validationErr := validateCatalogMetadataFile(file, before); validationErr != nil {
		return nil, "", 0, nil, fmt.Errorf("source is unsafe: %w", validationErr)
	}
	if before.Size() < 0 || before.Size() > maxBytes {
		return nil, "", 0, nil, fmt.Errorf("source exceeds %d bytes", maxBytes)
	}
	var output bytes.Buffer
	output.Grow(int(before.Size()))
	hash := sha256.New()
	reader := io.LimitReader(file, maxBytes+1)
	buffer := make([]byte, 64*1024)
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, "", 0, nil, contextErr
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			_, _ = output.Write(buffer[:count])
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", 0, nil, readErr
		}
	}
	if int64(output.Len()) != before.Size() {
		return nil, "", 0, nil, fmt.Errorf("source size changed while reading")
	}
	after, err := file.Stat()
	if err != nil {
		return nil, "", 0, nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, "", 0, nil, fmt.Errorf("source identity changed while reading")
	}
	return output.Bytes(), hex.EncodeToString(hash.Sum(nil)), before.Size(), before, nil
}

func readAttachmentRootFile(
	ctx context.Context,
	root *os.Root,
	name string,
	maxBytes int64,
) ([]byte, string, int64, error) {
	entry, err := root.Lstat(name)
	if err != nil {
		return nil, "", 0, err
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return nil, "", 0, fmt.Errorf("source path became a symbolic link")
	}
	file, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, "", 0, err
	}
	defer func() { _ = file.Close() }()
	data, digest, size, opened, err := readAttachmentFile(ctx, file, maxBytes)
	if err != nil {
		return nil, "", 0, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, "", 0, err
	}
	if !os.SameFile(entry, opened) || !os.SameFile(entry, current) {
		return nil, "", 0, fmt.Errorf("source identity changed while reading")
	}
	return data, digest, size, nil
}

func (s *Store) publishAttachmentBlob(
	ctx context.Context,
	root *os.Root,
	digest string,
	data []byte,
) error {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest || !isHex(digest) {
		return fmt.Errorf("coding attachment blob: digest is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	relativeDirectory := filepath.Join("blobs", "sha256", digest[:2])
	if directoryErr := s.ensureAttachmentDirectories(
		root,
		"blobs",
		filepath.Join("blobs", "sha256"),
		relativeDirectory,
	); directoryErr != nil {
		return directoryErr
	}
	shard, err := root.OpenRoot(relativeDirectory)
	if err != nil {
		return fmt.Errorf("coding attachment blob: open digest directory: %w", err)
	}
	defer func() { _ = shard.Close() }()
	if _, statErr := shard.Lstat(digest); statErr == nil {
		_, actual, size, readErr := readAttachmentRootFile(ctx, shard, digest, MaxAttachmentBytes)
		if readErr != nil || actual != digest || size != int64(len(data)) {
			return fmt.Errorf("coding attachment blob: existing digest path is invalid")
		}
		if syncErr := s.syncRoot(shard); syncErr != nil {
			return fmt.Errorf("coding attachment blob: sync existing digest directory: %w", syncErr)
		}
		return validatePinnedAttachmentDirectory(root, relativeDirectory, shard)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if writeErr := s.writeRoot(shard, digest, data, 0o600); writeErr != nil {
		return fmt.Errorf("coding attachment blob: publish: %w", writeErr)
	}
	_, actual, size, err := readAttachmentRootFile(ctx, shard, digest, MaxAttachmentBytes)
	if err != nil || actual != digest || size != int64(len(data)) {
		return fmt.Errorf("coding attachment blob: published content could not be verified: %w", err)
	}
	return validatePinnedAttachmentDirectory(root, relativeDirectory, shard)
}

func (s *Store) ensureAttachmentDirectories(root *os.Root, names ...string) error {
	for _, name := range names {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("coding attachment directory is invalid")
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := root.Mkdir(name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return fmt.Errorf("coding attachment: create directory %q: %w", name, mkdirErr)
			}
			info, err = root.Lstat(name)
		}
		if err != nil {
			return fmt.Errorf("coding attachment: inspect directory %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("coding attachment: directory %q is not direct", name)
		}
		if err := root.Chmod(name, 0o700); err != nil {
			return fmt.Errorf("coding attachment: secure directory %q: %w", name, err)
		}
		if syncErr := s.syncAttachmentDirectoryParent(root, name); syncErr != nil {
			return &fileutil.CommittedWriteError{Err: fmt.Errorf(
				"coding attachment: sync required directory %q: %w",
				name,
				syncErr,
			)}
		}
	}
	return nil
}

func (s *Store) syncAttachmentDirectoryParent(root *os.Root, name string) error {
	parentName := filepath.Dir(name)
	if parentName == "." {
		return s.syncRoot(root)
	}
	parent, err := root.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return s.syncRoot(parent)
}

func (s *Store) loadAttachmentManifest(threadID string) (attachmentManifestFile, error) {
	if err := validateThreadID(threadID); err != nil {
		return attachmentManifestFile{}, err
	}
	storeRoot, threadRoot, err := s.openAttachmentRoots(threadID)
	if err != nil {
		return attachmentManifestFile{}, err
	}
	defer func() { _ = errors.Join(threadRoot.Close(), storeRoot.Close()) }()
	manifest, err := s.loadPinnedAttachmentManifest(threadRoot, threadID)
	if err != nil {
		return attachmentManifestFile{}, err
	}
	if err := s.validatePinnedAttachmentView(storeRoot, threadRoot, threadID); err != nil {
		return attachmentManifestFile{}, err
	}
	return manifest, nil
}

func (s *Store) loadPinnedAttachmentManifest(
	threadRoot *os.Root,
	threadID string,
) (attachmentManifestFile, error) {
	empty := attachmentManifestFile{Version: AttachmentManifestVersion, ThreadID: threadID, Entries: []Attachment{}}
	info, err := threadRoot.Lstat(attachmentDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: inspect directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: directory is not direct")
	}
	attachments, err := threadRoot.OpenRoot(attachmentDirectory)
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: open directory: %w", err)
	}
	defer func() { _ = attachments.Close() }()
	file, err := attachments.OpenFile(attachmentManifest, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		if identityErr := validatePinnedAttachmentDirectory(
			threadRoot,
			attachmentDirectory,
			attachments,
		); identityErr != nil {
			return attachmentManifestFile{}, identityErr
		}
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: open: %w", err)
	}
	defer func() { _ = file.Close() }()
	fileInfo, err := file.Stat()
	if err != nil {
		return attachmentManifestFile{}, err
	}
	if validationErr := validateCatalogMetadataFile(file, fileInfo); validationErr != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: unsafe file: %w", validationErr)
	}
	if fileInfo.Size() < 0 || fileInfo.Size() > MaxAttachmentManifestBytes {
		return attachmentManifestFile{}, fmt.Errorf(
			"coding attachment manifest exceeds %d bytes",
			MaxAttachmentManifestBytes,
		)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentManifestBytes+1))
	if err != nil {
		return attachmentManifestFile{}, err
	}
	if s.attachmentManifestRead != nil {
		s.attachmentManifestRead()
	}
	after, err := file.Stat()
	if err != nil {
		return attachmentManifestFile{}, err
	}
	current, err := attachments.OpenFile(attachmentManifest, os.O_RDONLY, 0)
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: revalidate: %w", err)
	}
	currentInfo, currentErr := current.Stat()
	closeErr := current.Close()
	if currentErr != nil || closeErr != nil {
		return attachmentManifestFile{}, fmt.Errorf(
			"coding attachment manifest: revalidate: %w",
			errors.Join(currentErr, closeErr),
		)
	}
	if !os.SameFile(fileInfo, after) || !os.SameFile(fileInfo, currentInfo) || fileInfo.Size() != after.Size() ||
		fileInfo.ModTime() != after.ModTime() {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest changed while reading")
	}
	if identityErr := validatePinnedAttachmentDirectory(
		threadRoot,
		attachmentDirectory,
		attachments,
	); identityErr != nil {
		return attachmentManifestFile{}, identityErr
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest attachmentManifestFile
	if err := decoder.Decode(&manifest); err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: decode: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: trailing data")
	}
	if err := validateAttachmentManifest(threadID, manifest); err != nil {
		return attachmentManifestFile{}, err
	}
	if identityErr := validatePinnedAttachmentDirectory(
		threadRoot,
		attachmentDirectory,
		attachments,
	); identityErr != nil {
		return attachmentManifestFile{}, identityErr
	}
	return manifest, nil
}

func (s *Store) openPinnedAttachmentRoots(lease *Lease, threadID string) (*os.Root, *os.Root, error) {
	storeRoot, threadRoot, err := s.openAttachmentRoots(threadID)
	if err != nil {
		return nil, nil, err
	}
	if err := validatePinnedAttachmentLease(threadRoot, lease); err != nil {
		_ = errors.Join(threadRoot.Close(), storeRoot.Close())
		return nil, nil, err
	}
	return storeRoot, threadRoot, nil
}

func (s *Store) openAttachmentRoots(threadID string) (*os.Root, *os.Root, error) {
	storeRoot, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, nil, fmt.Errorf("coding attachment: anchor store root: %w", err)
	}
	closeStore := true
	defer func() {
		if closeStore {
			_ = storeRoot.Close()
		}
	}()
	if directoryErr := requireAttachmentDirectories(storeRoot, "threads"); directoryErr != nil {
		return nil, nil, directoryErr
	}
	threadsRoot, err := storeRoot.OpenRoot("threads")
	if err != nil {
		return nil, nil, fmt.Errorf("coding attachment: pin threads directory: %w", err)
	}
	defer func() { _ = threadsRoot.Close() }()
	if directoryErr := requireAttachmentDirectories(threadsRoot, threadID); directoryErr != nil {
		return nil, nil, directoryErr
	}
	threadRoot, err := threadsRoot.OpenRoot(threadID)
	if err != nil {
		return nil, nil, fmt.Errorf("coding attachment: pin thread directory: %w", err)
	}
	closeStore = false
	return storeRoot, threadRoot, nil
}

func validatePinnedAttachmentLease(threadRoot *os.Root, lease *Lease) error {
	pinnedLease, err := threadRoot.OpenFile(leaseFileName, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("coding attachment: pin lease file: %w", err)
	}
	defer func() { _ = pinnedLease.Close() }()
	pinnedInfo, err := pinnedLease.Stat()
	if err != nil {
		return err
	}
	if validationErr := validateCatalogMetadataFile(pinnedLease, pinnedInfo); validationErr != nil {
		return fmt.Errorf("coding attachment: pinned lease file is unsafe: %w", validationErr)
	}
	leaseInfo, err := lease.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pinnedInfo, leaseInfo) {
		return fmt.Errorf("coding attachment: held lease no longer identifies pinned thread")
	}
	return nil
}

func (s *Store) validatePinnedAttachmentWriter(threadID string, lease *Lease, threadRoot *os.Root) error {
	if err := s.validateAcquiredLeasePath(threadID, lease.file); err != nil {
		return err
	}
	return validatePinnedAttachmentLease(threadRoot, lease)
}

func (s *Store) validatePinnedAttachmentView(storeRoot, threadRoot *os.Root, threadID string) error {
	activeStore, err := os.OpenRoot(s.root)
	if err != nil {
		return fmt.Errorf("coding attachment: reopen active store root: %w", err)
	}
	defer func() { _ = activeStore.Close() }()
	if identityErr := compareAttachmentRoots(storeRoot, activeStore); identityErr != nil {
		return fmt.Errorf("coding attachment: active store root changed: %w", identityErr)
	}
	threadsRoot, err := activeStore.OpenRoot("threads")
	if err != nil {
		return fmt.Errorf("coding attachment: reopen active threads root: %w", err)
	}
	defer func() { _ = threadsRoot.Close() }()
	activeThread, err := threadsRoot.OpenRoot(threadID)
	if err != nil {
		return fmt.Errorf("coding attachment: reopen active thread root: %w", err)
	}
	defer func() { _ = activeThread.Close() }()
	if identityErr := compareAttachmentRoots(threadRoot, activeThread); identityErr != nil {
		return fmt.Errorf("coding attachment: active thread root changed: %w", identityErr)
	}
	return nil
}

func compareAttachmentRoots(left, right *os.Root) error {
	leftInfo, err := left.Stat(".")
	if err != nil {
		return err
	}
	rightInfo, err := right.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(leftInfo, rightInfo) {
		return fmt.Errorf("directory identity changed")
	}
	return nil
}

func validatePinnedAttachmentDirectory(parent *os.Root, name string, pinned *os.Root) error {
	pinnedInfo, err := pinned.Stat(".")
	if err != nil {
		return fmt.Errorf("coding attachment: inspect pinned directory %q: %w", name, err)
	}
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("coding attachment: inspect current directory %q: %w", name, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return fmt.Errorf("coding attachment: directory %q is not direct", name)
	}
	current, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("coding attachment: reopen directory %q: %w", name, err)
	}
	defer func() { _ = current.Close() }()
	currentInfo, err := current.Stat(".")
	if err != nil {
		return fmt.Errorf("coding attachment: inspect current directory %q: %w", name, err)
	}
	if !os.SameFile(pinnedInfo, currentInfo) {
		return fmt.Errorf("coding attachment: directory %q changed during publication", name)
	}
	return nil
}

func (s *Store) saveAttachmentManifest(
	threadRoot *os.Root,
	threadID string,
	manifest attachmentManifestFile,
) error {
	if err := validateAttachmentManifest(threadID, manifest); err != nil {
		return err
	}
	sort.Slice(manifest.Entries, func(left, right int) bool {
		return manifest.Entries[left].Ref < manifest.Entries[right].Ref
	})
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxAttachmentManifestBytes {
		return fmt.Errorf("coding attachment manifest exceeds %d bytes", MaxAttachmentManifestBytes)
	}
	if directoryErr := s.ensureAttachmentDirectories(threadRoot, attachmentDirectory); directoryErr != nil {
		return directoryErr
	}
	directory, err := threadRoot.OpenRoot(attachmentDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	writeErr := s.writeRoot(directory, attachmentManifest, data, 0o600)
	if writeErr != nil && !fileutil.IsCommittedWriteError(writeErr) {
		return fmt.Errorf("coding attachment manifest: save: %w", writeErr)
	}
	if err := validatePinnedAttachmentDirectory(threadRoot, attachmentDirectory, directory); err != nil {
		return fmt.Errorf("coding attachment manifest: revalidate published directory: %w", err)
	}
	if writeErr != nil {
		return &committedAttachmentManifestError{Err: fmt.Errorf(
			"coding attachment manifest: save: %w",
			writeErr,
		)}
	}
	return nil
}

func validateAttachmentManifest(threadID string, manifest attachmentManifestFile) error {
	if manifest.Version != AttachmentManifestVersion || manifest.ThreadID != threadID ||
		len(manifest.Entries) > MaxThreadAttachments {
		return fmt.Errorf("coding attachment manifest is invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if err := validateAttachment(entry); err != nil {
			return err
		}
		if _, exists := seen[entry.Ref]; exists {
			return fmt.Errorf("coding attachment manifest has duplicate reference")
		}
		seen[entry.Ref] = struct{}{}
	}
	return nil
}

func validateAttachment(entry Attachment) error {
	refID := strings.TrimPrefix(entry.Ref, attachmentRefPrefix)
	if refID == entry.Ref {
		return fmt.Errorf("coding attachment reference is invalid")
	}
	parsedRef, err := uuid.Parse(refID)
	if err != nil || parsedRef.String() != refID {
		return fmt.Errorf("coding attachment reference is invalid")
	}
	if err := validateAttachmentPresentation(entry.Filename, entry.ContentType); err != nil {
		return err
	}
	if entry.Size < 0 || entry.Size > MaxAttachmentBytes || len(entry.SHA256) != sha256.Size*2 ||
		strings.ToLower(entry.SHA256) != entry.SHA256 || !isHex(entry.SHA256) || entry.CreatedAt.IsZero() {
		return fmt.Errorf("coding attachment identity is invalid")
	}
	return nil
}

func requireAttachmentDirectories(root *os.Root, names ...string) error {
	for _, name := range names {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("coding attachment directory is invalid")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("coding attachment: inspect directory %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("coding attachment: directory %q is not direct", name)
		}
	}
	return nil
}

func equivalentAttachment(entries []Attachment, candidate Attachment) (Attachment, bool) {
	for _, entry := range entries {
		if entry.Filename == candidate.Filename && entry.ContentType == candidate.ContentType &&
			entry.Size == candidate.Size && entry.SHA256 == candidate.SHA256 {
			return entry, true
		}
	}
	return Attachment{}, false
}

func attachmentByRef(entries []Attachment, ref string) (Attachment, bool) {
	for _, entry := range entries {
		if entry.Ref == ref {
			return entry, true
		}
	}
	return Attachment{}, false
}
