package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentToolRequiresCurrentTurnRefBeforeMediaAccess(t *testing.T) {
	tool := NewDocumentTool()
	result := tool.Execute(t.Context(), map[string]any{
		"action": "inspect", "source": "media://older-route-ref",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureSourceUnauthorized)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDocumentToolRejectsOptionsFromAnotherAction(t *testing.T) {
	tool := NewDocumentTool()
	ctx := toolshared.WithToolDocumentContext(t.Context(), []string{"media://current"}, true)
	result := tool.Execute(ctx, map[string]any{
		"action": "inspect", "source": "media://current", "pages": []any{float64(1)},
	})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureInvalidInput)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestBoundedDocumentTextKeepsPageProvenanceAndUTF8(t *testing.T) {
	pages := []document.ExtractedPage{
		{Page: 2, Text: strings.Repeat("é", documentModelTextLimit)},
		{Page: 3, Text: "must not fit"},
	}
	content := boundedDocumentText(pages, 512)
	if len(content) > 512 || !strings.Contains(content, "[page 2]") || strings.Contains(content, "[page 3]") ||
		!utf8.ValidString(content) {
		t.Fatalf("bounded content is invalid: len=%d content=%q", len(content), content)
	}
}

type documentArtifactBytes map[string][]byte

func (source documentArtifactBytes) OpenArtifact(ref string) (io.ReadCloser, error) {
	data, ok := source[ref]
	if !ok {
		return nil, errors.New("missing artifact")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type documentBindFailStore struct {
	*media.FileMediaStore
	releaseCalls int
}

func (*documentBindFailStore) BindOwner(string, media.MediaOwner) error {
	return errors.New("injected bind failure")
}

func (store *documentBindFailStore) ReleaseAll(scope string) error {
	store.releaseCalls++
	return store.FileMediaStore.ReleaseAll(scope)
}

func TestDocumentRenderRegistrationIsAuthorityBoundAndCleansAtTurnEnd(t *testing.T) {
	data := []byte("verified rendered page bytes")
	report, source := documentRegistrationFixture(data)
	store := media.NewFileMediaStore()
	owner := documentToolTestOwner(t)
	tool := NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "execution-1")
	refs, err := tool.registerRenderedArtifacts(ctx, store, owner, source, report, true)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
	opened, err := store.OpenOwned(refs[0], owner)
	if err != nil {
		t.Fatalf("registered ref is not authority-bound: %v", err)
	}
	_ = opened.Close()
	if err := tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveWithMeta(refs[0]); err == nil {
		t.Fatal("current-turn render ref survived terminal cleanup")
	}
}

func TestDocumentRenderRegistrationRollsBackOnOwnerBindingFailure(t *testing.T) {
	data := []byte("verified rendered page bytes")
	report, source := documentRegistrationFixture(data)
	store := &documentBindFailStore{FileMediaStore: media.NewFileMediaStore()}
	tool := NewDocumentTool()
	_, err := tool.registerRenderedArtifacts(
		toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "execution-2"),
		store,
		documentToolTestOwner(t),
		source,
		report,
		true,
	)
	if err == nil || store.releaseCalls != 1 {
		t.Fatalf("err=%v release_calls=%d", err, store.releaseCalls)
	}
}

func documentRegistrationFixture(data []byte) (document.Report, documentArtifactBytes) {
	digest := sha256.Sum256(data)
	artifactDigest := hex.EncodeToString(digest[:])
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion,
		Operation:     "render",
		State:         document.StateSucceeded,
		Input:         &document.DocumentRef{SHA256: strings.Repeat("a", 64)},
		Artifacts: []document.Artifact{{
			Ref:          "document-artifact://page-1",
			Kind:         "page_render",
			ContentType:  "image/png",
			Size:         int64(len(data)),
			SHA256:       artifactDigest,
			SourceSHA256: strings.Repeat("a", 64),
			Pages:        []int{1},
		}},
	}
	return report, documentArtifactBytes{report.Artifacts[0].Ref: data}
}

func documentToolTestOwner(t *testing.T) media.MediaOwner {
	t.Helper()
	owner, err := media.NewMediaOwner(
		"workspace", "agent", "actor", "route", "session", "telegram", "chat", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
