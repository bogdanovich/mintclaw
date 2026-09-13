package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentToolLocalPathPolicy(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "inside.pdf")
	otherInside := filepath.Join(workspace, "other.pdf")
	outsideRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideRoot, "allowed.pdf")
	for _, path := range []string{inside, otherInside, outside} {
		if err = os.WriteFile(path, []byte("%PDF-1.7\n%%EOF\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restricted := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, true, nil))
	for _, path := range []string{"inside.pdf", inside} {
		ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{path})
		_, resolved, err := restricted.resolveSource(ctx, "inspect", map[string]any{
			"action": "inspect", "path": path,
		})
		if err != nil || resolved != inside {
			t.Fatalf("resolve %q = %q, %v", path, resolved, err)
		}
	}
	for name, args := range map[string]map[string]any{
		"outside":   {"action": "inspect", "path": outside},
		"traversal": {"action": "inspect", "path": filepath.Join("..", filepath.Base(outside))},
		"extract":   {"action": "extract", "path": inside},
		"two":       {"action": "inspect", "path": inside, "source": "media://current"},
		"empty":     {"action": "inspect", "path": ""},
		"malformed": {"action": "inspect", "path": map[string]any{"value": inside}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if path, ok := args["path"].(string); ok {
				ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{path})
			}
			if _, _, err := restricted.resolveSource(ctx, args["action"].(string), args); err == nil {
				t.Fatal("unauthorized source admitted")
			}
		})
	}
	ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{"inside.pdf"})
	if _, _, err := restricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": inside,
	}); err == nil {
		t.Fatal("model-authored alias absent from the current user message was admitted")
	}
	ctx = toolshared.WithToolDocumentLocalPaths(t.Context(), []string{inside})
	if _, _, err := restricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": otherInside,
	}); err == nil {
		t.Fatal("different in-policy PDF absent from the current user message was admitted")
	}

	allowed := NewDocumentTool(WithDocumentLocalPathPolicy(
		workspace,
		true,
		[]*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(outside) + "$")},
	))
	ctx = toolshared.WithToolDocumentLocalPaths(t.Context(), []string{outside})
	if _, resolved, err := allowed.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": outside,
	}); err != nil || resolved != outside {
		t.Fatalf("configured read path = %q, %v", resolved, err)
	}

	unrestricted := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, false, nil))
	if _, resolved, err := unrestricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": outside,
	}); err != nil || resolved != outside {
		t.Fatalf("unrestricted path = %q, %v", resolved, err)
	}
}

func TestDocumentToolLocalPathFailureDoesNotRevealPath(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.pdf")
	tool := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, true, nil))
	ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{outside})
	result := tool.Execute(ctx, map[string]any{"action": "inspect", "path": outside})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureSourceUnauthorized)) ||
		strings.Contains(result.ForLLM, outside) {
		t.Fatalf("unsafe local-path denial = %#v", result)
	}
}

func TestDocumentToolLocalPathDurabilityAndLoggingRedaction(t *testing.T) {
	path := "/private/workspace/tax-return.pdf"
	args := map[string]any{"action": "inspect", "path": path}
	projected, err := NewDocumentTool().DurableArguments(args)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := projected["path"].(string)
	if !strings.HasPrefix(value, documentLocalPathTokenPrefix) || strings.Contains(value, path) ||
		args["path"] != path {
		t.Fatalf("durable args = %#v, original = %#v", projected, args)
	}
	tool := NewDocumentTool()
	if !tool.ProtectedDurableArguments(args) || tool.ProtectedDurableResult(args) {
		t.Fatal("local document durability protection is inconsistent")
	}
	logged := ToolLogArguments("document", args)
	if logged["redacted"] != true || logged["action"] != "inspect" || strings.Contains(fmtAny(logged), path) {
		t.Fatalf("logged args = %#v", logged)
	}
	mediaArgs := map[string]any{"action": "inspect", "source": "media://current"}
	if got := ToolLogArguments("document", mediaArgs); got["source"] != "media://current" ||
		tool.ProtectedDurableArguments(mediaArgs) {
		t.Fatalf("attachment behavior changed: %#v", got)
	}
}

type documentInputBytes []byte

func (source documentInputBytes) OpenInput() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(source)), nil
}

func TestDocumentToolLocalSnapshotIsExecutionScopedAndCleaned(t *testing.T) {
	data := documentInputBytes("%PDF-1.7\nimmutable local bytes\n%%EOF\n")
	digest := sha256.Sum256(data)
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion,
		Operation:     "inspect",
		State:         document.StateSucceeded,
		Input: &document.DocumentRef{
			ContentType: "application/pdf",
			Size:        int64(len(data)),
			SHA256:      hex.EncodeToString(digest[:]),
		},
	}
	store := media.NewFileMediaStore()
	tool := NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "local-execution")
	ref, err := tool.registerLocalSnapshot(ctx, store, documentToolTestOwner(t), data, report)
	if err != nil {
		t.Fatal(err)
	}
	if !tool.localRefAllowed(ctx, ref) || tool.localRefAllowed(
		toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "other-execution"),
		ref,
	) {
		t.Fatal("local ref escaped its execution boundary")
	}
	opened, err := store.OpenOwned(ref, documentToolTestOwner(t))
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(opened.File)
	_ = opened.Close()
	if err != nil || !bytes.Equal(read, data) {
		t.Fatalf("snapshot bytes = %q, %v", read, err)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if tool.localRefAllowed(ctx, ref) {
		t.Fatal("local ref authority survived cleanup")
	}
	if _, _, err = store.ResolveWithMeta(ref); err == nil {
		t.Fatal("local snapshot survived cleanup")
	}
}

func TestCopyDocumentInputRejectsDescriptorMismatch(t *testing.T) {
	data := documentInputBytes("%PDF-1.7\nbytes\n%%EOF\n")
	digest := sha256.Sum256(data)
	input := document.DocumentRef{
		ContentType: "application/pdf",
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
	}
	for name, mutate := range map[string]func(*document.DocumentRef){
		"size":   func(ref *document.DocumentRef) { ref.Size-- },
		"digest": func(ref *document.DocumentRef) { ref.SHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			mutate(&changed)
			if path, err := copyDocumentInputToMediaTemp(data, changed); err == nil {
				_ = os.Remove(path)
				t.Fatal("mismatched snapshot descriptor was admitted")
			}
		})
	}
}

func fmtAny(value any) string {
	return strings.TrimSpace(
		strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(fmt.Sprint(value)), "\n", " "), "\t", " "),
	)
}

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
