package companion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type jobStoreDirectory struct {
	root *os.Root
}

func openJobStoreDirectory(path string) (*jobStoreDirectory, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &jobStoreDirectory{root: root}, nil
}

func (directory *jobStoreDirectory) close() error {
	if directory == nil || directory.root == nil {
		return nil
	}
	err := directory.root.Close()
	directory.root = nil
	return err
}

func validateJobStoreName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\`) {
		return errors.New("node job store name must be one path component")
	}
	return nil
}

func (directory *jobStoreDirectory) createRegularExclusive(
	name string,
	mode os.FileMode,
) (*os.File, error) {
	if directory == nil || directory.root == nil {
		return nil, errors.New("node job store is closed")
	}
	if err := validateJobStoreName(name); err != nil {
		return nil, err
	}
	return directory.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
}

func (directory *jobStoreDirectory) openRegular(name string) (*os.File, os.FileInfo, error) {
	if directory == nil || directory.root == nil {
		return nil, nil, errors.New("node job store is closed")
	}
	if err := validateJobStoreName(name); err != nil {
		return nil, nil, err
	}
	file, err := directory.root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("node job store entry %q is not regular", name)
	}
	return file, info, nil
}

func (directory *jobStoreDirectory) removeRegular(name string) error {
	if directory == nil || directory.root == nil {
		return errors.New("node job store is closed")
	}
	if err := validateJobStoreName(name); err != nil {
		return err
	}
	err := directory.root.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (directory *jobStoreDirectory) listNames() ([]string, error) {
	if directory == nil || directory.root == nil {
		return nil, errors.New("node job store is closed")
	}
	root, err := directory.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := root.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (directory *jobStoreDirectory) sync() error {
	if directory == nil || directory.root == nil {
		return errors.New("node job store is closed")
	}
	root, err := directory.root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Sync()
}
