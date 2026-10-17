package eval

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	maxPrivateFactsBytes    = 32 * 1024 * 1024
	maxPrivateScanBytes     = 128 * 1024 * 1024
	maxPrivateScanFileBytes = 8 * 1024 * 1024
	maxPrivateScanEntries   = 8192
)

// This receipt describes files, not transport coverage, domain settlement or
// cleanup. Authorized credential/key roots are excluded; no paths or values are
// exported. An opaque surface cannot establish absence of encoded private data.
type PrivateSurfaceObservation struct {
	Scope               string `json:"scope"`
	Code                string `json:"code"`
	Files               int    `json:"files"`
	Bytes               int64  `json:"bytes"`
	ExcludedDirectories int    `json:"excluded_directories"`
	OpaqueFiles         int    `json:"opaque_files"`
	AuthorizedDocuments int    `json:"authorized_documents"`
	LeakObserved        bool   `json:"leak_observed"`
}

func (observation *PrivateSurfaceObservation) valid() bool {
	if observation == nil {
		return true
	}
	return observation.Scope == "owned_profile_files_v1" &&
		observation.Files >= 0 && observation.Files <= maxPrivateScanEntries &&
		observation.Bytes >= 0 && observation.Bytes <= maxPrivateScanBytes &&
		observation.OpaqueFiles >= 0 && observation.OpaqueFiles <= observation.Files &&
		observation.AuthorizedDocuments >= 0 && observation.AuthorizedDocuments <= observation.Files &&
		observation.OpaqueFiles+observation.AuthorizedDocuments <= observation.Files &&
		observation.ExcludedDirectories >= 0 && observation.ExcludedDirectories <= 2 &&
		(observation.Code == "leak_observed" && observation.LeakObserved ||
			!observation.LeakObserved && (observation.Code == "unavailable" ||
				observation.Code == "observed" && observation.OpaqueFiles == 0))
}

func (observation *PrivateSurfaceObservation) verdict() Verdict {
	if observation == nil || observation.Code == "observed" {
		return Pass
	}
	if observation.LeakObserved {
		return Fail
	}
	return Inconclusive
}

// A run owns this ephemeral set until its target is stopped and raw surfaces
// have been inspected. Capturing it never publishes facts or expands authority.
type privateFactRetention struct {
	values    map[string]string
	documents map[string]bool
	bytes     int
}

func (retained *privateFactRetention) add(facts map[string]string) bool {
	additional := 0
	newValues := make(map[string]string)
	for _, value := range facts {
		if value == "" || retained.values[value] != "" || newValues[value] != "" {
			continue
		}
		newValues[value] = value
		additional += len(value)
	}
	if additional > maxPrivateFactsBytes-retained.bytes {
		return false
	}
	if retained.values == nil {
		retained.values = make(map[string]string)
	}
	for value := range newValues {
		retained.values[value] = value
	}
	retained.bytes += additional
	return true
}

func (retained *privateFactRetention) clear() {
	clear(retained.values)
	retained.values = nil
	clear(retained.documents)
	retained.documents = nil
	retained.bytes = 0
}

func observePrivateSurfaces(
	ctx context.Context, home string, facts map[string]string, filter *strings.Replacer, documents map[string]bool,
) PrivateSurfaceObservation {
	result := PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "unavailable"}
	if ctx == nil || ctx.Err() != nil {
		return result
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return result
	}
	defer func() { _ = root.Close() }()
	scan := privateContentScan{facts: facts, filter: filter, tokens: 1000000}
	complete, entries := true, 0
	pending := []string{"."}
	for len(pending) != 0 && entries < maxPrivateScanEntries {
		if ctx.Err() != nil {
			complete = false
			break
		}
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		directory, openErr := root.Open(name)
		if openErr != nil {
			complete = false
			continue
		}
		children, readErr := directory.ReadDir(maxPrivateScanEntries - entries + 1)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil ||
			len(children) > maxPrivateScanEntries-entries {
			complete = false
			break
		}
		for _, child := range children {
			entries++
			if ctx.Err() != nil {
				complete = false
				break
			}
			fileName := path.Join(name, child.Name())
			scan.uncertain = false
			if scan.text(fileName, 0) {
				if scan.uncertain {
					complete = false
				} else {
					result.LeakObserved = true
				}
			}
			info, statErr := root.Lstat(fileName)
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
				complete = false
				continue
			}
			if info.IsDir() {
				if fileName == ".security" || fileName == "keys" {
					result.ExcludedDirectories++
					continue
				}
				pending = append(pending, fileName)
				continue
			}
			if !info.Mode().IsRegular() {
				complete = false
				continue
			}
			data, fileErr := readBoundedFile(root, fileName,
				min(maxPrivateScanFileBytes, int64(maxPrivateScanBytes)-result.Bytes))
			if fileErr != nil {
				complete = false
				continue
			}
			result.Files++
			result.Bytes += int64(len(data))
			if filter != nil && filter.Replace(string(data)) != string(data) {
				result.LeakObserved = true
			} else if bytes.HasPrefix(data, []byte("%PDF-")) && documents[digest(data)] {
				result.AuthorizedDocuments++
			} else if containsRetainedLiteral(data, facts) {
				result.LeakObserved = true
			} else if bytes.HasPrefix(data, []byte("%PDF-")) {
				result.OpaqueFiles++
			} else if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				result.OpaqueFiles++
			} else {
				scan.uncertain = false
				if scan.text(string(data), 0) {
					if scan.uncertain {
						result.OpaqueFiles++
					} else {
						result.LeakObserved = true
					}
				}
			}
			clear(data)
		}
	}
	if result.LeakObserved {
		result.Code = "leak_observed"
	} else if complete && len(pending) == 0 && result.OpaqueFiles == 0 {
		result.Code = "observed"
	}
	return result
}

func containsRetainedLiteral(data []byte, facts map[string]string) bool {
	for _, value := range facts {
		if value != "" && bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	return false
}
