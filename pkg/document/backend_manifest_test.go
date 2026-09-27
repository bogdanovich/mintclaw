package document

import (
	"strings"
	"testing"
)

func TestAdmittedNativeBackendManifestIsComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		packageName     string
		packageRevision string
		role            string
		executables     int
	}{
		{
			name: PopplerBackendName, packageName: PopplerBackendPackage,
			packageRevision: PopplerBackendPackageRevision, role: "production", executables: 3,
		},
		{
			name: GhostscriptBackendName, packageName: GhostscriptBackendPackage,
			packageRevision: GhostscriptBackendPackageRevision, role: "independent_verifier", executables: 1,
		},
	}
	for _, test := range tests {
		backend, ok := nativeBackendSpecByName(test.name)
		if !ok || backend.identity.Package != test.packageName ||
			backend.identity.PackageRevision != test.packageRevision || backend.identity.Role != test.role ||
			backend.identity.IsolationMode != "one_shot_child" || len(backend.executables) != test.executables {
			t.Fatalf("backend %q manifest = %#v", test.name, backend)
		}
		for _, executable := range backend.executables {
			if executable.Name == "" || executable.Path == "" || len(executable.SHA256) != 64 {
				t.Fatalf("backend %q executable = %#v", test.name, executable)
			}
			resolved, found := nativeBackendExecutable(test.name, executable.Name)
			if !found || resolved != executable {
				t.Fatalf("backend %q executable lookup = %#v, found=%t", test.name, resolved, found)
			}
		}
	}
}

func TestEvaluateNativeBackendFailsClosedWithPackageRevision(t *testing.T) {
	t.Parallel()

	backend, ok := nativeBackendSpecByName(GhostscriptBackendName)
	if !ok {
		t.Fatal("Ghostscript manifest is missing")
	}
	supported := evaluateNativeBackend(backend, func(path string) string {
		for _, executable := range backend.executables {
			if executable.Path == path {
				return executable.SHA256
			}
		}
		return ""
	})
	if supported.State != CapabilitySupported || supported.Reason != "" {
		t.Fatalf("supported backend = %#v", supported)
	}

	unknown := evaluateNativeBackend(backend, func(string) string { return strings.Repeat("0", 64) })
	if unknown.State != CapabilityUnavailable ||
		!strings.Contains(unknown.Reason, GhostscriptBackendPackage+"="+GhostscriptBackendPackageRevision) ||
		len(unknown.Executables) != 1 || unknown.Executables[0].ObservedSHA256 != strings.Repeat("0", 64) {
		t.Fatalf("unknown backend = %#v", unknown)
	}
}
