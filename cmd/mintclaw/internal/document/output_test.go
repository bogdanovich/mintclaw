package document

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	documentpkg "github.com/bogdanovich/mintclaw/pkg/document"
)

func TestParsePageSelectionRejectsAmbiguity(t *testing.T) {
	pages, err := parsePageSelection("1,3-5,8", documentpkg.DefaultMaxExtractPages)
	if err != nil || len(pages) != 5 || pages[0] != 1 || pages[4] != 8 {
		t.Fatalf("pages = %v, err = %v", pages, err)
	}
	for _, value := range []string{"0", "2-1", "1,1", "2,1", "1,,2", "1-", "all", "-1"} {
		if pages, err = parsePageSelection(value, documentpkg.DefaultMaxExtractPages); err == nil {
			t.Fatalf("accepted %q as %v", value, pages)
		}
	}
}

func TestParsePageSelectionRejectsRangesBeforeExpansion(t *testing.T) {
	maximumInteger := int(^uint(0) >> 1)
	for _, value := range []string{
		"1-1000000000",
		"1-" + strconv.Itoa(maximumInteger),
		"1-20,21",
	} {
		if pages, err := parsePageSelection(value, documentpkg.DefaultMaxExtractPages); err == nil {
			t.Fatalf("accepted over-limit selection %q as %v", value, pages)
		}
	}
	if pages, err := parsePageSelection("1-8", documentpkg.DefaultMaxRenderPages); err != nil || len(pages) != 8 {
		t.Fatalf("bounded render pages = %v, %v", pages, err)
	}
	if pages, err := parsePageSelection(strconv.Itoa(maximumInteger), 1); err != nil ||
		len(pages) != 1 || pages[0] != maximumInteger {
		t.Fatalf("maximum page = %v, %v", pages, err)
	}
}

func TestPublishArtifactFileIsAtomicAndRequiresOverwrite(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/extracted-text.jsonl"
	source := filepath.Join(root, "extracted-text.jsonl")
	if err := os.WriteFile(source, []byte("new"), 0o400); err != nil {
		t.Fatal(err)
	}
	snapshot := testArtifactOpener{ref: ref, path: source}
	destination := filepath.Join(root, "result.jsonl")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishArtifactFile(snapshot, ref, destination, false); err == nil {
		t.Fatal("publication replaced an existing destination without --overwrite")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		t.Fatalf("failed publication changed destination: %q, %v", data, err)
	}
	if err := publishArtifactFile(snapshot, ref, destination, true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("published output = %q, %v", data, err)
	}
}

func TestArtifactPublicationWaitsForSnapshotCleanup(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/extracted-text.jsonl"
	source := filepath.Join(root, "extracted-text.jsonl")
	destination := filepath.Join(root, "result.jsonl")
	if err := os.WriteFile(source, []byte("new"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := stageArtifactFile(testArtifactOpener{ref: ref, path: source}, ref, destination, true)
	if err != nil {
		t.Fatal(err)
	}
	report := documentpkg.Report{
		State: documentpkg.StateSucceeded,
		Artifacts: []documentpkg.Artifact{{
			Ref: ref,
		}},
	}
	cleanupErr := errors.New("injected cleanup failure")
	finishArtifactPublication(func() error { return cleanupErr }, staged, &report)
	if report.State != documentpkg.StateFailed || report.Failure == nil ||
		report.Failure.Code != documentpkg.FailureInternal {
		t.Fatalf("cleanup failure report = %#v", report)
	}
	if data, readErr := os.ReadFile(destination); readErr != nil || string(data) != "old" {
		t.Fatalf("cleanup failure changed destination: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(staged.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged output survived cleanup failure: %v", statErr)
	}
}

func TestArtifactOverwriteRejectsWrongDestinationTypes(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/artifact"
	source := filepath.Join(root, "artifact")
	if err := os.WriteFile(source, []byte("new"), 0o400); err != nil {
		t.Fatal(err)
	}
	opener := testArtifactOpener{ref: ref, path: source}

	fileDestination := filepath.Join(root, "file-destination")
	if err := os.Mkdir(fileDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(fileDestination, "canary")
	if err := os.WriteFile(canary, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagedFile, err := stageArtifactFile(opener, ref, fileDestination, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = stagedFile.commit(); err == nil {
		t.Fatal("file publication replaced a directory")
	}
	stagedFile.abort()
	if data, readErr := os.ReadFile(canary); readErr != nil || string(data) != "preserved" {
		t.Fatalf("file publication changed directory destination: %q, %v", data, readErr)
	}

	directoryDestination := filepath.Join(root, "directory-destination")
	if err = os.WriteFile(directoryDestination, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagedDirectory, err := stageArtifactDirectory(
		opener,
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		directoryDestination,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = stagedDirectory.commit(); err == nil {
		t.Fatal("directory publication replaced a file")
	}
	stagedDirectory.abort()
	if data, readErr := os.ReadFile(directoryDestination); readErr != nil || string(data) != "preserved" {
		t.Fatalf("directory publication changed file destination: %q, %v", data, readErr)
	}
}

type testArtifactOpener struct {
	ref  string
	path string
}

func (opener testArtifactOpener) OpenArtifact(ref string) (io.ReadCloser, error) {
	if ref != opener.ref {
		return nil, errors.New("unknown artifact")
	}
	return os.Open(opener.path)
}
