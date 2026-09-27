package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		mediaIndex,
		media.MediaCleanerConfig{MaxAge: time.Nanosecond},
	)
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
		Filename: "source.pdf", ContentType: "",
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
	policy := document.FormAuditPolicy{
		PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
	}
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
	if started.IsError || started.Control.Suspension != nil || !started.Control.PreserveToolVisibility {
		t.Fatalf("start result = %#v", started)
	}
	startProjection := decodeWorkflowResult(t, started.ForLLM)
	if startProjection.Job == nil || startProjection.Job.State != document.FormJobPrepared ||
		startProjection.Mapping == nil || startProjection.Mapping.NextUnresolvedID != schema.Fields[0].ID ||
		startProjection.NextField != nil {
		t.Fatalf("start projection = %#v", startProjection)
	}
	collected := tool.Execute(
		workflowToolContext(t, "execution-collect", "call-collect", nil),
		map[string]any{
			"action": "form", "form_action": "collect", "job_id": startProjection.Job.JobID,
			"field_id": schema.Fields[0].ID, "question": "What name should this PDF contain?",
		},
	)
	if collected.IsError || collected.Control.Suspension == nil ||
		collected.Control.Suspension.ProtectedAnswer == nil {
		t.Fatalf("collect result = %#v", collected)
	}
	collectedProjection := decodeWorkflowResult(t, collected.ForLLM)
	if collectedProjection.NextField == nil || collectedProjection.NextField.FieldID != schema.Fields[0].ID ||
		collected.Control.Suspension.Questions[0].Question != "What name should this PDF contain?" {
		t.Fatalf("collect projection = %#v", collectedProjection)
	}
	time.Sleep(time.Millisecond)
	if removed := mediaStore.CleanExpired(); removed != 1 {
		t.Fatalf("media cleanup removed = %d, want only the unpinned inbound source", removed)
	}

	sink, err := document.NewFormProtectedAnswerSink(formStore)
	if err != nil {
		t.Fatal(err)
	}
	route := workflowInteractionRoute()
	privateValue := "MINTCLAW_PDF3_WORKFLOW_PRIVATE_6f81"
	receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: *collected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", IdempotencyKey: "message-workflow-1",
		Intent: interactions.ProtectedAnswerValue, Text: privateValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: *collected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", Receipt: receipt,
	}); err != nil {
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
		media.MediaCleanerConfig{MaxAge: time.Nanosecond},
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
	time.Sleep(time.Millisecond)
	if removed := restartedMediaStore.CleanExpired(); removed != 0 {
		t.Fatalf("restart cleanup removed active workflow sources = %d", removed)
	}
	continued := restarted.Execute(
		workflowToolContext(t, "execution-continue", "call-continue", nil),
		map[string]any{
			"action": "form", "form_action": "continue",
			"answer_ref": receipt.Reference,
		},
	)
	if continued.IsError || continued.Control.Suspension != nil || !continued.Control.PreserveToolVisibility {
		t.Fatalf("continued result = %#v", continued)
	}
	continuedProjection := decodeWorkflowResult(t, continued.ForLLM)
	if continuedProjection.Job == nil || continuedProjection.Job.State != document.FormJobCollecting ||
		continuedProjection.Mapping == nil || !continuedProjection.Mapping.ReadyForReview ||
		continuedProjection.Review != nil || continuedProjection.NextField != nil ||
		len(restartedAuditor.calls) != 0 {
		t.Fatalf("continued projection = %#v, audit=%#v", continuedProjection, restartedAuditor)
	}
	reviewed := restarted.Execute(
		workflowToolContext(t, "execution-review", "call-review", nil),
		map[string]any{
			"action": "form", "form_action": "review", "job_id": startProjection.Job.JobID,
		},
	)
	reviewedProjection := decodeWorkflowResult(t, reviewed.ForLLM)
	if reviewed.IsError || !reviewed.Control.PreserveToolVisibility || reviewedProjection.Job == nil ||
		reviewedProjection.Job.State != document.FormJobReviewReady || reviewedProjection.Review == nil ||
		!reviewedProjection.Review.Ready || reviewedProjection.Review.ReviewDigest == "" ||
		!slicesEqualStrings(restartedAuditor.calls, []string{"document-deliberative"}) ||
		!slicesEqualStrings(restartedAuditor.values, []string{privateValue}) {
		t.Fatalf("reviewed projection = %#v, audit=%#v", reviewedProjection, restartedAuditor)
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
		statusProjection.Review.ReviewDigest != reviewedProjection.Review.ReviewDigest {
		t.Fatalf("restart/compaction-independent status = %#v", status)
	}
	formOwner, err := documentFormOwner(workflowToolContext(t, "execution-owner", "call-owner", nil))
	if err != nil {
		t.Fatal(err)
	}
	retainedRef, err := reopened.SourceRef(t.Context(), startProjection.Job.JobID, formOwner)
	if err != nil {
		t.Fatal(err)
	}
	canceled := restarted.Execute(
		workflowToolContext(t, "execution-cancel", "call-cancel", nil),
		map[string]any{
			"action": "form", "form_action": "cancel", "job_id": startProjection.Job.JobID,
		},
	)
	if canceled.IsError {
		t.Fatalf("cancel result = %#v", canceled)
	}
	if _, err = restartedMediaStore.Resolve(retainedRef); err == nil {
		t.Fatal("canceled workflow retained its immutable source")
	}
	terminalStatus := restarted.Execute(
		workflowToolContext(t, "execution-terminal-status", "call-terminal-status", nil),
		map[string]any{
			"action": "form", "form_action": "status", "job_id": startProjection.Job.JobID,
		},
	)
	terminalProjection := decodeWorkflowResult(t, terminalStatus.ForLLM)
	if terminalStatus.IsError || terminalProjection.FormAction != "status" ||
		terminalProjection.Job == nil || terminalProjection.Job.State != document.FormJobCanceled ||
		terminalProjection.Review != nil || terminalProjection.Commit != nil {
		t.Fatalf("canceled terminal status = %#v projection=%#v", terminalStatus, terminalProjection)
	}
}

func TestPrepareDocumentFormSourceTreatsTransportContentTypeAsAdvisory(t *testing.T) {
	for _, contentType := range []string{"", "application/pdf", "application/octet-stream", "image/jpeg"} {
		contentType := contentType
		t.Run(contentType, func(t *testing.T) {
			store, err := media.NewFileMediaStoreWithPersistentIndex(
				filepath.Join(t.TempDir(), "media", "index.json"),
				media.MediaCleanerConfig{},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Stop)
			owner := documentToolTestOwner(t)
			sourceBytes := []byte("%PDF-1.7\nmetadata-independent form source\n%%EOF\n")
			sourcePath := filepath.Join(t.TempDir(), "source.pdf")
			if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			ref, storeErr := store.Store(sourcePath, media.MediaMeta{
				Filename: "source.pdf", ContentType: contentType, Source: "test",
				CleanupPolicy: media.CleanupPolicyForgetOnly,
			}, "inbound-form")
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if err = store.BindOwner(ref, owner); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(sourceBytes)
			prepared, prepareErr := prepareDocumentFormSource(
				store,
				owner,
				ref,
				"metadata-independent-"+contentType,
				hex.EncodeToString(digest[:]),
			)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			t.Cleanup(func() { _ = os.Remove(prepared.path) })
			retained, readErr := os.ReadFile(prepared.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(retained) != string(sourceBytes) {
				t.Fatal("retained source bytes differ from inspected source")
			}
		})
	}
}

func TestPrepareDocumentFormSourceFailsClosedOnAuthorityAndDigestMismatch(t *testing.T) {
	store, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}

	wrongOwner, err := media.NewMediaOwner(
		"workspace", "agent", "different-actor", "route", "session", "telegram", "chat", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actualDigest := sha256.Sum256(sourceBytes)
	_, err = prepareDocumentFormSource(
		store,
		wrongOwner,
		ref,
		"wrong-owner",
		hex.EncodeToString(actualDigest[:]),
	)
	assertDocumentFormRetentionPhase(t, err, "source_open")

	_, err = prepareDocumentFormSource(
		store,
		owner,
		ref,
		"wrong-digest",
		strings.Repeat("0", sha256.Size*2),
	)
	assertDocumentFormRetentionPhase(t, err, "inspected_identity")

	storedPath, err := store.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(storedPath, []byte("%PDF-1.7\ntampered\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = prepareDocumentFormSource(
		store,
		owner,
		ref,
		"changed-bytes",
		hex.EncodeToString(actualDigest[:]),
	)
	assertDocumentFormRetentionPhase(t, err, "source_open")
}

func TestDocumentFormWorkflowKeepsStableFailureForInspectedDigestMismatch(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(sourceBytes)
	schema.SourceSHA256 = strings.Repeat("0", sha256.Size*2)
	auditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(document.FormAuditPolicy{
			PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
		}, auditor),
	)
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)

	result := tool.Execute(
		workflowToolContext(t, "execution-digest-mismatch", "call-digest-mismatch", []string{ref}),
		map[string]any{"action": "form", "form_action": "start", "source": ref},
	)
	projection := decodeWorkflowResult(t, result.ForLLM)
	if !result.IsError || result.Control.Suspension != nil || projection.Failure == nil {
		t.Fatalf("start result = %#v; projection = %#v", result, projection)
	}
	if projection.Failure.Code != "protected_store_unavailable" ||
		projection.Failure.Message != "the immutable form source could not be retained" {
		t.Fatalf("failure = %#v", projection.Failure)
	}
}

func assertDocumentFormRetentionPhase(t *testing.T, err error, expected string) {
	t.Helper()
	var failure *documentFormSourceRetentionError
	if !errors.As(err, &failure) {
		t.Fatalf("retention error = %v, want typed safe failure", err)
	}
	if failure.phase != expected {
		t.Fatalf("retention phase = %q, want %q", failure.phase, expected)
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

func TestDocumentFormStatusRecoversTerminalTransitionDuringSchemaLoad(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	ctx := workflowToolContext(t, "execution-terminal-race", "call-terminal-race", nil)
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err := formStore.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "terminal-race-start", SourceRef: "media://terminal-race-source",
		SourceDigest: strings.Repeat("a", 64), FieldSchemaDigest: strings.Repeat("b", 64),
		BackendRevision: "pdfcpu-v0.15.0-mintclaw-write-v1", AuditPolicyRevision: "document-audit-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	tool := NewDocumentTool(WithDocumentFormJobStore(formStore))
	tool.SetMediaStore(media.NewFileMediaStore())
	tool.formSchema = func(
		context.Context,
		ownedDocumentMediaStore,
		string,
		media.MediaOwner,
	) (document.FormFieldsFacts, error) {
		if _, cancelErr := formStore.Cancel(
			t.Context(),
			created.JobID,
			created.Revision,
			owner,
		); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		return document.FormFieldsFacts{}, errors.New("retained source disappeared during terminal transition")
	}

	status := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "status", "job_id": created.JobID,
	})
	projection := decodeWorkflowResult(t, status.ForLLM)
	if status.IsError || projection.FormAction != "status" || projection.Job == nil ||
		projection.Job.State != document.FormJobCanceled || projection.Review != nil || projection.Commit != nil {
		t.Fatalf("terminal transition status = %#v projection=%#v", status, projection)
	}
}

func TestDocumentFormWorkflowArgumentsAreCompactAndStrict(t *testing.T) {
	valid := []map[string]any{
		{"action": "form", "form_action": "start", "source": "media://source"},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "What value belongs here?",
		},
		{"action": "form", "form_action": "continue", "answer_ref": "form_answer.form_job_a.form_value_b"},
		{"action": "form", "form_action": "continue", "event_id": "form_answer.form_job_a.form_value_b"},
		{"action": "form", "form_action": "status", "job_id": "job"},
		{
			"action": "form", "form_action": "correct", "job_id": "job", "field_id": "field",
			"question": "What should replace the current value?",
		},
		{"action": "form", "form_action": "review", "job_id": "job"},
		{"action": "form", "form_action": "commit", "job_id": "job"},
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
		{
			"action": "form", "form_action": "continue", "answer_ref": "form_answer.form_job_a.form_value_b",
			"event_id": "form_answer.form_job_a.form_value_b",
		},
		{"action": "form", "form_action": "status", "job_id": "job", "event_id": "event"},
		{"action": "form", "form_action": "correct", "job_id": "job"},
		{"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field"},
		{"action": "form", "form_action": "commit", "source": "media://source"},
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
