//go:build windows

package document

import "os"

func openSourceNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
