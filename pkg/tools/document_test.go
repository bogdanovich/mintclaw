package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentToolLocalPathPolicy(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "inside.pdf")
	otherInside := filepath.Join(workspace, "other.pdf")
	outsideRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideRoot, "allowed.pdf")
	for _, path := range []string{inside, otherInside, outside} {
		if err = os.WriteFile(path, []byte("%PDF-1.7\n%%EOF\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restricted := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, true, nil))
	for _, path := range []string{"inside.pdf", inside} {
		ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{path})
		_, resolved, err := restricted.resolveSource(ctx, "inspect", map[string]any{
			"action": "inspect", "path": path,
		})
		if err != nil || resolved != inside {
			t.Fatalf("resolve %q = %q, %v", path, resolved, err)
		}
	}
	for name, args := range map[string]map[string]any{
		"outside":   {"action": "inspect", "path": outside},
		"traversal": {"action": "inspect", "path": filepath.Join("..", filepath.Base(outside))},
		"extract":   {"action": "extract", "path": inside},
		"two":       {"action": "inspect", "path": inside, "source": "media://current"},
		"empty":     {"action": "inspect", "path": ""},
		"malformed": {"action": "inspect", "path": map[string]any{"value": inside}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if path, ok := args["path"].(string); ok {
				ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{path})
			}
			if _, _, err := restricted.resolveSource(ctx, args["action"].(string), args); err == nil {
				t.Fatal("unauthorized source admitted")
			}
		})
	}
	ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{"inside.pdf"})
	if _, _, err := restricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": inside,
	}); err == nil {
		t.Fatal("model-authored alias absent from the current user message was admitted")
	}
	ctx = toolshared.WithToolDocumentLocalPaths(t.Context(), []string{inside})
	if _, _, err := restricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": otherInside,
	}); err == nil {
		t.Fatal("different in-policy PDF absent from the current user message was admitted")
	}

	allowed := NewDocumentTool(WithDocumentLocalPathPolicy(
		workspace,
		true,
		[]*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(outside) + "$")},
	))
	ctx = toolshared.WithToolDocumentLocalPaths(t.Context(), []string{outside})
	if _, resolved, err := allowed.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": outside,
	}); err != nil || resolved != outside {
		t.Fatalf("configured read path = %q, %v", resolved, err)
	}

	unrestricted := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, false, nil))
	if _, resolved, err := unrestricted.resolveSource(ctx, "inspect", map[string]any{
		"action": "inspect", "path": outside,
	}); err != nil || resolved != outside {
		t.Fatalf("unrestricted path = %q, %v", resolved, err)
	}
}

func TestDocumentToolLocalPathFailureDoesNotRevealPath(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.pdf")
	tool := NewDocumentTool(WithDocumentLocalPathPolicy(workspace, true, nil))
	ctx := toolshared.WithToolDocumentLocalPaths(t.Context(), []string{outside})
	result := tool.Execute(ctx, map[string]any{"action": "inspect", "path": outside})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureSourceUnauthorized)) ||
		strings.Contains(result.ForLLM, outside) {
		t.Fatalf("unsafe local-path denial = %#v", result)
	}
}

func TestDocumentToolLocalPathDurabilityAndLoggingRedaction(t *testing.T) {
	path := "/private/workspace/tax-return.pdf"
	args := map[string]any{"action": "inspect", "path": path}
	projected, err := NewDocumentTool().DurableArguments(args)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := projected["path"].(string)
	if !strings.HasPrefix(value, documentLocalPathTokenPrefix) || strings.Contains(value, path) ||
		args["path"] != path {
		t.Fatalf("durable args = %#v, original = %#v", projected, args)
	}
	tool := NewDocumentTool()
	if !tool.ProtectedDurableArguments(args) || tool.ProtectedDurableResult(args) {
		t.Fatal("local document durability protection is inconsistent")
	}
	logged := ToolLogArguments("document", args)
	if logged["redacted"] != true || logged["action"] != "inspect" || strings.Contains(fmtAny(logged), path) {
		t.Fatalf("logged args = %#v", logged)
	}
	const mediaRef = "media://00000000-0000-4000-8000-000000000001"
	mediaArgs := map[string]any{"action": "inspect", "source": mediaRef}
	if got := ToolLogArguments("document", mediaArgs); got["source"] != mediaRef ||
		tool.ProtectedDurableArguments(mediaArgs) {
		t.Fatalf("attachment behavior changed: %#v", got)
	}
	for _, source := range []string{path, "media:///private/workspace/secret.pdf"} {
		invalidSourceArgs := map[string]any{"action": "form", "source": source}
		invalidSourceDurable, durableErr := tool.DurableArguments(invalidSourceArgs)
		if durableErr != nil {
			t.Fatal(durableErr)
		}
		if projected, _ := invalidSourceDurable["source"].(string); !strings.HasPrefix(
			projected,
			documentLocalPathTokenPrefix,
		) || strings.Contains(projected, source) || !tool.ProtectedDurableArguments(invalidSourceArgs) {
			t.Fatalf("invalid source durable args = %#v", invalidSourceDurable)
		}
		if got := ToolLogArguments("document", invalidSourceArgs); got["redacted"] != true ||
			strings.Contains(fmtAny(got), source) {
			t.Fatalf("invalid source logged args = %#v", got)
		}
	}
}

func TestDocumentToolFillArgumentsAreProtectedAndValueFreeDurably(t *testing.T) {
	privateValue := "private immigration answer"
	args := map[string]any{
		"action": "fill",
		"source": "media://current",
		"assignments": []any{map[string]any{
			"field_id": "field_" + strings.Repeat("a", 64),
			"value":    map[string]any{"type": "text", "text": privateValue},
		}},
	}
	tool := NewDocumentTool()
	projected, err := tool.DurableArguments(args)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	assignmentProjection, ok := projected["assignments"].([]any)
	if !ok || len(assignmentProjection) != 1 ||
		strings.Contains(string(encoded), privateValue) || !tool.ProtectedDurableArguments(args) {
		t.Fatalf("durable fill projection = %s", encoded)
	}
	registry := NewToolRegistry()
	registry.Register(tool)
	if _, protected, durableErr := registry.DurableArguments("document", args); durableErr != nil || !protected {
		t.Fatalf("schema-valid durable fill projection = protected %v, error %v", protected, durableErr)
	}
	logged := ToolLogArguments("document", args)
	if logged["redacted"] != true || logged["action"] != "fill" ||
		strings.Contains(fmtAny(logged), privateValue) {
		t.Fatalf("logged fill arguments = %#v", logged)
	}
	if tool.ToolLoopSemantics() != loopguard.SemanticsMutating {
		t.Fatalf("document tool semantics = %q", tool.ToolLoopSemantics())
	}
}

func TestDocumentToolWriteOperationIDIsStablePerDurableCall(t *testing.T) {
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "execution-one")
	ctx = toolshared.WithToolCallID(ctx, "call-one")
	first := documentToolWriteOperationID(ctx)
	second := documentToolWriteOperationID(ctx)
	other := documentToolWriteOperationID(toolshared.WithToolCallID(ctx, "call-two"))
	if first != second || first == other || !strings.HasPrefix(first, "document_write_") {
		t.Fatalf("operation IDs = first %q second %q other %q", first, second, other)
	}
}

func TestDocumentToolDeliverySettlementAdvancesDurableWriteState(t *testing.T) {
	for _, target := range []document.WriteOperationState{
		document.WriteDelivered,
		document.WriteDeliveryFailed,
		document.WriteDeliveryAmbiguous,
	} {
		t.Run(string(target), func(t *testing.T) {
			outboxDeliveryID := "out_" + strings.Repeat("a", 32)
			stateRoot := t.TempDir()
			tool := NewDocumentTool(WithDocumentStateRoot(stateRoot))
			journal, err := tool.documentWriteJournal()
			if err != nil {
				t.Fatal(err)
			}
			operationID := document.NewWriteOperationID()
			owner := document.Authority{
				Kind: "inbound_media", WorkspaceID: "workspace", AgentID: "agent",
				ActorID: "actor", RouteID: "route", SessionID: "session",
			}
			value := "protected"
			request := document.NormalizedFillRequest{
				SchemaVersion: document.NormalizedFillSchemaVersion,
				SourceSHA256:  strings.Repeat("a", 64),
				Assignments: []document.FormFillAssignment{{
					FieldID: "field_" + strings.Repeat("c", 64),
					Value:   document.FormValue{Type: document.FormValueText, Text: &value},
				}},
				AffectedPages: []int{1},
			}
			request.RequestSHA256 = documentToolTestFillRequestSHA256(t, request)
			record, _, err := journal.Accept(t.Context(), operationID, owner, request)
			if err != nil {
				t.Fatal(err)
			}
			for _, transition := range []document.WriteTransition{
				{State: document.WriteWriting},
				{State: document.WriteWritten, Artifact: &document.WriteArtifactEvidence{
					SHA256: strings.Repeat("d", 64), Size: 100,
				}},
				{State: document.WriteVerifying},
				{State: document.WriteVerified, Verification: &document.WriteVerificationEvidence{
					StructuralAssertions: 1, VisualAssertions: 1, CheckedFields: 1,
					CheckedWidgets: 1, RenderedPages: 1,
				}},
				{State: document.WriteRegistered, ArtifactRef: "media://" + uuid.NewString()},
			} {
				transition.ExpectedRevision = record.Revision
				record, _, err = journal.Transition(t.Context(), operationID, owner, transition)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = tool.advanceDocumentWriteDelivery(
				t.Context(), owner, operationID, document.WriteDeliveryPending, outboxDeliveryID,
			); err != nil {
				t.Fatal(err)
			}
			if err = tool.advanceDocumentWriteDelivery(
				t.Context(), owner, operationID, target, "out_"+strings.Repeat("f", 32),
			); !errors.Is(err, document.ErrWriteConflict) {
				t.Fatalf("mismatched settlement error = %v", err)
			}
			if err = tool.advanceDocumentWriteDelivery(
				t.Context(), owner, operationID, target, outboxDeliveryID,
			); err != nil {
				t.Fatal(err)
			}
			if err = tool.advanceDocumentWriteDelivery(
				t.Context(), owner, operationID, target, outboxDeliveryID,
			); err != nil {
				t.Fatalf("idempotent settlement: %v", err)
			}
			record, found, err := journal.Lookup(t.Context(), operationID, owner)
			if err != nil || !found || record.State != target {
				t.Fatalf("record = %#v found=%v err=%v", record, found, err)
			}
		})
	}
}

func TestDocumentToolReconcilesPendingDeliveryFromDurableOutbox(t *testing.T) {
	tests := []struct {
		name     string
		status   outbox.Status
		want     document.WriteOperationState
		terminal bool
		failure  document.FailureCode
	}{
		{name: "delivered", status: outbox.StatusDelivered, want: document.WriteDelivered, terminal: true},
		{
			name: "definitely failed", status: outbox.StatusDefinitelyFailed,
			want: document.WriteDeliveryFailed, terminal: true, failure: document.FailureDeliveryFailed,
		},
		{
			name: "ambiguous", status: outbox.StatusAmbiguous,
			want: document.WriteDeliveryAmbiguous, terminal: true, failure: document.FailureDeliveryAmbiguous,
		},
		{
			name: "abandoned", status: outbox.StatusAbandoned,
			want: document.WriteDeliveryFailed, terminal: true, failure: document.FailureDeliveryFailed,
		},
		{name: "pending", status: outbox.StatusPending, want: document.WriteDeliveryPending},
		{name: "attempting", status: outbox.StatusAttempting, want: document.WriteDeliveryPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outboxDeliveryID := "out_" + strings.Repeat("b", 32)
			stateRoot := t.TempDir()
			var intent outbox.Intent
			tool := NewDocumentTool(
				WithDocumentStateRoot(stateRoot),
				WithDocumentDeliveryInspector(func(deliveryID string) (outbox.DeliveryInspection, error) {
					if deliveryID != outboxDeliveryID {
						t.Fatalf("inspected delivery ID = %q", deliveryID)
					}
					return outbox.DeliveryInspection{Intent: intent}, nil
				}),
			)
			owner, operationID, record := primeDocumentDeliveryPending(t, tool, outboxDeliveryID)
			intent = documentDeliveryTestIntent(record, owner, operationID, test.status)

			reconciled, err := tool.reconcileDocumentWriteDelivery(
				t.Context(), owner, operationID, record,
			)
			if err != nil || reconciled.State != test.want || reconciled.FailureCode != test.failure {
				t.Fatalf("reconciled = %#v, err=%v", reconciled, err)
			}
			if test.terminal && reconciled.Revision != record.Revision+1 {
				t.Fatalf("terminal revision = %d, want %d", reconciled.Revision, record.Revision+1)
			}
			if !test.terminal && reconciled.Revision != record.Revision {
				t.Fatalf("nonterminal revision = %d, want %d", reconciled.Revision, record.Revision)
			}
		})
	}
}

func TestDocumentToolRejectsMismatchedRecoveredDelivery(t *testing.T) {
	outboxDeliveryID := "out_" + strings.Repeat("c", 32)
	stateRoot := t.TempDir()
	tool := NewDocumentTool(WithDocumentStateRoot(stateRoot))
	owner, operationID, record := primeDocumentDeliveryPending(t, tool, outboxDeliveryID)
	intent := documentDeliveryTestIntent(record, owner, operationID, outbox.StatusDelivered)
	intent.Media.Recovery.DomainDeliveryID = "delivery_" + strings.Repeat("d", 64)
	tool.deliveryState = func(string) (outbox.DeliveryInspection, error) {
		return outbox.DeliveryInspection{Intent: intent}, nil
	}
	if _, err := tool.reconcileDocumentWriteDelivery(
		t.Context(), owner, operationID, record,
	); !errors.Is(err, document.ErrWriteConflict) {
		t.Fatalf("mismatched recovery error = %v", err)
	}
}

func primeDocumentDeliveryPending(
	t *testing.T,
	tool *DocumentTool,
	outboxDeliveryID string,
) (document.Authority, string, document.WriteOperationRecord) {
	t.Helper()
	journal, err := tool.documentWriteJournal()
	if err != nil {
		t.Fatal(err)
	}
	owner := document.Authority{
		Kind: "inbound_media", WorkspaceID: "workspace", AgentID: "agent",
		ActorID: "actor", RouteID: "route", SessionID: "session",
	}
	operationID := document.NewWriteOperationID()
	value := "protected"
	request := document.NormalizedFillRequest{
		SchemaVersion: document.NormalizedFillSchemaVersion,
		SourceSHA256:  strings.Repeat("a", 64),
		Assignments: []document.FormFillAssignment{{
			FieldID: "field_" + strings.Repeat("c", 64),
			Value:   document.FormValue{Type: document.FormValueText, Text: &value},
		}},
		AffectedPages: []int{1},
	}
	request.RequestSHA256 = documentToolTestFillRequestSHA256(t, request)
	record, _, err := journal.Accept(t.Context(), operationID, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []document.WriteTransition{
		{State: document.WriteWriting},
		{State: document.WriteWritten, Artifact: &document.WriteArtifactEvidence{
			SHA256: strings.Repeat("d", 64), Size: 100,
		}},
		{State: document.WriteVerifying},
		{State: document.WriteVerified, Verification: &document.WriteVerificationEvidence{
			StructuralAssertions: 1, VisualAssertions: 1, CheckedFields: 1,
			CheckedWidgets: 1, RenderedPages: 1,
		}},
		{State: document.WriteRegistered, ArtifactRef: "media://" + uuid.NewString()},
		{State: document.WriteDeliveryPending, OutboxDeliveryID: outboxDeliveryID},
	} {
		transition.ExpectedRevision = record.Revision
		record, _, err = journal.Transition(t.Context(), operationID, owner, transition)
		if err != nil {
			t.Fatal(err)
		}
	}
	return owner, operationID, record
}

func documentDeliveryTestIntent(
	record document.WriteOperationRecord,
	owner document.Authority,
	operationID string,
	status outbox.Status,
) outbox.Intent {
	return outbox.Intent{
		ID: record.OutboxDeliveryID, OwnerWorkspace: "/workspace", Status: status,
		Identity: outbox.Identity{Kind: outbox.KindMedia},
		Media: &bus.OutboundMediaMessage{
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
		},
	}
}

func documentToolTestFillRequestSHA256(t *testing.T, request document.NormalizedFillRequest) string {
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

type documentInputBytes []byte

func (source documentInputBytes) OpenInput() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(source)), nil
}

func TestDocumentToolLocalSnapshotIsExecutionScopedAndCleaned(t *testing.T) {
	data := documentInputBytes("%PDF-1.7\nimmutable local bytes\n%%EOF\n")
	digest := sha256.Sum256(data)
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion,
		Operation:     "inspect",
		State:         document.StateSucceeded,
		Input: &document.DocumentRef{
			ContentType: "application/pdf",
			Size:        int64(len(data)),
			SHA256:      hex.EncodeToString(digest[:]),
		},
	}
	store := media.NewFileMediaStore()
	tool := NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "local-execution")
	ref, err := tool.registerLocalSnapshot(ctx, store, documentToolTestOwner(t), data, report)
	if err != nil {
		t.Fatal(err)
	}
	if !tool.localRefAllowed(ctx, ref) || tool.localRefAllowed(
		toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "other-execution"),
		ref,
	) {
		t.Fatal("local ref escaped its execution boundary")
	}
	opened, err := store.OpenOwned(ref, documentToolTestOwner(t))
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(opened.File)
	_ = opened.Close()
	if err != nil || !bytes.Equal(read, data) {
		t.Fatalf("snapshot bytes = %q, %v", read, err)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if tool.localRefAllowed(ctx, ref) {
		t.Fatal("local ref authority survived cleanup")
	}
	if _, _, err = store.ResolveWithMeta(ref); err == nil {
		t.Fatal("local snapshot survived cleanup")
	}
}

func TestCopyDocumentInputRejectsDescriptorMismatch(t *testing.T) {
	data := documentInputBytes("%PDF-1.7\nbytes\n%%EOF\n")
	digest := sha256.Sum256(data)
	input := document.DocumentRef{
		ContentType: "application/pdf",
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
	}
	for name, mutate := range map[string]func(*document.DocumentRef){
		"size":   func(ref *document.DocumentRef) { ref.Size-- },
		"digest": func(ref *document.DocumentRef) { ref.SHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			mutate(&changed)
			if path, err := copyDocumentInputToMediaTemp(data, changed); err == nil {
				_ = os.Remove(path)
				t.Fatal("mismatched snapshot descriptor was admitted")
			}
		})
	}
}

func fmtAny(value any) string {
	return strings.TrimSpace(
		strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(fmt.Sprint(value)), "\n", " "), "\t", " "),
	)
}

func TestDocumentToolRequiresCurrentTurnRefBeforeMediaAccess(t *testing.T) {
	tool := NewDocumentTool()
	result := tool.Execute(t.Context(), map[string]any{
		"action": "inspect", "source": "media://older-route-ref",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureSourceUnauthorized)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDocumentToolRejectsOptionsFromAnotherAction(t *testing.T) {
	tool := NewDocumentTool()
	ctx := toolshared.WithToolDocumentContext(t.Context(), []string{"media://current"}, true)
	result := tool.Execute(ctx, map[string]any{
		"action": "inspect", "source": "media://current", "pages": []any{float64(1)},
	})
	if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureInvalidInput)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDocumentToolReportProjectionIncludesInspectionAndBlockersWithoutUnsafeMetadata(t *testing.T) {
	pageCount := 10
	fieldCount := 250
	privatePath := "/private/workspace/hybrid-form.pdf"
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion,
		OperationID:   "document_operation_projection",
		Operation:     "fields",
		State:         document.StateUnsupported,
		Input: &document.DocumentRef{
			SourceRef:        "media://00000000-0000-4000-8000-000000000001",
			OriginalFilename: privatePath,
			ContentType:      "application/pdf", Size: 511302, SHA256: strings.Repeat("a", 64),
		},
		Inspection: &document.InspectionFacts{
			Backend:    document.BackendIdentity{Name: "private-backend-detail", Version: "secret-version"},
			PDFVersion: document.StringFact{State: document.FactPresent, Value: "1.7"},
			PageCount:  document.IntegerFact{State: document.FactPresent, Value: &pageCount},
			Encryption: document.EncryptionFacts{
				State: document.FactPresent, PasswordRequired: document.FactAbsent,
				Permissions: document.StringFact{State: document.FactPresent, Value: "restricted"},
			},
			Signatures: document.SignatureFacts{
				State:     document.FactPresent,
				Count:     document.IntegerFact{State: document.FactPresent, Value: documentIntPointer(1)},
				Certified: document.FactAbsent, Timestamped: document.FactUnknown,
			},
			Restrictions: document.RestrictionFacts{
				State: document.FactPresent, EncryptedPermissions: document.FactPresent,
				DocMDP: document.FactAbsent, FieldMDP: document.FactAbsent,
				UsageRights: document.FactPresent, ReaderExtensions: document.FactAbsent,
			},
			AcroForm: document.AcroFormFacts{
				State:      document.FactPresent,
				FieldCount: document.IntegerFact{State: document.FactPresent, Value: &fieldCount},
			},
			XFA: document.XFAFacts{
				State:          document.FactPresent,
				Representation: document.StringFact{State: document.FactPresent, Value: "packet_array"},
				Rendering:      document.StringFact{State: document.FactUnknown},
			},
			ExtractableText: document.TextFacts{State: document.FactPresent, PagesWithText: 10},
			Warnings:        []string{"text_signal_uses_content_stream_operators"},
		},
		FormEligibility: &document.FormEligibilityFacts{
			State: document.FormBlocked,
			Blockers: []document.FormBlocker{
				{Code: document.FormBlockerEncryption, State: document.FactPresent},
				{Code: document.FormBlockerSignature, State: document.FactPresent},
				{Code: document.FormBlockerEncryptedPermissions, State: document.FactPresent},
				{Code: document.FormBlockerUsageRights, State: document.FactPresent},
				{Code: document.FormBlockerXFA, State: document.FactPresent},
			},
		},
		Failure: &document.Failure{
			Code: document.FailureFormUnsupported, Message: "PDF form is not supported",
		},
	}

	result := documentToolReportResult(report)
	if !result.IsError {
		t.Fatalf("result = %#v", result)
	}
	var projection safeDocumentReport
	if err := json.Unmarshal([]byte(result.ForLLM), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Inspection == nil || projection.Inspection.PageCount.Value == nil ||
		*projection.Inspection.PageCount.Value != 10 ||
		projection.Inspection.AcroForm.FieldCount.Value == nil ||
		*projection.Inspection.AcroForm.FieldCount.Value != 250 ||
		projection.Inspection.XFA.Representation.Value != "packet_array" ||
		projection.FormEligibility == nil || len(projection.FormEligibility.Blockers) != 5 {
		t.Fatalf("projection = %#v", projection)
	}
	for _, forbidden := range []string{privatePath, "private-backend-detail", "secret-version"} {
		if strings.Contains(result.ForLLM, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, result.ForLLM)
		}
	}
}

func documentIntPointer(value int) *int { return &value }

func TestBoundedDocumentTextKeepsPageProvenanceAndUTF8(t *testing.T) {
	pages := []document.ExtractedPage{
		{Page: 2, Text: strings.Repeat("é", documentModelTextLimit)},
		{Page: 3, Text: "must not fit"},
	}
	content := boundedDocumentText(pages, 512)
	if len(content) > 512 || !strings.Contains(content, "[page 2]") || strings.Contains(content, "[page 3]") ||
		!utf8.ValidString(content) {
		t.Fatalf("bounded content is invalid: len=%d content=%q", len(content), content)
	}
}

type documentArtifactBytes map[string][]byte

func (source documentArtifactBytes) OpenArtifact(ref string) (io.ReadCloser, error) {
	data, ok := source[ref]
	if !ok {
		return nil, errors.New("missing artifact")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type documentBindFailStore struct {
	*media.FileMediaStore
	releaseCalls int
}

func (*documentBindFailStore) BindOwner(string, media.MediaOwner) error {
	return errors.New("injected bind failure")
}

func (store *documentBindFailStore) ReleaseAll(scope string) error {
	store.releaseCalls++
	return store.FileMediaStore.ReleaseAll(scope)
}

func TestDocumentRenderRegistrationIsAuthorityBoundAndCleansAtTurnEnd(t *testing.T) {
	data := []byte("verified rendered page bytes")
	report, source := documentRegistrationFixture(data)
	store := media.NewFileMediaStore()
	owner := documentToolTestOwner(t)
	tool := NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "execution-1")
	refs, err := tool.registerRenderedArtifacts(ctx, store, owner, source, report, true)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
	opened, err := store.OpenOwned(refs[0], owner)
	if err != nil {
		t.Fatalf("registered ref is not authority-bound: %v", err)
	}
	_ = opened.Close()
	if err := tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveWithMeta(refs[0]); err == nil {
		t.Fatal("current-turn render ref survived terminal cleanup")
	}
}

func TestDocumentRenderRegistrationRollsBackOnOwnerBindingFailure(t *testing.T) {
	data := []byte("verified rendered page bytes")
	report, source := documentRegistrationFixture(data)
	store := &documentBindFailStore{FileMediaStore: media.NewFileMediaStore()}
	tool := NewDocumentTool()
	_, err := tool.registerRenderedArtifacts(
		toolshared.WithToolExecutionIdentity(t.Context(), "workspace", "execution-2"),
		store,
		documentToolTestOwner(t),
		source,
		report,
		true,
	)
	if err == nil || store.releaseCalls != 1 {
		t.Fatalf("err=%v release_calls=%d", err, store.releaseCalls)
	}
}

func documentRegistrationFixture(data []byte) (document.Report, documentArtifactBytes) {
	digest := sha256.Sum256(data)
	artifactDigest := hex.EncodeToString(digest[:])
	report := document.Report{
		SchemaVersion: document.ReportSchemaVersion,
		Operation:     "render",
		State:         document.StateSucceeded,
		Input:         &document.DocumentRef{SHA256: strings.Repeat("a", 64)},
		Artifacts: []document.Artifact{{
			Ref:          "document-artifact://page-1",
			Kind:         "page_render",
			ContentType:  "image/png",
			Size:         int64(len(data)),
			SHA256:       artifactDigest,
			SourceSHA256: strings.Repeat("a", 64),
			Pages:        []int{1},
		}},
	}
	return report, documentArtifactBytes{report.Artifacts[0].Ref: data}
}

func documentToolTestOwner(t *testing.T) media.MediaOwner {
	t.Helper()
	owner, err := media.NewMediaOwner(
		"workspace", "agent", "actor", "route", "session", "telegram", "chat", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
