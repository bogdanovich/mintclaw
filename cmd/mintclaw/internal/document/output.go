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

func parsePageSelection(value string, maximum int) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if maximum <= 0 {
		return nil, errors.New("page selection limit is invalid")
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
		span := end - start
		if span >= maximum || len(pages) > maximum-1-span {
			return nil, fmt.Errorf("page selection exceeds the %d-page limit", maximum)
		}
		for offset := 0; offset <= span; offset++ {
			page := start + offset
			if page <= previous {
				return nil, errors.New("page selection must be sorted and contain no duplicates")
			}
			pages = append(pages, page)
			previous = page
		}
	}
	return pages, nil
}

type stagedArtifactOutput struct {
	path        string
	destination string
	directory   bool
	overwrite   bool
}

func (output *stagedArtifactOutput) abort() {
	if output == nil || output.path == "" {
		return
	}
	if output.directory {
		_ = os.RemoveAll(output.path)
	} else {
		_ = os.Remove(output.path)
	}
}

func (output *stagedArtifactOutput) commit() error {
	if output == nil || output.path == "" {
		return errors.New("document output was not staged")
	}
	if !output.overwrite {
		if output.directory {
			return os.Rename(output.path, output.destination)
		}
		if err := os.Link(output.path, output.destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errors.New("document output already exists; use --overwrite to replace it")
			}
			return err
		}
		_ = os.Remove(output.path)
		return nil
	}
	return replaceStagedPath(output.path, output.destination)
}

func replaceStagedPath(stage, destination string) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stage, destination)
	} else if err != nil {
		return err
	}

	parent := filepath.Dir(destination)
	base := filepath.Base(destination)
	backup, err := os.MkdirTemp(parent, "."+base+".mintclaw-backup-*")
	if err != nil {
		return err
	}
	if err = os.Remove(backup); err != nil {
		return err
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
}

func stageArtifactFile(
	snapshot artifactOpener,
	ref, destination string,
	overwrite bool,
) (*stagedArtifactOutput, error) {
	parent := filepath.Dir(destination)
	if parent == "" {
		parent = "."
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return nil, errors.New("document output parent directory is unavailable")
	}
	if !overwrite {
		if _, err := os.Lstat(destination); err == nil {
			return nil, errors.New("document output already exists; use --overwrite to replace it")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	output := &stagedArtifactOutput{destination: destination, overwrite: overwrite}
	source, err := snapshot.OpenArtifact(ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	temporary, err := os.CreateTemp(parent, ".mintclaw-document-output-*")
	if err != nil {
		return nil, err
	}
	output.path = temporary.Name()
	defer func() {
		if err != nil {
			output.abort()
		}
	}()
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
		return nil, err
	}
	return output, nil
}

func publishArtifactFile(snapshot artifactOpener, ref, destination string, overwrite bool) error {
	output, err := stageArtifactFile(snapshot, ref, destination, overwrite)
	if err != nil {
		return err
	}
	defer output.abort()
	return output.commit()
}

func stageArtifactDirectory(
	snapshot artifactOpener,
	artifacts []documentpkg.Artifact,
	destination string,
	overwrite bool,
) (*stagedArtifactOutput, error) {
	parent := filepath.Dir(destination)
	base := filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return nil, errors.New("document output directory is invalid")
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return nil, errors.New("document output parent directory is unavailable")
	}
	if !overwrite {
		if _, err := os.Lstat(destination); err == nil {
			return nil, errors.New("document output directory already exists; use --overwrite to replace it")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	stage, err := os.MkdirTemp(parent, "."+base+".mintclaw-stage-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, artifact := range artifacts {
		if len(artifact.Pages) != 1 {
			return nil, errors.New("rendered artifact page mapping is invalid")
		}
		name := fmt.Sprintf("page-%04d.png", artifact.Pages[0])
		if err = publishArtifactFile(snapshot, artifact.Ref, filepath.Join(stage, name), false); err != nil {
			return nil, err
		}
	}
	output := &stagedArtifactOutput{
		path: stage, destination: destination, directory: true, overwrite: overwrite,
	}
	stage = ""
	return output, nil
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

func finishArtifactPublication(closeSnapshot func() error, output *stagedArtifactOutput, report *documentpkg.Report) {
	if output != nil {
		defer output.abort()
	}
	if closeSnapshot != nil {
		if err := closeSnapshot(); err != nil {
			report.State = documentpkg.StateFailed
			report.Artifacts = nil
			report.Extraction = nil
			report.Rendering = nil
			report.Failure = &documentpkg.Failure{
				Code: documentpkg.FailureInternal, Message: "protected scratch cleanup failed",
			}
		}
	}
	if report.State != documentpkg.StateSucceeded || output == nil {
		return
	}
	if err := output.commit(); err != nil {
		failArtifactPublication(report)
	}
}
