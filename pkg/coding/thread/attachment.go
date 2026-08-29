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
		manifest, loadErr := s.loadAttachmentManifest(metadata.ThreadID)
		if loadErr != nil {
			return loadErr
		}
		candidate := Attachment{
			Filename: filename, ContentType: contentType,
			Size: size, SHA256: digest, CreatedAt: input.At.UTC(),
		}
		if existing, found := equivalentAttachment(manifest.Entries, candidate); found {
			admitted = existing
			return nil
		}
		if len(manifest.Entries) >= MaxThreadAttachments {
			return fmt.Errorf("coding attachment admission: thread exceeds %d attachments", MaxThreadAttachments)
		}
		storeRoot, threadRoot, pinErr := s.openPinnedAttachmentRoots(lease, metadata.ThreadID)
		if pinErr != nil {
			return pinErr
		}
		defer func() { _ = errors.Join(threadRoot.Close(), storeRoot.Close()) }()
		if publishErr := s.publishAttachmentBlob(ctx, storeRoot, digest, data); publishErr != nil {
			return publishErr
		}
		candidate.Ref = attachmentRefPrefix + uuid.NewString()
		manifest.Entries = append(manifest.Entries, candidate)
		admitted = candidate
		if identityErr := s.validateAcquiredLeasePath(metadata.ThreadID, lease.file); identityErr != nil {
			admitted = Attachment{}
			return fmt.Errorf("coding attachment: revalidate active thread before manifest publish: %w", identityErr)
		}
		if identityErr := validatePinnedAttachmentLease(threadRoot, lease); identityErr != nil {
			admitted = Attachment{}
			return identityErr
		}
		if saveErr := s.saveAttachmentManifest(threadRoot, metadata.ThreadID, manifest); saveErr != nil {
			if fileutil.IsCommittedWriteError(saveErr) {
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

// ResolveAttachment returns a stable verified path for a reference owned by
// the selected thread. It never repairs, rewrites, or removes manifest state.
func (s *Store) ResolveAttachment(
	ctx context.Context,
	threadID string,
	ref string,
) (string, Attachment, error) {
	if s == nil {
		return "", Attachment{}, fmt.Errorf("coding attachment store is nil")
	}
	if ctx == nil {
		return "", Attachment{}, fmt.Errorf("coding attachment resolve: context is required")
	}
	manifest, err := s.loadAttachmentManifest(threadID)
	if err != nil {
		return "", Attachment{}, err
	}
	entry, found := attachmentByRef(manifest.Entries, ref)
	if !found {
		return "", Attachment{}, &AttachmentUnavailableError{Ref: ref, Reason: "reference is not owned by thread"}
	}
	path := s.attachmentBlobPath(entry.SHA256)
	_, _, digest, size, readErr := readAttachmentSource(ctx, path, MaxAttachmentBytes, false)
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return "", entry, readErr
		}
		return "", entry, &AttachmentUnavailableError{Ref: ref, Reason: readErr.Error()}
	}
	if digest != entry.SHA256 || size != entry.Size {
		return "", entry, &AttachmentUnavailableError{Ref: ref, Reason: "content identity changed"}
	}
	return path, entry, nil
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
		return nil
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
	return nil
}

func (s *Store) ensureAttachmentDirectories(root *os.Root, names ...string) error {
	for _, name := range names {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("coding attachment directory is invalid")
		}
		info, err := root.Lstat(name)
		created := false
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := root.Mkdir(name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return fmt.Errorf("coding attachment: create directory %q: %w", name, mkdirErr)
			}
			created = true
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
		if created {
			if syncErr := s.syncAttachmentDirectoryParent(root, name); syncErr != nil {
				return &fileutil.CommittedWriteError{Err: fmt.Errorf(
					"coding attachment: sync new directory %q: %w",
					name,
					syncErr,
				)}
			}
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
	empty := attachmentManifestFile{Version: AttachmentManifestVersion, ThreadID: threadID, Entries: []Attachment{}}
	threadRoot, err := openAttachmentThreadDirectory(s.root, threadID)
	if err != nil {
		return attachmentManifestFile{}, err
	}
	defer func() { _ = threadRoot.Close() }()
	openedThreadInfo, err := threadRoot.stat()
	if err != nil {
		return attachmentManifestFile{}, err
	}
	attachments, err := openCatalogChildDirectory(threadRoot, attachmentDirectory)
	if errors.Is(err, os.ErrNotExist) {
		if validationErr := validateAttachmentThreadPath(s.root, threadID, openedThreadInfo); validationErr != nil {
			return attachmentManifestFile{}, validationErr
		}
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: open directory: %w", err)
	}
	defer func() { _ = attachments.Close() }()
	file, err := openCatalogFile(attachments, attachmentManifest)
	if errors.Is(err, os.ErrNotExist) {
		if validationErr := validateAttachmentThreadPath(s.root, threadID, openedThreadInfo); validationErr != nil {
			return attachmentManifestFile{}, validationErr
		}
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: open: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return attachmentManifestFile{}, err
	}
	if validationErr := validateCatalogMetadataFile(file, info); validationErr != nil {
		return attachmentManifestFile{}, fmt.Errorf(
			"coding attachment manifest: unsafe file: %w",
			validationErr,
		)
	}
	if info.Size() < 0 || info.Size() > MaxAttachmentManifestBytes {
		return attachmentManifestFile{}, fmt.Errorf(
			"coding attachment manifest exceeds %d bytes",
			MaxAttachmentManifestBytes,
		)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentManifestBytes+1))
	if err != nil {
		return attachmentManifestFile{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return attachmentManifestFile{}, err
	}
	current, err := openCatalogFile(attachments, attachmentManifest)
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
	if !os.SameFile(info, after) || !os.SameFile(info, currentInfo) || info.Size() != after.Size() ||
		info.ModTime() != after.ModTime() {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest changed while reading")
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
	if validationErr := validateAttachmentThreadPath(s.root, threadID, openedThreadInfo); validationErr != nil {
		return attachmentManifestFile{}, validationErr
	}
	return manifest, nil
}

func openAttachmentThreadDirectory(rootPath, threadID string) (*catalogDirectory, error) {
	root, err := openCatalogRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("coding attachment manifest: anchor store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	threads, err := openCatalogChildDirectory(root, "threads")
	if err != nil {
		return nil, fmt.Errorf("coding attachment manifest: open threads directory: %w", err)
	}
	defer func() { _ = threads.Close() }()
	threadRoot, err := openCatalogChildDirectory(threads, threadID)
	if err != nil {
		return nil, fmt.Errorf("coding attachment manifest: open thread %q: %w", threadID, err)
	}
	return threadRoot, nil
}

func validateAttachmentThreadPath(rootPath, threadID string, opened os.FileInfo) error {
	current, err := openAttachmentThreadDirectory(rootPath, threadID)
	if err != nil {
		return fmt.Errorf("coding attachment manifest: revalidate thread path: %w", err)
	}
	defer func() { _ = current.Close() }()
	currentInfo, err := current.stat()
	if err != nil {
		return fmt.Errorf("coding attachment manifest: revalidate thread path: %w", err)
	}
	if !os.SameFile(opened, currentInfo) {
		return fmt.Errorf("coding attachment manifest: thread path changed while reading")
	}
	return nil
}

func (s *Store) openPinnedAttachmentRoots(lease *Lease, threadID string) (*os.Root, *os.Root, error) {
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
	if err := validatePinnedAttachmentLease(threadRoot, lease); err != nil {
		_ = threadRoot.Close()
		return nil, nil, err
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
	if err := s.writeRoot(directory, attachmentManifest, data, 0o600); err != nil {
		return fmt.Errorf("coding attachment manifest: save: %w", err)
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

func (s *Store) attachmentBlobPath(digest string) string {
	return filepath.Join(s.root, "blobs", "sha256", digest[:2], digest)
}
