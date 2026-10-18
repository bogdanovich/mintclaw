package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedSystemBundleInspectionUsesCompiledBytes(t *testing.T) {
	manifest, err := snapshotSystemBundle(embeddedSystemSkills, systemBundleSourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		mode := os.FileMode(file.Mode)
		for _, generation := range []string{manifest.Fingerprint, manifest.Fingerprint + "-repaired1"} {
			if !IsEmbeddedSystemBundleFile(generation, file.Path, file.data, mode) {
				t.Fatal("compiled public file was not recognized")
			}
		}
		changed := append([]byte{}, file.data...)
		if len(changed) == 0 {
			changed = []byte{'x'}
		} else {
			changed[0] ^= 1
		}
		for _, input := range []struct {
			generation, name string
			data             []byte
			mode             os.FileMode
		}{
			{manifest.Fingerprint, file.Path, changed, mode},
			{manifest.Fingerprint, file.Path, append(append([]byte{}, file.data...), 'x'), mode},
			{manifest.Fingerprint, "../" + file.Path, file.data, mode},
			{manifest.Fingerprint, file.Path + ".other", file.data, mode},
			{strings.Repeat("0", 64), file.Path, file.data, mode},
			{manifest.Fingerprint + "-../other", file.Path, file.data, mode},
			{manifest.Fingerprint, file.Path, file.data, mode | os.ModeSymlink},
			{manifest.Fingerprint, file.Path, file.data, mode | os.ModeDir},
		} {
			if IsEmbeddedSystemBundleFile(input.generation, input.name, input.data, input.mode) {
				t.Fatal("untrusted bytes, path, generation or file type acquired compiled identity")
			}
		}
		if IsEmbeddedSystemBundleFile(
			manifest.Fingerprint,
			file.Path,
			file.data,
			0o600,
		) != (runtime.GOOS == "windows") {
			t.Fatal("inspection disagrees with native platform permission policy")
		}
	}
}

func TestEmbeddedSystemBundleInspectionIsConcurrentAndDoesNotRepair(t *testing.T) {
	home := t.TempDir()
	bundle, err := EnsureSystemBundle(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotSystemBundle(embeddedSystemSkills, systemBundleSourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	file := manifest.Files[0]
	name := filepath.Join(bundle.Root, filepath.FromSlash(file.Path))
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(func() {
			if !IsEmbeddedSystemBundleFile(bundle.Fingerprint, file.Path, file.data, os.FileMode(file.Mode)) {
				t.Error("pure compiled-byte observation depended on installed state")
			}
		})
	}
	calls.Wait()
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatal("read-only inspection repaired or created an installed file")
	}
}
