//go:build linux || darwin

package pdfiumwasm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
)

const (
	resourceHelperEnvironment = "MINTCLAW_PDFIUM_RESOURCE_HELPER"
	resourceEvidencePrefix    = "MINTCLAW_PDFIUM_RESOURCE_EVIDENCE="
	resourceGoMemoryLimit     = "384MiB"
	minimumCandidateRSS       = int64(32 * 1024 * 1024)
	maximumCandidateRSS       = int64(512 * 1024 * 1024)
	maximumColdDuration       = 20 * time.Second
	maximumRepeatedDuration   = 5 * time.Second
)

type resourceEvidence struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	GoMemoryLimit string `json:"go_memory_limit"`
	ColdMS        int64  `json:"cold_ms"`
	RepeatedMS    int64  `json:"repeated_ms"`
	PeakRSS       int64  `json:"peak_rss_bytes"`
}

func TestCandidateResourceEnvelope(t *testing.T) {
	first := runResourceHelper(t)
	second := runResourceHelper(t)
	for index, evidence := range []resourceEvidence{first, second} {
		if evidence.GOOS != runtime.GOOS || evidence.GOARCH != runtime.GOARCH {
			t.Fatalf("run %d platform = %s/%s", index+1, evidence.GOOS, evidence.GOARCH)
		}
		if evidence.GoMemoryLimit != resourceGoMemoryLimit {
			t.Fatalf("run %d Go memory limit = %q", index+1, evidence.GoMemoryLimit)
		}
		if evidence.ColdMS <= 0 || time.Duration(evidence.ColdMS)*time.Millisecond > maximumColdDuration {
			t.Fatalf("run %d cold duration = %dms", index+1, evidence.ColdMS)
		}
		if evidence.RepeatedMS <= 0 || time.Duration(evidence.RepeatedMS)*time.Millisecond > maximumRepeatedDuration {
			t.Fatalf("run %d repeated duration = %dms", index+1, evidence.RepeatedMS)
		}
		if evidence.PeakRSS < minimumCandidateRSS || evidence.PeakRSS > maximumCandidateRSS {
			t.Fatalf("run %d peak RSS = %d", index+1, evidence.PeakRSS)
		}
		t.Logf(
			"resource evidence run=%d platform=%s/%s cold=%dms repeated=%dms peak_rss=%d",
			index+1,
			evidence.GOOS,
			evidence.GOARCH,
			evidence.ColdMS,
			evidence.RepeatedMS,
			evidence.PeakRSS,
		)
	}
}

func TestCandidateResourceHelper(t *testing.T) {
	if os.Getenv(resourceHelperEnvironment) != "1" {
		t.Skip("resource helper runs only in a child process")
	}
	data := fixtureBytes(t, "text.pdf")
	started := time.Now()
	pool, err := newCandidatePool(t.Context())
	if err != nil {
		t.Fatalf("initialize candidate pool: %v", err)
	}
	instance, err := acquireCandidateInstance(t.Context(), pool)
	if err != nil {
		t.Fatalf("get candidate instance: %v", err)
	}
	measuredReadRender(t, instance, data)
	cold := time.Since(started)

	repeatedAt := time.Now()
	measuredReadRender(t, instance, data)
	repeated := time.Since(repeatedAt)
	if err = instance.Close(); err != nil {
		t.Fatalf("close candidate instance: %v", err)
	}
	if err = pool.Close(); err != nil {
		t.Fatalf("close candidate pool: %v", err)
	}

	evidence := resourceEvidence{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		GoMemoryLimit: os.Getenv("GOMEMLIMIT"),
		ColdMS:        cold.Milliseconds(),
		RepeatedMS:    repeated.Milliseconds(),
		PeakRSS:       maximumResidentBytes(t),
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode resource evidence: %v", err)
	}
	fmt.Println(resourceEvidencePrefix + string(encoded))
}

func measuredReadRender(t *testing.T, instance pdfium.Pdfium, data []byte) {
	t.Helper()
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("open resource fixture: %v", err)
	}
	if pageCount := candidatePageCount(t, instance, document.Document); pageCount != 1 {
		t.Fatalf("resource fixture page count = %d", pageCount)
	}
	if text := extractPageText(t, instance, document.Document, 0); !strings.Contains(text, "MintClaw text fixture") {
		t.Fatalf("resource fixture text = %q", text)
	}
	response, err := instance.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: requests.Page{ByIndex: &requests.PageByIndex{
			Document: document.Document,
			Index:    0,
		}},
		Width:  800,
		Height: 800,
	})
	if err != nil {
		t.Fatalf("render resource fixture: %v", err)
	}
	if response.Result.Width < 1 || response.Result.Height < 1 {
		t.Fatalf("resource render dimensions = %dx%d", response.Result.Width, response.Result.Height)
	}
	response.Cleanup()
	closeDocument(t, instance, document.Document)
}

func runResourceHelper(t *testing.T) resourceEvidence {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCandidateResourceHelper$", "-test.v")
	command.Env = append(
		os.Environ(),
		resourceHelperEnvironment+"=1",
		"GOMEMLIMIT="+resourceGoMemoryLimit,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resource helper: %v\n%s", err, output)
	}
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte(resourceEvidencePrefix)) {
			continue
		}
		var evidence resourceEvidence
		if err = json.Unmarshal(bytes.TrimPrefix(line, []byte(resourceEvidencePrefix)), &evidence); err != nil {
			t.Fatalf("decode resource evidence: %v", err)
		}
		return evidence
	}
	t.Fatalf("resource helper produced no evidence:\n%s", output)
	return resourceEvidence{}
}

func maximumResidentBytes(t *testing.T) int64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatalf("get process resource usage: %v", err)
	}
	if runtime.GOOS == "darwin" {
		return usage.Maxrss
	}
	return usage.Maxrss * 1024
}
