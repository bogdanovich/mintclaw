//go:build !windows

package document

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openSourceNoFollow(path string) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(
		strings.TrimPrefix(filepath.Clean(absPath), string(os.PathSeparator)),
		string(os.PathSeparator),
	)
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, fmt.Errorf("source path does not name a file")
	}
	directoryFD, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(directoryFD) }()
	for _, part := range parts[:len(parts)-1] {
		nextFD, openErr := unix.Openat(
			directoryFD,
			part,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(directoryFD)
		directoryFD = nextFD
	}
	fd, err := unix.Openat(
		directoryFD,
		parts[len(parts)-1],
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(absPath))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("source file handle is unavailable")
	}
	return file, nil
}
