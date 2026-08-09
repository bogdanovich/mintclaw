//go:build linux || darwin

package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

func InspectExecutable(path string, platform string, architecture string) (string, int64, error) {
	file, info, err := openExecutable(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	if err = validateExecutableFile(file, platform, architecture); err != nil {
		return "", 0, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, MaxPayloadBytes+1))
	if err != nil || written != info.Size() {
		return "", 0, errors.New("companion executable changed while hashing")
	}
	return hex.EncodeToString(digest.Sum(nil)), written, nil
}

func ValidateRunningCoordinator(installation Installation) error {
	path, err := os.Executable()
	if err != nil {
		return errors.New("resolve running coordinator executable")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return errors.New("resolve running coordinator executable identity")
	}
	path, err = filepath.Abs(path)
	if err != nil || filepath.Clean(path) != installation.CoordinatorPath {
		return errors.New("running coordinator path does not match managed installation")
	}
	digest, _, err := InspectExecutable(path, installation.Platform, installation.Architecture)
	if err != nil || digest != installation.CoordinatorSHA256 {
		return errors.New("running coordinator identity does not match managed installation")
	}
	if uint32(os.Geteuid()) != installation.ServiceUID || uint32(os.Getegid()) != installation.ServiceGID {
		return errors.New("running coordinator account does not match managed installation")
	}
	return nil
}

func openExecutable(path string) (*os.File, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, errors.New("companion executable must use a clean absolute path")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect companion executable: %w", err)
	}
	links, _, identityOK := unixFileIdentity(before)
	if !identityOK || !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 || links != 1 ||
		before.Size() <= 0 || before.Size() > MaxPayloadBytes {
		return nil, nil, errors.New("companion executable must be one bounded non-writable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open companion executable: %w", err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, errors.New("companion executable identity changed while opening")
	}
	return file, after, nil
}

func validateExecutableFile(file *os.File, platform string, architecture string) error {
	return nodeupdate.ValidateExecutable(file, platform, architecture)
}
