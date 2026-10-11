package intent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type requestPacket struct {
	Version int `json:"version"`
	Request
}

type responsePacket struct {
	Version int `json:"version"`
	Response
}

func EncodeRequest(request Request) ([]byte, error) {
	if request.Validate() != nil {
		return nil, ErrInvalid
	}
	return encode(requestPacket{Version, request})
}

func DecodeRequest(data []byte) (Request, error) {
	var packet requestPacket
	if decode(data, &packet, []string{
		"version", "operation", "account", "accounts", "approval_id", "grant_id", "duration_seconds",
	}) != nil || packet.Version != Version || packet.Validate() != nil {
		return Request{}, ErrInvalid
	}
	return packet.Request, nil
}

func EncodeResponse(operation Operation, response Response) ([]byte, error) {
	if response.Validate(operation) != nil {
		return nil, ErrInvalid
	}
	return encode(responsePacket{Version, response})
}

func DecodeResponse(operation Operation, data []byte) (Response, error) {
	var packet responsePacket
	if decode(data, &packet, []string{
		"version", "code", "approval_id", "grant_id", "session_id", "valid_for_seconds",
	}) != nil || packet.Version != Version || packet.Validate(operation) != nil {
		return Response{}, ErrInvalid
	}
	return packet.Response, nil
}

func encode(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > MaxPacketBytes {
		return nil, ErrInvalid
	}
	return data, nil
}

// Inspect exact top-level keys before typed decoding. encoding/json otherwise
// accepts duplicate members and case-insensitive field-name substitutions.
// Every admitted value is scalar or a flat string array; typed decoding rejects
// nested objects/arrays. All parser diagnostics stay private and fixed.
func decode(data []byte, target any, allowed []string) error {
	if len(data) == 0 || len(data) > MaxPacketBytes {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		key, keyErr := decoder.Token()
		name, ok := key.(string)
		if keyErr != nil || !ok || !contains(allowed, name) {
			return ErrInvalid
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrInvalid
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrInvalid
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return ErrInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrInvalid
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
