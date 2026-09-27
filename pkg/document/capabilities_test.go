package document

import "testing"

func TestCapabilitiesAdmitPortableOperationsOnSupportedPlatforms(t *testing.T) {
	tests := []struct {
		goos       string
		arch       string
		readState  string
		fieldState string
		fillState  string
	}{
		{
			goos: "linux", arch: "amd64", readState: CapabilitySupported,
			fieldState: CapabilitySupported, fillState: CapabilitySupported,
		},
		{
			goos: "linux", arch: "386", readState: CapabilityUnavailable,
			fieldState: CapabilityUnavailable, fillState: CapabilityUnavailable,
		},
		{
			goos: "linux", arch: "arm", readState: CapabilityUnavailable,
			fieldState: CapabilityUnavailable, fillState: CapabilityUnavailable,
		},
		{
			goos: "linux", arch: "arm64", readState: CapabilityUnavailable,
			fieldState: CapabilityUnavailable, fillState: CapabilityUnavailable,
		},
		{
			goos: "darwin", arch: "amd64", readState: CapabilitySupported,
			fieldState: CapabilitySupported, fillState: CapabilitySupported,
		},
		{
			goos: "darwin", arch: "arm64", readState: CapabilitySupported,
			fieldState: CapabilitySupported, fillState: CapabilitySupported,
		},
		{
			goos: "windows", arch: "amd64", readState: CapabilitySupported,
			fieldState: CapabilitySupported, fillState: CapabilitySupported,
		},
	}
	for _, test := range tests {
		t.Run(test.goos+"-"+test.arch, func(t *testing.T) {
			report := capabilitiesFor(test.goos, test.arch)
			if report.Operations["acquire"].State != test.readState {
				t.Fatalf("acquire state = %q, want %q", report.Operations["acquire"].State, test.readState)
			}
			if report.Operations["inspect"].State != test.readState {
				t.Fatalf("inspect state = %q, want %q", report.Operations["inspect"].State, test.readState)
			}
			if report.Operations["extract"].State != test.readState ||
				report.Operations["render"].State != test.readState {
				t.Fatalf("read capabilities = %#v, want %q", report.Operations, test.readState)
			}
			if report.Operations["fields"].State != test.fieldState {
				t.Fatalf("fields capability = %#v, want %q", report.Operations["fields"], test.fieldState)
			}
			if test.fieldState == CapabilitySupported {
				fields := report.Operations["fields"]
				if fields.Mode != CapabilityModePortable || fields.Primary == nil ||
					*fields.Primary != pdfcpuIdentityFor(test.goos) {
					t.Fatalf("fields backend identity = %#v", fields)
				}
			}
			if report.Operations[operationFill].State != test.fillState ||
				report.Operations[operationVerifyFormWrite].State != test.fillState {
				t.Fatalf("form write capabilities = %#v, want %q", report.Operations, test.fillState)
			}
			if report.Limits.MaxInputBytes != DefaultMaxInputBytes {
				t.Fatalf("max input bytes = %d, want %d", report.Limits.MaxInputBytes, DefaultMaxInputBytes)
			}
			if report.Limits.MaxPages != DefaultMaxPages ||
				report.Limits.MaxContentBytes != DefaultMaxContentBytes ||
				report.Limits.MaxObjects != DefaultMaxObjects ||
				report.Limits.MaxRecursionDepth != DefaultMaxRecursionDepth {
				t.Fatalf("inspection limits = %#v", report.Limits)
			}
			if report.ReadLimits[operationExtract].MaxPages != DefaultMaxExtractPages ||
				report.ReadLimits[operationRender].MaxPages != DefaultMaxRenderPages {
				t.Fatalf("read limits = %#v", report.ReadLimits)
			}
			if report.FormLimits[operationFields] != defaultFormFieldLimits() {
				t.Fatalf("form limits = %#v", report.FormLimits)
			}
			if report.FormLimits[operationFill] != defaultFormFieldLimits() {
				t.Fatalf("form fill limits = %#v", report.FormLimits)
			}
		})
	}
}

func TestCapabilitiesWithholdUnverifiedFormFlattening(t *testing.T) {
	capability := capabilitiesFor("linux", "amd64").Operations["flatten"]
	if capability.State != CapabilityUnavailable || capability.Reason == "" {
		t.Fatalf("flatten capability = %#v", capability)
	}
}

func TestWindowsCapabilitiesUseHandleIsolationWithoutNativeBackends(t *testing.T) {
	report := capabilitiesFor("windows", "amd64")
	for _, operation := range []string{operationAcquire, operationInspect, operationFields, operationFill} {
		capability := report.Operations[operation]
		if capability.State != CapabilitySupported || capability.Primary == nil ||
			capability.Primary.IsolationMode != WindowsWorkerIsolationMode {
			t.Fatalf("Windows %s capability = %#v", operation, capability)
		}
	}
	if _, found := backendCapabilityByName(report.Backends, PopplerBackendName); found {
		t.Fatalf("Windows capabilities include native Poppler: %#v", report.Backends)
	}
	if _, found := backendCapabilityByName(report.Backends, GhostscriptBackendName); found {
		t.Fatalf("Windows capabilities include native Ghostscript: %#v", report.Backends)
	}
}
