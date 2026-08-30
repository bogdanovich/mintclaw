package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

type fakeReferenceCatalogStore struct {
	references []media.Reference
	data       map[string][]byte
	listErr    error
	readErr    error
}

func (*fakeReferenceCatalogStore) Store(string, media.MediaMeta, string) (string, error) {
	return "", errors.New("not implemented")
}

func (*fakeReferenceCatalogStore) Resolve(string) (string, error) {
	return "", errors.New("not implemented")
}

func (*fakeReferenceCatalogStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, errors.New("not implemented")
}

func (*fakeReferenceCatalogStore) ReleaseAll(string) error { return nil }

func (s *fakeReferenceCatalogStore) ListReferences(context.Context) ([]media.Reference, error) {
	return append([]media.Reference(nil), s.references...), s.listErr
}

func (s *fakeReferenceCatalogStore) ReadReference(
	_ context.Context,
	ref string,
) ([]byte, media.Reference, error) {
	if s.readErr != nil {
		return nil, media.Reference{}, s.readErr
	}
	for _, reference := range s.references {
		if reference.Ref == ref {
			return append([]byte(nil), s.data[ref]...), reference, nil
		}
	}
	return nil, media.Reference{}, errors.New("reference not found")
}

func newFakeReferenceCatalogStore() *fakeReferenceCatalogStore {
	now := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	imageData, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	)
	if err != nil {
		panic(err)
	}
	return &fakeReferenceCatalogStore{
		references: []media.Reference{
			{Ref: "media://old", Filename: "old.log", ContentType: "text/plain", Size: 3, CreatedAt: now},
			{
				Ref:         "media://image",
				Filename:    "screen.png",
				ContentType: "image/png",
				Size:        int64(len(imageData)),
				CreatedAt:   now.Add(time.Minute),
			},
			{
				Ref:         "media://new",
				Filename:    "new.log",
				ContentType: "text/plain",
				Size:        6,
				CreatedAt:   now.Add(2 * time.Minute),
			},
		},
		data: map[string][]byte{
			"media://old":   []byte("old"),
			"media://image": imageData,
			"media://new":   []byte("αβγ"),
		},
	}
}

func TestCodingAttachmentToolListsNewestMetadataWithPagingAndFilter(t *testing.T) {
	store := newFakeReferenceCatalogStore()
	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(store)
	result := tool.Execute(t.Context(), map[string]any{
		"action": "list", "query": ".log", "limit": float64(1),
	})
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	var response codingAttachmentListResponse
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 2 || response.NextOffset != 1 || len(response.Attachments) != 1 ||
		response.Attachments[0].Ref != "media://new" {
		t.Fatalf("list response = %+v", response)
	}
	if store.references[0].Ref != "media://old" {
		t.Fatal("list mutated catalog ordering")
	}
}

func TestCodingAttachmentToolOpensImageAsCurrentMedia(t *testing.T) {
	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(newFakeReferenceCatalogStore())
	result := tool.Execute(t.Context(), map[string]any{"action": "open", "ref": "media://image"})
	if result.IsError || len(result.ContextMedia) != 1 || result.ContextMedia[0] != "media://image" ||
		len(result.Media) != 0 || result.Deliverable != nil {
		t.Fatalf("image result = %+v", result)
	}
}

func TestCodingAttachmentToolVerifiesImageBytesInsteadOfMetadata(t *testing.T) {
	store := newFakeReferenceCatalogStore()
	store.references = append(store.references, media.Reference{
		Ref: "media://fake-image", Filename: "fake.png", ContentType: "image/png", Size: 8,
	})
	store.data["media://fake-image"] = []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe, 0xfd, 0xfc}
	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(store)
	result := tool.Execute(t.Context(), map[string]any{"action": "open", "ref": "media://fake-image"})
	if !result.IsError || len(result.ContextMedia) != 0 || len(result.Media) != 0 {
		t.Fatalf("mislabeled image result = %+v", result)
	}
}

func TestCodingAttachmentToolRejectsTruncatedImageBody(t *testing.T) {
	store := newFakeReferenceCatalogStore()
	truncated := append([]byte(nil), store.data["media://image"][:33]...)
	store.references = append(store.references, media.Reference{
		Ref: "media://truncated", Filename: "truncated.png", ContentType: "image/png", Size: int64(len(truncated)),
	})
	store.data["media://truncated"] = truncated
	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(store)
	result := tool.Execute(t.Context(), map[string]any{"action": "open", "ref": "media://truncated"})
	if !result.IsError || len(result.ContextMedia) != 0 {
		t.Fatalf("truncated image result = %+v", result)
	}
}

type threadReferenceCatalogStore struct {
	store    *thread.Store
	threadID string
}

func (*threadReferenceCatalogStore) Store(string, media.MediaMeta, string) (string, error) {
	return "", errors.New("not implemented")
}

func (*threadReferenceCatalogStore) Resolve(string) (string, error) {
	return "", errors.New("not implemented")
}

func (*threadReferenceCatalogStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, errors.New("not implemented")
}

func (*threadReferenceCatalogStore) ReleaseAll(string) error { return nil }

func (s *threadReferenceCatalogStore) ListReferences(context.Context) ([]media.Reference, error) {
	attachments, err := s.store.ListAttachments(s.threadID)
	if err != nil {
		return nil, err
	}
	references := make([]media.Reference, len(attachments))
	for index, attachment := range attachments {
		references[index] = media.Reference{
			Ref: attachment.Ref, Filename: attachment.Filename, ContentType: attachment.ContentType,
			Size: attachment.Size, CreatedAt: attachment.CreatedAt,
		}
	}
	return references, nil
}

func (s *threadReferenceCatalogStore) ReadReference(
	ctx context.Context,
	ref string,
) ([]byte, media.Reference, error) {
	data, attachment, err := s.store.ResolveAttachment(ctx, s.threadID, ref)
	if err != nil {
		return nil, media.Reference{}, err
	}
	return data, media.Reference{
		Ref: attachment.Ref, Filename: attachment.Filename, ContentType: attachment.ContentType,
		Size: attachment.Size, CreatedAt: attachment.CreatedAt,
	}, nil
}

func TestCodingAttachmentToolSortsRealThreadCatalogNewestFirst(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "attachment ordering", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	inputDirectory := t.TempDir()
	inputs := make([]thread.AttachmentInput, 3)
	for index := range inputs {
		path := filepath.Join(inputDirectory, fmt.Sprintf("attachment-%02d.txt", index))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("attachment %d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
		inputs[index] = thread.AttachmentInput{Path: path, At: createdAt.Add(time.Duration(index) * time.Minute)}
	}
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, inputs)
	if err != nil {
		t.Fatal(err)
	}
	attachments[0].Ref = "media://coding-attachment/00000000-0000-4000-8000-000000000003"
	attachments[1].Ref = "media://coding-attachment/00000000-0000-4000-8000-000000000002"
	attachments[2].Ref = "media://coding-attachment/00000000-0000-4000-8000-000000000001"
	manifest := struct {
		Version  int                 `json:"version"`
		ThreadID string              `json:"thread_id"`
		Entries  []thread.Attachment `json:"entries"`
	}{
		Version: thread.AttachmentManifestVersion, ThreadID: metadata.ThreadID,
		Entries: []thread.Attachment{attachments[2], attachments[1], attachments[0]},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(threadRoot, "attachments", "manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	newest := attachments[2]

	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(&threadReferenceCatalogStore{store: store, threadID: metadata.ThreadID})
	result := tool.Execute(t.Context(), map[string]any{"action": "list", "limit": float64(1)})
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	var response codingAttachmentListResponse
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Attachments) != 1 || response.Attachments[0].Ref != newest.Ref {
		t.Fatalf("first real-store attachment = %+v, want newest ref %q", response.Attachments, newest.Ref)
	}
}

func TestCodingAttachmentToolPagesUTF8TextAtValidBoundaries(t *testing.T) {
	tool := NewCodingAttachmentTool()
	tool.SetMediaStore(newFakeReferenceCatalogStore())
	result := tool.Execute(t.Context(), map[string]any{
		"action": "open", "ref": "media://new", "limit": float64(1),
	})
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	var response codingAttachmentReadResponse
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil {
		t.Fatal(err)
	}
	if response.Content != "α" || response.NextOffset != 2 || response.Size != 6 {
		t.Fatalf("first page = %+v", response)
	}
	result = tool.Execute(t.Context(), map[string]any{
		"action": "open", "ref": "media://new", "offset": float64(response.NextOffset), "limit": float64(4),
	})
	response = codingAttachmentReadResponse{}
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil {
		t.Fatal(err)
	}
	if response.Content != "βγ" || response.NextOffset != 0 {
		t.Fatalf("second page = %+v", response)
	}
}

func TestCodingAttachmentToolFailsClosedForUnavailableOrBinaryContent(t *testing.T) {
	tool := NewCodingAttachmentTool()
	if result := tool.Execute(t.Context(), map[string]any{"action": "list"}); !result.IsError {
		t.Fatal("tool accepted missing catalog")
	}
	store := newFakeReferenceCatalogStore()
	store.references = append(store.references, media.Reference{
		Ref: "media://binary", Filename: "data.bin", ContentType: "application/octet-stream", Size: 2,
	})
	store.data["media://binary"] = []byte{0xff, 0xfe}
	tool.SetMediaStore(store)
	if result := tool.Execute(t.Context(), map[string]any{
		"action": "open", "ref": "media://missing",
	}); !result.IsError {
		t.Fatal("tool accepted missing ref")
	}
	if result := tool.Execute(t.Context(), map[string]any{
		"action": "open", "ref": "media://binary",
	}); !result.IsError {
		t.Fatal("tool returned binary bytes as prompt text")
	}
}
