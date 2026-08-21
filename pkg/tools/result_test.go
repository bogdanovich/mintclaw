package tools

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestNewToolResult(t *testing.T) {
	result := toolshared.NewToolResult("test content")

	if result.ForLLM != "test content" {
		t.Errorf("Expected ForLLM 'test content', got '%s'", result.ForLLM)
	}
	if result.Delivery.IsSilent() {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Control.Async {
		t.Error("Expected Async to be false")
	}
}

func TestToolResultHasOneTaskResultContract(t *testing.T) {
	typeOfResult := reflect.TypeOf(toolshared.ToolResult{})
	for _, removed := range []string{
		"Completion", "Messages", "Silent", "Async", "AsyncDelivery", "AsyncTaskID", "TaskSuspended",
		"ResponseHandled", "ImmediateDelivery", "DeliveryIntent", "Outbound", "CommitOutbound", "ConfirmOutbound",
		"Suspension", "SuspensionResolution",
	} {
		if _, exists := typeOfResult.FieldByName(removed); exists {
			t.Fatalf("ToolResult must not expose removed field %q", removed)
		}
	}
	field, exists := typeOfResult.FieldByName("Deliverable")
	if !exists || field.Type != reflect.TypeOf((*taskresult.Deliverable)(nil)) {
		t.Fatalf("Deliverable field type = %v", field.Type)
	}
}

func TestSilentResult(t *testing.T) {
	result := toolshared.SilentResult("silent operation")

	if result.ForLLM != "silent operation" {
		t.Errorf("Expected ForLLM 'silent operation', got '%s'", result.ForLLM)
	}
	if !result.Delivery.IsSilent() {
		t.Error("Expected Silent to be true")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Control.Async {
		t.Error("Expected Async to be false")
	}
}

func TestDiffResult(t *testing.T) {
	result := toolshared.DiffResult("pkg/tools/fs/edit.go", []byte("hello world\n"), []byte("hello universe\n"))

	if result.Delivery.IsSilent() {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Control.Async {
		t.Error("Expected Async to be false")
	}
	if result.ForLLM == result.ForUser {
		t.Fatalf("Expected ForLLM to omit the full diff, got %q", result.ForLLM)
	}
	if len(result.ForLLM) >= len(result.ForUser) {
		t.Fatalf("Expected ForLLM to stay smaller than ForUser, got %d vs %d", len(result.ForLLM), len(result.ForUser))
	}

	for _, want := range []string{
		"File edited: pkg/tools/fs/edit.go",
		"```diff",
		"--- a/pkg/tools/fs/edit.go",
		"+++ b/pkg/tools/fs/edit.go",
		"-hello world",
		"+hello universe",
	} {
		if !strings.Contains(result.ForUser, want) {
			t.Fatalf("DiffResult output missing %q:\n%s", want, result.ForUser)
		}
	}
}

func TestDiffResult_NormalizesAbsolutePathsAndHandlesNoOpChanges(t *testing.T) {
	result := toolshared.DiffResult("/tmp/test.txt", []byte("same\n"), []byte("same\n"))

	if !strings.Contains(result.ForUser, "File edited: /tmp/test.txt") {
		t.Fatalf("Expected original path in output, got %q", result.ForUser)
	}
	if !strings.Contains(result.ForUser, "(no content change)") {
		t.Fatalf("Expected no-content-change marker, got %q", result.ForUser)
	}
	if !strings.Contains(result.ForLLM, "(no content change)") {
		t.Fatalf("Expected compact no-op summary in ForLLM, got %q", result.ForLLM)
	}
}

func TestAsyncResult(t *testing.T) {
	result := toolshared.AsyncResult("async task started")

	if result.ForLLM != "async task started" {
		t.Errorf("Expected ForLLM 'async task started', got '%s'", result.ForLLM)
	}
	if result.Delivery.IsSilent() {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if !result.Control.Async {
		t.Error("Expected Async to be true")
	}
	if result.Delivery.AsyncMode != "" {
		t.Errorf("Expected empty AsyncDelivery by default, got %q", result.Delivery.AsyncMode)
	}
}

func TestToolResultWithAsyncDelivery(t *testing.T) {
	result := toolshared.AsyncResult("async task started").WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	if result.Delivery.AsyncMode != toolshared.AsyncDeliveryUserOnly {
		t.Fatalf("AsyncDelivery = %q, want %q", result.Delivery.AsyncMode, toolshared.AsyncDeliveryUserOnly)
	}
}

func TestToolResultWithTaskID(t *testing.T) {
	result := toolshared.AsyncResult("async task started").WithTaskID(" subagent-7 ")

	if result.Control.TaskID != "subagent-7" {
		t.Fatalf("AsyncTaskID = %q, want subagent-7", result.Control.TaskID)
	}
}

func TestToolResultContentForLLMIncludesDeliverableOutcome(t *testing.T) {
	result := toolshared.NewToolResult("child finished").WithDeliverable(&taskresult.Deliverable{
		Text: "recipe text",
		Artifacts: []taskresult.Artifact{
			{
				Ref:         "media://video",
				Kind:        "video",
				Filename:    "clip.mp4",
				ContentType: "video/mp4",
			},
		},
		ObjectiveOutcome: &taskresult.Outcome{
			Status: taskresult.OutcomePartial,
			CompletedItems: []taskresult.Item{{
				Item: "Yakima published", Kind: "external_action",
				Receipts: []taskresult.Receipt{{ID: "inv_yakima", Tool: "browser_act"}},
			}},
			MissingItems: []string{"Vissani not published"},
		},
	})

	content := result.ContentForLLM()
	if !strings.Contains(content, "child finished") {
		t.Fatalf("expected base content, got %q", content)
	}
	if !strings.Contains(content, "Structured deliverable:") {
		t.Fatalf("expected structured deliverable note, got %q", content)
	}
	if !strings.Contains(content, `"text":"recipe text"`) {
		t.Fatalf("expected deliverable text JSON, got %q", content)
	}
	if !strings.Contains(content, `"ref":"media://video"`) {
		t.Fatalf("expected deliverable artifact JSON, got %q", content)
	}
	if !strings.Contains(content, `"kind":"video"`) {
		t.Fatalf("expected deliverable artifact kind JSON, got %q", content)
	}
	if !strings.Contains(content, `"status":"partial"`) ||
		!strings.Contains(content, `"id":"inv_yakima"`) ||
		!strings.Contains(content, `"missing_items":["Vissani not published"]`) {
		t.Fatalf("expected verified objective outcome JSON, got %q", content)
	}
	if len(result.Deliverable.Artifacts) != 1 || result.Deliverable.Artifacts[0].Ref != "media://video" {
		t.Fatalf("unexpected deliverable: %+v", result.Deliverable)
	}
	if result.Deliverable.ObjectiveOutcome == nil ||
		result.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomePartial {
		t.Fatalf("deliverable objective outcome was lost: %+v", result.Deliverable)
	}
}

func TestToolResultContentForLLMIncludesDeliverable(t *testing.T) {
	result := toolshared.NewToolResult("tool finished").WithDeliverable(&taskresult.Deliverable{
		Text: "saved recipe",
		Artifacts: []taskresult.Artifact{
			{
				Ref:         "file:/tmp/recipe.md",
				Kind:        "file",
				Filename:    "recipe.md",
				ContentType: "text/markdown",
			},
		},
		Metadata: map[string]string{"source": "instagram"},
	})

	content := result.ContentForLLM()
	for _, want := range []string{
		"tool finished",
		"Structured deliverable:",
		`"text":"saved recipe"`,
		`"ref":"file:/tmp/recipe.md"`,
		`"kind":"file"`,
		`"source":"instagram"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in content:\n%s", want, content)
		}
	}
}

func TestToolResultContentForLLMIncludesDeliverableReport(t *testing.T) {
	result := toolshared.NewToolResult("tool finished").WithDeliverable(&taskresult.Deliverable{
		Report: &taskresult.Report{
			SchemaVersion: "deliverable_report.v1",
			ReportID:      "review-1",
			Summary:       "No high-confidence issues found",
			Claims: []taskresult.Claim{{
				Kind:       "negative_evidence",
				Text:       "No correctness issues found",
				Confidence: "high",
			}},
		},
	})

	content := result.ContentForLLM()
	for _, want := range []string{
		"tool finished",
		"Structured deliverable:",
		`"report_id":"review-1"`,
		`"summary":"No high-confidence issues found"`,
		`"kind":"negative_evidence"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in content:\n%s", want, content)
		}
	}
}

func TestMediaResultCreatesDeliverable(t *testing.T) {
	result := toolshared.MediaResult("media ready", []string{"media://one", "media://two"})

	if result.Deliverable == nil {
		t.Fatal("expected media result to include deliverable")
	}
	if len(result.Deliverable.Artifacts) != 2 {
		t.Fatalf("artifact count = %d, want 2", len(result.Deliverable.Artifacts))
	}
	if result.Deliverable.Artifacts[0].Kind != "" {
		t.Fatalf("artifact kind = %q, want inference at the delivery edge", result.Deliverable.Artifacts[0].Kind)
	}
}

func TestErrorResult(t *testing.T) {
	result := toolshared.ErrorResult("operation failed")

	if result.ForLLM != "operation failed" {
		t.Errorf("Expected ForLLM 'operation failed', got '%s'", result.ForLLM)
	}
	if result.Delivery.IsSilent() {
		t.Error("Expected Silent to be false")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if result.Control.Async {
		t.Error("Expected Async to be false")
	}
}

func TestUserResult(t *testing.T) {
	content := "user visible message"
	result := toolshared.UserResult(content)

	if result.ForLLM != content {
		t.Errorf("Expected ForLLM '%s', got '%s'", content, result.ForLLM)
	}
	if result.ForUser != content {
		t.Errorf("Expected ForUser '%s', got '%s'", content, result.ForUser)
	}
	if result.Delivery.IsSilent() {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Control.Async {
		t.Error("Expected Async to be false")
	}
}

func TestToolResultJSONSerialization(t *testing.T) {
	tests := []struct {
		name   string
		result *toolshared.ToolResult
	}{
		{
			name:   "basic result",
			result: toolshared.NewToolResult("basic content"),
		},
		{
			name:   "silent result",
			result: toolshared.SilentResult("silent content"),
		},
		{
			name:   "async result",
			result: toolshared.AsyncResult("async content"),
		},
		{
			name:   "error result",
			result: toolshared.ErrorResult("error content"),
		},
		{
			name:   "user result",
			result: toolshared.UserResult("user content"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.result)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			// Unmarshal back
			var decoded toolshared.ToolResult
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			// Verify fields match (Err should be excluded)
			if decoded.ForLLM != tt.result.ForLLM {
				t.Errorf("ForLLM mismatch: got '%s', want '%s'", decoded.ForLLM, tt.result.ForLLM)
			}
			if decoded.ForUser != tt.result.ForUser {
				t.Errorf("ForUser mismatch: got '%s', want '%s'", decoded.ForUser, tt.result.ForUser)
			}
			if decoded.Delivery.IsSilent() != tt.result.Delivery.IsSilent() {
				t.Errorf("Silent mismatch: got %v, want %v", decoded.Delivery.IsSilent(), tt.result.Delivery.IsSilent())
			}
			if decoded.IsError != tt.result.IsError {
				t.Errorf("IsError mismatch: got %v, want %v", decoded.IsError, tt.result.IsError)
			}
			if decoded.Control.Async != tt.result.Control.Async {
				t.Errorf("Async mismatch: got %v, want %v", decoded.Control.Async, tt.result.Control.Async)
			}
		})
	}
}

func TestToolResultWithErrors(t *testing.T) {
	err := errors.New("underlying error")
	result := toolshared.ErrorResult("error message").WithError(err)

	if result.Err == nil {
		t.Error("Expected Err to be set")
	}
	if result.Err.Error() != "underlying error" {
		t.Errorf("Expected Err message 'underlying error', got '%s'", result.Err.Error())
	}

	// Verify Err is not serialized
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("Failed to marshal: %v", marshalErr)
	}

	var decoded toolshared.ToolResult
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatalf("Failed to unmarshal: %v", unmarshalErr)
	}

	if decoded.Err != nil {
		t.Error("Expected Err to be nil after JSON round-trip (should not be serialized)")
	}
}

func TestToolResultJSONStructure(t *testing.T) {
	result := toolshared.UserResult("test content").
		WithTaskID("task-1").
		WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly).
		WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	result.Control.Async = true

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify JSON structure
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Stable output remains top-level.
	if _, ok := parsed["for_llm"]; !ok {
		t.Error("Expected 'for_llm' key in JSON")
	}
	if _, ok := parsed["for_user"]; !ok {
		t.Error("Expected 'for_user' key in JSON")
	}
	if _, ok := parsed["is_error"]; !ok {
		t.Error("Expected 'is_error' key in JSON")
	}
	control, ok := parsed["control"].(map[string]any)
	if !ok || control["async"] != true || control["task_id"] != "task-1" {
		t.Fatalf("control = %#v", parsed["control"])
	}
	delivery, ok := parsed["delivery"].(map[string]any)
	if !ok || delivery["intent"] != string(toolshared.DeliveryImmediateContinue) ||
		delivery["async_mode"] != string(toolshared.AsyncDeliveryUserOnly) {
		t.Fatalf("delivery = %#v", parsed["delivery"])
	}

	for _, removed := range []string{
		"err", "silent", "async", "response_handled", "immediate_delivery", "delivery_intent", "async_delivery",
	} {
		if _, exists := parsed[removed]; exists {
			t.Fatalf("removed flat field %q was serialized: %#v", removed, parsed)
		}
	}

	// Verify values
	if parsed["for_llm"] != "test content" {
		t.Errorf("Expected for_llm 'test content', got %v", parsed["for_llm"])
	}
}

func TestToolResultControlAndDeliveryDoNotMutateOutput(t *testing.T) {
	result := toolshared.UserResult("stable output").WithDeliverable(&taskresult.Deliverable{Text: "artifact output"})
	wantForLLM := result.ForLLM
	wantForUser := result.ForUser
	wantDeliverable := taskresult.CloneDeliverable(result.Deliverable)

	result.Control = toolshared.ToolControl{Async: true, TaskID: "task-7"}
	result.Delivery = toolshared.ToolDelivery{
		Intent:    toolshared.DeliveryFinalHandled,
		AsyncMode: toolshared.AsyncDeliveryUserOnly,
	}

	if result.ForLLM != wantForLLM || result.ForUser != wantForUser ||
		!reflect.DeepEqual(result.Deliverable, wantDeliverable) {
		t.Fatalf("directives mutated output: %#v", result)
	}
	if !result.Delivery.IsFinalHandled() || result.Delivery.IsImmediate() || result.Delivery.IsSilent() {
		t.Fatalf("delivery intent is not exclusive: %#v", result.Delivery)
	}
}

func TestToolResultContentForLLM_AppendsHandledDeliveryNote(t *testing.T) {
	result := toolshared.MediaResult("Screenshot attached.", []string{"media://example"}).
		WithDeliveryIntent(toolshared.DeliveryFinalHandled)

	content := result.ContentForLLM()
	if !strings.Contains(content, "Screenshot attached.") {
		t.Fatalf("expected original content in ContentForLLM, got %q", content)
	}
	if !strings.Contains(content, toolshared.HandledToolLLMNote) {
		t.Fatalf("expected handled delivery note in ContentForLLM, got %q", content)
	}
}

func TestToolResultContentForLLM_UsesHandledDeliveryNoteWhenEmpty(t *testing.T) {
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)

	if got := result.ContentForLLM(); got != toolshared.HandledToolLLMNote {
		t.Fatalf("ContentForLLM() = %q, want %q", got, toolshared.HandledToolLLMNote)
	}
}

func TestToolResultContentForLLM_AppendsArtifactPaths(t *testing.T) {
	result := &toolshared.ToolResult{
		ForLLM: "Artifact created.",
		Deliverable: &taskresult.Deliverable{Artifacts: []taskresult.Artifact{{
			Ref: "file:/tmp/example.png", LocalPath: "/tmp/example.png", Kind: "file",
		}}},
	}

	content := result.ContentForLLM()
	if !strings.Contains(content, "Artifact created.") {
		t.Fatalf("expected original content in ContentForLLM, got %q", content)
	}
	if !strings.Contains(content, "Local artifact paths: [file:/tmp/example.png]") {
		t.Fatalf("expected artifact path note in ContentForLLM, got %q", content)
	}
	if !strings.Contains(content, toolshared.ArtifactPathsLLMNote) {
		t.Fatalf("expected artifact guidance note in ContentForLLM, got %q", content)
	}
}
