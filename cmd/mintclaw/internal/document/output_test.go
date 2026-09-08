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

func TestReadCommandsDoNotExposeOverwrite(t *testing.T) {
	if flag := newExtractCommand(commandDeps{}).Flags().Lookup("overwrite"); flag != nil {
		t.Fatal("extract unexpectedly exposes --overwrite")
	}
	if flag := newRenderCommand(commandDeps{}).Flags().Lookup("overwrite"); flag != nil {
		t.Fatal("render unexpectedly exposes --overwrite")
	}
}

func TestPublishArtifactFileIsAtomicAndRefusesReplacement(t *testing.T) {
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
	if err := publishArtifactFile(snapshot, ref, destination); err == nil {
		t.Fatal("publication replaced an existing destination")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		t.Fatalf("failed publication changed destination: %q, %v", data, err)
	}
	freshDestination := filepath.Join(root, "fresh-result.jsonl")
	if err := publishArtifactFile(snapshot, ref, freshDestination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(freshDestination); err != nil || string(data) != "new" {
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
	staged, err := stageArtifactFile(testArtifactOpener{ref: ref, path: source}, ref, destination)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := staged.path
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
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup failure published destination: %v", statErr)
	}
	if _, statErr := os.Stat(stagePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged output survived cleanup failure: %v", statErr)
	}
}

func TestArtifactPublicationRejectsEveryExistingDestinationType(t *testing.T) {
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
	if err := publishArtifactFile(opener, ref, fileDestination); err == nil {
		t.Fatal("file publication replaced a directory")
	}
	if data, readErr := os.ReadFile(canary); readErr != nil || string(data) != "preserved" {
		t.Fatalf("file publication changed directory destination: %q, %v", data, readErr)
	}

	directoryDestination := filepath.Join(root, "directory-destination")
	if err := os.WriteFile(directoryDestination, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageArtifactDirectory(
		opener,
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		directoryDestination,
	); err == nil {
		t.Fatal("directory publication replaced a file")
	}
	if data, readErr := os.ReadFile(directoryDestination); readErr != nil || string(data) != "preserved" {
		t.Fatalf("directory publication changed file destination: %q, %v", data, readErr)
	}
}

func TestDirectoryPublicationDoesNotReplaceConcurrentDestination(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/page-0001.png"
	source := filepath.Join(root, "source.png")
	if err := os.WriteFile(source, []byte("page"), 0o400); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "rendered")
	staged, err := stageArtifactDirectory(
		testArtifactOpener{ref: ref, path: source},
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	err = staged.commitWithHook(func() {
		hookErr = os.Mkdir(destination, 0o700)
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("publication replaced a concurrently created directory")
	}
	staged.abort()
	if info, statErr := os.Stat(destination); statErr != nil || !info.IsDir() {
		t.Fatalf("concurrent destination was not preserved: %#v, %v", info, statErr)
	}
}

func TestDirectoryPublicationRejectsForeignStageEntryWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/page-0001.png"
	source := filepath.Join(root, "source.png")
	if err := os.WriteFile(source, []byte("page"), 0o400); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "rendered")
	staged, err := stageArtifactDirectory(
		testArtifactOpener{ref: ref, path: source},
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := staged.path
	foreignPath := filepath.Join(stagePath, "foreign-canary")
	var hookErr error
	err = staged.commitWithHook(func() {
		hookErr = os.WriteFile(foreignPath, []byte("preserved"), 0o600)
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("publication accepted a foreign staging entry")
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("foreign staging entry reached destination: %v", statErr)
	}
	staged.abort()
	if data, readErr := os.ReadFile(foreignPath); readErr != nil || string(data) != "preserved" {
		t.Fatalf("abort deleted foreign staging entry: %q, %v", data, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(stagePath, "page-0001.png")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("abort retained owned staged artifact: %v", statErr)
	}
}

func TestDirectoryAbortPreservesReplacementArtifact(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/page-0001.png"
	source := filepath.Join(root, "source.png")
	if err := os.WriteFile(source, []byte("page"), 0o400); err != nil {
		t.Fatal(err)
	}
	staged, err := stageArtifactDirectory(
		testArtifactOpener{ref: ref, path: source},
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		filepath.Join(root, "rendered"),
	)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := staged.path
	pagePath := filepath.Join(stagePath, "page-0001.png")
	if err = os.Remove(pagePath); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(pagePath, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged.abort()
	if data, readErr := os.ReadFile(pagePath); readErr != nil || string(data) != "foreign" {
		t.Fatalf("abort deleted replacement artifact: %q, %v", data, readErr)
	}
}

func TestFilePublicationDoesNotReplaceConcurrentDestination(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/extracted-text.jsonl"
	source := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(source, []byte("new"), 0o400); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "result.jsonl")
	staged, err := stageArtifactFile(testArtifactOpener{ref: ref, path: source}, ref, destination)
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	err = staged.commitWithHook(func() {
		hookErr = os.WriteFile(destination, []byte("concurrent"), 0o600)
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("publication replaced a concurrently created file")
	}
	if data, readErr := os.ReadFile(destination); readErr != nil || string(data) != "concurrent" {
		t.Fatalf("concurrent destination changed: %q, %v", data, readErr)
	}
	staged.abort()
}

func TestAbortDoesNotDeleteReplacementStage(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/page-0001.png"
	source := filepath.Join(root, "source.png")
	if err := os.WriteFile(source, []byte("page"), 0o400); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "rendered")
	staged, err := stageArtifactDirectory(
		testArtifactOpener{ref: ref, path: source},
		[]documentpkg.Artifact{{Ref: ref, Pages: []int{1}}},
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement-stage")
	if err = os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementCanary := filepath.Join(replacement, "replacement-canary")
	if err = os.WriteFile(replacementCanary, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagePath := staged.path
	if err = os.RemoveAll(stagePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, stagePath); err != nil {
		t.Fatal(err)
	}
	staged.abort()
	if data, readErr := os.ReadFile(
		filepath.Join(stagePath, "replacement-canary"),
	); readErr != nil ||
		string(data) != "preserved" {
		t.Fatalf("replacement stage was deleted: %q, %v", data, readErr)
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
