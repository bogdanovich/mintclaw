//go:build linux && amd64

package document

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

const maximumExecutableBytes = int64(64 * 1024 * 1024)

func currentNativeBackendIsolationFailure() error {
	return nativeBackendIsolationFailure(isolation.DocumentPolicyActive(), isolation.DocumentPolicyStatus)
}

func nativeBackendCapabilities() []BackendCapability {
	backends := make([]BackendCapability, 0, len(admittedNativeBackendManifest))
	for _, backend := range admittedNativeBackendManifest {
		backends = append(backends, evaluateNativeBackend(backend, executableSHA256))
	}
	return applyNativeBackendIsolationFailure(backends, currentNativeBackendIsolationFailure())
}

func nativeBackendAvailable(name string) bool {
	backend, ok := nativeBackendSpecByName(name)
	return ok && evaluateNativeBackend(backend, executableSHA256).State == CapabilitySupported &&
		currentNativeBackendIsolationFailure() == nil
}

func nativeBackendExecutablePaths(operation string) []string {
	paths := make([]string, 0)
	for _, backend := range admittedNativeBackendManifest {
		if backend.identity.Name == GhostscriptBackendName && operation != workerOperationFillCandidate {
			continue
		}
		for _, executable := range backend.executables {
			paths = append(paths, executable.Path)
		}
	}
	return paths
}

func executableSHA256(path string) string {
	file, err := openSourceNoFollow(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximumExecutableBytes+1))
	if err != nil || written <= 0 || written > maximumExecutableBytes {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}
