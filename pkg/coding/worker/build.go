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

// ExecutableBuildID returns a content identity for the executable at path.
// Parent and child processes compare this value during initialization so a
// task cannot silently start on a different MintClaw artifact.
func ExecutableBuildID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("coding worker build identity: open executable: %w", err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", fmt.Errorf("coding worker build identity: hash executable: %w", err)
	}
	return executableBuildIDPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

// CurrentExecutableBuildID identifies the artifact running this process.
func CurrentExecutableBuildID() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("coding worker build identity: locate executable: %w", err)
	}
	return ExecutableBuildID(path)
}
