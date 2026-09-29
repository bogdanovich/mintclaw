package coding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type materializedAttachment struct {
	path     string
	meta     media.MediaMeta
	identity os.FileInfo
	size     int64
	digest   string
}

// codingAttachmentMediaStore resolves thread-owned immutable bytes into a
// private, process-owned media directory. Other media lifecycle operations are
// delegated so coding tools can continue to emit ordinary media refs.
type codingAttachmentMediaStore struct {
	mu sync.Mutex

	store    *thread.Store
	lease    *thread.Lease
	threadID string
	delegate *media.FileMediaStore
	owner    *media.MediaOwner

	resolveCtx     context.Context
	cancelResolve  context.CancelFunc
	parentRoot     *os.Root
	baseRoot       *os.Root
	privateRoot    *os.Root
	baseName       string
	privateName    string
	privatePath    string
	materialized   map[string]materializedAttachment
	delegateScopes map[string]struct{}
	closed         bool
}

var (
	_ media.CodingMediaStore          = (*codingAttachmentMediaStore)(nil)
	_ document.OwnedMediaResolver     = (*codingAttachmentMediaStore)(nil)
	_ tools.CodingRemoteArtifactStore = (*codingAttachmentMediaStore)(nil)
)

func newCodingAttachmentMediaStore(
	store *thread.Store,
	lease *thread.Lease,
	threadID string,
	owner *media.MediaOwner,
) (*codingAttachmentMediaStore, error) {
	if store == nil || lease == nil || strings.TrimSpace(threadID) == "" {
		return nil, fmt.Errorf("coding attachment media store requires a thread store, lease, and ID")
	}
	if err := store.ValidateLease(lease, threadID); err != nil {
		return nil, fmt.Errorf("coding attachment media store requires the active thread lease: %w", err)
	}
	basePath := filepath.Clean(media.TempDir())
	parentPath := filepath.Dir(basePath)
	baseName := filepath.Base(basePath)
	if !filepath.IsLocal(baseName) || baseName == "." {
		return nil, fmt.Errorf("coding attachment media base is invalid")
	}
	parentRoot, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("coding attachment media: open temporary parent: %w", err)
	}
	cleanupParent := true
	defer func() {
		if cleanupParent {
			_ = parentRoot.Close()
		}
	}()
	if directoryErr := ensureDirectDirectory(parentRoot, baseName); directoryErr != nil {
		return nil, directoryErr
	}
	baseRoot, err := parentRoot.OpenRoot(baseName)
	if err != nil {
		return nil, fmt.Errorf("coding attachment media: open base: %w", err)
	}
	privateName := "coding-attachments-" + uuid.NewString()
	if mkdirErr := baseRoot.Mkdir(privateName, 0o700); mkdirErr != nil {
		_ = baseRoot.Close()
		return nil, fmt.Errorf("coding attachment media: create private directory: %w", mkdirErr)
	}
	privateRoot, err := baseRoot.OpenRoot(privateName)
	if err != nil {
		_ = baseRoot.Close()
		return nil, fmt.Errorf("coding attachment media: open private directory: %w", err)
	}
	resolveCtx, cancelResolve := context.WithCancel(context.Background())
	var boundOwner *media.MediaOwner
	if owner != nil {
		cloned := *owner
		boundOwner = &cloned
	}
	result := &codingAttachmentMediaStore{
		store:          store,
		lease:          lease,
		threadID:       threadID,
		delegate:       media.NewFileMediaStore(),
		owner:          boundOwner,
		resolveCtx:     resolveCtx,
		cancelResolve:  cancelResolve,
		parentRoot:     parentRoot,
		baseRoot:       baseRoot,
		privateRoot:    privateRoot,
		baseName:       baseName,
		privateName:    privateName,
		privatePath:    filepath.Join(basePath, privateName),
		materialized:   make(map[string]materializedAttachment),
		delegateScopes: make(map[string]struct{}),
	}
	if err := result.validateHierarchy(); err != nil {
		_ = result.Close()
		return nil, err
	}
	cleanupParent = false
	return result, nil
}

func ensureDirectDirectory(parent *os.Root, name string) error {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := parent.Mkdir(name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("coding attachment media: create base: %w", mkdirErr)
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return fmt.Errorf("coding attachment media: inspect base: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("coding attachment media: base must be a direct directory")
	}
	return nil
}

func (s *codingAttachmentMediaStore) Store(
	localPath string,
	meta media.MediaMeta,
	scope string,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("coding attachment media store is closed")
	}
	ref, err := s.delegate.Store(localPath, meta, scope)
	if err == nil && strings.TrimSpace(scope) != "" {
		s.delegateScopes[scope] = struct{}{}
	}
	return ref, err
}

// CodingDocumentAuthorityAvailable reports whether this thread store was
// bound to the same runtime principal that document calls receive per turn.
func (s *codingAttachmentMediaStore) CodingDocumentAuthorityAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.owner != nil
}

func (s *codingAttachmentMediaStore) BindOwner(ref string, owner media.MediaOwner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("coding attachment media store is closed")
	}
	if err := s.requireDocumentOwner(owner); err != nil {
		return err
	}
	if !thread.IsAttachmentRef(ref) {
		return s.delegate.BindOwner(ref, owner)
	}
	_, _, err := s.resolveWithMetaLocked(ref)
	return err
}

func (s *codingAttachmentMediaStore) OpenOwned(
	ref string,
	owner media.MediaOwner,
) (*media.OwnedMediaSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("coding attachment media store is closed")
	}
	if err := s.requireDocumentOwner(owner); err != nil {
		return nil, err
	}
	if !thread.IsAttachmentRef(ref) {
		return s.delegate.OpenOwned(ref, owner)
	}
	path, meta, err := s.resolveWithMetaLocked(ref)
	if err != nil {
		return nil, err
	}
	cached, ok := s.materialized[ref]
	if !ok || cached.path != path {
		return nil, fmt.Errorf("coding attachment media: materialized source is unavailable")
	}
	file, err := s.privateRoot.Open(filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("coding attachment media: open owned source: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(cached.identity, info) {
		_ = file.Close()
		return nil, fmt.Errorf("coding attachment media: owned source identity changed")
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, io.LimitReader(file, thread.MaxAttachmentBytes+1))
	if readErr != nil || size != cached.size || size > thread.MaxAttachmentBytes ||
		hex.EncodeToString(hash.Sum(nil)) != cached.digest {
		_ = file.Close()
		return nil, fmt.Errorf("coding attachment media: owned source identity changed")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("coding attachment media: rewind owned source: %w", err)
	}
	return &media.OwnedMediaSource{
		File: file,
		Meta: meta,
		Identity: media.ContentIdentity{
			Size: cached.size, SHA256: cached.digest,
		},
	}, nil
}

func (s *codingAttachmentMediaStore) requireDocumentOwner(owner media.MediaOwner) error {
	if s.owner == nil || *s.owner != owner {
		return fmt.Errorf("coding attachment media: document owner is not authorized")
	}
	return nil
}

func (s *codingAttachmentMediaStore) ImportRemoteArtifact(
	ctx context.Context,
	localPath string,
	meta tools.CodingRemoteArtifactMetadata,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("coding attachment media store is closed")
	}
	if ctx == nil || meta.Size < 0 || meta.Size > thread.MaxAttachmentBytes ||
		len(meta.SHA256) != sha256.Size*2 {
		return "", fmt.Errorf("remote coding artifact metadata is invalid")
	}
	file, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open remote coding artifact: %w", err)
	}
	digest := sha256.New()
	size, copyErr := io.Copy(digest, io.LimitReader(file, thread.MaxAttachmentBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size != meta.Size || size > thread.MaxAttachmentBytes ||
		hex.EncodeToString(digest.Sum(nil)) != meta.SHA256 {
		return "", fmt.Errorf("remote coding artifact identity changed")
	}
	metadata, err := s.store.Load(s.threadID)
	if err != nil {
		return "", fmt.Errorf("load coding thread for remote artifact: %w", err)
	}
	attachment, err := s.store.AdmitAttachment(ctx, s.lease, metadata, thread.AttachmentInput{
		Path: localPath, Filename: meta.Filename, ContentType: meta.ContentType, At: time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	if attachment.Size != meta.Size || attachment.SHA256 != meta.SHA256 {
		return "", fmt.Errorf("remote coding artifact admission identity changed")
	}
	return attachment.Ref, nil
}

func (s *codingAttachmentMediaStore) Resolve(ref string) (string, error) {
	path, _, err := s.ResolveWithMeta(ref)
	return path, err
}

func (s *codingAttachmentMediaStore) ShouldResolveHistorical(ref string) bool {
	return !thread.IsAttachmentRef(ref)
}

func (s *codingAttachmentMediaStore) ShouldAttachCurrentImage(ref string, meta media.MediaMeta) bool {
	return thread.IsAttachmentRef(ref) && media.IsSupportedImageContentType(meta.ContentType)
}

func (s *codingAttachmentMediaStore) ListReferences(ctx context.Context) ([]media.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("coding attachment media store is closed")
	}
	attachments, err := s.store.ListAttachmentsWithLease(ctx, s.lease, s.threadID)
	if err != nil {
		return nil, err
	}
	result := make([]media.Reference, len(attachments))
	for index, attachment := range attachments {
		result[index] = media.Reference{
			Ref:         attachment.Ref,
			Filename:    attachment.Filename,
			ContentType: attachment.ContentType,
			Size:        attachment.Size,
			CreatedAt:   attachment.CreatedAt,
		}
	}
	return result, nil
}

func (s *codingAttachmentMediaStore) ReadReference(
	ctx context.Context,
	ref string,
) ([]byte, media.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, media.Reference{}, fmt.Errorf("coding attachment media store is closed")
	}
	data, attachment, err := s.store.ResolveAttachmentWithLease(ctx, s.lease, s.threadID, ref)
	if err != nil {
		return nil, media.Reference{}, err
	}
	return data, media.Reference{
		Ref:         attachment.Ref,
		Filename:    attachment.Filename,
		ContentType: attachment.ContentType,
		Size:        attachment.Size,
		CreatedAt:   attachment.CreatedAt,
	}, nil
}

func (s *codingAttachmentMediaStore) ResolveWithMeta(ref string) (string, media.MediaMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", media.MediaMeta{}, fmt.Errorf("coding attachment media store is closed")
	}
	return s.resolveWithMetaLocked(ref)
}

func (s *codingAttachmentMediaStore) resolveWithMetaLocked(ref string) (string, media.MediaMeta, error) {
	if !thread.IsAttachmentRef(ref) {
		return s.delegate.ResolveWithMeta(ref)
	}
	data, attachment, err := s.store.ResolveAttachmentWithLease(s.resolveCtx, s.lease, s.threadID, ref)
	if err != nil {
		return "", media.MediaMeta{}, err
	}
	verifiedImageContentType := media.DetectSupportedImageContentType(data)
	if cached, ok := s.materialized[ref]; ok {
		if validationErr := s.validateMaterialized(cached, attachment); validationErr != nil {
			return "", media.MediaMeta{}, validationErr
		}
		return cached.path, cached.meta, nil
	}
	name := materializedAttachmentName(ref, attachment.Filename)
	file, err := s.privateRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", media.MediaMeta{}, fmt.Errorf("coding attachment media: create materialized file: %w", err)
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if materializeErr := errors.Join(writeErr, syncErr, closeErr); materializeErr != nil {
		_ = s.privateRoot.Remove(name)
		return "", media.MediaMeta{}, fmt.Errorf(
			"coding attachment media: materialize verified bytes: %w",
			materializeErr,
		)
	}
	path := filepath.Join(s.privatePath, name)
	identity, err := s.privateRoot.Lstat(name)
	if err != nil {
		_ = s.privateRoot.Remove(name)
		return "", media.MediaMeta{}, fmt.Errorf("coding attachment media: inspect materialized identity: %w", err)
	}
	cached := materializedAttachment{
		path: path, identity: identity, size: attachment.Size, digest: attachment.SHA256,
	}
	if err := s.validateMaterialized(cached, attachment); err != nil {
		_ = s.privateRoot.Remove(name)
		return "", media.MediaMeta{}, err
	}
	contentType := attachment.ContentType
	if strings.HasPrefix(strings.ToLower(contentType), "image/") {
		contentType = "application/octet-stream"
	}
	meta := media.MediaMeta{
		Filename:      attachment.Filename,
		ContentType:   contentType,
		Source:        "coding-attachment",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}
	if verifiedImageContentType != "" {
		meta.ContentType = verifiedImageContentType
	}
	cached.meta = meta
	s.materialized[ref] = cached
	return path, meta, nil
}

func materializedAttachmentName(ref, filename string) string {
	digest := sha256.Sum256([]byte(ref))
	extension := strings.ToLower(filepath.Ext(filename))
	if len(extension) > 16 || strings.ContainsAny(extension, `/\\`) {
		extension = ""
	}
	return hex.EncodeToString(digest[:]) + extension
}

func (s *codingAttachmentMediaStore) validateHierarchy() error {
	baseEntry, err := s.parentRoot.Lstat(s.baseName)
	if err != nil {
		return fmt.Errorf("coding attachment media: inspect active base: %w", err)
	}
	basePinned, err := s.baseRoot.Stat(".")
	if err != nil || baseEntry.Mode()&os.ModeSymlink != 0 || !baseEntry.IsDir() ||
		!os.SameFile(baseEntry, basePinned) {
		return fmt.Errorf("coding attachment media: base directory changed")
	}
	privateEntry, err := s.baseRoot.Lstat(s.privateName)
	if err != nil {
		return fmt.Errorf("coding attachment media: inspect active private directory: %w", err)
	}
	privatePinned, err := s.privateRoot.Stat(".")
	if err != nil || privateEntry.Mode()&os.ModeSymlink != 0 || !privateEntry.IsDir() ||
		!os.SameFile(privateEntry, privatePinned) {
		return fmt.Errorf("coding attachment media: private directory changed")
	}
	return nil
}

func (s *codingAttachmentMediaStore) validateMaterialized(
	cached materializedAttachment,
	attachment thread.Attachment,
) error {
	if err := s.validateHierarchy(); err != nil {
		return err
	}
	if cached.size != attachment.Size || cached.digest != attachment.SHA256 {
		return fmt.Errorf("coding attachment media: cached attachment identity changed")
	}
	name := filepath.Base(cached.path)
	pinned, err := s.privateRoot.Open(name)
	if err != nil {
		return fmt.Errorf("coding attachment media: open materialized file: %w", err)
	}
	defer func() { _ = pinned.Close() }()
	before, err := pinned.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(cached.identity, before) {
		return fmt.Errorf("coding attachment media: materialized file changed")
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, io.LimitReader(pinned, thread.MaxAttachmentBytes+1))
	active, activeErr := os.Lstat(cached.path)
	if readErr != nil || activeErr != nil || size != cached.size || size > thread.MaxAttachmentBytes ||
		!os.SameFile(before, active) || hex.EncodeToString(hash.Sum(nil)) != cached.digest {
		return fmt.Errorf("coding attachment media: materialized file changed")
	}
	return nil
}

func (s *codingAttachmentMediaStore) ReleaseAll(scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("coding attachment media store is closed")
	}
	err := s.delegate.ReleaseAll(scope)
	if err == nil {
		delete(s.delegateScopes, scope)
	}
	return err
}

func (s *codingAttachmentMediaStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancelResolve()
	var delegateErr error
	for scope := range s.delegateScopes {
		delegateErr = errors.Join(delegateErr, s.delegate.ReleaseAll(scope))
	}
	s.delegateScopes = nil
	s.delegate.Stop()
	hierarchyErr := s.validateHierarchy()
	closePrivateErr := s.privateRoot.Close()
	var removeErr error
	if hierarchyErr == nil {
		removeErr = s.baseRoot.RemoveAll(s.privateName)
	}
	closeBaseErr := s.baseRoot.Close()
	closeParentErr := s.parentRoot.Close()
	return errors.Join(delegateErr, hierarchyErr, closePrivateErr, removeErr, closeBaseErr, closeParentErr)
}
