//go:build unix

package thread

import (
	"fmt"
	"os"
	"syscall"
)

func validateAttachmentGCLinkedMetadataFile(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink < 1 || stat.Nlink > 2 {
		return fmt.Errorf("not a direct regular file with a bounded link count")
	}
	return nil
}
