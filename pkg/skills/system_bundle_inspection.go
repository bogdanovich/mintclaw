package skills

import (
	"bytes"
	"os"
	"runtime"
	"sync"
)

var embeddedBundleForInspection = sync.OnceValues(func() (systemBundleManifest, error) {
	return snapshotSystemBundle(embeddedSystemSkills, systemBundleSourceRoot)
})

// IsEmbeddedSystemBundleFile compares caller-observed bytes with the public
// assets compiled into this binary. It neither reads nor repairs an installed
// bundle and does not attest which generation or skills a runtime loaded.
func IsEmbeddedSystemBundleFile(generation, name string, contents []byte, mode os.FileMode) bool {
	manifest, err := embeddedBundleForInspection()
	if err != nil || !mode.IsRegular() || !validBundleGenerationName(generation, manifest.Fingerprint) {
		return false
	}
	for _, file := range manifest.Files {
		if file.Path == name {
			return systemBundleFileModeMatches(runtime.GOOS, mode, os.FileMode(file.Mode)) &&
				bytes.Equal(contents, file.data)
		}
	}
	return false
}
