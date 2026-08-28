//go:build windows

package thread

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func validateCatalogMetadataFile(file *os.File, info os.FileInfo) error {
	handleInfo, err := windowsCatalogHandleInfo(windows.Handle(file.Fd()))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		handleInfo.NumberOfLinks != 1 {
		return fmt.Errorf("not a singly linked regular file")
	}
	return nil
}
