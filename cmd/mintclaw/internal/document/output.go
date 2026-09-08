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
	identity    *pathIdentity
}

type pathIdentity struct {
	file      *os.File
	info      os.FileInfo
	directory bool
}

func openPathIdentity(path string, directory bool) (*pathIdentity, error) {
	file, err := openOutputIdentity(path)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	current, currentErr := os.Lstat(path)
	if statErr != nil || currentErr != nil || !validOutputType(info, directory) ||
		!validOutputType(current, directory) || !os.SameFile(info, current) {
		_ = file.Close()
		return nil, errors.New("document output identity is unavailable")
	}
	return &pathIdentity{file: file, info: info, directory: directory}, nil
}

func (identity *pathIdentity) matches(path string) bool {
	if identity == nil || identity.file == nil || path == "" {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && validOutputType(current, identity.directory) && os.SameFile(identity.info, current)
}

func (identity *pathIdentity) close() {
	if identity != nil && identity.file != nil {
		_ = identity.file.Close()
		identity.file = nil
	}
}

func (output *stagedArtifactOutput) clearStage() {
	if output == nil {
		return
	}
	output.path = ""
	output.identity.close()
	output.identity = nil
}

func (output *stagedArtifactOutput) bindStageIdentity() error {
	if output == nil || output.path == "" || output.identity != nil {
		return errors.New("document staged output identity is invalid")
	}
	identity, err := openPathIdentity(output.path, output.directory)
	if err != nil {
		return err
	}
	output.identity = identity
	return nil
}

func (output *stagedArtifactOutput) abort() {
	if output == nil {
		return
	}
	if output.path != "" && output.identity != nil && output.identity.matches(output.path) {
		if output.directory {
			_ = os.RemoveAll(output.path)
		} else {
			_ = os.Remove(output.path)
		}
	}
	output.clearStage()
}

func (output *stagedArtifactOutput) commit() error {
	return output.commitWithHook(nil)
}

func (output *stagedArtifactOutput) commitWithHook(afterValidation func()) error {
	if output == nil || output.path == "" {
		return errors.New("document output was not staged")
	}
	if output.identity == nil || !output.identity.matches(output.path) {
		return errors.New("document staged output changed during publication")
	}
	if !output.overwrite {
		return output.commitNoReplace(afterValidation)
	}
	return output.replaceWithHook(afterValidation)
}

func (output *stagedArtifactOutput) commitNoReplace(afterValidation func()) error {
	if afterValidation != nil {
		afterValidation()
	}
	if !output.identity.matches(output.path) {
		return errors.New("document staged output changed during publication")
	}
	if err := renamePathNoReplace(output.path, output.destination); err != nil {
		if _, statErr := os.Lstat(output.destination); statErr == nil {
			return errors.New("document output already exists; use --overwrite to replace it")
		}
		return err
	}
	if !output.identity.matches(output.destination) {
		return errors.New("document staged output identity changed during publication")
	}
	output.clearStage()
	return nil
}

func (output *stagedArtifactOutput) replaceWithHook(afterValidation func()) error {
	info, err := os.Lstat(output.destination)
	if errors.Is(err, os.ErrNotExist) {
		return output.commitNoReplace(afterValidation)
	} else if err != nil {
		return err
	}
	if !validOutputType(info, output.directory) {
		return errors.New("document output destination has the wrong type")
	}
	destinationIdentity, err := openPathIdentity(output.destination, output.directory)
	if err != nil {
		return err
	}
	defer destinationIdentity.close()
	if !os.SameFile(info, destinationIdentity.info) {
		return errors.New("document output destination changed during publication")
	}
	backupRoot, err := os.MkdirTemp(
		filepath.Dir(output.destination),
		"."+filepath.Base(output.destination)+".mintclaw-backup-*",
	)
	if err != nil {
		return err
	}
	backupRootIdentity, err := openPathIdentity(backupRoot, true)
	if err != nil {
		return errors.New("document output backup identity is unavailable")
	}
	defer backupRootIdentity.close()
	preserveBackup := false
	defer func() {
		if !preserveBackup && backupRootIdentity.matches(backupRoot) {
			_ = os.RemoveAll(backupRoot)
		}
	}()
	if afterValidation != nil {
		afterValidation()
	}
	if !output.identity.matches(output.path) || !destinationIdentity.matches(output.destination) ||
		!backupRootIdentity.matches(backupRoot) {
		return errors.New("document output identity changed during publication")
	}
	if err = exchangePaths(output.path, output.destination); err != nil {
		return err
	}
	if !output.identity.matches(output.destination) || !destinationIdentity.matches(output.path) {
		if rollbackErr := rollbackVerifiedExchange(
			output.path,
			destinationIdentity,
			output.destination,
			output.identity,
			nil,
		); rollbackErr != nil {
			preserveBackup = true
			return fmt.Errorf("document output destination changed and rollback failed: %w", rollbackErr)
		}
		return errors.New("document output destination changed during publication")
	}

	backup := filepath.Join(backupRoot, "validated-destination")
	if !destinationIdentity.matches(output.path) || !backupRootIdentity.matches(backupRoot) {
		preserveBackup = true
		return errors.New("document output destination changed during publication cleanup")
	}
	if err = renamePathNoReplace(output.path, backup); err != nil {
		rollbackErr := rollbackVerifiedExchange(
			output.path,
			destinationIdentity,
			output.destination,
			output.identity,
			nil,
		)
		if rollbackErr != nil {
			preserveBackup = true
			return fmt.Errorf("document output cleanup and rollback failed: %w", rollbackErr)
		}
		return fmt.Errorf("document output cleanup failed: %w", err)
	}
	if !output.identity.matches(output.destination) || !destinationIdentity.matches(backup) ||
		!backupRootIdentity.matches(backupRoot) {
		preserveBackup = true
		return errors.New("document output identity changed during publication cleanup")
	}
	output.clearStage()
	if output.directory {
		if err = os.RemoveAll(backup); err != nil {
			preserveBackup = true
			return fmt.Errorf("document output cleanup failed: %w", err)
		}
	} else if err = os.Remove(backup); err != nil {
		preserveBackup = true
		return fmt.Errorf("document output cleanup failed: %w", err)
	}
	return nil
}

func rollbackVerifiedExchange(
	left string,
	leftIdentity *pathIdentity,
	right string,
	rightIdentity *pathIdentity,
	afterValidation func(),
) error {
	if leftIdentity == nil || rightIdentity == nil {
		return errors.New("document output rollback identities are unavailable")
	}
	if !leftIdentity.matches(left) || !rightIdentity.matches(right) {
		return errors.New("document output rollback identities changed")
	}
	if afterValidation != nil {
		afterValidation()
	}
	if !leftIdentity.matches(left) || !rightIdentity.matches(right) {
		return errors.New("document output rollback identities changed")
	}
	if err := exchangePaths(left, right); err != nil {
		return err
	}
	if !leftIdentity.matches(right) || !rightIdentity.matches(left) {
		return errors.New("document output rollback identities changed")
	}
	return nil
}

func validOutputType(info os.FileInfo, directory bool) bool {
	return info != nil && ((directory && info.IsDir()) || (!directory && info.Mode().IsRegular()))
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
	if err = output.bindStageIdentity(); err != nil {
		_ = temporary.Close()
		output.abort()
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
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
	complete = true
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
	output := &stagedArtifactOutput{
		path: stage, destination: destination, directory: true, overwrite: overwrite,
	}
	if err = output.bindStageIdentity(); err != nil {
		output.abort()
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			output.abort()
		}
	}()
	for _, artifact := range artifacts {
		if len(artifact.Pages) != 1 {
			return nil, errors.New("rendered artifact page mapping is invalid")
		}
		name := fmt.Sprintf("page-%04d.png", artifact.Pages[0])
		if err = publishArtifactFile(snapshot, artifact.Ref, filepath.Join(stage, name), false); err != nil {
			return nil, err
		}
	}
	complete = true
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
