package document

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	documentpkg "github.com/bogdanovich/mintclaw/pkg/document"
)

type artifactOpener interface {
	OpenArtifact(string) (io.ReadCloser, error)
}

func parsePageSelection(value string) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var pages []int
	previous := 0
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, errors.New("page selection contains an empty item")
		}
		bounds := strings.Split(item, "-")
		if len(bounds) > 2 {
			return nil, errors.New("page selection is invalid")
		}
		start, err := strconv.Atoi(bounds[0])
		if err != nil || start <= 0 {
			return nil, errors.New("page selection must use positive one-based pages")
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(bounds[1])
			if err != nil || end < start {
				return nil, errors.New("page range is invalid or reversed")
			}
		}
		for page := start; page <= end; page++ {
			if page <= previous {
				return nil, errors.New("page selection must be sorted and contain no duplicates")
			}
			pages = append(pages, page)
			previous = page
		}
	}
	return pages, nil
}

func publishArtifactFile(snapshot artifactOpener, ref, destination string, overwrite bool) error {
	parent := filepath.Dir(destination)
	if parent == "" {
		parent = "."
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return errors.New("document output parent directory is unavailable")
	}
	source, err := snapshot.OpenArtifact(ref)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	temporary, err := os.CreateTemp(parent, ".mintclaw-document-output-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = io.Copy(temporary, source)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if overwrite {
		return os.Rename(temporaryPath, destination)
	}
	if err = os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("document output already exists; use --overwrite to replace it")
		}
		return err
	}
	return nil
}

func publishArtifactDirectory(
	snapshot artifactOpener,
	artifacts []documentpkg.Artifact,
	destination string,
	overwrite bool,
) error {
	parent := filepath.Dir(destination)
	base := filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return errors.New("document output directory is invalid")
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return errors.New("document output parent directory is unavailable")
	}
	stage, err := os.MkdirTemp(parent, "."+base+".mintclaw-stage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, artifact := range artifacts {
		if len(artifact.Pages) != 1 {
			return errors.New("rendered artifact page mapping is invalid")
		}
		name := fmt.Sprintf("page-%04d.png", artifact.Pages[0])
		if err = publishArtifactFile(snapshot, artifact.Ref, filepath.Join(stage, name), false); err != nil {
			return err
		}
	}
	if _, err = os.Lstat(destination); err == nil {
		if !overwrite {
			return errors.New("document output directory already exists; use --overwrite to replace it")
		}
		backup, backupErr := os.MkdirTemp(parent, "."+base+".mintclaw-backup-*")
		if backupErr != nil {
			return backupErr
		}
		if backupErr = os.Remove(backup); backupErr != nil {
			return backupErr
		}
		if err = os.Rename(destination, backup); err != nil {
			return err
		}
		if err = os.Rename(stage, destination); err != nil {
			_ = os.Rename(backup, destination)
			return err
		}
		_ = os.RemoveAll(backup)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(stage, destination)
}

func failArtifactPublication(report *documentpkg.Report) {
	report.State = documentpkg.StateFailed
	report.Artifacts = nil
	report.Extraction = nil
	report.Rendering = nil
	report.Failure = &documentpkg.Failure{
		Code: documentpkg.FailureArtifactRegistration, Message: "document artifacts could not be published",
	}
}

func closeSnapshot(snapshot *documentpkg.Snapshot, report *documentpkg.Report) {
	if snapshot == nil {
		return
	}
	if err := snapshot.Close(); err != nil {
		report.State = documentpkg.StateFailed
		report.Artifacts = nil
		report.Extraction = nil
		report.Rendering = nil
		report.Failure = &documentpkg.Failure{
			Code: documentpkg.FailureInternal, Message: "protected scratch cleanup failed",
		}
	}
}
