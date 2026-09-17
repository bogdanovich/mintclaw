//go:build !windows

package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openImageInput(path string) (*os.File, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(path))
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("image input file handle is unavailable")
	}
	return file, nil
}
