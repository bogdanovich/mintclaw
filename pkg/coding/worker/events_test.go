package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func validEventItem(sequence uint64) Item {
	return Item{
		ID:       "message:turn-1:item-1",
		TurnID:   "turn-1",
		Sequence: sequence,
		Revision: 1,
		Message: &Message{
			Kind:     MessageAssistant,
			Phase:    AssistantPhaseFinal,
			Text:     "done",
			Complete: true,
		},
	}
}

func TestWorkerEventSchemasRoundTrip(t *testing.T) {
	binding := testBinding(t)
	control := binding.ControlIdentity()
	snapshot := Snapshot{
		ThreadID: binding.ThreadID,
		Activity: ActivityIdle,
		Items:    []Item{validEventItem(1)},
	}
	tests := map[EventName]any{
		EventWorkerReady: WorkerReadyPayload{
			ControlIdentity: control,
			Snapshot:        snapshot,
		},
		EventItemUpdated: ItemUpdatedPayload{
			ControlIdentity: control,
			Item:            validEventItem(1),
		},
		EventStatusChanged: StatusChangedPayload{
			ControlIdentity: control,
			Activity:        ActivityRunning,
			Status:          "running",
		},
		EventQuestionState: QuestionStatePayload{
			ControlIdentity: control,
			Question: QuestionState{
				QuestionID: "question-1",
				Revision:   1,
				Status:     QuestionWaiting,
				Prompt:     "Choose a target",
				Options: []QuestionOption{{
					ID:          "target-1",
					Label:       "Target one",
					Description: "Use the first target",
				}},
			},
		},
		EventContextUsage: ContextUsagePayload{
			ControlIdentity: control,
			Usage:           ContextUsage{UsedTokens: 25, LimitTokens: 100},
		},
		EventTurnTerminal: TurnTerminalPayload{
			ControlIdentity: control,
			TurnID:          "turn-1",
			Outcome:         TurnOutcomeCompleted,
			Status:          "completed",
		},
		EventWorkerStopped: WorkerStoppedPayload{
			ControlIdentity: control,
			Reason:          WorkerStopCompleted,
		},
	}
	for event, payload := range tests {
		t.Run(string(event), func(t *testing.T) {
			raw := mustPayload(t, payload)
			record := Record{
				SchemaVersion: ProtocolV1,
				Type:          RecordEvent,
				Event:         event,
				Payload:       raw,
			}
			encoded, err := Encode(record)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if decoded.Type != RecordEvent || decoded.Event != event {
				t.Fatalf("Decode() = %#v", decoded)
			}
			if _, err := DecodeEventPayload(event, decoded.Payload); err != nil {
				t.Fatalf("DecodeEventPayload() error = %v", err)
			}
		})
	}
}

func TestEventEnvelopeRejectsCrossVariantFieldsByPresence(t *testing.T) {
	binding := testBinding(t)
	payload := mustPayload(t, ContextUsagePayload{
		ControlIdentity: binding.ControlIdentity(),
		Usage:           ContextUsage{UsedTokens: 1, LimitTokens: 10},
	})
	record := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordEvent,
		Event:         EventContextUsage,
		Payload:       payload,
	}
	encoded, err := Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	for name, suffix := range map[string][]byte{
		"null request ID": []byte(",\"id\":null}"),
		"empty method":    []byte(",\"method\":\"\"}"),
		"null ok":         []byte(",\"ok\":null}"),
	} {
		t.Run(name, func(t *testing.T) {
			malformed := append(bytes.Clone(encoded[:len(encoded)-1]), suffix...)
			if _, err := Decode(malformed); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Decode() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestSnapshotReadRequestAndResult(t *testing.T) {
	binding := testBinding(t)
	request := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordRequest,
		ID:            "snapshot-1",
		Method:        MethodSnapshotRead,
		Params: mustPayload(t, GenerationParams{
			ControlIdentity: binding.ControlIdentity(),
		}),
	}
	if _, err := Encode(request); err != nil {
		t.Fatalf("Encode(request) error = %v", err)
	}
	result := SnapshotResult{
		ControlIdentity: binding.ControlIdentity(),
		Snapshot: Snapshot{
			ThreadID: binding.ThreadID,
			Activity: ActivityIdle,
			Question: &QuestionState{
				QuestionID: "question-1",
				Revision:   2,
				Status:     QuestionWaiting,
				Prompt:     "Continue?",
			},
		},
	}
	response := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            request.ID,
		Method:        request.Method,
		OK:            boolPointer(true),
		Result:        mustPayload(t, result),
	}
	encoded, err := Encode(response)
	if err != nil {
		t.Fatalf("Encode(response) error = %v", err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("Decode(response) error = %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"items_truncated":false`)) {
		t.Fatalf("snapshot response does not explicitly report truncation: %s", encoded)
	}

	result.Snapshot.Question.Status = QuestionAnswered
	response.Result = mustPayload(t, result)
	if _, err := Encode(response); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("terminal snapshot question error = %v, want %v", err, ErrInvalidRecord)
	}
}

func TestItemRequiresOneValidNestedPayload(t *testing.T) {
	valid := validEventItem(1)
	for name, mutate := range map[string]func(*Item){
		"missing payload": func(item *Item) {
			item.Message = nil
		},
		"multiple payloads": func(item *Item) {
			item.Tool = &Tool{CallID: "call-1", Name: "read_file", Status: ToolSucceeded}
		},
		"zero sequence": func(item *Item) {
			item.Sequence = 0
		},
		"unsafe identity": func(item *Item) {
			item.ID = "item\nforged"
		},
		"invalid message phase": func(item *Item) {
			item.Message.Kind = MessageUser
			item.Message.Phase = AssistantPhaseFinal
		},
	} {
		t.Run(name, func(t *testing.T) {
			item := valid
			message := *valid.Message
			item.Message = &message
			mutate(&item)
			if err := item.Validate(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestToolExplorationWireValidationFailsClosed(t *testing.T) {
	valid := Item{
		ID: "tool:turn-1:call-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
		Tool: &Tool{
			CallID: "call-1", Name: "read_file", Status: ToolSucceeded,
			Exploration: &Exploration{Operation: ExplorationRead, Path: "README.md"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid exploration item: %v", err)
	}
	for name, mutate := range map[string]func(*Tool){
		"unknown operation": func(tool *Tool) {
			tool.Exploration.Operation = "execute"
		},
		"oversized path": func(tool *Tool) {
			tool.Exploration.Path = strings.Repeat("p", MaxExplorationValue+1)
		},
		"ambiguous command": func(tool *Tool) {
			tool.Command = &Command{Status: CommandSucceeded}
		},
	} {
		t.Run(name, func(t *testing.T) {
			item := valid
			tool := *valid.Tool
			exploration := *valid.Tool.Exploration
			tool.Exploration = &exploration
			item.Tool = &tool
			mutate(item.Tool)
			if err := item.Validate(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidRecord)
			}
		})
	}
}

func TestQuestionAndWorkerStopInvariants(t *testing.T) {
	question := QuestionState{
		QuestionID: "question-1",
		Revision:   1,
		Status:     QuestionWaiting,
		Prompt:     "Choose",
		Options: []QuestionOption{
			{ID: "one", Label: "One"},
			{ID: "one", Label: "Duplicate"},
		},
	}
	if err := question.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("duplicate question option error = %v, want %v", err, ErrInvalidRecord)
	}

	control := testBinding(t).ControlIdentity()
	for _, stopped := range []WorkerStoppedPayload{
		{ControlIdentity: control, Reason: WorkerStopFailed},
		{
			ControlIdentity: control,
			Reason:          WorkerStopCompleted,
			Error:           &ProtocolError{Code: ErrorInternal, Message: "unexpected"},
		},
	} {
		if err := stopped.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("WorkerStoppedPayload.Validate() error = %v, want %v", err, ErrInvalidRecord)
		}
	}
	failed := WorkerStoppedPayload{
		ControlIdentity: control,
		Reason:          WorkerStopFailed,
		Error:           &ProtocolError{Code: ErrorInternal, Message: "worker failed"},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("WorkerStoppedPayload.Validate(failed) error = %v", err)
	}
}

func TestEventPayloadSchemasRejectUnknownAndUnsafeContent(t *testing.T) {
	binding := testBinding(t)
	payload := mustPayload(t, StatusChangedPayload{
		ControlIdentity: binding.ControlIdentity(),
		Activity:        ActivityRunning,
		Status:          "running",
	})
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = json.RawMessage("true")
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEventPayload(EventStatusChanged, unknown); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("unknown field error = %v, want %v", err, ErrInvalidRecord)
	}

	item := validEventItem(1)
	item.Message.Text = "unsafe\bcontent"
	raw := mustPayload(t, ItemUpdatedPayload{
		ControlIdentity: binding.ControlIdentity(),
		Item:            item,
	})
	if _, err := DecodeEventPayload(EventItemUpdated, raw); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("unsafe content error = %v, want %v", err, ErrInvalidRecord)
	}
	item.Message.Text = "multiline\ncontent\tkept"
	raw = mustPayload(t, ItemUpdatedPayload{
		ControlIdentity: binding.ControlIdentity(),
		Item:            item,
	})
	if _, err := DecodeEventPayload(EventItemUpdated, raw); err != nil {
		t.Fatalf("multiline content error = %v", err)
	}
}

func TestEscapingHeavyMaximumQuestionAndDenseItemsFitSnapshotRecords(t *testing.T) {
	binding := testBinding(t)
	options := make([]QuestionOption, MaxQuestionOptions)
	for index := range options {
		options[index] = QuestionOption{
			ID:          fmt.Sprintf("option-%02d", index),
			Label:       strings.Repeat("<", MaxAttachmentMeta),
			Description: strings.Repeat("<", MaxQuestionTextBytes),
		}
	}
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityWaitingInput,
		Items:    make([]frontend.PresentationItem, MaxSnapshotItems),
	}
	for index := range source.Items {
		sequence := uint64(index + 1)
		source.Items[index] = frontend.PresentationItem{
			ID:       fmt.Sprintf("message:turn-1:%02d", index),
			TurnID:   "turn-1",
			Sequence: sequence,
			Revision: 1,
			Message: &frontend.TranscriptEntry{
				Kind:     frontend.EntryAssistant,
				Phase:    frontend.AssistantPhaseCommentary,
				Text:     strings.Repeat("<", 4<<10),
				Complete: true,
			},
		}
	}
	question := &QuestionState{
		QuestionID: "question-1",
		Revision:   1,
		Status:     QuestionWaiting,
		Prompt:     strings.Repeat("<", MaxQuestionTextBytes),
		Options:    options,
	}
	snapshot := SnapshotFromFrontend(source, question)
	if len(snapshot.Items) == 0 || len(snapshot.Items) >= len(source.Items) || !snapshot.ItemsTruncated {
		t.Fatalf("dense snapshot items = %d, truncated = %t", len(snapshot.Items), snapshot.ItemsTruncated)
	}
	questionBytes, marshalErr := json.Marshal(question)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if len(questionBytes) <= 512<<10 {
		t.Fatalf("escaping-heavy question uses only %d bytes", len(questionBytes))
	}
	itemBytes, marshalErr := json.Marshal(snapshot.Items)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	nextItemBytes, marshalErr := json.Marshal(itemFromFrontend(source.Items[0]))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if remaining := snapshotItemsBudget(snapshot) - len(itemBytes); remaining >= len(nextItemBytes)+1 {
		t.Fatalf("snapshot item budget has %d avoidable bytes remaining", remaining)
	}
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot(maximum question) error = %v", err)
	}

	readyPayload := mustPayload(t, WorkerReadyPayload{
		ControlIdentity: binding.ControlIdentity(),
		Snapshot:        snapshot,
	})
	if _, err := Encode(Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordEvent,
		Event:         EventWorkerReady,
		Payload:       readyPayload,
	}); err != nil {
		t.Fatalf("Encode(worker.ready maximum snapshot) error = %v", err)
	}

	result := SnapshotResult{ControlIdentity: binding.ControlIdentity(), Snapshot: snapshot}
	resultPayload := mustPayload(t, result)
	if _, err := Encode(Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordResponse,
		ID:            "snapshot-maximum",
		Method:        MethodSnapshotRead,
		OK:            boolPointer(true),
		Result:        resultPayload,
	}); err != nil {
		t.Fatalf("Encode(snapshot.read maximum result) error = %v", err)
	}
}

func TestAggregatePayloadLimitRejectsOtherwiseStructuredOversizeError(t *testing.T) {
	details := json.RawMessage(`{"value":"` + strings.Repeat("x", MaxWirePayloadBytes) + `"}`)
	protocolError := ProtocolError{Code: ErrorInternal, Message: "failed", Details: details}
	if err := protocolError.Validate(); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("ProtocolError.Validate() error = %v, want %v", err, ErrRecordTooLarge)
	}
	stopped := WorkerStoppedPayload{
		ControlIdentity: testBinding(t).ControlIdentity(),
		Reason:          WorkerStopFailed,
		Error:           &protocolError,
	}
	if err := stopped.Validate(); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("WorkerStoppedPayload.Validate() error = %v, want %v", err, ErrRecordTooLarge)
	}
}
