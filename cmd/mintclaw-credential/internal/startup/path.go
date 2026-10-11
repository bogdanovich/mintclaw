package startup

import (
	"path/filepath"
	"strings"
)

func validDirectoryPath(path string) bool {
	if len(path) < 2 || len(path) > 4096 || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path || strings.Count(path, "/") > 16 {
		return false
	}
	for _, character := range []byte(path) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
