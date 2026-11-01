package browser

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const referenceRegionMetadataBytes = 4096

type referenceRegionEntry struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type referenceRegionMetadata struct {
	Version  int                    `json:"version"`
	Complete *bool                  `json:"complete"`
	Entries  []referenceRegionEntry `json:"entries"`
}

// Decode the driver's structured projection, not the indentation/captions of
// untrusted snapshot prose. Empty/legacy/truncated evidence grants no context.
func applyReferenceRegions(header string, refs [][]string, elements []DriverElement, truncated bool) error {
	const prefix = "- Reference regions: "
	var raw string
	for _, line := range strings.Split(header, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if raw != "" {
			return ErrDriverIncompatible
		}
		raw = strings.TrimPrefix(line, prefix)
		if raw == "" {
			return ErrDriverIncompatible
		}
	}
	if raw == "" {
		return nil
	}
	if len(raw) > referenceRegionMetadataBytes ||
		!utf8.ValidString(raw) ||
		extractPlaywrightLine(header, "- Snapshot encoding: ") != "json-names-v1" ||
		rejectDuplicateJSONMembers([]byte(raw)) != nil ||
		!exactRegionJSONKeys([]byte(raw), "version", "complete", "entries") {
		return ErrDriverIncompatible
	}
	var metadata referenceRegionMetadata
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil ||
		ensureJSONEOF(decoder) != nil || metadata.Version != 1 ||
		metadata.Complete == nil || metadata.Entries == nil ||
		len(metadata.Entries) > 32 ||
		!*metadata.Complete && len(metadata.Entries) != 0 {
		return ErrDriverIncompatible
	}
	var shape struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if json.Unmarshal([]byte(raw), &shape) != nil {
		return ErrDriverIncompatible
	}
	counts := make(map[string]int, len(refs))
	for _, ref := range refs {
		counts[ref[1]]++
	}
	regions := make(map[string]string, len(metadata.Entries))
	for index, entry := range metadata.Entries {
		if !exactRegionJSONKeys(shape.Entries[index], "ref", "name") ||
			!playwrightTargetPattern.MatchString(entry.Ref) ||
			strings.HasPrefix(entry.Ref, "f") ||
			counts[entry.Ref] != 1 ||
			regions[entry.Ref] != "" ||
			strings.TrimSpace(entry.Name) == "" ||
			len(entry.Name) > MaxElementNameBytes {
			return ErrDriverIncompatible
		}
		regions[entry.Ref] = entry.Name
	}
	if truncated || !*metadata.Complete {
		return nil
	}
	for index := range elements {
		if name := regions[elements[index].Target]; name != "" {
			if elements[index].Role != "button" {
				return ErrDriverIncompatible
			}
			elements[index].RegionName = name
		}
	}
	return nil
}

func exactRegionJSONKeys(data []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, found := fields[key]; !found {
			return false
		}
	}
	return true
}
