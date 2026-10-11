package diagnostictrace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreScanReportsValidatedTracesAndContentFreeProblems(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	store := Store{Root: root, MaxTraces: 10}
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"trace-first", "trace-second"} {
		trace := baseTrace(created.Add(time.Duration(index) * time.Minute))
		trace.TraceID = id
		trace, err := Finalize(trace)
		if err != nil {
			t.Fatalf("Finalize(%s): %v", id, err)
		}
		if _, err := store.Save(trace); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "trace-corrupt.json"), []byte("{private-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "_unsafe-name.json"), []byte("private-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(unsafe): %v", err)
	}

	result, err := store.Scan(ScanOptions{MaxFiles: 10, MaxBytes: 1024 * 1024})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result.Traces) != 2 || result.Traces[0].TraceID != "trace-second" ||
		result.Traces[1].TraceID != "trace-first" {
		t.Fatalf("traces = %#v", result.Traces)
	}
	if result.CandidateFiles != 4 || result.ScannedFiles != 4 || result.Truncated {
		t.Fatalf("scan counters = %#v", result)
	}
	if len(result.Problems) != 2 {
		t.Fatalf("problems = %#v", result.Problems)
	}
	problemByCode := make(map[string]StoredTraceProblem, len(result.Problems))
	for _, problem := range result.Problems {
		problemByCode[problem.Code] = problem
	}
	if problemByCode["corrupt_trace"].TraceID != "trace-corrupt" {
		t.Fatalf("corrupt problem = %#v", problemByCode["corrupt_trace"])
	}
	unsafe := problemByCode["invalid_trace_filename"]
	if unsafe.TraceID != "" || len(unsafe.NameHash) != 64 || strings.Contains(unsafe.NameHash, "unsafe") {
		t.Fatalf("unsafe-name problem = %#v", unsafe)
	}
}

func TestStoreScanIsBoundedAndMissingStoreIsEmpty(t *testing.T) {
	missing := Store{Root: filepath.Join(t.TempDir(), "missing")}
	result, err := missing.Scan(ScanOptions{})
	if err != nil || result.CandidateFiles != 0 || len(result.Traces) != 0 {
		t.Fatalf("missing Scan() = %#v, %v", result, err)
	}

	root := filepath.Join(t.TempDir(), "traces")
	store := Store{Root: root, MaxTraces: 10}
	for _, id := range []string{"trace-a", "trace-b"} {
		trace := baseTrace(time.Now().UTC())
		trace.TraceID = id
		trace, finalizeErr := Finalize(trace)
		if finalizeErr != nil {
			t.Fatalf("Finalize(%s): %v", id, finalizeErr)
		}
		if _, saveErr := store.Save(trace); saveErr != nil {
			t.Fatalf("Save(%s): %v", id, saveErr)
		}
	}

	countLimited, err := store.Scan(ScanOptions{MaxFiles: 1})
	if err != nil {
		t.Fatalf("Scan(count): %v", err)
	}
	if !countLimited.Truncated || !containsScanReason(countLimited.Reasons, "trace_count_limit") ||
		countLimited.ScannedFiles != 1 {
		t.Fatalf("count-limited result = %#v", countLimited)
	}

	byteLimited, err := store.Scan(ScanOptions{MaxFiles: 10, MaxBytes: 1})
	if err != nil {
		t.Fatalf("Scan(bytes): %v", err)
	}
	if !byteLimited.Truncated || !containsScanReason(byteLimited.Reasons, "scan_byte_limit") ||
		byteLimited.ScannedFiles != 0 {
		t.Fatalf("byte-limited result = %#v", byteLimited)
	}
}

func TestStoreScanExactReadsOnlySelectedTrace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	store := Store{Root: root, MaxTraces: 10}
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"trace-selected", "trace-unrelated"} {
		trace := baseTrace(created.Add(time.Duration(index) * time.Minute))
		trace.TraceID = id
		trace, err := Finalize(trace)
		if err != nil {
			t.Fatalf("Finalize(%s): %v", id, err)
		}
		if _, err := store.Save(trace); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "trace-corrupt.json"), []byte("{private"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}

	result, err := store.ScanExact([]string{"trace-selected", "trace-missing"}, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanExact: %v", err)
	}
	if result.CandidateFiles != 1 || result.ScannedFiles != 1 || len(result.Traces) != 1 ||
		result.Traces[0].TraceID != "trace-selected" || len(result.Problems) != 0 {
		t.Fatalf("exact result = %#v", result)
	}
}

func TestStoreScanBoundsDirectoryEnumeration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	for index := range HardScanMaxEntries + 1 {
		path := filepath.Join(root, fmt.Sprintf("ignored-%04d.tmp", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%d): %v", index, err)
		}
	}

	result, err := (Store{Root: root}).Scan(ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if result.ExaminedEntries != HardScanMaxEntries || !result.Truncated ||
		!containsScanReason(result.Reasons, "directory_entry_limit") {
		t.Fatalf("directory-bounded result = %#v", result)
	}
}

func TestStoreScanRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges on Windows")
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	symlinkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := (Store{Root: symlinkRoot}).Scan(ScanOptions{}); err == nil {
		t.Fatal("Scan() followed a symlink root")
	}
}

func containsScanReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
