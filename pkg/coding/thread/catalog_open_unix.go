//go:build unix

package thread

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type catalogDirectory struct {
	file *os.File
}

func openCatalogRoot(path string) (*catalogDirectory, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return newCatalogDirectory(fd, path)
}

func openCatalogChildDirectory(parent *catalogDirectory, name string) (*catalogDirectory, error) {
	if parent == nil || parent.file == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("catalog directory and local child name are required")
	}
	fd, err := unix.Openat(
		int(parent.file.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	return newCatalogDirectory(fd, filepath.Join(parent.file.Name(), name))
}

func newCatalogDirectory(fd int, name string) (*catalogDirectory, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("create catalog directory handle")
	}
	return &catalogDirectory{file: file}, nil
}

func (d *catalogDirectory) readDir(count int) ([]os.DirEntry, error) {
	return d.file.ReadDir(count)
}

func (d *catalogDirectory) Close() error {
	if d == nil || d.file == nil {
		return nil
	}
	return d.file.Close()
}

func openCatalogMetadataFile(root *catalogDirectory) (*os.File, error) {
	fd, err := unix.Openat(
		int(root.file.Fd()),
		metadataFileName,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root.file.Name(), metadataFileName))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("create catalog metadata handle")
	}
	return file, nil
}
