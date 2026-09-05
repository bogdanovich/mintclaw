//go:build unix

package thread

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openPinnedRootFile(root *os.Root, name string) (*os.File, error) {
	if root == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("pinned root and local file name are required")
	}
	return root.OpenFile(
		name,
		os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
}
