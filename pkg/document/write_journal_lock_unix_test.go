//go:build !windows

package document

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJournalRejectsUnsafeLockFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "outside.lock")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "broad permissions",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			journal, err := NewWriteJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, journal.lockPath())
			_, _, err = journal.Accept(
				t.Context(),
				writeTestOperationID("unsafe_lock"),
				writeTestOwner(),
				normalizedWriteTestRequest(t, "safe value"),
			)
			if !errors.Is(err, ErrWriteJournalFailed) {
				t.Fatalf("unsafe lock error = %v", err)
			}
		})
	}
}
