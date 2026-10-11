package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

// DecodeManifest validates exact JSON member names/types with the repository's
// existing schema library; token parsing also rejects nested duplicate keys.
func DecodeManifest[T any](data []byte) (T, error) {
	var result T
	if len(data) == 0 || len(data) > MaxManifestBytes || !utf8.Valid(data) {
		return result, ErrInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return result, ErrInvalidJSON
	}
	if _, trailingErr := decoder.Token(); !errors.Is(trailingErr, io.EOF) {
		return result, ErrInvalidJSON
	}
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		return result, ErrInvalidJSON
	}
	resolved, err := schema.Resolve(nil)
	if err != nil || resolved.Validate(value) != nil {
		return result, ErrInvalidJSON
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return result, ErrInvalidJSON
	}
	return result, nil
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 24 {
		return nil, ErrInvalidJSON
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return nil, ErrInvalidJSON
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, ErrInvalidJSON
			}
			if _, duplicate := object[key]; duplicate {
				return nil, ErrInvalidJSON
			}
			child, childErr := decodeValue(decoder, depth+1)
			if childErr != nil {
				return nil, childErr
			}
			object[key] = child
		}
		if closing, closingErr := decoder.Token(); closingErr != nil || closing != json.Delim('}') {
			return nil, ErrInvalidJSON
		}
		return object, nil
	case '[':
		values := make([]any, 0)
		for decoder.More() {
			child, childErr := decodeValue(decoder, depth+1)
			if childErr != nil {
				return nil, childErr
			}
			values = append(values, child)
		}
		if closing, closingErr := decoder.Token(); closingErr != nil || closing != json.Delim(']') {
			return nil, ErrInvalidJSON
		}
		return values, nil
	default:
		return nil, ErrInvalidJSON
	}
}
