//go:build linux

package pdfiumwasm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

const (
	rssMeasurementHelperEnvironment = "MINTCLAW_PDFIUM_RSS_MEASUREMENT_HELPER"
	rssMeasurementEvidencePrefix    = "MINTCLAW_PDFIUM_RSS_MEASUREMENT="
	launcherAllocationBytes         = 64 * 1024 * 1024
)

type rssMeasurementEvidence struct {
	PeakRSS          int64 `json:"peak_rss_bytes"`
	InheritedPeakRSS int64 `json:"getrusage_peak_rss_bytes"`
}

func TestLinuxResourcePeakIgnoresLauncherRSS(t *testing.T) {
	allocation := make([]byte, launcherAllocationBytes)
	for index := 0; index < len(allocation); index += os.Getpagesize() {
		allocation[index] = 1
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLinuxResourcePeakHelper$", "-test.v")
	command.Env = append(os.Environ(), rssMeasurementHelperEnvironment+"=1")
	output, err := command.CombinedOutput()
	runtime.KeepAlive(allocation)
	if err != nil {
		t.Fatalf("RSS measurement helper: %v\n%s", err, output)
	}
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte(rssMeasurementEvidencePrefix)) {
			continue
		}
		var evidence rssMeasurementEvidence
		if err = json.Unmarshal(bytes.TrimPrefix(line, []byte(rssMeasurementEvidencePrefix)), &evidence); err != nil {
			t.Fatalf("decode RSS measurement evidence: %v", err)
		}
		if evidence.PeakRSS <= 0 || evidence.PeakRSS >= launcherAllocationBytes {
			t.Fatalf("small helper inherited launcher RSS: %+v", evidence)
		}
		t.Logf("small helper peak_rss=%d getrusage_peak_rss=%d", evidence.PeakRSS, evidence.InheritedPeakRSS)
		return
	}
	t.Fatalf("RSS measurement helper produced no evidence:\n%s", output)
}

func TestLinuxResourcePeakHelper(t *testing.T) {
	if os.Getenv(rssMeasurementHelperEnvironment) != "1" {
		t.Skip("RSS measurement helper runs only in a child process")
	}
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatalf("get inherited resource usage: %v", err)
	}
	evidence := rssMeasurementEvidence{
		PeakRSS: maximumResidentBytes(t), InheritedPeakRSS: usage.Maxrss * 1024,
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode RSS measurement evidence: %v", err)
	}
	fmt.Println(rssMeasurementEvidencePrefix + string(encoded))
}
