package coding

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestThreadsTrashPurgeUsesConfiguredRetentionAndNoCurrentProject(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	deps := testDependencies(home, projectRoot, &now)
	deps.loadConfig = func() (*config.Config, error) {
		cfg := config.DefaultConfig()
		cfg.Coding.State.TrashRetentionHours = 48
		return cfg, nil
	}
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "private-title", now.Add(-72*time.Hour))
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
	trashed, trashErr := store.TrashThread(lease, metadata.ThreadID, now.Add(-72*time.Hour))
	if err := errors.Join(trashErr, lease.Release()); err != nil {
		t.Fatal(err)
	}
	deps.cwd = func() (string, error) { return "", errors.New("current project unavailable") }
	output := executeCommand(t, newThreadsCommand(deps), "trash", "purge", "--json")
	var planned purgeThreadTrashOutput
	if err := json.Unmarshal(output, &planned); err != nil {
		t.Fatal(err)
	}
	if planned.Action != "planned" || planned.Result.Deleted != 0 || len(planned.Result.Candidates) != 1 ||
		!planned.Result.Before.Equal(now.Add(-48*time.Hour)) {
		t.Fatalf("configured plan = %+v", planned)
	}
	for _, private := range []string{projectRoot, home, "private-title"} {
		if strings.Contains(string(output), private) {
			t.Fatalf("purge JSON disclosed private value %q", private)
		}
	}
	for _, confirmation := range []string{"wrong", " " + threadTrashPurgeConfirmation + " "} {
		if _, err := executeCommandError(
			newThreadsCommand(deps), "trash", "purge", "--confirm", confirmation,
		); err == nil || !strings.Contains(err.Error(), "exactly match") {
			t.Fatalf("confirmation %q error = %v", confirmation, err)
		}
	}
	var purged purgeThreadTrashOutput
	if err := json.Unmarshal(executeCommand(
		t, newThreadsCommand(deps), "trash", "purge", "--confirm", threadTrashPurgeConfirmation, "--json",
	), &purged); err != nil {
		t.Fatal(err)
	}
	if purged.Action != "purged" || purged.Result.Deleted != 1 {
		t.Fatalf("purged = %+v", purged)
	}
	if _, err := os.Stat(trashed.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed purge retained root: %v", err)
	}
}

func TestThreadsTrashPurgeValidatesRetentionBeforeCreatingState(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	deps := testDependencies(home, "", &now)
	for _, duration := range []string{"-1h", "23h", "8761h"} {
		if _, err := executeCommandError(
			newThreadsCommand(deps), "trash", "purge", "--older-than", duration,
		); err == nil || !strings.Contains(err.Error(), "must be between") {
			t.Fatalf("retention %q error = %v", duration, err)
		}
	}
	for _, args := range [][]string{
		{"trash", "purge", "--json"},
		{"trash", "purge", "--confirm", threadTrashPurgeConfirmation, "--json"},
	} {
		var output purgeThreadTrashOutput
		if err := json.Unmarshal(executeCommand(t, newThreadsCommand(deps), args...), &output); err != nil {
			t.Fatal(err)
		}
		if output.Result.Candidates == nil || output.Result.Scanned != 0 || output.Result.Deleted != 0 {
			t.Fatalf("missing store output = %+v", output)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "coding")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid or empty purge materialized store: %v", err)
	}
}
