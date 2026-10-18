package bus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestInboundSpoolDirectoryRetryReconfirmsEveryCommittedCreationBoundary(t *testing.T) {
	for _, alias := range []bool{false, true} {
		for level := 0; level < 3; level++ {
			name := []string{"first_component", "middle_component", "target_component"}[level]
			if alias {
				name += "_alias"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				if alias {
					linked := filepath.Join(t.TempDir(), "root-alias")
					if err := os.Symlink(root, linked); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
					root = linked
				}
				target := filepath.Join(root, "a", "b", "spool")
				parents := []string{root, filepath.Join(root, "a"), filepath.Join(root, "a", "b")}
				failParent := parents[level]
				injected := errors.New("creation directory sync unconfirmed")
				firstAttempt := true
				uncertain := ""
				var synced []string
				syncDirectory := func(dir string) error {
					synced = append(synced, dir)
					if firstAttempt && dir == failParent {
						uncertain = dir
						return injected
					}
					if !firstAttempt && dir == uncertain {
						uncertain = ""
					}
					return fileutil.SyncDirectory(dir)
				}
				mkdirDurable := func(parent, relative string, mode os.FileMode) error {
					// The shared helper performs the real creation and parent flush. A
					// returned committed error deliberately leaves its outcome unconfirmed.
					if err := fileutil.MkdirAllDurable(parent, relative, mode); err != nil {
						return err
					}
					if err := syncDirectory(parent); err != nil {
						return &fileutil.CommittedWriteError{Err: err}
					}
					return nil
				}
				err := createInboundSpoolDirectoryWithOps(target, mkdirDurable, syncDirectory)
				if !errors.Is(err, injected) || !fileutil.IsCommittedWriteError(err) || uncertain == "" {
					t.Fatalf("first nested creation = %v, uncertain %q", err, uncertain)
				}
				// Retain the actual partial namespace. A retry cannot treat the
				// newly-existing directory as a confirmed durable anchor.
				firstAttempt = false
				synced = nil
				if err = createInboundSpoolDirectoryWithOps(target, mkdirDurable, syncDirectory); err != nil {
					t.Fatal(err)
				}
				if uncertain != "" {
					t.Fatalf("retry skipped the unconfirmed ancestor %q: synced %#v", uncertain, synced)
				}
				if info, err := os.Stat(target); err != nil || !info.IsDir() {
					t.Fatalf("retried spool target: %#v, %v", info, err)
				}
			})
		}
	}
}
