package document

import (
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

const (
	PopplerBackendPackage         = "poppler-utils"
	PopplerBackendPackageRevision = "24.02.0-1ubuntu9.9"

	GhostscriptBackendPackage             = "ghostscript"
	GhostscriptBackendPackageRevision     = "10.02.1~dfsg1-0ubuntu7.9"
	NativeBackendIsolationMode            = isolation.DocumentPolicyMode
	NativeBackendIsolationPackageRevision = isolation.DocumentPolicyPackageRevision

	popplerTextExecutableName   = "pdftotext"
	popplerRenderExecutableName = "pdftoppm"
	popplerInfoExecutableName   = "pdfinfo"
	ghostscriptExecutableName   = "gs"
)

type nativeBackendSpec struct {
	identity    BackendIdentity
	executables []BackendExecutableCapability
}

var admittedNativeBackendManifest = []nativeBackendSpec{
	{
		identity: BackendIdentity{
			Name:            PopplerBackendName,
			Version:         PopplerBackendVersion,
			Package:         PopplerBackendPackage,
			PackageRevision: PopplerBackendPackageRevision,
			Role:            "production",
			IsolationMode:   NativeBackendIsolationMode,
		},
		executables: []BackendExecutableCapability{
			{
				Name:   popplerTextExecutableName,
				Path:   "/usr/bin/pdftotext",
				SHA256: "0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7",
			},
			{
				Name:   popplerRenderExecutableName,
				Path:   "/usr/bin/pdftoppm",
				SHA256: "207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8",
			},
			{
				Name:   popplerInfoExecutableName,
				Path:   "/usr/bin/pdfinfo",
				SHA256: "3293dda06d80e1e38dab859aa47368c2876aedc41cbc2e24e8fb9a4e66392078",
			},
		},
	},
	{
		identity: BackendIdentity{
			Name:            GhostscriptBackendName,
			Version:         GhostscriptBackendVersion,
			Package:         GhostscriptBackendPackage,
			PackageRevision: GhostscriptBackendPackageRevision,
			Role:            "independent_verifier",
			IsolationMode:   NativeBackendIsolationMode,
		},
		executables: []BackendExecutableCapability{
			{
				Name:   ghostscriptExecutableName,
				Path:   "/usr/bin/gs",
				SHA256: "eed795c04354a20cecc95a21155b560b55094330c279459b68037984b7c23667",
			},
		},
	},
}

func nativeBackendSpecByName(name string) (nativeBackendSpec, bool) {
	for _, backend := range admittedNativeBackendManifest {
		if backend.identity.Name == name {
			return backend, true
		}
	}
	return nativeBackendSpec{}, false
}

func nativeBackendExecutable(backendName, executableName string) (BackendExecutableCapability, bool) {
	backend, ok := nativeBackendSpecByName(backendName)
	if !ok {
		return BackendExecutableCapability{}, false
	}
	for _, executable := range backend.executables {
		if executable.Name == executableName {
			return executable, true
		}
	}
	return BackendExecutableCapability{}, false
}

func nativeBackendIdentity(name string) BackendIdentity {
	backend, ok := nativeBackendSpecByName(name)
	if !ok {
		return BackendIdentity{}
	}
	return backend.identity
}

func evaluateNativeBackend(
	backend nativeBackendSpec,
	digest func(string) string,
) BackendCapability {
	result := BackendCapability{
		Identity:    backend.identity,
		State:       CapabilitySupported,
		Executables: make([]BackendExecutableCapability, 0, len(backend.executables)),
	}
	for _, expected := range backend.executables {
		observed := digest(expected.Path)
		executable := expected
		executable.ObservedSHA256 = observed
		result.Executables = append(result.Executables, executable)
		if observed != expected.SHA256 && result.State == CapabilitySupported {
			result.State = CapabilityUnavailable
			result.Reason = fmt.Sprintf(
				"%s backend requires Ubuntu package %s=%s; %s is missing or has an unadmitted SHA-256",
				backend.identity.Name,
				backend.identity.Package,
				backend.identity.PackageRevision,
				expected.Path,
			)
		}
	}
	return result
}

func backendCapabilityByName(backends []BackendCapability, name string) (BackendCapability, bool) {
	for _, backend := range backends {
		if backend.Identity.Name == name {
			return backend, true
		}
	}
	return BackendCapability{}, false
}

func applyNativeBackendIsolationFailure(backends []BackendCapability, isolationErr error) []BackendCapability {
	if isolationErr == nil {
		return backends
	}
	for index := range backends {
		reason := fmt.Sprintf(
			"%s backend requires isolation mode %s: %v",
			backends[index].Identity.Name,
			NativeBackendIsolationMode,
			isolationErr,
		)
		if backends[index].Reason != "" {
			backends[index].Reason += "; additionally, " + reason
		} else {
			backends[index].Reason = reason
		}
		backends[index].State = CapabilityUnavailable
	}
	return backends
}
