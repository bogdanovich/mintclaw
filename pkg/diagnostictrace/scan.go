package diagnostictrace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	DefaultScanMaxBytes int64 = 64 * 1024 * 1024
	HardScanMaxBytes    int64 = 256 * 1024 * 1024
	HardScanMaxFiles          = 500
	HardScanMaxEntries        = 2048
)

// ScanOptions bounds one read-only trace-store scan. Zero values select the
// store retention count and the default byte budget.
type ScanOptions struct {
	MaxFiles int
	MaxBytes int64
}

// StoredTraceProblem reports a content-free reason why one candidate could
// not be treated as a valid trace. NameHash is used when the filename itself
// is not a valid, safe trace identity.
type StoredTraceProblem struct {
	TraceID    string    `json:"trace_id,omitempty"`
	NameHash   string    `json:"name_hash,omitempty"`
	Code       string    `json:"code"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	ModifiedAt time.Time `json:"modified_at,omitempty"`
}

// ScanResult contains validated traces and bounded store-health facts. It
// never returns paths or raw decoder errors.
type ScanResult struct {
	Traces          []Trace              `json:"-"`
	Problems        []StoredTraceProblem `json:"problems,omitempty"`
	ExaminedEntries int                  `json:"examined_entries"`
	CandidateFiles  int                  `json:"candidate_files"`
	ScannedFiles    int                  `json:"scanned_files"`
	ScannedBytes    int64                `json:"scanned_bytes"`
	Truncated       bool                 `json:"truncated,omitempty"`
	Reasons         []string             `json:"reasons,omitempty"`
}

// ScanExact validates only the named trace files. Missing IDs are ignored so
// callers can safely query several stores without turning absence into a
// failure. The same file-count, byte, path, type, schema, and digest bounds as
// Scan apply.
func (s Store) ScanExact(traceIDs []string, options ScanOptions) (ScanResult, error) {
	if len(traceIDs) > HardScanMaxFiles {
		return ScanResult{}, fmt.Errorf("exact trace scan exceeds ID limit %d", HardScanMaxFiles)
	}
	root, rootErr := s.safeRoot()
	if rootErr != nil {
		return ScanResult{}, rootErr
	}
	if symlinkErr := rejectSymlinkPath(root); symlinkErr != nil {
		return ScanResult{}, symlinkErr
	}
	maximumFiles, maximumBytes := s.scanLimits(options)
	ids := append([]string(nil), traceIDs...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, traceID := range ids {
		if !safeIDPattern.MatchString(traceID) {
			return ScanResult{}, errors.New("exact trace scan contains an invalid trace ID")
		}
	}
	result := ScanResult{}
	if len(ids) > maximumFiles {
		ids = ids[:maximumFiles]
		result.Truncated = true
		result.Reasons = append(result.Reasons, "trace_count_limit")
	}
	for _, traceID := range ids {
		name := traceID + ".json"
		info, infoErr := os.Lstat(filepath.Join(root, name))
		if errors.Is(infoErr, os.ErrNotExist) {
			continue
		}
		if infoErr != nil {
			return ScanResult{}, infoErr
		}
		result.CandidateFiles++
		if !s.scanCandidate(name, info, maximumBytes, &result) {
			break
		}
	}
	sortScannedTraces(result.Traces)
	return result, nil
}

// Scan validates a bounded newest-first snapshot of one trace store. Missing
// stores are healthy empty stores. Rich record data is decoded only so the
// existing schema and digest validation can run; callers decide what safe
// projection, if any, is returned to an operator.
func (s Store) Scan(options ScanOptions) (ScanResult, error) {
	root, rootErr := s.safeRoot()
	if rootErr != nil {
		return ScanResult{}, rootErr
	}
	if symlinkErr := rejectSymlinkPath(root); symlinkErr != nil {
		return ScanResult{}, symlinkErr
	}
	directory, openErr := os.Open(root)
	if errors.Is(openErr, os.ErrNotExist) {
		return ScanResult{}, nil
	}
	if openErr != nil {
		return ScanResult{}, openErr
	}
	entries, readDirErr := directory.ReadDir(HardScanMaxEntries + 1)
	closeErr := directory.Close()
	if readDirErr != nil && !errors.Is(readDirErr, io.EOF) {
		return ScanResult{}, readDirErr
	}
	if closeErr != nil {
		return ScanResult{}, closeErr
	}
	directoryTruncated := len(entries) > HardScanMaxEntries
	entries = entries[:min(len(entries), HardScanMaxEntries)]

	type candidate struct {
		name string
		info os.FileInfo
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, infoErr := os.Lstat(filepath.Join(root, entry.Name()))
		if errors.Is(infoErr, os.ErrNotExist) {
			continue
		}
		if infoErr != nil {
			return ScanResult{}, infoErr
		}
		candidates = append(candidates, candidate{name: entry.Name(), info: info})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if compared := b.info.ModTime().Compare(a.info.ModTime()); compared != 0 {
			return compared
		}
		return strings.Compare(a.name, b.name)
	})

	maximumFiles, maximumBytes := s.scanLimits(options)

	result := ScanResult{ExaminedEntries: len(entries), CandidateFiles: len(candidates)}
	if directoryTruncated {
		result.Truncated = true
		result.Reasons = append(result.Reasons, "directory_entry_limit")
	}
	if len(candidates) > maximumFiles {
		candidates = candidates[:maximumFiles]
		result.Truncated = true
		result.Reasons = append(result.Reasons, "trace_count_limit")
	}
	for _, candidate := range candidates {
		if !s.scanCandidate(candidate.name, candidate.info, maximumBytes, &result) {
			break
		}
	}
	sortScannedTraces(result.Traces)
	return result, nil
}

func (s Store) scanLimits(options ScanOptions) (int, int64) {
	maximumFiles := options.MaxFiles
	if maximumFiles <= 0 {
		maximumFiles = s.MaxTraces
	}
	if maximumFiles <= 0 {
		maximumFiles = DefaultMaxTraces
	}
	maximumFiles = min(maximumFiles, HardScanMaxFiles)
	maximumBytes := options.MaxBytes
	if maximumBytes <= 0 {
		maximumBytes = DefaultScanMaxBytes
	}
	maximumBytes = min(maximumBytes, HardScanMaxBytes)
	return maximumFiles, maximumBytes
}

func (s Store) scanCandidate(
	name string,
	info os.FileInfo,
	maximumBytes int64,
	result *ScanResult,
) bool {
	size := max(int64(0), info.Size())
	if size > maximumBytes-result.ScannedBytes {
		result.Truncated = true
		result.Reasons = appendUniqueString(result.Reasons, "scan_byte_limit")
		return false
	}
	result.ScannedFiles++
	result.ScannedBytes += size

	traceID := strings.TrimSuffix(name, ".json")
	problem := StoredTraceProblem{SizeBytes: size, ModifiedAt: info.ModTime().UTC()}
	if !safeIDPattern.MatchString(traceID) {
		problem.NameHash = storedNameHash(name)
		problem.Code = "invalid_trace_filename"
		result.Problems = append(result.Problems, problem)
		return true
	}
	problem.TraceID = traceID
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		problem.Code = "symlink_rejected"
	case !info.Mode().IsRegular():
		problem.Code = "unsupported_file_type"
	case size > HardMaxTraceBytes:
		problem.Code = "trace_too_large"
	default:
		trace, loadErr := s.Load(traceID)
		if loadErr == nil {
			result.Traces = append(result.Traces, trace)
			return true
		}
		var corrupt *CorruptTraceError
		if errors.As(loadErr, &corrupt) {
			problem.Code = "corrupt_trace"
		} else {
			problem.Code = "trace_read_failed"
		}
	}
	result.Problems = append(result.Problems, problem)
	return true
}

func sortScannedTraces(traces []Trace) {
	slices.SortFunc(traces, func(a, b Trace) int {
		if compared := b.CreatedAt.Compare(a.CreatedAt); compared != 0 {
			return compared
		}
		return strings.Compare(a.TraceID, b.TraceID)
	})
}

func appendUniqueString(values []string, value string) []string {
	if !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func storedNameHash(name string) string {
	digest := sha256.Sum256([]byte(name))
	return hex.EncodeToString(digest[:])
}
