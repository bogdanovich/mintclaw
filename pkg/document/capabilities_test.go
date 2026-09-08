package document

import "testing"

func TestCapabilitiesAdmitOnlyLinuxAMD64AcquisitionAndInspection(t *testing.T) {
	tests := []struct {
		goos  string
		arch  string
		state string
	}{
		{goos: "linux", arch: "amd64", state: CapabilitySupported},
		{goos: "linux", arch: "386", state: CapabilityUnavailable},
		{goos: "linux", arch: "arm", state: CapabilityUnavailable},
		{goos: "linux", arch: "arm64", state: CapabilityUnavailable},
		{goos: "darwin", arch: "amd64", state: CapabilityUnavailable},
		{goos: "darwin", arch: "arm64", state: CapabilityUnavailable},
		{goos: "windows", arch: "amd64", state: CapabilityUnavailable},
	}
	for _, test := range tests {
		t.Run(test.goos+"-"+test.arch, func(t *testing.T) {
			report := capabilitiesFor(test.goos, test.arch)
			if report.Operations["acquire"].State != test.state {
				t.Fatalf("acquire state = %q, want %q", report.Operations["acquire"].State, test.state)
			}
			if report.Operations["inspect"].State != test.state {
				t.Fatalf("inspect state = %q, want %q", report.Operations["inspect"].State, test.state)
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
		})
	}
}
