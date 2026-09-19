//go:build linux && amd64

package tools

import (
	"os"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

// TestMain lets production-style document tool tests exercise the same
// one-shot worker protocol as the mintclaw executable.
func TestMain(main *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "document" && os.Args[2] == "_worker" {
		input := os.NewFile(document.WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(1)
		}
		err := document.ServeWorker(os.Stdin, input, os.Stdout)
		_ = input.Close()
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(main.Run())
}
