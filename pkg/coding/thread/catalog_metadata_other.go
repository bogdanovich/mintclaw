//go:build !unix && !windows

package thread

import (
	"fmt"
	"os"
)

func validateCatalogMetadataFile(_ *os.File, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	return nil
}
