//go:build !windows

package media

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openMediaSourceNoFollow(path string) (*os.File, os.FileInfo, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("media source is not a direct regular file")
	}
	fd, err := unix.Open(absPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(absPath))
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("media source file handle is unavailable")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("media source is not a regular file")
	}
	return file, opened, nil
}
