package document

import "testing"

func TestCapabilitiesAdmitOnlyLinuxAMD64Acquisition(t *testing.T) {
	tests := []struct {
		goos  string
		arch  string
		state string
	}{
		{goos: "linux", arch: "amd64", state: CapabilitySupported},
		{goos: "linux", arch: "arm64", state: CapabilityUnavailable},
		{goos: "darwin", arch: "arm64", state: CapabilityUnavailable},
		{goos: "windows", arch: "amd64", state: CapabilityUnavailable},
	}
	for _, test := range tests {
		t.Run(test.goos+"-"+test.arch, func(t *testing.T) {
			report := capabilitiesFor(test.goos, test.arch)
			if report.Operations["acquire"].State != test.state {
				t.Fatalf("acquire state = %q, want %q", report.Operations["acquire"].State, test.state)
			}
			if report.Operations["inspect"].State != CapabilityUnavailable {
				t.Fatalf("inspect must remain unavailable before the isolated worker lands")
			}
			if report.Limits.MaxInputBytes != DefaultMaxInputBytes {
				t.Fatalf("max input bytes = %d, want %d", report.Limits.MaxInputBytes, DefaultMaxInputBytes)
			}
		})
	}
}
