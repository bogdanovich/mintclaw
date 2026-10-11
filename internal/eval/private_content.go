package eval

import (
	"encoding/json"
	"strings"
)

// Scan decoded JSON tokens rather than maps, so duplicate object keys cannot
// discard a leaked value. Encoded strings can contain another JSON fragment.
// Exhaustion or malformed escaped content refuses projection, not safety proof.
type privateContentScan struct {
	facts      map[string]string
	tokens     int
	candidates int
}

func (scan *privateContentScan) text(text string, layer int) bool {
	for _, value := range scan.facts {
		if value == "" || strings.Contains(text, value) {
			return true
		}
	}
	if len(scan.facts) == 0 || !strings.ContainsRune(text, '\\') {
		return false
	}
	if layer >= 8 || len(text) > MaxInputBytes {
		return true
	}
	// Opaque prose can itself contain JSON string escapes without outer quotes.
	var decoded string
	if json.Unmarshal([]byte(`"`+text+`"`), &decoded) == nil && decoded != text && scan.text(decoded, layer+1) {
		return true
	}
	for index := 0; index < len(text); index++ {
		if text[index] != '{' && text[index] != '[' && text[index] != '"' {
			continue
		}
		scan.candidates--
		if scan.candidates < 0 {
			return true
		}
		decoder := json.NewDecoder(strings.NewReader(text[index:]))
		matched, valid := scan.value(decoder, layer)
		if matched || !valid {
			return true
		}
		index += int(decoder.InputOffset()) - 1
	}
	return false
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
