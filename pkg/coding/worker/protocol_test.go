package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
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
		ThreadOpenMode:        ThreadOpenNew,
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
	resumed := binding
	resumed.ThreadOpenMode = ThreadOpenResume
	if err := resumed.Validate(); err != nil {
		t.Fatalf("resume binding Validate() error = %v", err)
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
		{name: "thread open mode", mutate: func(value *Binding) { value.ThreadOpenMode = "discover" }},
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
		QuestionAnswer: &QuestionAnswerRef{
			QuestionID: "question-1", QuestionRevision: 2, AnswerID: "answer-1",
		},
	}
	if err := steer.Validate(); err != nil {
		t.Fatalf("TurnSteerParams.Validate() error = %v", err)
	}
	steer.QuestionAnswer.QuestionRevision = 0
	if err := steer.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("stale question revision error = %v, want %v", err, ErrInvalidRecord)
	}
	steer.QuestionAnswer.QuestionRevision = 2
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
			`{"schema_version":1,"schema_version":1,"type":"request","id":"request-1",` +
				`"method":"turn.interrupt","idempotency_key":"interrupt-1",` +
				`"params":{"task_id":"task-1","task_generation_id":"task-generation-1",` +
				`"worker_generation_id":"worker-generation-1"}}`,
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

func TestEveryCommandRequiresIdempotencyKey(t *testing.T) {
	binding := testBinding(t)
	requests := map[Method]any{
		MethodInitialize: InitializeParams{
			MinProtocolVersion: ProtocolV1,
			MaxProtocolVersion: ProtocolV1,
			ParentBuildID:      "parent-test-build",
			Binding:            binding,
		},
		MethodTurnStart: TurnStartParams{
			ControlIdentity: binding.ControlIdentity(),
			Text:            "inspect",
		},
		MethodTurnSteer: TurnSteerParams{
			ControlIdentity: binding.ControlIdentity(),
			Text:            "focus on the parser",
		},
		MethodTurnInterrupt: GenerationParams{ControlIdentity: binding.ControlIdentity()},
		MethodTurnCancel:    GenerationParams{ControlIdentity: binding.ControlIdentity()},
		MethodShutdown:      GenerationParams{ControlIdentity: binding.ControlIdentity()},
	}
	for method, value := range requests {
		t.Run(string(method), func(t *testing.T) {
			if !method.Valid() || !method.RequiresIdempotencyKey() {
				t.Fatalf("%s must be valid and idempotent", method)
			}
			params := mustPayload(t, value)
			record := Record{
				SchemaVersion: ProtocolV1,
				Type:          RecordRequest,
				ID:            "request-1",
				Method:        method,
				Params:        params,
			}
			if _, err := Encode(record); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Encode(without key) error = %v, want %v", err, ErrInvalidRecord)
			}
			record.IdempotencyKey = "operation-1"
			if _, err := Encode(record); err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
		})
	}
	snapshot := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordRequest,
		ID:            "snapshot-1",
		Method:        MethodSnapshotRead,
		Params: mustPayload(t, GenerationParams{
			ControlIdentity: binding.ControlIdentity(),
		}),
	}
	if !snapshot.Method.Valid() || snapshot.Method.RequiresIdempotencyKey() {
		t.Fatal("snapshot.read must be a valid read-only command")
	}
	if _, err := Encode(snapshot); err != nil {
		t.Fatalf("Encode(snapshot.read) error = %v", err)
	}
	snapshot.IdempotencyKey = "not-accepted"
	if _, err := Encode(snapshot); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Encode(snapshot.read with key) error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestTurnStartRejectsAggregatePayloadBeyondRecordBudget(t *testing.T) {
	binding := testBinding(t)
	attachments := make([]TurnAttachment, 32)
	for index := range attachments {
		attachments[index] = TurnAttachment{SourcePath: "/" + strings.Repeat("p", MaxPathBytes-1)}
	}
	params := TurnStartParams{
		ControlIdentity: binding.ControlIdentity(),
		Text:            strings.Repeat("x", thread.MaxPromptBytes),
		Attachments:     attachments,
	}
	if err := params.Validate(); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("TurnStartParams.Validate() error = %v, want %v", err, ErrRecordTooLarge)
	}
}

func TestSuccessfulResultSchemasAreClosed(t *testing.T) {
	binding := testBinding(t)
	initialize := mustPayload(t, InitializeResult{Identity: BoundIdentity{
		ProtocolVersion: ProtocolV1,
		WorkerBuildID:   binding.ExpectedWorkerBuildID,
		Binding:         binding,
	}})
	response := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            "initialize-1",
		Method:        MethodInitialize,
		OK:            boolPointer(true),
		Result:        initialize,
	}
	if _, err := Encode(response); err != nil {
		t.Fatalf("Encode(initialize result) error = %v", err)
	}

	ack := mustPayload(t, AckResult{})
	for _, method := range []Method{
		MethodTurnStart,
		MethodTurnSteer,
		MethodTurnInterrupt,
		MethodTurnCancel,
		MethodShutdown,
	} {
		response.Method = method
		response.Result = ack
		if _, err := Encode(response); err != nil {
			t.Fatalf("Encode(%s result) error = %v", method, err)
		}
		response.Result = json.RawMessage("{\"unexpected\":true}")
		if _, err := Encode(response); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Encode(%s open result) error = %v, want %v", method, err, ErrInvalidRecord)
		}
	}
}

func TestRecordSeparatesRequestAndResponseFields(t *testing.T) {
	empty := json.RawMessage("{}")
	for _, record := range []Record{
		{
			SchemaVersion:  ProtocolV1,
			Type:           RecordRequest,
			ID:             "request-1",
			Method:         MethodTurnStart,
			IdempotencyKey: "start-1",
			Params:         empty,
			Result:         empty,
		},
		{
			SchemaVersion:  ProtocolV1,
			Type:           RecordResponse,
			ID:             "request-1",
			Method:         MethodTurnStart,
			IdempotencyKey: "start-1",
			OK:             boolPointer(true),
			Result:         empty,
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
		Method:        MethodTurnInterrupt,
		OK:            boolPointer(false),
		Error: &ProtocolError{
			Code:    ErrorNoActiveTurn,
			Message: "no active turn",
		},
	}
	if _, err := Encode(failure); err != nil {
		t.Fatalf("Encode(failure) error = %v", err)
	}
}

func TestDecodeRejectsPresentZeroFieldsFromOtherEnvelopeVariant(t *testing.T) {
	identity := `{"task_id":"task-1","task_generation_id":"task-generation-1",` +
		`"worker_generation_id":"worker-generation-1"}`
	for name, raw := range map[string][]byte{
		"request null ok": []byte(
			`{"schema_version":1,"type":"request","id":"request-1",` +
				`"method":"turn.interrupt","idempotency_key":"interrupt-1","params":` + identity +
				`,"ok":null}`,
		),
		"request null error": []byte(
			`{"schema_version":1,"type":"request","id":"request-1",` +
				`"method":"turn.interrupt","idempotency_key":"interrupt-1","params":` + identity +
				`,"error":null}`,
		),
		"response empty idempotency key": []byte(
			`{"schema_version":1,"type":"response","id":"request-1",` +
				`"method":"turn.interrupt","ok":true,"result":{},"idempotency_key":""}`,
		),
		"response null params": []byte(
			`{"schema_version":1,"type":"response","id":"request-1",` +
				`"method":"turn.interrupt","ok":true,"result":{},"params":null}`,
		),
		"successful response null error": []byte(
			`{"schema_version":1,"type":"response","id":"request-1",` +
				`"method":"turn.interrupt","ok":true,"result":{},"error":null}`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(raw); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Decode() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestDecodeRejectsCaseFoldedJSONAliasesAtEveryDepth(t *testing.T) {
	topLevel := []byte(
		"{\"SCHEMA_VERSION\":1,\"type\":\"request\",\"id\":\"r\"," +
			"\"method\":\"turn.interrupt\",\"idempotency_key\":\"k\",\"params\":{}}",
	)
	if _, err := Decode(topLevel); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Decode(alias) error = %v, want %v", err, ErrInvalidRecord)
	}

	identityJSON := "\"task_generation_id\":\"task-generation-1\"," +
		"\"worker_generation_id\":\"worker-generation-1\""
	for name, test := range map[string]struct {
		raw         json.RawMessage
		destination any
	}{
		"embedded alias": {
			raw:         json.RawMessage("{\"TASK_ID\":\"task-1\"," + identityJSON + "}"),
			destination: &GenerationParams{},
		},
		"alias beside canonical": {
			raw: json.RawMessage(
				"{\"task_id\":\"task-1\",\"TASK_ID\":\"changed\"," + identityJSON + "}",
			),
			destination: &GenerationParams{},
		},
		"nested collection alias": {
			raw: json.RawMessage(
				"{\"task_id\":\"task-1\"," + identityJSON +
					",\"attachments\":[{\"SOURCE_PATH\":\"/tmp/screen.png\"}]}",
			),
			destination: &TurnStartParams{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := DecodePayload(test.raw, test.destination); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("DecodePayload(%s) error = %v, want %v", test.raw, err, ErrInvalidRecord)
			}
		})
	}
}

func TestSteerLimitHasStableWireCode(t *testing.T) {
	protocolError := ProtocolError{Code: ErrorSteerLimit, Message: "steer limit reached"}
	if err := protocolError.Validate(); err != nil {
		t.Fatalf("ProtocolError.Validate() error = %v", err)
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

func TestRequestPayloadRejectsUnsafeText(t *testing.T) {
	binding := testBinding(t)
	params := mustPayload(t, TurnSteerParams{
		ControlIdentity: binding.ControlIdentity(),
		Text:            "inspect\bthe parser",
	})
	record := Record{
		SchemaVersion: ProtocolV1, Type: RecordRequest, ID: "steer-1",
		Method: MethodTurnSteer, IdempotencyKey: "steer-1", Params: params,
	}
	if _, err := Encode(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Encode(unsafe steer) error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestDecodeRequestRejectsControlsInStructuralFields(t *testing.T) {
	binding := testBinding(t)
	for name, control := range map[string]string{
		"line feed":         "\n",
		"carriage return":   "\r",
		"tab":               "\t",
		"non-ASCII control": "\u0085",
	} {
		t.Run(name, func(t *testing.T) {
			initialize := InitializeParams{
				MinProtocolVersion: ProtocolV1,
				MaxProtocolVersion: ProtocolV1,
				ParentBuildID:      "parent" + control + "build",
				Binding:            binding,
			}
			raw := mustPayload(t, initialize)
			if _, err := DecodeRequestPayload(MethodInitialize, raw); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("DecodeRequestPayload() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestDecodeInitializeRejectsControlInModel(t *testing.T) {
	binding := testBinding(t)
	binding.Model = "gpt\t5"
	initialize := InitializeParams{
		MinProtocolVersion: ProtocolV1,
		MaxProtocolVersion: ProtocolV1,
		ParentBuildID:      "parent-test-build",
		Binding:            binding,
	}
	raw := mustPayload(t, initialize)
	if _, err := DecodeRequestPayload(MethodInitialize, raw); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("DecodeRequestPayload() error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestDecodeStartRejectsControlsInAttachmentMetadata(t *testing.T) {
	binding := testBinding(t)
	for name, control := range map[string]string{
		"line feed":         "\n",
		"carriage return":   "\r",
		"tab":               "\t",
		"non-ASCII control": "\u0085",
	} {
		t.Run(name, func(t *testing.T) {
			start := TurnStartParams{
				ControlIdentity: binding.ControlIdentity(),
				Attachments: []TurnAttachment{{
					SourcePath:  filepath.Join(binding.ExecutionRoot, "screen.png"),
					Filename:    "screen" + control + ".png",
					ContentType: "image/png",
				}},
			}
			raw := mustPayload(t, start)
			if _, err := DecodeRequestPayload(MethodTurnStart, raw); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("DecodeRequestPayload() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestDecodeInitializeRejectsControlsInProjectPaths(t *testing.T) {
	for name, control := range map[string]string{
		"line feed":         "\n",
		"carriage return":   "\r",
		"tab":               "\t",
		"non-ASCII control": "\u0085",
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project"+control+"root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			project, err := thread.ResolveProject(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			binding := testBinding(t)
			binding.Project = project
			binding.ExecutionRoot = project.ProjectRoot
			binding.ExecutionRootIdentity = ExecutionRootIdentity(project.ProjectRoot)
			initialize := InitializeParams{
				MinProtocolVersion: ProtocolV1,
				MaxProtocolVersion: ProtocolV1,
				ParentBuildID:      "parent-test-build",
				Binding:            binding,
			}
			raw := mustPayload(t, initialize)
			if _, err := DecodeRequestPayload(MethodInitialize, raw); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("DecodeRequestPayload() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestFailedResponseRejectsUnsafeErrorText(t *testing.T) {
	for name, protocolError := range map[string]*ProtocolError{
		"message": {
			Code: ErrorInternal, Message: "worker\bfailed",
		},
		"detail value": {
			Code: ErrorInternal, Message: "worker failed",
			Details: json.RawMessage(`{"reason":"unsafe\bdetail"}`),
		},
		"detail key": {
			Code: ErrorInternal, Message: "worker failed",
			Details: json.RawMessage(`{"unsafe\bkey":"detail"}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := Record{
				SchemaVersion: ProtocolV1, Type: RecordResponse, ID: "request-1",
				Method: MethodTurnStart, OK: boolPointer(false), Error: protocolError,
			}
			if _, err := Encode(record); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Encode(failed response) error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func mustPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := MarshalPayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
