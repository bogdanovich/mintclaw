package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// readBoundedDirectFile reads a bounded regular file and verifies that its
// directory entry still names the opened file after the read. The second
// lstat closes the symlink-to-the-same-inode replacement gap left by a single
// pre-open observation.
func readBoundedDirectFile(path, label string, maxBytes int64) ([]byte, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !isDirectRegularFile(entry) {
		return nil, fmt.Errorf("coding worktree: %s is not a direct regular file", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	current, currentErr := os.Lstat(path)
	closeErr := file.Close()
	if err := errors.Join(statErr, readErr, currentErr, closeErr); err != nil {
		return nil, err
	}
	if !sameDirectRegularFile(entry, opened, current) || int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("coding worktree: %s changed or exceeded its bound", label)
	}
	return data, nil
}

func isDirectRegularFile(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func sameDirectRegularFile(entry, opened, current os.FileInfo) bool {
	return isDirectRegularFile(entry) && isDirectRegularFile(opened) && isDirectRegularFile(current) &&
		os.SameFile(entry, opened) && os.SameFile(opened, current)
}
