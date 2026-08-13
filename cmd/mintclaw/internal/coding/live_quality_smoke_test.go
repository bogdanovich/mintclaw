package coding

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestCodingQualityLiveSmoke is an opt-in check that the deterministic tool
// fixture remains representative when a configured live model plans the edit.
// It is intentionally not a merge gate because provider availability and model
// behavior are nondeterministic.
//
// Run with:
//
//	MINTCLAW_CODING_LIVE_SMOKE=1 go test ./cmd/mintclaw/internal/coding \
//	  -run TestCodingQualityLiveSmoke -count=1 -v
//
// Optionally select a configured alias with MINTCLAW_CODING_LIVE_MODEL.
func TestCodingQualityLiveSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("MINTCLAW_CODING_LIVE_SMOKE")) != "1" {
		t.Skip("set MINTCLAW_CODING_LIVE_SMOKE=1 to enable the live-provider smoke test")
	}

	project := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(project, "AGENTS.md"),
		[]byte("Keep the live smoke edit minimal and use the available coding file tools.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	deps := defaultDependencies()
	deps.home = func() string { return home }
	deps.cwd = func() (string, error) { return project, nil }
	deps.newThreadID = uuid.NewString
	deps.now = time.Now

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	model := strings.TrimSpace(os.Getenv("MINTCLAW_CODING_LIVE_MODEL"))
	err := runNew(
		ctx,
		io.Discard,
		deps,
		"Create live-smoke.txt containing exactly MINTCLAW_CODING_LIVE_OK followed by one newline, then read it back.",
		model,
		false,
	)
	if err != nil {
		t.Fatalf("live coding turn failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(project, "live-smoke.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "MINTCLAW_CODING_LIVE_OK\n" {
		t.Fatalf("live smoke artifact = %q", content)
	}
}
