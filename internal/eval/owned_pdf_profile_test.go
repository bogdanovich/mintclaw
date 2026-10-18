package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestPreparedPDFProfileHasARealEmptyNativeBaselineBeforeFirstTask(t *testing.T) {
	fixture, parent := preparationFixture(t)
	enableAdmissionPDF(t, fixture)
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	// A seed's existing journal must not become part of the new baseline.
	seedJournal := filepath.Join(fixture.home, "state", "document-writes", "journal")
	if _, err := document.NewWriteJournal(seedJournal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedJournal, "old.json"), []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || !result.PDFJournalPrepared || result.RuntimeRun || fixture.tasks.Load() != 0 ||
		fixture.probes.Load() != 0 {
		t.Fatal("PDF profile preparation ran work or lacked its native journal", err)
	}
	journal := filepath.Join(result.Home, "state", "document-writes", "journal")
	entries, err := os.ReadDir(journal)
	if err != nil || len(entries) != 0 {
		t.Fatal("new journal copied seed operations or acquired a lock", err)
	}
	preset, err := LoadPreset(result.PresetPath)
	if err != nil {
		t.Fatal(err)
	}
	writeAdmissionJSON(t, filepath.Join(result.Home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: "normal", ParentPID: os.Getpid(),
	})
	t.Setenv(config.EnvHome, result.Home)
	t.Setenv(config.EnvConfig, result.ConfigPath)
	child, target, err := startOwnedGateway(t.Context(), preset, result.Home)
	registerOwnedGatewayCleanup(t, child)
	if err != nil || child == nil || !child.admitted {
		t.Fatal("prepared PDF native baseline was refused", err)
	}
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := preparePDFTrial(items[0], result.Home, result.Workspace, uuid.NewString(), preset.ExpectedAgent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.release(true) }()
	prepared.filter = target.config.SensitiveDataReplacer()
	if err = prepared.beginNativeCensus(t.Context()); err != nil || prepared.census.before == nil ||
		len(prepared.census.before.files) != 0 {
		t.Fatal("fresh native roots were not observed before any task", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if receipt := child.stopAndWait(ctx); receipt.Code != "native_exit_zero" {
		t.Fatal("prepared target did not stop", receipt.Code)
	}
}

func TestOwnedPDFProfileRefusesAnythingExceptAnEmptyNativeJournal(t *testing.T) {
	for _, mode := range []string{
		"missing", "operation", "lock", "state-extra", "write-extra", "state-alias", "write-alias", "journal-alias", "public",
		"not-pdf", "temporary",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "normal")
			if mode != "not-pdf" {
				enableAdmissionPDF(t, fixture)
			}
			journal := filepath.Join(fixture.home, "state", "document-writes", "journal")
			if mode != "missing" {
				if _, err := document.NewWriteJournal(journal); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "operation", "lock", "state-extra", "write-extra":
				name := filepath.Join(journal, "operation.json")
				switch mode {
				case "lock":
					name = filepath.Join(journal, ".write-journal.lock")
				case "state-extra":
					name = filepath.Join(fixture.home, "state", "other.json")
				case "write-extra":
					name = filepath.Join(filepath.Dir(journal), "other.json")
				}
				if err := os.WriteFile(name, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "state-alias", "write-alias", "journal-alias":
				original := journal
				switch mode {
				case "state-alias":
					original = filepath.Join(fixture.home, "state")
				case "write-alias":
					original = filepath.Dir(journal)
				}
				moved := filepath.Join(t.TempDir(), "original")
				if err := os.Rename(original, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, original); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(journal, 0o755); err != nil {
					t.Fatal(err)
				}
			case "temporary":
				if err := os.Mkdir(filepath.Join(fixture.home, ownedGatewayTempDirectory), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
			registerOwnedGatewayCleanup(t, child)
			if !errors.Is(err, ErrOwnedGateway) || child != nil {
				t.Fatal("retained or absent native state reached process launch", err)
			}
			if _, err = os.Lstat(filepath.Join(fixture.home, ".eval-gateway-owner")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused profile acquired process ownership")
			}
		})
	}
}

func TestOwnedGatewayUsesPrivateTempAndRawScanRetainsItAfterStop(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	ambient := t.TempDir()
	t.Setenv("TMPDIR", ambient)
	t.Setenv("TMP", ambient)
	t.Setenv("TEMP", ambient)
	child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
	registerOwnedGatewayCleanup(t, child)
	if err != nil {
		t.Fatal("private temp child did not start", err)
	}
	temporary := filepath.Join(fixture.home, ownedGatewayTempDirectory)
	info, err := os.Lstat(temporary)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("owned temp is not a direct private directory", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if receipt := child.stopAndWait(ctx); receipt.Code != "native_exit_zero" {
		t.Fatal("temp control did not stop", receipt.Code)
	}
	observation := observePrivateSurfaces(t.Context(), fixture.home,
		map[string]string{"temp": "owned-gateway-private-temp-control"}, nil, nil)
	if !observation.LeakObserved || observation.Code != "leak_observed" {
		t.Fatal("owned temp was removed or missed before raw observation", observation)
	}
	entries, err := os.ReadDir(ambient)
	if err != nil || len(entries) != 0 {
		t.Fatal("child used the parent's temp directory", err)
	}
	entries, err = os.ReadDir(temporary)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "owned-temp-proof-") {
		t.Fatal("native temp evidence was not retained privately", err)
	}
	if _, _, err = startOwnedGateway(t.Context(), fixture.preset, fixture.home); !errors.Is(err, ErrOwnedGateway) {
		t.Fatal("retained native temp was reused")
	}
}
