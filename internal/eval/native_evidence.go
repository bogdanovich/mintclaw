package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

var errNativeEvidence = errors.New("eval: native evidence unavailable or invalid")

// Inspect raw bytes before projection can discard duplicate keys or excluded
// events. Incomplete inspection is uncertainty, not proof of a private leak.
func inspectNativeJSON(data []byte, facts map[string]string, filter *strings.Replacer) error {
	scan := privateContentScan{facts: facts, filter: filter, tokens: 32768}
	decoder := json.NewDecoder(bytes.NewReader(data))
	matched, complete := scan.value(decoder, 0)
	if matched && !scan.uncertain {
		return ErrPrivateEvidence
	}
	if matched || !complete || scan.uncertain {
		return errNativeEvidence
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errNativeEvidence
	}
	return nil
}

func readNativeEvidenceFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errNativeEvidence
	}
	data, err := readBoundedFile(root, name, maximum)
	if err != nil {
		return nil, errNativeEvidence
	}
	return data, nil
}
