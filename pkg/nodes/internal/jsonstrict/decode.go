package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
)

var (
	ErrDuplicateMember   = errors.New("duplicate JSON object member")
	ErrCanonicalTooLarge = errors.New("canonical JSON exceeds size limit")
)

var numberPattern = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

const (
	maxCanonicalSignificantDigits = 4096
	maxCanonicalExponentMagnitude = 1_000_000
)

// Decode preserves JSON numbers and rejects duplicate object members at every
// nesting level. Duplicate rejection avoids parser-dependent first/last-wins
// behavior in signed, hashed, or routed protocol data.
func Decode(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("read trailing JSON data: %w", err)
	}
	return value, nil
}

// Canonical returns the protocol-v1 deterministic JSON representation.
// Protocol-v2 callers use CanonicalV2 during the coordinated number-format
// cutover.
func Canonical(data []byte) ([]byte, error) {
	return canonical(data, normalizeNumberV1)
}

// CanonicalV2 returns deterministic JSON in which mathematical integers use
// plain base-10 syntax and fractional values use bounded normalized notation.
func CanonicalV2(data []byte) ([]byte, error) {
	return canonical(data, normalizeNumberV2)
}

// CanonicalV2Bounded limits both the accepted input and accumulated normalized
// number text before the final encoding allocation.
func CanonicalV2Bounded(data []byte, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 || len(data) > maxBytes {
		return nil, ErrCanonicalTooLarge
	}
	return canonicalBounded(data, normalizeNumberV2, maxBytes)
}

func canonical(data []byte, normalize func(json.Number) (json.Number, error)) ([]byte, error) {
	return canonicalBounded(data, normalize, 0)
}

func canonicalBounded(
	data []byte,
	normalize func(json.Number) (json.Number, error),
	maxBytes int,
) ([]byte, error) {
	value, err := Decode(data)
	if err != nil {
		return nil, err
	}
	var remaining *int
	if maxBytes > 0 {
		budget := maxBytes
		remaining = &budget
	}
	value, err = normalizeNumbersBounded(value, normalize, remaining)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err == nil && remaining != nil && len(encoded) > maxBytes {
		return nil, ErrCanonicalTooLarge
	}
	return encoded, err
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		return decodeObject(decoder)
	case '[':
		return decodeArray(decoder)
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func decodeObject(decoder *json.Decoder) (map[string]any, error) {
	object := make(map[string]any)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("JSON object member name is not a string")
		}
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateMember, key)
		}
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return object, nil
}

func decodeArray(decoder *json.Decoder) ([]any, error) {
	values := make([]any, 0)
	for decoder.More() {
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return values, nil
}

func normalizeNumbersBounded(
	value any,
	normalize func(json.Number) (json.Number, error),
	remaining *int,
) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		normalized, err := normalize(typed)
		if err != nil {
			return nil, err
		}
		if remaining != nil {
			if len(normalized.String()) > *remaining {
				return nil, ErrCanonicalTooLarge
			}
			*remaining -= len(normalized.String())
		}
		return normalized, nil
	case map[string]any:
		for key, child := range typed {
			normalized, err := normalizeNumbersBounded(child, normalize, remaining)
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
	case []any:
		for index, child := range typed {
			normalized, err := normalizeNumbersBounded(child, normalize, remaining)
			if err != nil {
				return nil, err
			}
			typed[index] = normalized
		}
	}
	return value, nil
}

func normalizeNumberV1(number json.Number) (json.Number, error) {
	parts := numberPattern.FindStringSubmatch(number.String())
	if parts == nil {
		return "", fmt.Errorf("invalid JSON number %q", number)
	}
	digits := strings.TrimLeft(parts[2]+parts[3], "0")
	if digits == "" {
		return json.Number("0"), nil
	}
	trailingZeros := len(digits) - len(strings.TrimRight(digits, "0"))
	digits = strings.TrimRight(digits, "0")

	exponent := big.NewInt(int64(-len(parts[3]) + trailingZeros))
	if parts[4] != "" {
		parsedExponent, ok := new(big.Int).SetString(parts[4], 10)
		if !ok {
			return "", fmt.Errorf("invalid JSON number exponent %q", parts[4])
		}
		exponent.Add(exponent, parsedExponent)
	}
	exponent.Add(exponent, big.NewInt(int64(len(digits)-1)))

	mantissa := digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	if exponent.Sign() != 0 {
		mantissa += "e" + exponent.String()
	}
	return json.Number(parts[1] + mantissa), nil
}

func normalizeNumberV2(number json.Number) (json.Number, error) {
	parts := numberPattern.FindStringSubmatch(number.String())
	if parts == nil {
		return "", fmt.Errorf("invalid JSON number %q", number)
	}
	digits := strings.TrimLeft(parts[2]+parts[3], "0")
	if digits == "" {
		return json.Number("0"), nil
	}
	trailingZeros := len(digits) - len(strings.TrimRight(digits, "0"))
	digits = strings.TrimRight(digits, "0")
	if len(digits) > maxCanonicalSignificantDigits {
		return "", fmt.Errorf("JSON number %q has too many significant digits", number)
	}
	scale := big.NewInt(int64(-len(parts[3]) + trailingZeros))
	if parts[4] != "" {
		parsedExponent, ok := new(big.Int).SetString(parts[4], 10)
		if !ok {
			return "", fmt.Errorf("invalid JSON number exponent %q", parts[4])
		}
		scale.Add(scale, parsedExponent)
	}

	if scale.Sign() >= 0 {
		remainingDigits := big.NewInt(int64(maxCanonicalSignificantDigits - len(digits)))
		if scale.Cmp(remainingDigits) > 0 {
			return "", fmt.Errorf("integer JSON number %q is outside bounds", number)
		}
		return json.Number(parts[1] + digits + strings.Repeat("0", int(scale.Int64()))), nil
	}

	exponent := new(big.Int).Add(scale, big.NewInt(int64(len(digits)-1)))
	if !canonicalExponentInRange(exponent) {
		return "", fmt.Errorf("fractional JSON number %q is outside bounds", number)
	}
	mantissa := digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	if exponent.Sign() != 0 {
		mantissa += "e" + exponent.String()
	}
	return json.Number(parts[1] + mantissa), nil
}

func canonicalExponentInRange(exponent *big.Int) bool {
	if exponent == nil {
		return false
	}
	limit := big.NewInt(maxCanonicalExponentMagnitude)
	return new(big.Int).Abs(new(big.Int).Set(exponent)).Cmp(limit) <= 0
}
