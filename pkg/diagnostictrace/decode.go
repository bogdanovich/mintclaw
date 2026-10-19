package diagnostictrace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Decode reads one bounded native trace document without filesystem access.
// Content/privacy inspection belongs to the caller before decoding.
func Decode(data []byte) (Trace, error) {
	if len(data) > HardMaxTraceBytes {
		return Trace{}, fmt.Errorf("trace exceeds hard byte limit")
	}
	var trace Trace
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&trace); err != nil {
		return Trace{}, fmt.Errorf("decode trace: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Trace{}, fmt.Errorf("decode trace: trailing JSON data")
	}
	if err := Validate(trace); err != nil {
		return Trace{}, err
	}
	return trace, nil
}
