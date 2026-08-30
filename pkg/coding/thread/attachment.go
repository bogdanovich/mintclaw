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

type attachmentStoreView struct {
	store    *Store
	root     *os.Root
	threads  *os.Root
	thread   *os.Root
	threadID string
}

type pinnedAttachmentDirectory struct {
	parent *os.Root
	name   string
	root   *os.Root
}

type pinnedAttachmentHierarchy struct {
	directories []pinnedAttachmentDirectory
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
		data, canonicalPath, digest, size, readErr := readAttachmentSource(ctx, input.Path, MaxAttachmentBytes)
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
		view, openErr := s.openAttachmentStoreView(metadata.ThreadID)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = view.Close() }()
		if identityErr := view.validateWriter(lease); identityErr != nil {
			return fmt.Errorf("coding attachment: validate writer view: %w", identityErr)
		}
		manifest, loadErr := view.loadManifest()
		if loadErr != nil {
			return loadErr
		}
		candidate := Attachment{
			Filename: filename, ContentType: contentType,
			Size: size, SHA256: digest, CreatedAt: input.At.UTC(),
		}
		_, found := equivalentAttachment(manifest.Entries, candidate)
		if !found && len(manifest.Entries) >= MaxThreadAttachments {
			return fmt.Errorf("coding attachment admission: thread exceeds %d attachments", MaxThreadAttachments)
		}
		if publishErr := view.publishBlob(ctx, digest, data); publishErr != nil {
			return publishErr
		}
		if identityErr := view.validateWriter(lease); identityErr != nil {
			return fmt.Errorf("coding attachment: revalidate writer after blob publication: %w", identityErr)
		}
		if found {
			durable, durabilityErr := view.confirmDurableEquivalent(candidate)
			if durabilityErr != nil {
				return durabilityErr
			}
			if identityErr := view.validateWriter(lease); identityErr != nil {
				return fmt.Errorf("coding attachment: revalidate writer after manifest sync: %w", identityErr)
			}
			admitted = durable
			return nil
		}
		candidate.Ref = attachmentRefPrefix + uuid.NewString()
		manifest.Entries = append(manifest.Entries, candidate)
		admitted = candidate
		if identityErr := view.validateWriter(lease); identityErr != nil {
			admitted = Attachment{}
			return fmt.Errorf("coding attachment: revalidate active thread before manifest publish: %w", identityErr)
		}
		saveErr := view.saveManifest(manifest)
		var committedManifest *committedAttachmentManifestError
		if saveErr == nil || errors.As(saveErr, &committedManifest) {
			if identityErr := view.validateWriter(lease); identityErr != nil {
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
	view, err := s.openAttachmentStoreView(threadID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = view.Close() }()
	manifest, err := view.loadManifest()
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
	view, err := s.openAttachmentStoreView(threadID)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, Attachment{}, contextErr
		}
		return nil, Attachment{}, err
	}
	defer func() { _ = view.Close() }()
	manifest, err := view.loadManifest()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, Attachment{}, contextErr
	}
	if err != nil {
		return nil, Attachment{}, err
	}
	entry, found := attachmentByRef(manifest.Entries, ref)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, Attachment{}, contextErr
	}
	if !found {
		return nil, Attachment{}, &AttachmentUnavailableError{Ref: ref, Reason: "reference is not owned by thread"}
	}
	data, readErr := view.readBlob(ctx, entry.SHA256, entry.Size)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, entry, contextErr
	}
	if readErr != nil {
		return nil, entry, &AttachmentUnavailableError{Ref: ref, Reason: readErr.Error()}
	}
	return data, entry, nil
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
) ([]byte, string, string, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", "", 0, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", "", 0, err
	}
	canonical := filepath.Clean(absolute)
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return nil, "", "", 0, err
	}
	canonical = filepath.Clean(canonical)
	file, err := openAttachmentSourceFile(canonical)
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

func openAttachmentSourceFile(path string) (*os.File, error) {
	parent, err := openCatalogRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	return openCatalogFile(parent, filepath.Base(path))
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

func (s *Store) openAttachmentStoreView(threadID string) (*attachmentStoreView, error) {
	if err := validateThreadID(threadID); err != nil {
		return nil, err
	}
	root, err := openPinnedCatalogRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("coding attachment: pin store root: %w", err)
	}
	threads, err := openPinnedAttachmentChild(root, "threads")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("coding attachment: pin threads directory: %w", err)
	}
	thread, err := openPinnedAttachmentChild(threads, threadID)
	if err != nil {
		_ = errors.Join(threads.Close(), root.Close())
		return nil, fmt.Errorf("coding attachment: pin thread %q: %w", threadID, err)
	}
	view := &attachmentStoreView{store: s, root: root, threads: threads, thread: thread, threadID: threadID}
	if err := view.validateActive(); err != nil {
		return nil, errors.Join(err, view.Close())
	}
	return view, nil
}

func openPinnedAttachmentChild(parent *os.Root, name string) (*os.Root, error) {
	if parent == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("coding attachment: pinned parent and local child name are required")
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("coding attachment: directory %q is not direct", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if err := validatePinnedAttachmentDirectory(parent, name, child); err != nil {
		return nil, errors.Join(err, child.Close())
	}
	return child, nil
}

func (s *Store) openAttachmentHierarchy(
	parent *os.Root,
	durable bool,
	names ...string,
) (*pinnedAttachmentHierarchy, error) {
	hierarchy := &pinnedAttachmentHierarchy{}
	current := parent
	for _, name := range names {
		if !filepath.IsLocal(name) || name == "." {
			return nil, errors.Join(fmt.Errorf("coding attachment directory name is invalid"), hierarchy.Close())
		}
		info, err := current.Lstat(name)
		created := false
		if durable && errors.Is(err, os.ErrNotExist) {
			mkdirErr := current.Mkdir(name, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return nil, errors.Join(
					fmt.Errorf("coding attachment: create directory %q: %w", name, mkdirErr),
					hierarchy.Close(),
				)
			}
			created = mkdirErr == nil
			info, err = current.Lstat(name)
		}
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("coding attachment: inspect directory %q: %w", name, err),
				hierarchy.Close(),
			)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.Join(
				fmt.Errorf("coding attachment: directory %q is not direct", name),
				hierarchy.Close(),
			)
		}
		if durable {
			if chmodErr := current.Chmod(name, 0o700); chmodErr != nil {
				return nil, errors.Join(
					fmt.Errorf("coding attachment: secure directory %q: %w", name, chmodErr),
					hierarchy.Close(),
				)
			}
			if syncErr := s.syncRoot(current); syncErr != nil {
				durabilityErr := fmt.Errorf("coding attachment: sync directory %q parent: %w", name, syncErr)
				if created {
					durabilityErr = &fileutil.CommittedWriteError{Err: durabilityErr}
				}
				return nil, errors.Join(durabilityErr, hierarchy.Close())
			}
		}
		child, err := current.OpenRoot(name)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("coding attachment: open directory %q: %w", name, err),
				hierarchy.Close(),
			)
		}
		if err := validatePinnedAttachmentDirectory(current, name, child); err != nil {
			return nil, errors.Join(err, child.Close(), hierarchy.Close())
		}
		hierarchy.directories = append(hierarchy.directories, pinnedAttachmentDirectory{
			parent: current,
			name:   name,
			root:   child,
		})
		current = child
	}
	return hierarchy, nil
}

func (h *pinnedAttachmentHierarchy) Leaf() *os.Root {
	if h == nil || len(h.directories) == 0 {
		return nil
	}
	return h.directories[len(h.directories)-1].root
}

func (h *pinnedAttachmentHierarchy) validate() error {
	if h == nil {
		return fmt.Errorf("coding attachment hierarchy is required")
	}
	for _, directory := range h.directories {
		if err := validatePinnedAttachmentDirectory(directory.parent, directory.name, directory.root); err != nil {
			return err
		}
	}
	return nil
}

func (h *pinnedAttachmentHierarchy) Close() error {
	if h == nil {
		return nil
	}
	var closeErr error
	for index := len(h.directories) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, h.directories[index].root.Close())
	}
	return closeErr
}

func (v *attachmentStoreView) Close() error {
	if v == nil {
		return nil
	}
	return errors.Join(v.thread.Close(), v.threads.Close(), v.root.Close())
}

func (v *attachmentStoreView) validateActive() error {
	if v == nil || v.store == nil {
		return fmt.Errorf("coding attachment store view is required")
	}
	if err := validatePinnedAttachmentRoot(v.store.root, v.root); err != nil {
		return err
	}
	if err := validatePinnedAttachmentDirectory(v.root, "threads", v.threads); err != nil {
		return err
	}
	return validatePinnedAttachmentDirectory(v.threads, v.threadID, v.thread)
}

func validatePinnedAttachmentRoot(path string, pinned *os.Root) error {
	pinnedInfo, err := pinned.Stat(".")
	if err != nil {
		return fmt.Errorf("coding attachment: inspect pinned store root: %w", err)
	}
	active, err := openCatalogRoot(path)
	if err != nil {
		return fmt.Errorf("coding attachment: reopen active store root: %w", err)
	}
	defer func() { _ = active.Close() }()
	activeInfo, err := active.stat()
	if err != nil {
		return fmt.Errorf("coding attachment: inspect active store root: %w", err)
	}
	if !os.SameFile(pinnedInfo, activeInfo) {
		return fmt.Errorf("coding attachment: store root changed during operation")
	}
	return nil
}

func (v *attachmentStoreView) validateHierarchy(hierarchy *pinnedAttachmentHierarchy) error {
	if err := hierarchy.validate(); err != nil {
		return err
	}
	return v.validateActive()
}

func (v *attachmentStoreView) validateWriter(lease *Lease) error {
	if err := v.validateActive(); err != nil {
		return err
	}
	return validatePinnedAttachmentLease(v.thread, lease)
}

func (v *attachmentStoreView) publishBlob(ctx context.Context, digest string, data []byte) error {
	if err := validateAttachmentDigest(digest); err != nil {
		return fmt.Errorf("coding attachment blob: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hierarchy, err := v.store.openAttachmentHierarchy(v.root, true, "blobs", "sha256", digest[:2])
	if err != nil {
		return err
	}
	defer func() { _ = hierarchy.Close() }()
	shard := hierarchy.Leaf()
	existing := false
	if _, statErr := shard.Lstat(digest); statErr == nil {
		existing = true
		_, actual, size, readErr := readAttachmentRootFile(ctx, shard, digest, MaxAttachmentBytes)
		if readErr != nil || actual != digest || size != int64(len(data)) {
			return fmt.Errorf("coding attachment blob: existing digest path is invalid")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("coding attachment blob: inspect digest path: %w", statErr)
	} else {
		if writeErr := v.store.writeRoot(shard, digest, data, 0o600); writeErr != nil {
			return fmt.Errorf("coding attachment blob: publish: %w", writeErr)
		}
		_, actual, size, readErr := readAttachmentRootFile(ctx, shard, digest, MaxAttachmentBytes)
		if readErr != nil || actual != digest || size != int64(len(data)) {
			return fmt.Errorf("coding attachment blob: published content could not be verified: %w", readErr)
		}
	}
	if existing {
		if syncErr := v.store.syncRoot(shard); syncErr != nil {
			return fmt.Errorf("coding attachment blob: sync existing digest directory: %w", syncErr)
		}
	}
	if err := v.validateHierarchy(hierarchy); err != nil {
		return fmt.Errorf("coding attachment blob: revalidate hierarchy: %w", err)
	}
	return nil
}

func (v *attachmentStoreView) readBlob(ctx context.Context, digest string, expectedSize int64) ([]byte, error) {
	if err := validateAttachmentDigest(digest); err != nil {
		return nil, fmt.Errorf("coding attachment blob: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hierarchy, err := v.store.openAttachmentHierarchy(v.root, false, "blobs", "sha256", digest[:2])
	if err != nil {
		return nil, err
	}
	defer func() { _ = hierarchy.Close() }()
	data, actual, size, err := readAttachmentRootFile(ctx, hierarchy.Leaf(), digest, MaxAttachmentBytes)
	if err != nil {
		return nil, err
	}
	if actual != digest || size != expectedSize {
		return nil, fmt.Errorf("content identity changed")
	}
	if err := v.validateHierarchy(hierarchy); err != nil {
		return nil, fmt.Errorf("coding attachment blob: revalidate hierarchy: %w", err)
	}
	return data, nil
}

func validateAttachmentDigest(digest string) error {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest || !isHex(digest) {
		return fmt.Errorf("digest is invalid")
	}
	return nil
}

func (v *attachmentStoreView) loadManifest() (attachmentManifestFile, error) {
	empty := attachmentManifestFile{
		Version: AttachmentManifestVersion, ThreadID: v.threadID, Entries: []Attachment{},
	}
	hierarchy, err := v.store.openAttachmentHierarchy(v.thread, false, attachmentDirectory)
	if errors.Is(err, os.ErrNotExist) {
		if validationErr := v.validateActive(); validationErr != nil {
			return attachmentManifestFile{}, validationErr
		}
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: open directory: %w", err)
	}
	defer func() { _ = hierarchy.Close() }()
	return v.loadManifestFromHierarchy(hierarchy)
}

func (v *attachmentStoreView) loadManifestFromHierarchy(
	hierarchy *pinnedAttachmentHierarchy,
) (attachmentManifestFile, error) {
	empty := attachmentManifestFile{
		Version: AttachmentManifestVersion, ThreadID: v.threadID, Entries: []Attachment{},
	}
	data, err := readAttachmentManifestData(hierarchy.Leaf())
	if errors.Is(err, os.ErrNotExist) {
		if validationErr := v.validateHierarchy(hierarchy); validationErr != nil {
			return attachmentManifestFile{}, validationErr
		}
		return empty, nil
	}
	if err != nil {
		return attachmentManifestFile{}, err
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
	if err := validateAttachmentManifest(v.threadID, manifest); err != nil {
		return attachmentManifestFile{}, err
	}
	if err := v.validateHierarchy(hierarchy); err != nil {
		return attachmentManifestFile{}, fmt.Errorf("coding attachment manifest: revalidate hierarchy: %w", err)
	}
	return manifest, nil
}

func (v *attachmentStoreView) confirmDurableEquivalent(candidate Attachment) (Attachment, error) {
	hierarchy, err := v.store.openAttachmentHierarchy(v.thread, false, attachmentDirectory)
	if err != nil {
		return Attachment{}, fmt.Errorf("coding attachment manifest: reopen directory for durability: %w", err)
	}
	defer func() { _ = hierarchy.Close() }()
	manifest, err := v.loadManifestFromHierarchy(hierarchy)
	if err != nil {
		return Attachment{}, err
	}
	existing, found := equivalentAttachment(manifest.Entries, candidate)
	if !found {
		return Attachment{}, fmt.Errorf("coding attachment manifest changed before durability confirmation")
	}
	if syncErr := v.store.syncRoot(hierarchy.Leaf()); syncErr != nil {
		return Attachment{}, fmt.Errorf("coding attachment manifest: sync existing directory: %w", syncErr)
	}
	if err := v.validateHierarchy(hierarchy); err != nil {
		return Attachment{}, fmt.Errorf("coding attachment manifest: revalidate synced hierarchy: %w", err)
	}
	return existing, nil
}

func readAttachmentManifestData(root *os.Root) ([]byte, error) {
	entry, err := root.Lstat(attachmentManifest)
	if err != nil {
		return nil, err
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("coding attachment manifest is a symbolic link")
	}
	file, err := root.OpenFile(attachmentManifest, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(entry, info) {
		return nil, fmt.Errorf("coding attachment manifest changed while opening")
	}
	if validationErr := validateCatalogMetadataFile(file, info); validationErr != nil {
		return nil, fmt.Errorf("coding attachment manifest: unsafe file: %w", validationErr)
	}
	if info.Size() < 0 || info.Size() > MaxAttachmentManifestBytes {
		return nil, fmt.Errorf("coding attachment manifest exceeds %d bytes", MaxAttachmentManifestBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentManifestBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(attachmentManifest)
	if err != nil {
		return nil, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(entry, after) || !os.SameFile(entry, current) ||
		info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("coding attachment manifest changed while reading")
	}
	return data, nil
}

func (v *attachmentStoreView) saveManifest(manifest attachmentManifestFile) error {
	if err := validateAttachmentManifest(v.threadID, manifest); err != nil {
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
	hierarchy, err := v.store.openAttachmentHierarchy(v.thread, true, attachmentDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = hierarchy.Close() }()
	writeErr := v.store.writeRoot(hierarchy.Leaf(), attachmentManifest, data, 0o600)
	if writeErr != nil && !fileutil.IsCommittedWriteError(writeErr) {
		return fmt.Errorf("coding attachment manifest: save: %w", writeErr)
	}
	if err := v.validateHierarchy(hierarchy); err != nil {
		return fmt.Errorf("coding attachment manifest: revalidate hierarchy: %w", err)
	}
	if writeErr != nil {
		return &committedAttachmentManifestError{Err: fmt.Errorf(
			"coding attachment manifest: save: %w",
			writeErr,
		)}
	}
	return nil
}

func validatePinnedAttachmentLease(threadRoot *os.Root, lease *Lease) error {
	entry, err := threadRoot.Lstat(leaseFileName)
	if err != nil {
		return fmt.Errorf("coding attachment: inspect pinned lease file: %w", err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("coding attachment: pinned lease file is a symbolic link")
	}
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
	if !os.SameFile(entry, pinnedInfo) {
		return fmt.Errorf("coding attachment: pinned lease file changed while opening")
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
	activePathInfo, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("coding attachment: recheck current directory %q: %w", name, err)
	}
	if activePathInfo.Mode()&os.ModeSymlink != 0 || !activePathInfo.IsDir() ||
		!os.SameFile(activePathInfo, currentInfo) || !os.SameFile(pinnedInfo, currentInfo) {
		return fmt.Errorf("coding attachment: directory %q changed during operation", name)
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
