package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	agenttools "github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestRecoveredDocumentDeliveryPublishesAndSettlesExactIntent(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "document-writes")
	outboxRoot := t.TempDir()
	documentTool := agenttools.NewDocumentTool(
		agenttools.WithDocumentStateRoot(stateRoot),
		agenttools.WithDocumentLocalPathPolicy(workspace, true, nil),
	)
	al := documentRecoveryAgentLoop(workspace, documentTool)
	first, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	owner, operationID, record := primeRecoveredDocumentJournal(t, stateRoot)
	admission, err := first.AdmitMedia(
		workspace,
		documentRecoveryIdentity(operationID),
		documentRecoveryMessage(record, owner, operationID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	al.SetOutboundOutbox(second)
	recovered, err := second.Recover()
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovered admissions = %#v, err=%v", recovered, err)
	}
	publish, err := al.ReconcileRecoveredOutboundAdmission(recovered[0], time.Now().UTC())
	if err != nil || !publish {
		t.Fatalf("reconcile recovered document = %t, %v", publish, err)
	}
	assertRecoveredDocumentState(
		t, stateRoot, owner, operationID, document.WriteDeliveryPending, admission.Intent.ID,
	)
	if err = second.PrepareAdmission(recovered[0].Lease); err != nil {
		t.Fatal(err)
	}
	if err = second.CommitAdmission(recovered[0].Lease); err != nil {
		t.Fatal(err)
	}
	if err = second.BeginAttempt(recovered[0].Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = second.MarkDelivered(recovered[0].Intent.ID, outbox.Outcome{
		PlatformMessageIDs: []string{"remote-document-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err = al.SettleRecoveredOutboundAdmission(t.Context(), recovered[0]); err != nil {
		t.Fatal(err)
	}
	assertRecoveredDocumentState(
		t, stateRoot, owner, operationID, document.WriteDelivered, admission.Intent.ID,
	)
	if record.OutboxDeliveryID != "" {
		t.Fatalf("pre-crash document unexpectedly had outbox ID %q", record.OutboxDeliveryID)
	}
}

func TestRecoveredDocumentDeliveryDoesNotReplayTerminalOperation(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "document-writes")
	outboxRoot := t.TempDir()
	documentTool := agenttools.NewDocumentTool(
		agenttools.WithDocumentStateRoot(stateRoot),
		agenttools.WithDocumentLocalPathPolicy(workspace, true, nil),
	)
	al := documentRecoveryAgentLoop(workspace, documentTool)
	first, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	owner, operationID, record := primeRecoveredDocumentJournal(t, stateRoot)
	admission, err := first.AdmitMedia(
		workspace,
		documentRecoveryIdentity(operationID),
		documentRecoveryMessage(record, owner, operationID),
	)
	if err != nil {
		t.Fatal(err)
	}
	record = bindRecoveredDocumentOutbox(t, stateRoot, owner, operationID, record, admission.Intent.ID)
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.Transition(t.Context(), operationID, owner, document.WriteTransition{
		ExpectedRevision: record.Revision, State: document.WriteDelivered,
	}); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	al.SetOutboundOutbox(second)
	recovered, err := second.Recover()
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovered admissions = %#v, err=%v", recovered, err)
	}
	publish, err := al.ReconcileRecoveredOutboundAdmission(recovered[0], time.Now().UTC())
	if err != nil || publish {
		t.Fatalf("terminal recovered document = %t, %v", publish, err)
	}
	intent, err := second.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("abandoned outbox intent = %#v, err=%v", intent, err)
	}
	assertRecoveredDocumentState(
		t, stateRoot, owner, operationID, document.WriteDelivered, admission.Intent.ID,
	)
}

func TestRecoveredExpiredFormDeliverySettlesBeforeAcknowledgement(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "document-writes")
	formRoot := t.TempDir()
	formStateRoot := filepath.Join(formRoot, "state")
	formKeyRoot := filepath.Join(formRoot, "keys")
	for _, directory := range []string{formStateRoot, formKeyRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	formStore, err := document.OpenFormJobStore(document.FormJobStoreOptions{
		StateRoot: formStateRoot, KeyRoot: formKeyRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(formStore.Close)
	formOwner, delivering := primeExpiringRecoveredForm(t, formStore)
	writeOwner, writeRecord := primeRecoveredDocumentJournalAt(
		t, stateRoot, delivering.OperationID, delivering.ArtifactRef,
	)
	documentTool := agenttools.NewDocumentTool(
		agenttools.WithDocumentStateRoot(stateRoot),
		agenttools.WithDocumentFormJobStore(formStore),
	)
	al := documentRecoveryAgentLoop(workspace, documentTool)
	outboxRoot := t.TempDir()
	first, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	message := documentRecoveryMessage(writeRecord, writeOwner, delivering.OperationID)
	message.Recovery.DomainJobID = delivering.JobID
	message.Recovery.DomainOwnerDigest = delivering.OwnerDigest
	admission, err := first.AdmitMedia(
		workspace,
		documentRecoveryIdentity(delivering.OperationID),
		message,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}

	wait := time.Until(time.UnixMilli(delivering.ExpiresAt)) + 25*time.Millisecond
	if wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	second, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	al.SetOutboundOutbox(second)
	recovered, err := second.Recover()
	if err != nil || len(recovered) != 1 || !recovered[0].Dispatch {
		t.Fatalf("recovered admission = %#v, %v", recovered, err)
	}
	publish, err := al.ReconcileRecoveredOutboundAdmission(recovered[0], time.Now().UTC())
	if err != nil || publish {
		t.Fatalf("expired recovered form = publish:%t, err:%v", publish, err)
	}
	intent, err := second.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned || !intent.RecoverySettled {
		t.Fatalf("settled abandoned intent = %#v, %v", intent, err)
	}
	assertRecoveredDocumentState(
		t, stateRoot, writeOwner, delivering.OperationID, document.WriteDeliveryFailed, admission.Intent.ID,
	)
	settled, err := formStore.Get(t.Context(), delivering.JobID, formOwner)
	if err != nil || settled.State != document.FormJobFailed ||
		settled.FailureCode != string(document.FailureDeliveryFailed) || len(settled.Fields) != 0 {
		t.Fatalf("settled expired form = %#v, %v", settled, err)
	}
	if recovered, err = second.Recover(); err != nil || len(recovered) != 0 {
		t.Fatalf("recovery after settlement = %#v, %v", recovered, err)
	}
}

func TestRecoveredDocumentToolMatchesWorkspaceAlias(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("create workspace alias: %v", err)
	}
	documentTool := agenttools.NewDocumentTool()
	al := documentRecoveryAgentLoop(workspace, documentTool)

	got, err := al.recoveredDocumentTool(outbox.Intent{
		OwnerWorkspace: alias,
		Media: &bus.OutboundMediaMessage{Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryDocumentFill,
		}},
	})
	if err != nil || got != documentTool {
		t.Fatalf("recoveredDocumentTool() = %p, %v; want %p", got, err, documentTool)
	}
}

func documentRecoveryAgentLoop(workspace string, documentTool *agenttools.DocumentTool) *AgentLoop {
	registry := agenttools.NewToolRegistry()
	registry.RegisterHidden(documentTool)
	return &AgentLoop{registry: &AgentRegistry{agents: map[string]*AgentInstance{
		"main": {ID: "main", Workspace: workspace, Tools: registry},
	}}}
}

func primeRecoveredDocumentJournal(
	t *testing.T,
	stateRoot string,
) (document.Authority, string, document.WriteOperationRecord) {
	t.Helper()
	operationID := document.NewWriteOperationID()
	owner, record := primeRecoveredDocumentJournalAt(
		t,
		stateRoot,
		operationID,
		"media://"+uuid.NewString(),
	)
	return owner, operationID, record
}

func primeRecoveredDocumentJournalAt(
	t *testing.T,
	stateRoot string,
	operationID string,
	artifactRef string,
) (document.Authority, document.WriteOperationRecord) {
	t.Helper()
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := document.Authority{
		Kind: "inbound_media", WorkspaceID: "workspace_1", AgentID: "agent_1",
		ActorID: "actor_1", RouteID: "route_1", SessionID: "session_1",
	}
	value := "protected"
	request := document.NormalizedFillRequest{
		SchemaVersion: document.NormalizedFillSchemaVersion,
		SourceSHA256:  strings.Repeat("a", 64),
		Assignments: []document.FormFillAssignment{{
			FieldID: "field_" + strings.Repeat("b", 64),
			Value:   document.FormValue{Type: document.FormValueText, Text: &value},
		}},
		AffectedPages: []int{1},
	}
	request.RequestSHA256 = recoveredDocumentRequestSHA256(t, request)
	record, _, err := journal.Accept(t.Context(), operationID, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []document.WriteTransition{
		{State: document.WriteWriting},
		{State: document.WriteWritten, Artifact: &document.WriteArtifactEvidence{
			SHA256: strings.Repeat("c", 64), Size: 1024,
		}},
		{State: document.WriteVerifying},
		{State: document.WriteVerified, Verification: &document.WriteVerificationEvidence{
			StructuralAssertions: 1, VisualAssertions: 1, CheckedFields: 1,
			CheckedWidgets: 1, RenderedPages: 1,
		}},
		{State: document.WriteRegistered, ArtifactRef: artifactRef},
	} {
		transition.ExpectedRevision = record.Revision
		record, _, err = journal.Transition(t.Context(), operationID, owner, transition)
		if err != nil {
			t.Fatal(err)
		}
	}
	return owner, record
}

type recoveredFormAuditor struct{}

func (recoveredFormAuditor) AuditForm(
	context.Context,
	string,
	document.FormAuditView,
) (document.FormAuditProposal, error) {
	return document.FormAuditProposal{Decision: document.FormAuditPass}, nil
}

func primeExpiringRecoveredForm(
	t *testing.T,
	store *document.FormJobStore,
) (document.FormJobOwner, document.FormJobRecord) {
	t.Helper()
	owner := document.FormJobOwner{
		AgentID: "main", WorkspaceID: "workspace_1", RouteSessionKey: "pdf-session",
		Channel: "telegram", AccountID: "primary", ChatID: "pdf-chat", ChatType: "private",
		SenderID: "actor_1", SpaceType: "direct",
	}
	fieldDigest := sha256.Sum256([]byte("recovered-expiring-field"))
	widgetDigest := sha256.Sum256([]byte("recovered-expiring-widget"))
	fieldID := "field_" + hex.EncodeToString(fieldDigest[:])
	schema := document.FormFieldsFacts{
		SourceSHA256: strings.Repeat("a", 64),
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
			ID: fieldID, Name: "display-name", Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{
				ID: "widget_" + hex.EncodeToString(widgetDigest[:]), Page: 1, Ordinal: 1,
			}},
		}},
	}
	policy := document.FormAuditPolicy{
		PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
	}
	policyRevision, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	schemaDigest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backendRevision, err := document.FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "expiring-recovery", SourceRef: "media://private-source",
		SourceDigest: schema.SourceSHA256, FieldSchemaDigest: schemaDigest,
		BackendRevision: backendRevision, AuditPolicyRevision: policyRevision, Retention: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, event, err := store.AppendValue(t.Context(), document.FormJobAppendValueRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		FieldID: fieldID, IdempotencyKey: "expiring-answer",
		Value: document.FormProtectedValue{Kind: document.ProtectedValueText, Text: "private expiring value"},
		State: document.FormValueSupplied, Source: document.FormValueSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := store.MapFormField(t.Context(), document.FormFieldMappingRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, FieldID: fieldID, SourceEventID: event.EventID, IdempotencyKey: "expiring-map",
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := store.ReviewFormJob(t.Context(), document.FormReviewRequest{
		JobID: mapped.Job.JobID, ExpectedRevision: mapped.Job.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: recoveredFormAuditor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := document.FormCommitRequest{
		JobID: reviewed.Job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: document.FormCommitOutputPolicyRevision,
	}
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = awaiting.Revision
	committing, err := store.BeginFormCommit(t.Context(), request, binding)
	if err != nil {
		t.Fatal(err)
	}
	delivering, err := store.RecordFormCommitArtifact(t.Context(), document.FormCommitArtifactRequest{
		JobID: committing.JobID, ExpectedRevision: committing.Revision, Owner: owner,
		OperationID: committing.OperationID, ArtifactRef: "media://" + uuid.NewString(),
		ArtifactDigest: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner, delivering
}

func bindRecoveredDocumentOutbox(
	t *testing.T,
	stateRoot string,
	owner document.Authority,
	operationID string,
	record document.WriteOperationRecord,
	outboxDeliveryID string,
) document.WriteOperationRecord {
	t.Helper()
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = journal.Transition(t.Context(), operationID, owner, document.WriteTransition{
		ExpectedRevision: record.Revision,
		State:            document.WriteDeliveryPending,
		OutboxDeliveryID: outboxDeliveryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func documentRecoveryIdentity(operationID string) outbox.Identity {
	return outbox.Identity{
		SourceID: operationID, Ordinal: 0, Kind: outbox.KindMedia,
		Channel: "telegram", ChatID: "pdf-chat", SessionKey: "pdf-session",
	}
}

func documentRecoveryMessage(
	record document.WriteOperationRecord,
	owner document.Authority,
	operationID string,
) bus.OutboundMediaMessage {
	return bus.OutboundMediaMessage{
		Parts: []bus.MediaPart{{
			Type: "file", Ref: record.ArtifactRef,
			Filename: "filled-document.pdf", ContentType: "application/pdf",
		}},
		Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryDocumentFill, MediaRef: record.ArtifactRef,
			WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID, ActorID: owner.ActorID,
			RouteID: owner.RouteID, SessionID: owner.SessionID, AuthorityKind: owner.Kind,
			OperationID: operationID, DomainDeliveryID: record.DeliveryID,
		},
	}
}

func recoveredDocumentRequestSHA256(t *testing.T, request document.NormalizedFillRequest) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		SchemaVersion string                        `json:"schema_version"`
		SourceSHA256  string                        `json:"source_sha256"`
		Assignments   []document.FormFillAssignment `json:"assignments"`
		AffectedPages []int                         `json:"affected_pages"`
	}{
		SchemaVersion: request.SchemaVersion,
		SourceSHA256:  request.SourceSHA256,
		Assignments:   request.Assignments,
		AffectedPages: request.AffectedPages,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func assertRecoveredDocumentState(
	t *testing.T,
	stateRoot string,
	owner document.Authority,
	operationID string,
	want document.WriteOperationState,
	outboxDeliveryID string,
) {
	t.Helper()
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := journal.Lookup(context.Background(), operationID, owner)
	if err != nil || !found || record.State != want || record.OutboxDeliveryID != outboxDeliveryID {
		t.Fatalf("recovered document state = %#v, found=%v, err=%v", record, found, err)
	}
}
