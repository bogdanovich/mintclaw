//go:build !unix && !windows

package thread

import (
	"fmt"
	"os"
)

func validateAttachmentGCLinkedMetadataFile(_ *os.File, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a direct regular file")
	}
	return nil
}
