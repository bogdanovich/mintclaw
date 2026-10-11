package eval

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func nativePDFPromptFixture(t *testing.T, trial *pdfTrial) (interactions.Record, Event, *document.FormJobStore) {
	t.Helper()
	route := interactions.Route{
		AgentID: trial.agent, SessionKey: "eval-session-key", RouteSessionKey: "eval-session-key",
		Channel: "mintclaw", ChatID: "mintclaw:" + trial.sessionID, ChatType: "direct", SenderID: "mintclaw-user",
	}
	owner, err := document.FormJobOwnerFromProtectedRoute(trial.workspace, route)
	if err != nil {
		t.Fatal(err)
	}
	options := trial.formStoreOptions()
	for _, directory := range []string{options.StateRoot, options.KeyRoot} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	job, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "synthetic-start", SourceRef: "media://eval-source",
		SourceDigest: trial.item.Case.Fixtures[0].SHA256, FieldSchemaDigest: strings.Repeat("c", 64),
		BackendRevision: "eval-control-v1", AuditPolicyRevision: "eval-control-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), document.FormProtectedAnswerBindingRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, FieldID: "contact_reference",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(trial.workspace))
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction_eval_control111", Kind: interactions.KindQuestion, Route: route,
		Origin:          interactions.Origin{ToolName: "document", ToolCallID: "eval-call", TurnID: "eval-turn"},
		Questions:       []interactions.Question{{ID: "contact", Question: "What contact reference should I use?"}},
		ProtectedAnswer: &binding, ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := outbox.NewMessageIntent(trial.workspace, outbox.Identity{
		SourceID: "synthetic-prompt", Channel: route.Channel, ChatID: route.ChatID, SessionKey: route.SessionKey,
	}, bus.OutboundMessage{
		AgentID: trial.agent, SessionKey: route.SessionKey, Content: "What contact reference should I use?",
		Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID,
			InteractionKind: string(record.Kind), InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(trial.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.Create(intent); err != nil {
		t.Fatal(err)
	}
	if _, err = box.BeginAttempt(intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = box.MarkDelivered(
		intent.ID,
		outbox.Outcome{PlatformMessageIDs: []string{"wire-prompt-message"}},
	); err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindPromptDelivery(record.ID, record.Revision, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	prompt := Event{
		ID: "event-2", Turn: 1, Kind: "question", AgentID: trial.agent, SessionKey: route.SessionKey,
		InteractionID: record.ID, InteractionShortID: record.ShortID, MessageID: "wire-prompt-message",
	}
	return record, prompt, store
}

func TestPDFPrivateRequestUsesReadOnlyNativePromptJobAndHandle(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, prompt, _ := nativePDFPromptFixture(t, trial)
	path := interactions.WorkspaceStorePath(trial.workspace)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := trial.protectedRequest(prompt, "contact-reference")
	if err != nil || request.Type != protocol.TypeMessageSend || request.SessionID != trial.sessionID ||
		!validID(request.ID) || len(request.Payload) != 1 ||
		payloadString(request.Payload, protocol.PayloadKeyContent) !=
			"/answer "+record.ShortID+" "+trial.item.Case.Driver.Facts["contact-reference"] {
		t.Fatal("admitted protected request not bound to native prompt/handle", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observation changed native registry")
	}
	if _, err = os.Stat(
		filepath.Join(trial.workspace, "state", "interaction_hmac.key"),
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatal("private-input observation created an approval key")
	}
}

func TestPDFPromptRefusesWrongIdentityRouteStatusExpiryAndTool(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, prompt, _ := nativePDFPromptFixture(t, trial)
	if !trial.promptMatches(record, prompt, time.Now()) {
		t.Fatal("valid current native prompt refused")
	}
	for name, mutate := range map[string]func(*interactions.Record, *Event){
		"full id":      func(_ *interactions.Record, e *Event) { e.InteractionID += "wrong" },
		"short id":     func(_ *interactions.Record, e *Event) { e.InteractionShortID += "wrong" },
		"agent":        func(_ *interactions.Record, e *Event) { e.AgentID = "another-agent" },
		"session":      func(r *interactions.Record, _ *Event) { r.Route.SessionKey += "wrong" },
		"route alias":  func(r *interactions.Record, _ *Event) { r.Route.RouteSessionKey += "wrong" },
		"chat":         func(r *interactions.Record, _ *Event) { r.Route.ChatID += "wrong" },
		"sender":       func(r *interactions.Record, _ *Event) { r.Route.SenderID = "other-user" },
		"channel":      func(r *interactions.Record, _ *Event) { r.Route.Channel = "telegram" },
		"topic":        func(r *interactions.Record, _ *Event) { r.Route.TopicID = "another-topic" },
		"tool":         func(r *interactions.Record, _ *Event) { r.Origin.ToolName = "request_user_input" },
		"resolved":     func(r *interactions.Record, _ *Event) { r.Status = interactions.StatusResolved },
		"not waiting":  func(r *interactions.Record, _ *Event) { r.Status = interactions.StatusCreated },
		"expired":      func(r *interactions.Record, _ *Event) { r.ExpiresAt = trial.started.UnixMilli() },
		"old trial":    func(r *interactions.Record, _ *Event) { r.CreatedAt = trial.started.UnixMilli() - 1 },
		"future":       func(r *interactions.Record, _ *Event) { r.CreatedAt = time.Now().Add(time.Hour).UnixMilli() },
		"wrong intent": func(_ *interactions.Record, e *Event) { e.Kind = "approval" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, event := record, prompt
			mutate(&candidate, &event)
			if trial.promptMatches(candidate, event, time.Now()) {
				t.Fatal("unbound private-input prompt admitted")
			}
		})
	}
}

func TestPDFPromptSeparatesOutboxIdentityFromWireMessageIdentity(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, prompt, _ := nativePDFPromptFixture(t, trial)
	if record.PromptDeliveryID == prompt.MessageID || trial.observePromptDelivery(record, prompt) != nil {
		t.Fatal("native outbox and platform identities were conflated")
	}
	prompt.MessageID = record.PromptDeliveryID
	if !errors.Is(trial.observePromptDelivery(record, prompt), ErrPDFTrial) {
		t.Fatal("outbox id incorrectly admitted as a delivered platform message")
	}
	prompt.MessageID = "another-wire-message"
	if _, err := trial.protectedRequest(prompt, "contact-reference"); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("unbound delivered prompt admitted a private value")
	}
}

func TestPDFPrivateRequestRefusesUnprotectedAndUnknownHandle(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, prompt, _ := nativePDFPromptFixture(t, trial)
	if _, err := trial.protectedRequest(prompt, "not-admitted"); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("unknown fact handle admitted")
	}
	for _, binding := range []*interactions.ProtectedAnswerBinding{
		nil,
		{Namespace: "other.domain", Token: record.ProtectedAnswer.Token},
		{Namespace: document.FormProtectedAnswerNamespace, Token: "untrusted-prose"},
	} {
		candidate := record
		candidate.ProtectedAnswer = binding
		if _, _, err := trial.observeProtectedJob(candidate); !errors.Is(err, ErrPDFTrial) {
			t.Fatal("unsupported protected sink or opaque binding admitted")
		}
	}
}

func TestPDFProtectedJobRefusesWrongOwnerSourceAndCanceledJob(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, _, store := nativePDFPromptFixture(t, trial)
	job, owner, err := trial.observeProtectedJob(record)
	if err != nil {
		t.Fatal("native job fixture was not authenticated", err)
	}
	candidate := record
	candidate.Route.SenderID = "another-user"
	if _, _, err = trial.observeProtectedJob(candidate); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("native job owner mismatch admitted")
	}
	other, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "another-source", SourceRef: "media://another-source",
		SourceDigest: strings.Repeat("0", 64), FieldSchemaDigest: strings.Repeat("c", 64),
		BackendRevision: "eval-control-v1", AuditPolicyRevision: "eval-control-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), document.FormProtectedAnswerBindingRequest{
		JobID: other.JobID, ExpectedRevision: other.Revision, Owner: owner, FieldID: "contact_reference",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate = record
	candidate.ProtectedAnswer = &binding
	if _, _, err = trial.observeProtectedJob(candidate); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("unrelated authenticated source admitted")
	}
	if _, err = store.Cancel(t.Context(), job.JobID, job.Revision, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err = trial.observeProtectedJob(record); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("canceled native job admitted another private value")
	}
}

func TestPDFNativeSurfaceScannerCannotHidePrivateDuplicateJSONValue(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	value := trial.item.Case.Driver.Facts["contact-reference"]
	encoded, _ := json.Marshal(value)
	for _, data := range [][]byte{
		[]byte(`{"value":` + string(encoded) + `,"value":"safe"}`),
		[]byte(`{"safe":true} trailing`), []byte(`{"truncated":`),
	} {
		if !errors.Is(trial.scanNativeSurface(data), ErrPrivateEvidence) {
			t.Fatal("private, trailing or truncated raw native surface admitted")
		}
	}
	if trial.scanNativeSurface([]byte(`{"value":"ordinary-safe-text"}`)) != nil {
		t.Fatal("ordinary bounded native evidence refused")
	}
}

func TestPDFPrivateRequestScansOtherJobsInAuthenticatedSnapshot(t *testing.T) {
	trial := newPDFTrialFixture(t)
	defer func() { _ = trial.release(true) }()
	record, prompt, store := nativePDFPromptFixture(t, trial)
	job, owner, err := trial.observeProtectedJob(record)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "another-job-with-public-leak", SourceRef: "media://eval-source",
		SourceDigest: trial.item.Case.Fixtures[0].SHA256, FieldSchemaDigest: strings.Repeat("c", 64),
		BackendRevision: trial.item.Case.Driver.Facts["contact-reference"], AuditPolicyRevision: "eval-control-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = document.InspectFormJob(trial.formStoreOptions(), job.JobID, owner); err != nil {
		t.Fatal("control is not a valid native snapshot", err)
	}
	if _, err = trial.protectedRequest(prompt, "contact-reference"); !errors.Is(err, ErrPrivateEvidence) {
		t.Fatal("private value in another public job escaped whole-snapshot admission", err)
	}
}

func TestPDFOpaqueLocatorRejectsDuplicateAndInvalidIdentityWithoutOpeningPayload(t *testing.T) {
	for _, header := range []string{
		`{}`, `{"job_id":"a","job_id":"b"}`, `{"job_id":"../escape"}`,
		`{"job_id":"eval-job","kind":"source","version":"document_form_envelope.v1"}`,
	} {
		if _, err := protectedJobLocator(
			base64.RawURLEncoding.EncodeToString([]byte(header)),
		); !errors.Is(
			err,
			ErrPDFTrial,
		) {
			t.Fatal("invalid opaque locator admitted")
		}
	}
	header := []byte(`{"job_id":"eval-job","kind":"answer_binding","version":"document_form_envelope.v1",` +
		`"ciphertext":"not-opened-by-eval"}`)
	if locator, err := protectedJobLocator(
		base64.RawURLEncoding.EncodeToString(header),
	); err != nil ||
		locator != "eval-job" {
		t.Fatal("locator inspection incorrectly pretended to validate sealed authority")
	}
}
