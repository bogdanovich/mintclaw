package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
)

// This is a known-encoding canary guard, not a binary-format decoder. It scans
// contiguous raw bytes, including deleted SQLite payloads, without requiring a
// surviving JSON frame. It never relaxes the model-context scanner.
func (scan *privateContentScan) rawEscapes(ctx context.Context, data []byte) bool {
	scan.uncertain = false
	if ctx == nil || len(data) > maxPrivateScanFileBytes {
		scan.uncertain = true
		return true
	}
	current, owned := data, false
	defer func() {
		if owned {
			clear(current)
		}
	}()
	for layer := 0; layer <= 8; layer++ {
		if ctx.Err() != nil {
			scan.uncertain = true
			return true
		}
		if scan.filter != nil && scan.filter.Replace(string(current)) != string(current) {
			return true
		}
		for _, value := range scan.facts {
			if ctx.Err() != nil {
				scan.uncertain = true
				return true
			}
			if value != "" && bytes.Contains(current, []byte(value)) {
				return true
			}
		}
		next, changed, complete := scan.unescapeRawLayer(ctx, current)
		if !complete || changed && layer == 8 {
			clear(next)
			scan.uncertain = true
			return true
		}
		if !changed {
			return false
		}
		if owned {
			clear(current)
		}
		current, owned = next, true
	}
	return false
}

func (scan *privateContentScan) unescapeRawLayer(ctx context.Context, data []byte) ([]byte, bool, bool) {
	var result []byte
	changed := false
	for index := 0; index < len(data); {
		if index%4096 == 0 && ctx.Err() != nil {
			clear(result)
			return nil, false, false
		}
		if rawJSONEscapeWidth(data[index:]) == 0 {
			if changed {
				result = append(result, data[index])
			}
			index++
			continue
		}
		end := index
		for end < len(data) {
			width := rawJSONEscapeWidth(data[end:])
			if width == 0 {
				break
			}
			scan.tokens--
			end += width
			if scan.tokens < 0 || end-index > MaxInputBytes || ctx.Err() != nil {
				clear(result)
				return nil, false, false
			}
		}
		token := make([]byte, end-index+2)
		token[0], token[len(token)-1] = '"', '"'
		copy(token[1:], data[index:end])
		var decoded string
		err := json.Unmarshal(token, &decoded)
		clear(token)
		if err != nil {
			clear(result)
			return nil, false, false
		}
		if !changed {
			result = make([]byte, 0, len(data))
			result = append(result, data[:index]...)
		}
		result = append(result, decoded...)
		changed, index = true, end
	}
	return result, changed, true
}

func rawJSONEscapeWidth(data []byte) int {
	if len(data) < 2 || data[0] != '\\' {
		return 0
	}
	switch data[1] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return 2
	case 'u':
		if len(data) >= 6 {
			if _, err := strconv.ParseUint(string(data[2:6]), 16, 16); err == nil {
				return 6
			}
		}
	}
	return 0
}
