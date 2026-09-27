//go:build windows

package document

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

const workerInputHandleEnvironment = "MINTCLAW_DOCUMENT_INPUT_HANDLE"

// OpenWorkerInput opens the immutable snapshot handle explicitly inherited by
// the private worker. The handle value is process-local and carries no path.
func OpenWorkerInput() (io.ReadCloser, error) {
	value := os.Getenv(workerInputHandleEnvironment)
	handle, err := strconv.ParseUint(value, 10, strconv.IntSize)
	if err != nil || handle == 0 {
		return nil, fmt.Errorf("document worker input is unavailable")
	}
	file := os.NewFile(uintptr(handle), "document-snapshot")
	if file == nil {
		return nil, fmt.Errorf("document worker input is unavailable")
	}
	return file, nil
}
