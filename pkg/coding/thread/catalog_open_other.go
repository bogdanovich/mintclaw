//go:build !unix && !windows

package thread

import (
	"fmt"
	"os"
	"path/filepath"
)

type catalogDirectory struct {
	root   *os.Root
	reader *os.File
}

func openCatalogRoot(path string) (*catalogDirectory, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return newCatalogDirectory(root)
}

func openCatalogChildDirectory(parent *catalogDirectory, name string) (*catalogDirectory, error) {
	if parent == nil || parent.root == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("catalog directory and local child name are required")
	}
	root, err := parent.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return newCatalogDirectory(root)
}

func newCatalogDirectory(root *os.Root) (*catalogDirectory, error) {
	reader, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &catalogDirectory{root: root, reader: reader}, nil
}

func (d *catalogDirectory) readDir(count int) ([]os.DirEntry, error) {
	return d.reader.ReadDir(count)
}

func (d *catalogDirectory) Close() error {
	if d == nil {
		return nil
	}
	readerErr := d.reader.Close()
	rootErr := d.root.Close()
	if readerErr != nil {
		return readerErr
	}
	return rootErr
}

func openCatalogMetadataFile(root *catalogDirectory) (*os.File, error) {
	return root.root.OpenFile(metadataFileName, os.O_RDONLY, 0)
}
