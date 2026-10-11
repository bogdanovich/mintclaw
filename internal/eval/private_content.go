package eval

import (
	"encoding/json"
	"io"
	"strings"
)

// Scan decoded JSON tokens rather than maps, so duplicate object keys cannot
// discard a leaked value. Encoded strings can contain another JSON fragment.
// Escape-bearing content must be one complete JSON value. Opaque prose,
// trailing content and exhausted bounds refuse projection, not safety proof.
type privateContentScan struct {
	facts  map[string]string
	filter *strings.Replacer
	tokens int
}

func (scan *privateContentScan) text(text string, layer int) bool {
	if scan.filter != nil && scan.filter.Replace(text) != text {
		return true
	}
	for _, value := range scan.facts {
		if value == "" || strings.Contains(text, value) {
			return true
		}
	}
	if len(scan.facts) == 0 && scan.filter == nil || !strings.ContainsRune(text, '\\') {
		return false
	}
	if layer >= 8 || len(text) > MaxInputBytes {
		return true
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	matched, valid := scan.value(decoder, layer)
	if matched || !valid {
		return true
	}
	_, err := decoder.Token()
	return err != io.EOF
}

func (scan *privateContentScan) value(decoder *json.Decoder, layer int) (bool, bool) {
	depth := 0
	for {
		scan.tokens--
		if scan.tokens < 0 {
			return true, false
		}
		token, err := decoder.Token()
		if err != nil {
			return false, false
		}
		if text, ok := token.(string); ok && scan.text(text, layer+1) {
			return true, true
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
				if depth > 24 {
					return true, false
				}
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return false, true
		}
	}
}
