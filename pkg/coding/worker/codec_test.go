package worker

import (
	"bytes"
	"encoding/json"
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
				`"method":"snapshot.read","params":{` + identityFields + `}}`,
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
		"nested event": {
			raw: `{` + identityFields + `,"item":{"id":"item-1","turn_id":"turn-1",` +
				`"sequence":1,"revision":1,"kind":"assistant_message","lifecycle":"completed",` +
				`"message":{"id":"message-1","turn_id":"turn-1","kind":"assistant",` +
				`"phase":"final","text":"BAD_UTF8","complete":true}}}`,
			decode: func(raw []byte) error {
				_, err := DecodeEventPayload(EventItemUpdated, raw)
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
	nestedItem := validSnapshotItem("", "message-1", 1)
	nestedItem.Message.Text = invalid
	params := mustPayload(t, GenerationParams{ControlIdentity: binding.ControlIdentity()})
	invalidEnvelope := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordRequest,
		ID:            invalid,
		Method:        MethodSnapshotRead,
		Params:        params,
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
		"nested event":  ItemUpdatedPayload{ControlIdentity: binding.ControlIdentity(), Item: nestedItem},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MarshalPayload(payload); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("MarshalPayload() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}

	malformedPayload := malformedUTF8(`{"value":"BAD_UTF8"}`, []byte{0xff})
	record := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordEvent,
		Event:         EventStatusChanged,
		Payload:       json.RawMessage(malformedPayload),
	}
	if _, err := Encode(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Encode() error = %v, want %v", err, ErrInvalidRecord)
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

func malformedUTF8(template string, invalid []byte) []byte {
	return bytes.ReplaceAll([]byte(template), []byte("BAD_UTF8"), invalid)
}
