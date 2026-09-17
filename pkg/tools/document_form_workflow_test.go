package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type workflowTestAuditor struct {
	proposal document.FormAuditProposal
	values   []string
	calls    []string
}

func (auditor *workflowTestAuditor) AuditForm(
	_ context.Context,
	model string,
	view document.FormAuditView,
) (document.FormAuditProposal, error) {
	auditor.calls = append(auditor.calls, model)
	for _, field := range view.Fields {
		auditor.values = append(auditor.values, field.Value.Text)
	}
	return auditor.proposal, nil
}

func TestDocumentFormWorkflowSurvivesRestartAndProducesRedactedReview(t *testing.T) {
	formStore, options := newWorkflowFormStore(t)
	mediaIndex := filepath.Join(t.TempDir(), "media", "index.json")
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(mediaIndex, media.MediaCleanerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRef, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "application/pdf",
		Source: "test", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(sourceRef, owner); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(sourceBytes)
	auditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	policy := document.FormAuditPolicy{PrimaryModel: "document-deliberative"}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(policy, auditor),
	)
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)

	startCtx := workflowToolContext(t, "execution-start", "call-start", []string{sourceRef})
	started := tool.Execute(startCtx, map[string]any{
		"action": "form", "form_action": "start", "source": sourceRef,
	})
	if started.IsError || started.Control.Suspension == nil ||
		started.Control.Suspension.ProtectedAnswer == nil {
		t.Fatalf("start result = %#v", started)
	}
	startProjection := decodeWorkflowResult(t, started.ForLLM)
	if startProjection.Job == nil || startProjection.Job.State != document.FormJobPrepared ||
		startProjection.NextField == nil || startProjection.NextField.FieldID != schema.Fields[0].ID {
		t.Fatalf("start projection = %#v", startProjection)
	}

	sink, err := document.NewFormProtectedAnswerSink(formStore)
	if err != nil {
		t.Fatal(err)
	}
	route := workflowInteractionRoute()
	privateValue := "MINTCLAW_PDF3_WORKFLOW_PRIVATE_6f81"
	receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: *started.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", IdempotencyKey: "message-workflow-1",
		Intent: interactions.ProtectedAnswerValue, Text: privateValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: *started.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.ReleaseAll("inbound-form"); err != nil {
		t.Fatal(err)
	}
	sink.Close()
	mediaStore.Stop()

	reopened, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	restartedMediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		mediaIndex,
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restartedMediaStore.Stop)
	restartedAuditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	restarted := NewDocumentTool(
		WithDocumentFormJobStore(reopened),
		WithDocumentFormAudit(policy, restartedAuditor),
	)
	restarted.SetMediaStore(restartedMediaStore)
	restarted.formSchema = workflowSchemaResolver(schema)
	continued := restarted.Execute(
		workflowToolContext(t, "execution-continue", "call-continue", nil),
		map[string]any{
			"action": "form", "form_action": "continue",
			"event_id": receipt.Reference,
		},
	)
	if continued.IsError || continued.Control.Suspension != nil {
		t.Fatalf("continued result = %#v", continued)
	}
	continuedProjection := decodeWorkflowResult(t, continued.ForLLM)
	if continuedProjection.Job == nil || continuedProjection.Job.State != document.FormJobReviewReady ||
		continuedProjection.Review == nil || !continuedProjection.Review.Ready ||
		continuedProjection.Review.ReviewDigest == "" ||
		!slicesEqualStrings(restartedAuditor.calls, []string{"document-deliberative"}) ||
		!slicesEqualStrings(restartedAuditor.values, []string{privateValue}) {
		t.Fatalf("continued projection = %#v, audit=%#v", continuedProjection, restartedAuditor)
	}
	if strings.Contains(continued.ForLLM, privateValue) {
		t.Fatal("protected form value leaked into tool result")
	}
	stateBytes, err := os.ReadFile(filepath.Join(options.StateRoot, "document_form_jobs", "form_jobs.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), privateValue) {
		t.Fatal("protected form value leaked into durable job snapshot")
	}

	status := restarted.Execute(
		workflowToolContext(t, "execution-status", "call-status", nil),
		map[string]any{
			"action": "form", "form_action": "status", "job_id": startProjection.Job.JobID,
		},
	)
	statusProjection := decodeWorkflowResult(t, status.ForLLM)
	if status.IsError || statusProjection.Review == nil || !statusProjection.Review.Ready ||
		statusProjection.Review.ReviewDigest != continuedProjection.Review.ReviewDigest {
		t.Fatalf("restart/compaction-independent status = %#v", status)
	}
}

func TestDocumentFormWorkflowFailsClosedWithoutAuditRole(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	mediaStore := media.NewFileMediaStore()
	tool := NewDocumentTool(WithDocumentFormJobStore(formStore))
	tool.SetMediaStore(mediaStore)
	result := tool.Execute(
		workflowToolContext(t, "execution", "call", []string{"media://missing"}),
		map[string]any{"action": "form", "form_action": "start", "source": "media://missing"},
	)
	if !result.IsError || !strings.Contains(result.ForLLM, `"code":"audit_unavailable"`) ||
		strings.Contains(result.ForLLM, "media://missing") {
		t.Fatalf("missing audit result = %#v", result)
	}
}

func TestDocumentFormWorkflowArgumentsAreCompactAndStrict(t *testing.T) {
	valid := []map[string]any{
		{"action": "form", "form_action": "start", "source": "media://source"},
		{"action": "form", "form_action": "continue", "job_id": "job"},
		{"action": "form", "form_action": "continue", "event_id": "form_answer.form_job_a.form_value_b"},
		{
			"action":      "form",
			"form_action": "continue",
			"job_id":      "job",
			"event_id":    "form_answer.form_job_a.form_value_b",
		},
		{"action": "form", "form_action": "status", "job_id": "job"},
		{"action": "form", "form_action": "correct", "job_id": "job", "field_id": "field"},
		{"action": "form", "form_action": "cancel", "job_id": "job"},
	}
	for _, args := range valid {
		if err := validateDocumentActionOptions("form", args); err != nil {
			t.Fatalf("valid form args %#v: %v", args, err)
		}
	}
	invalid := []map[string]any{
		{"action": "form", "form_action": "start", "job_id": "job"},
		{"action": "form", "form_action": "continue"},
		{"action": "form", "form_action": "status", "job_id": "job", "event_id": "event"},
		{"action": "form", "form_action": "correct", "job_id": "job"},
		{"action": "form", "form_action": "unknown", "job_id": "job"},
		{"action": "form", "form_action": "cancel", "job_id": "job", "path": "/secret"},
	}
	for _, args := range invalid {
		if err := validateDocumentActionOptions("form", args); err == nil {
			t.Fatalf("invalid form args admitted: %#v", args)
		}
	}
}

func newWorkflowFormStore(t *testing.T) (*document.FormJobStore, document.FormJobStoreOptions) {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	keyRoot := filepath.Join(root, "keys")
	for _, directory := range []string{stateRoot, keyRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options := document.FormJobStoreOptions{
		StateRoot: stateRoot, KeyRoot: keyRoot,
		Retention:         document.DefaultFormJobRetention,
		TerminalRetention: document.DefaultFormJobTerminalRetention,
		MaxJobs:           document.DefaultFormJobMaxJobs, MaxEventsPerJob: document.DefaultFormJobMaxEvents,
		MaxBytes: document.DefaultFormJobMaxBytes,
	}
	store, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	return store, options
}

func workflowTestSchema(source []byte) document.FormFieldsFacts {
	sourceDigest := sha256.Sum256(source)
	fieldDigest := sha256.Sum256([]byte("workflow-field"))
	widgetDigest := sha256.Sum256([]byte("workflow-widget"))
	return document.FormFieldsFacts{
		SourceSHA256: hex.EncodeToString(sourceDigest[:]),
		Backend: document.BackendIdentity{
			Name: document.PDFCPUBackendName, Version: document.PDFCPUBackendVersion,
			Role: "production", IsolationMode: "one_shot_process",
		},
		Limits: document.FormFieldLimits{
			MaxFields: document.DefaultMaxFormFields, MaxWidgets: document.DefaultMaxFieldWidgets,
			MaxOptions: document.DefaultMaxFieldOptions, MaxTextBytes: document.DefaultMaxFieldTextBytes,
			MaxReportBytes: document.DefaultMaxFormReportBytes,
		},
		Fields: []document.FormField{{
			ID: "field_" + hex.EncodeToString(fieldDigest[:]), Name: "Legal name", Kind: document.FormFieldText,
			Required: true, Widgets: []document.FormFieldWidget{{
				ID: "widget_" + hex.EncodeToString(widgetDigest[:]), Page: 1, Ordinal: 1,
			}},
		}},
	}
}

func workflowSchemaResolver(schema document.FormFieldsFacts) documentFormSchemaResolver {
	return func(
		_ context.Context,
		store ownedDocumentMediaStore,
		ref string,
		owner media.MediaOwner,
	) (document.FormFieldsFacts, error) {
		source, err := store.OpenOwned(ref, owner)
		if err != nil {
			return document.FormFieldsFacts{}, err
		}
		_ = source.Close()
		return schema, nil
	}
}

func workflowToolContext(t *testing.T, executionID, callID string, refs []string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", executionID)
	ctx = toolshared.WithToolSessionContext(ctx, "agent", "session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "route")
	ctx = toolshared.WithToolCallID(ctx, callID)
	ctx = toolshared.WithToolContext(ctx, "telegram", "chat")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat", ChatType: "direct",
		SenderID: "actor", ActorID: "actor", MessageID: "message-" + executionID,
	})
	return toolshared.WithToolDocumentContext(ctx, refs, true)
}

func workflowInteractionRoute() interactions.Route {
	return interactions.Route{
		AgentID: "agent", SessionKey: "session", RouteSessionKey: "route",
		Channel: "telegram", AccountID: "primary", ChatID: "chat", ChatType: "direct",
		SenderID: "actor",
	}
}

func decodeWorkflowResult(t *testing.T, content string) safeDocumentFormResult {
	t.Helper()
	var projection safeDocumentFormResult
	if err := json.Unmarshal([]byte(content), &projection); err != nil {
		t.Fatalf("decode workflow result: %v\n%s", err, content)
	}
	return projection
}

func slicesEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
