//go:build !windows

package document

import (
	"fmt"
	"io"
	"os"
)

// OpenWorkerInput opens the immutable snapshot inherited by the private worker.
func OpenWorkerInput() (io.ReadCloser, error) {
	file := os.NewFile(WorkerInputFileDescriptor(), "document-snapshot")
	if file == nil {
		return nil, fmt.Errorf("document worker input is unavailable")
	}
	return file, nil
}
