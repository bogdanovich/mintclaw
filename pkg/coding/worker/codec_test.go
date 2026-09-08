package worker

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestConsumersRejectMalformedUTF8BeforeJSONDecode(t *testing.T) {
	identityFields := `"task_id":"task-1","task_generation_id":"task-generation-1",` +
		`"worker_generation_id":"worker-generation-1"`
	tests := map[string]struct {
		raw    string
		decode func([]byte) error
	}{
		"envelope identity": {
			raw: `{"schema_version":1,"type":"request","id":"BAD_UTF8",` +
				`"method":"turn.interrupt","idempotency_key":"interrupt-1","params":{` + identityFields + `}}`,
			decode: func(raw []byte) error {
				_, err := Decode(raw)
				return err
			},
		},
		"control identity": {
			raw: `{"task_id":"BAD_UTF8","task_generation_id":"task-generation-1",` +
				`"worker_generation_id":"worker-generation-1"}`,
			decode: func(raw []byte) error {
				var payload GenerationParams
				return DecodePayload(raw, &payload)
			},
		},
		"steer command": {
			raw: `{` + identityFields + `,"text":"BAD_UTF8"}`,
			decode: func(raw []byte) error {
				_, err := DecodeRequestPayload(MethodTurnSteer, raw)
				return err
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			for encoding, invalid := range map[string][]byte{
				"isolated byte":      {0xff},
				"truncated sequence": {0xe2, 0x82},
			} {
				t.Run(encoding, func(t *testing.T) {
					raw := malformedUTF8(test.raw, invalid)
					if err := test.decode(raw); !errors.Is(err, ErrInvalidRecord) {
						t.Fatalf("decode error = %v, want %v", err, ErrInvalidRecord)
					}
					if bytes.Contains(raw, []byte("\ufffd")) {
						t.Fatal("malformed fixture was normalized before reaching the codec")
					}
				})
			}
		})
	}
}

func TestProducersRejectMalformedUTF8BeforeJSONMarshal(t *testing.T) {
	invalid := string([]byte{'b', 'a', 'd', 0xff})
	binding := testBinding(t)
	params := mustPayload(t, GenerationParams{ControlIdentity: binding.ControlIdentity()})
	invalidEnvelope := Record{
		SchemaVersion:  ProtocolV1,
		Type:           RecordRequest,
		ID:             invalid,
		Method:         MethodTurnInterrupt,
		IdempotencyKey: "interrupt-1",
		Params:         params,
	}
	if _, err := Encode(invalidEnvelope); !errors.Is(err, ErrInvalidRecord) ||
		!strings.Contains(err.Error(), "malformed UTF-8") {
		t.Fatalf("Encode(invalid envelope) error = %v, want malformed UTF-8", err)
	}

	for name, payload := range map[string]any{
		"control identity": GenerationParams{ControlIdentity: ControlIdentity{
			TaskID: invalid, TaskGenerationID: "task-generation-1", WorkerGenerationID: "worker-generation-1",
		}},
		"steer command": TurnSteerParams{ControlIdentity: binding.ControlIdentity(), Text: invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MarshalPayload(payload); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("MarshalPayload() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestCodecRejectsUnpairedSurrogateEscapes(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"value":"\ud800"}`),
		[]byte(`{"value":"\udfff"}`),
		[]byte(`{"value":"\ud800x"}`),
	} {
		var payload struct {
			Value string `json:"value"`
		}
		if err := DecodePayload(raw, &payload); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("DecodePayload(%s) error = %v, want %v", raw, err, ErrInvalidRecord)
		}
	}

	var payload struct {
		Value string `json:"value"`
	}
	if err := DecodePayload([]byte(`{"value":"\ud83d\ude80"}`), &payload); err != nil {
		t.Fatalf("DecodePayload(valid surrogate pair) error = %v", err)
	}
	if payload.Value != "🚀" {
		t.Fatalf("decoded value = %q, want rocket", payload.Value)
	}
}

func TestPayloadCodecEnforcesRecordBound(t *testing.T) {
	oversized := strings.Repeat("x", MaxRecordBytes+1)
	if _, err := MarshalPayload(map[string]string{"value": oversized}); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("MarshalPayload() error = %v, want %v", err, ErrRecordTooLarge)
	}
	var payload map[string]string
	raw := append([]byte("{\"value\":\""), []byte(oversized)...)
	raw = append(raw, []byte("\"}")...)
	if err := DecodePayload(raw, &payload); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("DecodePayload() error = %v, want %v", err, ErrRecordTooLarge)
	}
}

func malformedUTF8(template string, invalid []byte) []byte {
	return bytes.ReplaceAll([]byte(template), []byte("BAD_UTF8"), invalid)
}
