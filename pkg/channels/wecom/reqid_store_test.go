package wecom

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestDefaultReqIDStorePathHonorsHomeOverride(t *testing.T) {
	firstHome := t.TempDir()
	t.Setenv(config.EnvHome, firstHome)
	firstPath := defaultReqIDStorePath()
	if want := filepath.Join(firstHome, "wecom", "reqid-store.json"); firstPath != want {
		t.Fatalf("defaultReqIDStorePath() = %q, want %q", firstPath, want)
	}

	secondHome := t.TempDir()
	t.Setenv(config.EnvHome, secondHome)
	secondPath := defaultReqIDStorePath()
	if want := filepath.Join(secondHome, "wecom", "reqid-store.json"); secondPath != want {
		t.Fatalf("defaultReqIDStorePath() after override change = %q, want %q", secondPath, want)
	}
	if secondPath == firstPath {
		t.Fatalf("defaultReqIDStorePath() did not change with %s", config.EnvHome)
	}
}

func TestReqIDStorePathSeparatesConfiguredInstances(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	canonical := reqIDStorePath(config.ChannelWeCom)
	if want := filepath.Join(home, "wecom", "reqid-store.json"); canonical != want {
		t.Fatalf("canonical reqIDStorePath() = %q, want %q", canonical, want)
	}
	first := reqIDStorePath("wecom_support")
	second := reqIDStorePath("wecom_alerts")
	if first == canonical || second == canonical || first == second {
		t.Fatalf("instance store paths are not distinct: canonical=%q first=%q second=%q", canonical, first, second)
	}
	if filepath.Dir(first) != filepath.Join(home, "wecom") || filepath.Dir(second) != filepath.Join(home, "wecom") {
		t.Fatalf("instance store paths escaped the WeCom state directory: first=%q second=%q", first, second)
	}
}

func TestReqIDStorePersistsRoutes(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "reqids.json")
	store := newReqIDStore(storePath)
	if err := store.Put("chat-1", "req-1", 2, time.Hour); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	reloaded := newReqIDStore(storePath)
	route, ok := reloaded.Get("chat-1")
	if !ok {
		t.Fatal("expected persisted route to be loaded")
	}
	if route.ChatID != "chat-1" || route.ReqID != "req-1" || route.ChatType != 2 {
		t.Fatalf("loaded route = %+v", route)
	}
}
