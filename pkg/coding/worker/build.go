package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const executableBuildIDPrefix = "sha256:"

type pinnedExecutable struct {
	file *os.File
	err  error
}

// Package initialization pins the running image before the worker can signal
// readiness. Atomic replacement of its pathname can no longer change the
// bytes used by CurrentExecutableBuildID.
var currentExecutable = pinCurrentExecutable()

// ExecutableBuildID returns a content identity for the executable at path.
// Parent and child processes compare this value during initialization so a
// task cannot silently start on a different MintClaw artifact.
func ExecutableBuildID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("coding worker build identity: open executable: %w", err)
	}
	buildID, hashErr := executableFileBuildID(file)
	closeErr := file.Close()
	if err := errors.Join(hashErr, closeErr); err != nil {
		return "", fmt.Errorf("coding worker build identity: hash executable: %w", err)
	}
	return buildID, nil
}

// CurrentExecutableBuildID identifies the artifact running this process.
func CurrentExecutableBuildID() (string, error) {
	if currentExecutable.err != nil {
		return "", currentExecutable.err
	}
	if currentExecutable.file == nil {
		return "", fmt.Errorf("coding worker build identity: running executable is unavailable")
	}
	buildID, err := executableFileBuildID(currentExecutable.file)
	if err != nil {
		return "", fmt.Errorf("coding worker build identity: hash running executable: %w", err)
	}
	return buildID, nil
}

func pinCurrentExecutable() pinnedExecutable {
	file, err := openRunningExecutable()
	if err != nil {
		return pinnedExecutable{err: fmt.Errorf("coding worker build identity: pin running executable: %w", err)}
	}
	return pinnedExecutable{file: file}
}

func executableFileBuildID(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return "", fmt.Errorf("executable is not a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(file, 0, info.Size())); err != nil {
		return "", err
	}
	return executableBuildIDPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}
