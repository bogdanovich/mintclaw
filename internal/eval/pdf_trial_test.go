package eval

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func newPDFTrialFixture(t *testing.T) *pdfTrial {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err = os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, "workspace")
	if err = os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	trial, err := preparePDFTrial(items[0], home, workspace, uuid.NewString(), "eval-agent")
	if err != nil {
		t.Fatal("synthetic PDF preparation failed", err)
	}
	return trial
}

func TestPDFTrialHasFreshFactsPinnedFixtureAndNoTemplateMutation(t *testing.T) {
	first := newPDFTrialFixture(t)
	defer func() { _ = first.release(true) }()
	second, err := preparePDFTrial(first.item, first.home, first.workspace, uuid.NewString(), first.agent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.release(true) }()
	if first.directory == second.directory || first.task == second.task ||
		strings.Contains(first.task, "{{fixture}}") || !strings.Contains(first.item.Case.Task, "{{fixture}}") ||
		first.item.Digest != second.item.Digest || !bytes.Equal(first.source, second.source) {
		t.Fatal("trial identity, fixture pin, or immutable task template lost")
	}
	// Reload the public template, not the already-resolved ephemeral facts.
	corpus, err := LoadCorpus(first.item.Root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{first.item.Case.ID}})
	if err != nil {
		t.Fatal(err)
	}
	third, err := preparePDFTrial(selected[0], first.home, first.workspace, uuid.NewString(), first.agent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.release(true) }()
	for handle, value := range first.item.Case.Driver.Facts {
		if value == generatePrivateFact || value == "" || value == third.item.Case.Driver.Facts[handle] ||
			selected[0].Case.Driver.Facts[handle] != generatePrivateFact ||
			strings.Contains(first.task, value) {
			t.Fatal("private generation reused a canary or changed/leaked its public template")
		}
	}
	pdf := first.resolvedPDF()
	for _, field := range pdf.Fields {
		if field.Fact != "" || field.Name == "contact_reference" &&
			field.Value != first.item.Case.Driver.Facts["contact-reference"] {
			t.Fatal("ephemeral oracle expectation not resolved")
		}
	}
	if first.item.Case.PDF.Fields[1].Fact == "" {
		t.Fatal("oracle resolution mutated the admitted case")
	}
	info, err := first.scratch.Stat("source.pdf")
	if err != nil || info.Mode().Perm() != 0o600 || digest(first.source) != first.item.Case.Fixtures[0].SHA256 {
		t.Fatal("prepared source is not private or pinned")
	}
}

func TestPDFTrialNeverReusesDirectoryOrDeletesUnsettledSource(t *testing.T) {
	trial := newPDFTrialFixture(t)
	if _, err := preparePDFTrial(
		trial.item,
		trial.home,
		trial.workspace,
		trial.sessionID,
		trial.agent,
	); !errors.Is(
		err,
		ErrPDFTrial,
	) {
		t.Fatal("existing trial was resumed or replaced")
	}
	path := filepath.Join(trial.workspace, ".eval-trials", trial.directory, "source.pdf")
	if trial.release(false) != nil {
		t.Fatal("unsettled local handles not closed")
	}
	data, err := os.ReadFile(path)
	if err != nil || digest(data) != trial.item.Case.Fixtures[0].SHA256 || trial.source != nil ||
		trial.item.Case.Driver.Facts != nil {
		t.Fatal("unsettled remote work lost its source or retained in-memory facts")
	}
}

func TestPDFTrialCleanupDoesNotRecursivelyRemoveUnexpectedFiles(t *testing.T) {
	trial := newPDFTrialFixture(t)
	file, err := trial.scratch.OpenFile("unexpected", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if !errors.Is(trial.release(true), ErrPDFTrial) {
		t.Fatal("cleanup claimed success with unexpected resources")
	}
	if _, err = os.Stat(filepath.Join(trial.workspace, ".eval-trials", trial.directory, "unexpected")); err != nil {
		t.Fatal("unexpected file was recursively removed")
	}
}

func TestPDFTrialRechecksFixtureAndRefusesUnsafeRoot(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	for _, mutate := range []func(*LoadedCase){
		func(item *LoadedCase) { item.Root = "." },
		func(item *LoadedCase) { item.Root = "/missing/eval-corpus" },
		func(item *LoadedCase) {
			item.Case.Fixtures = append([]Fixture(nil), item.Case.Fixtures...)
			item.Case.Fixtures[0].SHA256 = strings.Repeat("0", 64)
		},
	} {
		item := trial.item
		mutate(&item)
		if _, err := preparePDFTrial(
			item,
			trial.home,
			trial.workspace,
			uuid.NewString(),
			trial.agent,
		); !errors.Is(
			err,
			ErrPDFTrial,
		) {
			t.Fatal("unsafe corpus or changed fixture admitted")
		}
	}
}
