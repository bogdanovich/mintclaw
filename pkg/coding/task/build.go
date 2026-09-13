package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// ExecutableBuildID returns a content identity for an operator-pinned worker
// artifact without importing or initializing the worker runtime.
func ExecutableBuildID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("coding worker build identity: open executable: %w", err)
	}
	info, statErr := file.Stat()
	if statErr == nil && (!info.Mode().IsRegular() || info.Size() < 0) {
		statErr = fmt.Errorf("executable is not a regular file")
	}
	digest := sha256.New()
	if statErr == nil {
		_, statErr = io.Copy(digest, io.NewSectionReader(file, 0, info.Size()))
	}
	closeErr := file.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return "", fmt.Errorf("coding worker build identity: hash executable: %w", err)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
