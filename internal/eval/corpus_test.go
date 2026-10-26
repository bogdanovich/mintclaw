package eval

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCase(t *testing.T, root, name string, item Case) {
	t.Helper()
	full := filepath.Join(root, "cases", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(full, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCorpusSelection(t *testing.T) {
	root := t.TempDir()
	first := testCase()
	first.ID = "general.z-last"
	second := testCase()
	second.ID = "general.a-first"
	second.Tags = []string{"context", "multi-turn"}
	writeCase(t, root, "first.json", first)
	writeCase(t, root, "second.json", second)
	corpus, err := LoadCorpus(root)
	if err != nil || len(corpus) != 2 || corpus[0].Case.ID != second.ID || !validDigest(corpus[0].Digest) {
		t.Fatal("unexpected corpus", err)
	}
	selected, err := Select(corpus, Selection{Suite: "general", Tags: []string{"context", "multi-turn"}})
	if err != nil || len(selected) != 1 || selected[0].Case.ID != second.ID {
		t.Fatal("tag intersection failed", err)
	}
	selected, err = Select(corpus, Selection{IDs: []string{first.ID, second.ID}})
	if err != nil || len(selected) != 2 {
		t.Fatal("explicit ID selection failed", err)
	}
	for _, selection := range []Selection{
		{Suite: "pdf"},
		{Tags: []string{"unknown"}},
		{IDs: []string{first.ID, "general.unknown"}},
		{IDs: []string{first.ID, first.ID}},
		{Suite: "../general"},
		{Tags: []string{"Context"}},
		{Suite: "pdf", IDs: []string{first.ID}},
	} {
		if _, err = Select(corpus, selection); !errors.Is(err, ErrInvalidFilter) {
			t.Fatal("invalid or partial selection admitted")
		}
	}
	writeCase(t, root, "duplicate.json", first)
	if _, err = LoadCorpus(root); !errors.Is(err, ErrInvalidCorpus) {
		t.Fatal("duplicate case admitted")
	}
}

func TestCorpusRejectsUnsafeFiles(t *testing.T) {
	for _, mode := range []string{"case-symlink", "case-directory-symlink", "fixture-escape", "fixture-digest", "oversized", "empty"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside.json")
			if err := os.WriteFile(outside, encodedCase(t), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "cases"), 0o700); err != nil {
				t.Fatal(err)
			}
			item := testCase()
			switch mode {
			case "case-symlink", "case-directory-symlink":
				target := outside
				if mode == "case-directory-symlink" {
					target = filepath.Dir(outside)
				}
				if err := os.Symlink(target, filepath.Join(root, "cases", "link.json")); err != nil {
					t.Skip("platform does not permit symlink fixture")
				}
			case "fixture-escape", "fixture-digest":
				item.Fixtures = []Fixture{
					{ID: "source", Path: "fixture.pdf", Kind: "pdf", SHA256: digest([]byte("fixture"))},
				}
				if mode == "fixture-escape" {
					if err := os.Symlink(outside, filepath.Join(root, "fixture.pdf")); err != nil {
						t.Skip("platform does not permit symlink fixture")
					}
				} else if err := os.WriteFile(filepath.Join(root, "fixture.pdf"), []byte("wrong"), 0o600); err != nil {
					t.Fatal(err)
				}
				writeCase(t, root, "one.json", item)
			case "oversized":
				if err := os.WriteFile(
					filepath.Join(root, "cases", "one.json"),
					[]byte(strings.Repeat(" ", MaxManifestBytes+1)),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadCorpus(root); !errors.Is(err, ErrInvalidCorpus) {
				t.Fatal("unsafe or empty corpus admitted")
			}
		})
	}
}

func TestFixtureContract(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", ".", "fixtures\\escape", "C:escape", "fixtures/../escape"} {
		item := testCase()
		item.Fixtures = []Fixture{{ID: "source", Path: name, Kind: "pdf", SHA256: strings.Repeat("0", 64)}}
		if !errors.Is(item.Validate(), ErrInvalidCase) {
			t.Fatal("unsafe fixture path admitted")
		}
	}
	root := t.TempDir()
	data := []byte("synthetic fixture bytes")
	if err := os.WriteFile(filepath.Join(root, "fixture.pdf"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	item := testCase()
	item.Fixtures = []Fixture{{ID: "source", Path: "fixture.pdf", Kind: "pdf", SHA256: digest(data)}}
	writeCase(t, root, "one.json", item)
	if _, err := LoadCorpus(root); err != nil {
		t.Fatal("verified fixture rejected", err)
	}
	item.Fixtures = append(item.Fixtures, item.Fixtures[0])
	if !errors.Is(item.Validate(), ErrInvalidCase) {
		t.Fatal("duplicate fixture admitted")
	}
}

func TestRepositoryCorpus(t *testing.T) {
	corpus, err := LoadCorpus("../../evals")
	if err != nil || len(corpus) != 25 {
		t.Fatal("repository cases invalid", err)
	}
	defaults, err := Select(corpus, Selection{DefaultOnly: true})
	if err != nil || len(defaults) != 6 || defaults[0].Case.ID != "general.context.correction" ||
		defaults[1].Case.ID != "general.context.followup" || defaults[2].Case.ID != "general.interaction.answer" ||
		defaults[3].Case.ID != "general.interaction.navigation" || defaults[4].Case.ID != "general.steering.boundary" ||
		defaults[5].Case.ID != "general.steering.order" || CheckRunAvailability(defaults, false) != nil {
		t.Fatal("repository general baseline missing", err)
	}
	pending := 0
	for _, item := range corpus {
		policy := item.Availability()
		if policy.Status == "pending" {
			pending++
			core := item.Case.ID == "general.session.isolation" || item.Case.ID == "general.tools.recovery" ||
				item.Case.ID == "general.control.stop" || item.Case.ID == "general.approval.denied" ||
				item.Case.ID == "general.delivery.final-once" || item.Case.ID == "general.skills.selection" ||
				item.Case.ID == "general.history.restart"
			if policy.Default || policy.Reason == "" ||
				item.Case.Suite == "general" && !core {
				t.Fatal("invalid deferred coverage")
			}
		}
	}
	if pending != 19 || !errors.Is(CheckRunAvailability(corpus, false), ErrPendingCases) {
		t.Fatal("deferred domain cases were lost or implicitly enabled")
	}
	pdf, err := Select(corpus, Selection{Suite: "pdf"})
	if err != nil || len(pdf) != 6 {
		t.Fatal("required PDF coverage missing", err)
	}
	browser, err := Select(corpus, Selection{Suite: "browser"})
	if err != nil || len(browser) != 6 {
		t.Fatal("required browser contracts missing", err)
	}
}

type syntheticDirectory struct {
	fs.File
	entry     fs.DirEntry
	remaining int
	requested []int
}

func (directory *syntheticDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	directory.requested = append(directory.requested, count)
	if count < 1 || count > corpusReadBatch {
		return nil, errors.New("unbounded directory enumeration")
	}
	batch := make([]fs.DirEntry, min(count, directory.remaining))
	for index := range batch {
		batch[index] = directory.entry
	}
	directory.remaining -= len(batch)
	if directory.remaining == 0 {
		return batch, io.EOF
	}
	return batch, nil
}

func TestCorpusEnumerationEnforcesBudgetBeforeRetention(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, total := range []int{maxCorpusEntries - 1, 1_000_000_000} {
		directory := &syntheticDirectory{entry: files[0], remaining: total}
		entries, visited := 1, 0
		err = consumeCorpusDirectory(directory, &entries, func(fs.DirEntry) error {
			visited++
			return nil
		})
		if total == maxCorpusEntries-1 && err != nil ||
			total > maxCorpusEntries && !errors.Is(err, ErrInvalidCorpus) ||
			visited != maxCorpusEntries-1 || len(directory.requested) > maxCorpusEntries/corpusReadBatch+1 {
			t.Fatal("directory budget was enforced too late", err, visited)
		}
	}
	first := &syntheticDirectory{entry: files[0], remaining: 3}
	second := &syntheticDirectory{entry: files[0], remaining: 1_000_000_000}
	entries := maxCorpusEntries - 4
	if consumeCorpusDirectory(first, &entries, func(fs.DirEntry) error { return nil }) != nil ||
		!errors.Is(
			consumeCorpusDirectory(second, &entries, func(fs.DirEntry) error { return nil }),
			ErrInvalidCorpus,
		) ||
		len(second.requested) != 1 || second.requested[0] != 2 {
		t.Fatal("directory budgets were reset between directories")
	}
}
