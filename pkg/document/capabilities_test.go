package document

import "testing"

func TestCapabilitiesAdmitPortableReadOperationsOnSupportedMacOS(t *testing.T) {
	tests := []struct {
		goos      string
		arch      string
		readState string
		formState string
	}{
		{goos: "linux", arch: "amd64", readState: CapabilitySupported, formState: CapabilitySupported},
		{goos: "linux", arch: "386", readState: CapabilityUnavailable, formState: CapabilityUnavailable},
		{goos: "linux", arch: "arm", readState: CapabilityUnavailable, formState: CapabilityUnavailable},
		{goos: "linux", arch: "arm64", readState: CapabilityUnavailable, formState: CapabilityUnavailable},
		{goos: "darwin", arch: "amd64", readState: CapabilitySupported, formState: CapabilityUnavailable},
		{goos: "darwin", arch: "arm64", readState: CapabilitySupported, formState: CapabilityUnavailable},
		{goos: "windows", arch: "amd64", readState: CapabilityUnavailable, formState: CapabilityUnavailable},
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
			if report.Operations["fields"].State != test.formState {
				t.Fatalf("fields capability = %#v, want %q", report.Operations["fields"], test.formState)
			}
			if report.Operations[operationFill].State != test.formState ||
				report.Operations[operationVerifyFormWrite].State != test.formState {
				t.Fatalf("form write capabilities = %#v, want %q", report.Operations, test.formState)
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
