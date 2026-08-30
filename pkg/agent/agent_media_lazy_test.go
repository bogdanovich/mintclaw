package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tokenizer"
)

type lazyHistoricalMediaStore struct {
	media.MediaStore
	lazy     map[string]bool
	resolved map[string]int
}

func (s *lazyHistoricalMediaStore) ShouldResolveHistorical(ref string) bool {
	return !s.lazy[ref]
}

func (s *lazyHistoricalMediaStore) ResolveWithMeta(ref string) (string, media.MediaMeta, error) {
	s.resolved[ref]++
	return s.MediaStore.ResolveWithMeta(ref)
}

func TestResolveMediaRefsKeepsHistoryLazyAndAccountsForSelectedToolImage(t *testing.T) {
	delegate := media.NewFileMediaStore()
	store := &lazyHistoricalMediaStore{
		MediaStore: delegate,
		lazy:       make(map[string]bool),
		resolved:   make(map[string]int),
	}
	directory := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	oldPath := filepath.Join(directory, "old.png")
	currentPath := filepath.Join(directory, "current.png")
	if err := os.WriteFile(oldPath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	oldRef, err := store.Store(oldPath, media.MediaMeta{ContentType: "image/png"}, "old")
	if err != nil {
		t.Fatal(err)
	}
	currentRef, err := store.Store(currentPath, media.MediaMeta{ContentType: "image/png"}, "current")
	if err != nil {
		t.Fatal(err)
	}
	store.lazy[oldRef] = true
	messages := []providers.Message{
		{Role: "user", Content: "[image: old.png]", Media: []string{oldRef}},
		{Role: "assistant", Content: "Opening the selected historical image."},
		{Role: "tool", Content: "Opened thread image", Media: []string{currentRef}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize, 1)
	if store.resolved[oldRef] != 0 || result[0].Content != "[image: old.png]" || len(result[0].Media) != 0 {
		t.Fatalf("historical media was resolved: calls=%d message=%+v", store.resolved[oldRef], result[0])
	}
	if store.resolved[currentRef] != 1 || len(result) != 4 || result[3].Role != "user" ||
		len(result[3].Media) != 1 || !strings.HasPrefix(result[3].Media[0], "data:image/png;base64,") {
		t.Fatalf(
			"selected tool media was not resolved exactly once: calls=%d result=%+v",
			store.resolved[currentRef],
			result,
		)
	}
	withoutSelectedMedia := result[3]
	withoutSelectedMedia.Media = nil
	if tokenizer.EstimateMessageTokens(result[3]) <= tokenizer.EstimateMessageTokens(withoutSelectedMedia) {
		t.Fatal("selected current media was omitted from prompt token accounting")
	}
}
