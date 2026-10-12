package eval

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const generatePrivateFact = "MC_EVAL_GENERATE_PRIVATE_V1"

var ErrPDFTrial = errors.New("eval: PDF trial evidence unavailable or invalid")

// pdfTrial owns one fresh fixture and ephemeral facts. Native document state,
// protected-input authorization, grants and delivery are not owned here.
type pdfTrial struct {
	item      LoadedCase
	home      string
	workspace string
	sessionID string
	agent     string
	started   time.Time
	task      string
	source    []byte
	scratch   *os.Root
	parent    *os.Root
	directory string
	filter    *strings.Replacer
	census    *pdfNativeCensus
}

func preparePDFTrial(item LoadedCase, home, workspace, sessionID, agent string) (*pdfTrial, error) {
	if item.Case.Validate() != nil || item.Case.PDF == nil || !validDigest(item.Digest) ||
		!validID(sessionID) || !validID(agent) || !filepath.IsAbs(item.Root) {
		return nil, ErrPDFTrial
	}
	home, err := privateHome(home)
	if err != nil {
		return nil, ErrPDFTrial
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil || !pathWithin(home, workspace) {
		return nil, ErrPDFTrial
	}
	corpus, err := os.OpenRoot(item.Root)
	if err != nil {
		return nil, ErrPDFTrial
	}
	fixture := item.Case.Fixtures[0]
	info, err := corpus.Lstat(fixture.Path)
	if err != nil || !info.Mode().IsRegular() {
		_ = corpus.Close()
		return nil, ErrPDFTrial
	}
	source, err := readBoundedFile(corpus, fixture.Path, maxPDFBytes)
	_ = corpus.Close()
	if err != nil || len(source) == 0 || digest(source) != fixture.SHA256 {
		clear(source)
		return nil, ErrPDFTrial
	}
	trial := &pdfTrial{
		item: item, home: home, workspace: workspace, sessionID: sessionID, agent: agent,
		started: time.Now().UTC(), source: source,
	}
	trial.item.Case.Driver.Facts = maps.Clone(item.Case.Driver.Facts)
	for handle, value := range trial.item.Case.Driver.Facts {
		if value != generatePrivateFact {
			continue
		}
		var random [8]byte
		if _, err = rand.Read(random[:]); err != nil {
			clear(source)
			return nil, ErrPDFTrial
		}
		trial.item.Case.Driver.Facts[handle] = "MC_EVAL_" + hex.EncodeToString(random[:])
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		clear(source)
		return nil, ErrPDFTrial
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir(".eval-trials", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		clear(source)
		return nil, ErrPDFTrial
	}
	info, err = root.Lstat(".eval-trials")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		clear(source)
		return nil, ErrPDFTrial
	}
	trial.parent, err = root.OpenRoot(".eval-trials")
	if err != nil {
		clear(source)
		return nil, ErrPDFTrial
	}
	trial.directory = sessionID
	// Refuse an existing trial directory; never resume, replace or replay it.
	if err = trial.parent.Mkdir(trial.directory, 0o700); err != nil {
		_ = trial.parent.Close()
		clear(source)
		return nil, ErrPDFTrial
	}
	trial.scratch, err = trial.parent.OpenRoot(trial.directory)
	if err != nil {
		_ = trial.parent.Remove(trial.directory)
		_ = trial.parent.Close()
		clear(source)
		return nil, ErrPDFTrial
	}
	file, err := trial.scratch.OpenFile("source.pdf", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(source)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = trial.release(true)
		return nil, ErrPDFTrial
	}
	sourcePath := filepath.Join(workspace, ".eval-trials", trial.directory, "source.pdf")
	trial.task = strings.Replace(item.Case.Task, "{{fixture}}", sourcePath, 1)
	if !boundedText(trial.task, MaxInputBytes) || containsPrivate(trial.task, trial.item.Case.Driver.Facts) {
		_ = trial.release(true)
		return nil, ErrPDFTrial
	}
	return trial, nil
}

func (trial *pdfTrial) resolvedPDF() PDFCase {
	pdf := *trial.item.Case.PDF
	pdf.Fields = slices.Clone(pdf.Fields)
	for index, field := range pdf.Fields {
		if field.Fact != "" {
			pdf.Fields[index].Value = trial.item.Case.Driver.Facts[field.Fact]
			pdf.Fields[index].Fact = ""
		}
	}
	return pdf
}

// release never deletes a fixture still potentially in use remotely. The
// caller must establish native terminal settlement, not infer it from timeout.
// It removes only the owned source and empty directory, not arbitrary contents.
func (trial *pdfTrial) release(settled bool) error {
	if trial.census != nil {
		trial.census.before.close()
		trial.census = nil
	}
	clear(trial.source)
	trial.source = nil
	trial.item.Case.Driver.Facts = nil
	var result error
	if settled {
		if err := trial.scratch.Remove("source.pdf"); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = ErrPDFTrial
		}
	}
	if trial.scratch.Close() != nil {
		result = ErrPDFTrial
	}
	if settled && trial.parent.Remove(trial.directory) != nil {
		result = ErrPDFTrial
	}
	if trial.parent.Close() != nil {
		result = ErrPDFTrial
	}
	return result
}
