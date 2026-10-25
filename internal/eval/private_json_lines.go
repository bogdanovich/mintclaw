package eval

import (
	"bytes"
	"encoding/json"
	"io"
	"path"
)

const maxPrivateJSONRecords = 8192

// A bounded storage document can exceed the model-context string ceiling.
// Decode its outer container directly; nested escaped strings retain their
// existing byte/depth limits and share the file census token budget.
func (scan *privateContentScan) jsonDocument(data []byte) bool {
	if scan.filter != nil && scan.filter.Replace(string(data)) != string(data) ||
		containsRetainedLiteral(data, scan.facts) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	matched, valid := scan.value(decoder, 0)
	if matched && !scan.uncertain {
		return true
	}
	if matched || !valid || scan.uncertain {
		scan.uncertain = true
		return true
	}
	if _, err := decoder.Token(); err != io.EOF {
		scan.uncertain = true
		return true
	}
	return false
}

// Native logs and histories contain one JSON value per line, not one value per
// file. Keep this format-specific admission out of the model-context scanner.
func (scan *privateContentScan) jsonLines(data []byte) bool {
	if scan.filter != nil && scan.filter.Replace(string(data)) != string(data) ||
		containsRetainedLiteral(data, scan.facts) {
		return true
	}
	uncertain, records := false, 0
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		records++
		if records > maxPrivateJSONRecords || scan.tokens <= 0 {
			scan.uncertain = true
			return true
		}
		// Empty lines also consume budget; a newline-only file is not free work.
		scan.tokens--
		if len(line) > MaxInputBytes {
			uncertain = true
			continue
		}
		line = bytes.Trim(line, " \t\r")
		if len(line) == 0 {
			continue
		}
		scan.uncertain = false
		decoder := json.NewDecoder(bytes.NewReader(line))
		matched, valid := scan.value(decoder, 0)
		if matched && !scan.uncertain {
			return true
		}
		if matched || !valid || scan.uncertain {
			uncertain = true
			continue
		}
		if _, err := decoder.Token(); err != io.EOF {
			uncertain = true
		}
	}
	scan.uncertain = uncertain
	return uncertain
}

func privateJSONLinesFile(name string) bool {
	// Extensions select a strict parser, never an exemption from raw scanning.
	extension := path.Ext(name)
	return extension == ".jsonl" || extension == ".ndjson" || name == "logs/gateway.log"
}
