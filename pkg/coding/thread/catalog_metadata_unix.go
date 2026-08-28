//go:build unix

package thread

import (
	"fmt"
	"os"
	"syscall"
)

func validateCatalogMetadataFile(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		return fmt.Errorf("not a singly linked regular file")
	}
	return nil
}
