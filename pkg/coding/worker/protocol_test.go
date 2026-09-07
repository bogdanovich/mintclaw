package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

func testBinding(t *testing.T) Binding {
	t.Helper()
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Binding{
		TaskID:                "task-1",
		TaskGenerationID:      "task-generation-1",
		WorkerGenerationID:    "worker-generation-1",
		ThreadID:              thread.NewThreadID(),
		Project:               project,
		ExecutionRoot:         project.ProjectRoot,
		ExecutionRootIdentity: ExecutionRootIdentity(project.ProjectRoot),
		Mode:                  TaskModeInvestigate,
		ProviderProfile:       "openai-default",
		Model:                 "gpt-5.6-sol",
		Provider:              "openai",
		ExpectedWorkerBuildID: "mintclaw-test-build",
	}
}

func TestNegotiateProtocol(t *testing.T) {
	for _, test := range []struct {
		name    string
		minimum int
		maximum int
		wantErr bool
	}{
		{name: "exact", minimum: ProtocolV1, maximum: ProtocolV1},
		{name: "newer maximum", minimum: ProtocolV1, maximum: ProtocolV1 + 1},
		{name: "missing current", minimum: ProtocolV1 + 1, maximum: ProtocolV1 + 2, wantErr: true},
		{name: "reversed", minimum: ProtocolV1, maximum: 0, wantErr: true},
		{name: "zero minimum", minimum: 0, maximum: ProtocolV1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NegotiateProtocol(test.minimum, test.maximum)
			if test.wantErr {
				if !errors.Is(err, ErrIncompatibleProtocol) {
					t.Fatalf("NegotiateProtocol() error = %v, want %v", err, ErrIncompatibleProtocol)
				}
				return
			}
			if err != nil || got != ProtocolV1 {
				t.Fatalf("NegotiateProtocol() = %d, %v, want %d, nil", got, err, ProtocolV1)
			}
		})
	}
}

func TestBindingPinsEveryWorkerAuthorityDimension(t *testing.T) {
	binding := testBinding(t)
	if err := binding.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := binding.Authorize(binding.ControlIdentity()); err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	wrongControl := binding.ControlIdentity()
	wrongControl.WorkerGenerationID = "stale-worker-generation"
	if err := binding.Authorize(wrongControl); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Authorize(stale) error = %v, want %v", err, ErrIdentityMismatch)
	}
	mutations := []struct {
		name   string
		mutate func(*Binding)
	}{
		{name: "task", mutate: func(value *Binding) { value.TaskID = "" }},
		{name: "task generation", mutate: func(value *Binding) { value.TaskGenerationID = "bad id" }},
		{name: "worker generation", mutate: func(value *Binding) { value.WorkerGenerationID = "" }},
		{name: "thread", mutate: func(value *Binding) { value.ThreadID = "not-a-uuid" }},
		{name: "project", mutate: func(value *Binding) { value.Project.ProjectKey = "changed" }},
		{name: "execution root", mutate: func(value *Binding) { value.ExecutionRoot = "relative" }},
		{
			name:   "execution identity",
			mutate: func(value *Binding) { value.ExecutionRootIdentity = strings.Repeat("0", 64) },
		},
		{name: "mode", mutate: func(value *Binding) { value.Mode = "admin" }},
		{name: "provider", mutate: func(value *Binding) { value.ProviderProfile = "bad profile" }},
		{name: "model", mutate: func(value *Binding) { value.Model = "" }},
		{name: "resolved provider", mutate: func(value *Binding) { value.Provider = "bad provider" }},
		{name: "build", mutate: func(value *Binding) { value.ExpectedWorkerBuildID = "" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			mutated := binding
			test.mutate(&mutated)
			if err := mutated.Validate(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
	identity := BoundIdentity{
		ProtocolVersion: ProtocolV1,
		WorkerBuildID:   binding.ExpectedWorkerBuildID,
		Binding:         binding,
	}
	if err := identity.Validate(); err != nil {
		t.Fatalf("BoundIdentity.Validate() error = %v", err)
	}
	identity.WorkerBuildID = "different-build"
	if err := identity.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("mismatched build error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestInitializeAndCommandPayloadValidation(t *testing.T) {
	binding := testBinding(t)
	initialize := InitializeParams{
		MinProtocolVersion: ProtocolV1,
		MaxProtocolVersion: ProtocolV1,
		ParentBuildID:      "parent-test-build",
		Binding:            binding,
	}
	if err := initialize.Validate(); err != nil {
		t.Fatalf("InitializeParams.Validate() error = %v", err)
	}
	start := TurnStartParams{ControlIdentity: binding.ControlIdentity(), Text: "inspect the parser"}
	if err := start.Validate(); err != nil {
		t.Fatalf("TurnStartParams.Validate() error = %v", err)
	}
	attachmentPath := filepath.Join(binding.ExecutionRoot, "screen.png")
	start = TurnStartParams{
		ControlIdentity: binding.ControlIdentity(),
		Attachments: []TurnAttachment{{
			SourcePath: attachmentPath, Filename: "screen.png", ContentType: "image/png",
		}},
	}
	if err := start.Validate(); err != nil {
		t.Fatalf("attachment TurnStartParams.Validate() error = %v", err)
	}
	input := start.FrontendInput()
	if len(input.Attachments) != 1 || input.Attachments[0].Path != attachmentPath {
		t.Fatalf("FrontendInput() = %#v", input)
	}
	steer := TurnSteerParams{
		ControlIdentity: binding.ControlIdentity(),
		Text:            "use the failing fixture",
		QuestionAnswer:  &QuestionAnswerRef{QuestionID: "question-1", AnswerID: "answer-1"},
	}
	if err := steer.Validate(); err != nil {
		t.Fatalf("TurnSteerParams.Validate() error = %v", err)
	}
	steer.QuestionAnswer.AnswerID = "bad answer"
	if err := steer.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid question answer error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestRecordRoundTripAndClosedWorldShape(t *testing.T) {
	identity := ControlIdentity{
		TaskID: "task-1", TaskGenerationID: "task-generation-1", WorkerGenerationID: "worker-generation-1",
	}
	params, err := MarshalPayload(GenerationParams{ControlIdentity: identity})
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		SchemaVersion:  ProtocolV1,
		Type:           RecordRequest,
		ID:             "request-1",
		Method:         MethodTurnInterrupt,
		IdempotencyKey: "interrupt-1",
		Params:         params,
	}
	encoded, err := Encode(record)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.ID != record.ID || decoded.Method != record.Method ||
		decoded.IdempotencyKey != record.IdempotencyKey {
		t.Fatalf("Decode() = %#v, want %#v", decoded, record)
	}
	var generation GenerationParams
	if err := DecodePayload(decoded.Params, &generation); err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if err := generation.Validate(); err != nil || generation.ControlIdentity != identity {
		t.Fatalf("generation = %#v", generation)
	}

	for name, malformed := range map[string][]byte{
		"unknown envelope field": append(encoded[:len(encoded)-1], []byte(`,"extra":true}`)...),
		"duplicate envelope field": []byte(
			`{"schema_version":1,"schema_version":1,"type":"event","event":"worker.ready","payload":{}}`,
		),
		"unknown method": []byte(
			`{"schema_version":1,"type":"request","id":"r","method":"shell.exec","idempotency_key":"k","params":{}}`,
		),
		"trailing value": append(encoded, []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(malformed); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Decode() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
	if _, err := Decode(bytes.Repeat([]byte("x"), MaxRecordBytes+1)); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("oversized Decode() error = %v, want %v", err, ErrRecordTooLarge)
	}
}

func TestRecordSeparatesRequestResponseAndEventFields(t *testing.T) {
	empty := json.RawMessage(`{}`)
	truth := true
	for _, record := range []Record{
		{
			SchemaVersion: ProtocolV1, Type: RecordRequest, ID: "request-1",
			Method: MethodTurnStart, IdempotencyKey: "start-1", Params: empty, Result: empty,
		},
		{
			SchemaVersion: ProtocolV1, Type: RecordResponse, ID: "request-1", OK: &truth,
			Result: empty, Event: EventWorkerReady,
		},
		{
			SchemaVersion: ProtocolV1, Type: RecordEvent, Event: EventWorkerReady,
			Payload: empty, ID: "not-allowed",
		},
	} {
		if err := record.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Record.Validate(%#v) error = %v, want %v", record, err, ErrInvalidRecord)
		}
	}
	failure := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            "request-2",
		OK:            boolPointer(false),
		Error: &ProtocolError{
			Code: ErrorNoActiveTurn, Message: "no active turn",
		},
	}
	if _, err := Encode(failure); err != nil {
		t.Fatalf("Encode(failure) error = %v", err)
	}
}

func TestDecodePayloadRejectsUnknownAndDuplicateFields(t *testing.T) {
	type payload struct {
		Value string `json:"value"`
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"value":"one","unknown":true}`),
		json.RawMessage(`{"value":"one","value":"two"}`),
	} {
		var destination payload
		if err := DecodePayload(raw, &destination); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("DecodePayload(%s) error = %v, want %v", raw, err, ErrInvalidRecord)
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}
