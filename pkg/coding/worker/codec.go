package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Encode validates a record before crossing the worker wire boundary. The
// producer-side text walk must happen before json.Marshal, which otherwise
// replaces malformed UTF-8 in Go strings with U+FFFD.
func Encode(record Record) ([]byte, error) {
	if err := validateProducerText(record); err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode coding worker record: %w", err)
	}
	if len(data) > MaxRecordBytes {
		return nil, ErrRecordTooLarge
	}
	if err := requireValidJSONText(data); err != nil {
		return nil, fmt.Errorf("%w: encoded record: %w", ErrInvalidRecord, err)
	}
	return data, nil
}

// Decode validates raw text before encoding/json can normalize malformed
// UTF-8, then applies the exact v1 envelope and payload schemas.
func Decode(data []byte) (Record, error) {
	if len(data) > MaxRecordBytes {
		return Record{}, ErrRecordTooLarge
	}
	var record Record
	if err := decodeStrict(data, &record); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// DecodePayload applies the v1 closed-world field policy to one params or
// result object.
func DecodePayload(raw json.RawMessage, destination any) error {
	if destination == nil {
		return fmt.Errorf("%w: payload destination is required", ErrInvalidRecord)
	}
	if len(raw) > MaxRecordBytes {
		return ErrRecordTooLarge
	}
	if err := validateJSONObject("payload", raw); err != nil {
		return err
	}
	if err := decodeStrict(raw, destination); err != nil {
		return fmt.Errorf("%w: decode payload: %w", ErrInvalidRecord, err)
	}
	return nil
}

// MarshalPayload rejects malformed producer text before encoding/json can
// collapse distinct byte sequences into the same replacement character.
func MarshalPayload(value any) (json.RawMessage, error) {
	if err := validateProducerText(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal coding worker payload: %w", err)
	}
	if len(data) > MaxRecordBytes {
		return nil, ErrRecordTooLarge
	}
	if err := validateJSONObject("payload", data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateProtocolText(raw json.RawMessage) error {
	return validateProtocolTextWith(raw, containsTerminalControl)
}

func validateProtocolTextWith(raw json.RawMessage, containsUnsafe func(string) bool) error {
	if err := requireValidJSONText(raw); err != nil {
		return fmt.Errorf("%w: malformed protocol payload: %w", ErrInvalidRecord, err)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: malformed protocol payload: %w", ErrInvalidRecord, err)
	}
	if err := requireDecoderEOF(decoder); err != nil {
		return fmt.Errorf("%w: malformed protocol payload: %w", ErrInvalidRecord, err)
	}
	var inspect func(any) error
	inspect = func(current any) error {
		switch typed := current.(type) {
		case string:
			if !utf8.ValidString(typed) || containsUnsafe(typed) {
				return fmt.Errorf("%w: protocol payload contains unsafe or oversized text", ErrInvalidRecord)
			}
		case []any:
			for _, entry := range typed {
				if err := inspect(entry); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, entry := range typed {
				if !utf8.ValidString(key) || containsUnsafe(key) {
					return fmt.Errorf("%w: protocol payload contains unsafe or oversized text", ErrInvalidRecord)
				}
				if err := inspect(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}

func containsTerminalControl(value string) bool {
	return strings.ContainsFunc(value, func(character rune) bool {
		if character == '\n' || character == '\r' || character == '\t' {
			return false
		}
		return character == '\x1b' || unicode.IsControl(character)
	})
}

func containsStructuralControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

func validateStructuredText(value any) error {
	if err := validateProducerText(value); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode protocol value for text validation: %w", ErrInvalidRecord, err)
	}
	return validateProtocolTextWith(raw, containsStructuralControl)
}

func validateJSONObject(label string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: missing %s", ErrInvalidRecord, label)
	}
	var object map[string]json.RawMessage
	if err := decodeStrict(raw, &object); err != nil {
		return fmt.Errorf("%w: malformed %s: %w", ErrInvalidRecord, label, err)
	}
	if object == nil {
		return fmt.Errorf("%w: %s must be an object", ErrInvalidRecord, label)
	}
	return nil
}

func decodeStrict(data []byte, destination any) error {
	if err := requireValidJSONText(data); err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("empty JSON")
	}
	if err := validateJSONMembers(data, reflect.TypeOf(destination)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireDecoderEOF(decoder)
}

func requireValidJSONText(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("malformed UTF-8")
	}
	if err := rejectUnpairedSurrogateEscapes(data); err != nil {
		return err
	}
	return nil
}

func rejectUnpairedSurrogateEscapes(data []byte) error {
	insideString := false
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '"':
			insideString = !insideString
		case '\\':
			if !insideString || index+1 >= len(data) {
				continue
			}
			index++
			if data[index] != 'u' || index+4 >= len(data) {
				continue
			}
			code, ok := decodeHexQuad(data[index+1 : index+5])
			if !ok {
				continue
			}
			index += 4
			switch {
			case code >= 0xdc00 && code <= 0xdfff:
				return errors.New("unpaired low-surrogate escape")
			case code >= 0xd800 && code <= 0xdbff:
				if index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
					return errors.New("unpaired high-surrogate escape")
				}
				low, valid := decodeHexQuad(data[index+3 : index+7])
				if !valid || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired high-surrogate escape")
				}
				index += 6
			}
		}
	}
	return nil
}

func decodeHexQuad(data []byte) (uint16, bool) {
	if len(data) != 4 {
		return 0, false
	}
	var value uint16
	for _, character := range data {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value += uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func requireDecoderEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

type producerVisit struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

func validateProducerText(value any) error {
	return inspectProducerText(reflect.ValueOf(value), make(map[producerVisit]struct{}))
}

func inspectProducerText(value reflect.Value, visited map[producerVisit]struct{}) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return inspectProducerText(value.Elem(), visited)
	}
	if value.Type() == rawMessageType {
		if value.IsNil() {
			return nil
		}
		if err := requireValidJSONText(value.Bytes()); err != nil {
			return fmt.Errorf("%w: protocol value contains malformed text: %w", ErrInvalidRecord, err)
		}
		return nil
	}
	if value.Kind() == reflect.String {
		if utf8.ValidString(value.String()) {
			return nil
		}
		return fmt.Errorf("%w: protocol value contains malformed UTF-8", ErrInvalidRecord)
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() || producerVisited(value, visited) {
			return nil
		}
		return inspectProducerText(value.Elem(), visited)
	case reflect.Map:
		if value.IsNil() || producerVisited(value, visited) {
			return nil
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := inspectProducerText(iterator.Key(), visited); err != nil {
				return err
			}
			if err := inspectProducerText(iterator.Value(), visited); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() || producerVisited(value, visited) {
			return nil
		}
		for index := 0; index < value.Len(); index++ {
			if err := inspectProducerText(value.Index(index), visited); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := inspectProducerText(value.Index(index), visited); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typeOf := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeOf.Field(index)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := inspectProducerText(value.Field(index), visited); err != nil {
				return err
			}
		}
	}
	return nil
}

func producerVisited(value reflect.Value, visited map[producerVisit]struct{}) bool {
	visit := producerVisit{typeOf: value.Type(), pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.length = value.Len()
	}
	if _, ok := visited[visit]; ok {
		return true
	}
	visited[visit] = struct{}{}
	return false
}

var rawMessageType = reflect.TypeOf(json.RawMessage{})

func validateJSONMembers(data []byte, destinationType reflect.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, destinationType); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, destinationType reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		fields, exact := exactJSONFields(destinationType)
		childType := mapElementType(destinationType)
		seen := make(map[string]struct{})
		for decoder.More() {
			member, memberErr := decoder.Token()
			if memberErr != nil {
				return memberErr
			}
			name, ok := member.(string)
			if !ok {
				return errors.New("JSON object member is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON member %q", name)
			}
			seen[name] = struct{}{}
			if exact {
				var allowed bool
				childType, allowed = fields[name]
				if !allowed {
					return fmt.Errorf("non-canonical JSON member %q", name)
				}
			}
			if scanErr := scanJSONValue(decoder, childType); scanErr != nil {
				return scanErr
			}
		}
	case '[':
		childType := collectionElementType(destinationType)
		for decoder.More() {
			if scanErr := scanJSONValue(decoder, childType); scanErr != nil {
				return scanErr
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if delimiter == '[' {
		expected = ']'
	}
	if closing != expected {
		return errors.New("mismatched JSON delimiter")
	}
	return nil
}

func exactJSONFields(destinationType reflect.Type) (map[string]reflect.Type, bool) {
	destinationType = indirectJSONType(destinationType)
	if destinationType == nil || destinationType == rawMessageType || destinationType.Kind() != reflect.Struct {
		return nil, false
	}
	fields := make(map[string]reflect.Type)
	collectExactJSONFields(destinationType, fields)
	return fields, true
}

func collectExactJSONFields(destinationType reflect.Type, fields map[string]reflect.Type) {
	for index := 0; index < destinationType.NumField(); index++ {
		field := destinationType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			embeddedType := indirectJSONType(field.Type)
			if embeddedType != nil && embeddedType.Kind() == reflect.Struct && embeddedType != rawMessageType {
				collectExactJSONFields(embeddedType, fields)
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
}

func indirectJSONType(destinationType reflect.Type) reflect.Type {
	for destinationType != nil && destinationType.Kind() == reflect.Pointer {
		destinationType = destinationType.Elem()
	}
	return destinationType
}

func mapElementType(destinationType reflect.Type) reflect.Type {
	destinationType = indirectJSONType(destinationType)
	if destinationType != nil && destinationType.Kind() == reflect.Map {
		return destinationType.Elem()
	}
	return nil
}

func collectionElementType(destinationType reflect.Type) reflect.Type {
	destinationType = indirectJSONType(destinationType)
	if destinationType != nil &&
		(destinationType.Kind() == reflect.Array || destinationType.Kind() == reflect.Slice) {
		return destinationType.Elem()
	}
	return nil
}
