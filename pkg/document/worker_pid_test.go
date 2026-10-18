//go:build linux || darwin

package document

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func publishTestWorkerPID(path string, pid int) error {
	return fileutil.WriteFileAtomic(path, []byte(strconv.Itoa(pid)), 0o600)
}

func TestWorkerPIDPublicationNeverExposesIncompleteNumber(t *testing.T) {
	const first, second = 123456789, 987654321
	path := filepath.Join(t.TempDir(), "child.pid")
	if err := publishTestWorkerPID(path, first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for index := range 32 {
			pid := first
			if index%2 != 0 {
				pid = second
			}
			if err := publishTestWorkerPID(path, pid); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var observeErr error
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if observeErr != nil {
				t.Fatal(observeErr)
			}
			return
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				observeErr = err
				continue
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				observeErr = fmt.Errorf("parse published PID %q: %w", data, err)
			} else if pid != first && pid != second {
				observeErr = fmt.Errorf("unexpected published PID: %d", pid)
			}
		}
	}
}
