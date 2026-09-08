//go:build windows

package media

import (
	"fmt"
	"os"
)

func openMediaSourceNoFollow(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("media source is not a direct regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("media source changed while opening")
	}
	return file, opened, nil
}
