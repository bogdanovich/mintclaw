package eval

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

// observePrompt admits native local evidence, not text-extracted IDs. It is a
// read-only observation; the native ingress/sink still authorizes consumption.
func (trial *pdfTrial) observePrompt(prompt Event) (interactions.Record, error) {
	return trial.observePromptOrigin(prompt, "document")
}

func (trial *pdfTrial) observePromptOrigin(prompt Event, tool string) (interactions.Record, error) {
	if tool != "document" && tool != "request_user_input" {
		return interactions.Record{}, ErrPDFTrial
	}
	record, err := trial.observeInteraction(prompt.InteractionID)
	if err != nil {
		return interactions.Record{}, err
	}
	if !trial.promptMatchesOrigin(record, prompt, time.Now(), tool) {
		return interactions.Record{}, ErrPDFTrial
	}
	if err = trial.observePromptDelivery(record, prompt); err != nil {
		return interactions.Record{}, err
	}
	return record, nil
}

func (trial *pdfTrial) observeInteraction(id string) (interactions.Record, error) {
	if !validID(id) {
		return interactions.Record{}, ErrPDFTrial
	}
	root, err := os.OpenRoot(trial.workspace)
	if err != nil {
		return interactions.Record{}, ErrPDFTrial
	}
	defer func() { _ = root.Close() }()
	data, err := readPDFNativeFile(root, "state/interaction_registry.json", interactions.DefaultMaxBytes)
	if err != nil {
		return interactions.Record{}, err
	}
	defer clear(data)
	if err = trial.scanNativeSurface(data); err != nil {
		return interactions.Record{}, err
	}
	snapshot, err := interactions.InspectSnapshot(data)
	if err != nil {
		return interactions.Record{}, ErrPDFTrial
	}
	for _, candidate := range snapshot.Records {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return interactions.Record{}, ErrPDFTrial
}

func (trial *pdfTrial) promptMatches(record interactions.Record, prompt Event, now time.Time) bool {
	return trial.promptMatchesOrigin(record, prompt, now, "document")
}

func (trial *pdfTrial) promptMatchesOrigin(record interactions.Record, prompt Event, now time.Time, tool string) bool {
	if !slices.Contains([]string{"question", "approval"}, prompt.Kind) ||
		prompt.AgentID != trial.agent || prompt.SessionKey == "" ||
		record.ID == "" || record.ID != prompt.InteractionID ||
		record.ShortID == "" || record.ShortID != prompt.InteractionShortID ||
		record.PromptDeliveryID == "" || prompt.MessageID == "" ||
		string(record.Kind) != prompt.Kind || record.Status != interactions.StatusWaiting ||
		record.CreatedAt < trial.started.UnixMilli() || record.CreatedAt > now.UnixMilli() ||
		record.UpdatedAt > now.UnixMilli() || record.ExpiresAt <= now.UnixMilli() ||
		record.Origin.ToolName != tool ||
		record.Route.AgentID != prompt.AgentID || record.Route.SessionKey != prompt.SessionKey ||
		record.Route.RouteSessionKey != "" && record.Route.RouteSessionKey != prompt.SessionKey ||
		record.Route.Channel != "mintclaw" || record.Route.ChatID != "mintclaw:"+trial.sessionID ||
		record.Route.ChatType != "direct" || record.Route.SenderID != "mintclaw-user" ||
		record.Route.AccountID != "" || record.Route.TopicID != "" ||
		record.Route.SpaceID != "" || record.Route.SpaceType != "" {
		return false
	}
	return true
}

func (trial *pdfTrial) observePromptDelivery(record interactions.Record, prompt Event) error {
	intent, err := trial.observeOutbox(record.PromptDeliveryID)
	if err != nil {
		return err
	}
	if intent.Status != outbox.StatusDelivered || intent.OwnerWorkspace != trial.workspace ||
		intent.Identity.Channel != record.Route.Channel || intent.Identity.ChatID != record.Route.ChatID ||
		intent.Identity.SessionKey != record.Route.SessionKey || intent.Message == nil ||
		intent.Message.AgentID != record.Route.AgentID || intent.Message.SessionKey != record.Route.SessionKey ||
		intent.Message.Metadata.InteractionID != record.ID ||
		intent.Message.Metadata.InteractionShortID != record.ShortID ||
		intent.Message.Metadata.InteractionKind != string(record.Kind) ||
		intent.Message.Metadata.InteractionControls != bus.OutboundInteractionControlsPrompt ||
		!slices.Contains(intent.PlatformMessageIDs, prompt.MessageID) ||
		intent.CreatedAt.Before(trial.started) || intent.UpdatedAt.Before(intent.CreatedAt) {
		return ErrPDFTrial
	}
	return nil
}

func (trial *pdfTrial) observeOutbox(id string) (outbox.Intent, error) {
	if !validID(id) {
		return outbox.Intent{}, ErrPDFTrial
	}
	root, err := os.OpenRoot(trial.workspace)
	if err != nil {
		return outbox.Intent{}, ErrPDFTrial
	}
	defer func() { _ = root.Close() }()
	data, err := readPDFNativeFile(root, "state/outbox/"+id+".json", outbox.MaxInspectionBytes)
	if err != nil {
		return outbox.Intent{}, err
	}
	defer clear(data)
	if err = trial.scanNativeSurface(data); err != nil {
		return outbox.Intent{}, err
	}
	intent, err := outbox.InspectIntent(data, id)
	if err != nil {
		return outbox.Intent{}, ErrPDFTrial
	}
	return intent, nil
}

func (trial *pdfTrial) observeProtectedJob(
	record interactions.Record,
) (document.FormJobRecord, document.FormJobOwner, error) {
	if record.Kind != interactions.KindQuestion || record.ProtectedAnswer == nil ||
		record.ProtectedAnswer.Namespace != document.FormProtectedAnswerNamespace {
		return document.FormJobRecord{}, document.FormJobOwner{}, ErrPDFTrial
	}
	jobID, err := protectedJobLocator(record.ProtectedAnswer.Token)
	if err != nil {
		return document.FormJobRecord{}, document.FormJobOwner{}, ErrPDFTrial
	}
	owner, err := document.FormJobOwnerFromProtectedRoute(trial.workspace, record.Route)
	if err != nil {
		return document.FormJobRecord{}, document.FormJobOwner{}, ErrPDFTrial
	}
	job, err := trial.observeExistingJob(jobID, owner)
	if err != nil {
		return document.FormJobRecord{}, document.FormJobOwner{}, err
	}
	if !slices.Contains([]document.FormJobState{document.FormJobPrepared, document.FormJobCollecting}, job.State) {
		return document.FormJobRecord{}, document.FormJobOwner{}, ErrPDFTrial
	}
	return job, owner, nil
}

func (trial *pdfTrial) observeExistingJob(jobID string, owner document.FormJobOwner) (document.FormJobRecord, error) {
	currentSource, err := readPDFNativeFile(trial.scratch, "source.pdf", maxPDFBytes)
	if err != nil || digest(currentSource) != trial.item.Case.Fixtures[0].SHA256 {
		clear(currentSource)
		return document.FormJobRecord{}, ErrPDFTrial
	}
	clear(currentSource)
	root, err := os.OpenRoot(trial.home)
	if err != nil {
		return document.FormJobRecord{}, ErrPDFTrial
	}
	data, err := readPDFNativeFile(root,
		"state/document-form-jobs/document_form_jobs/form_jobs.v1.json", document.DefaultFormJobMaxBytes)
	_ = root.Close()
	if err != nil {
		return document.FormJobRecord{}, err
	}
	defer clear(data)
	err = trial.scanNativeSurface(data)
	if err != nil {
		return document.FormJobRecord{}, err
	}
	job, err := document.InspectFormJobSnapshot(trial.formStoreOptions(), data, jobID, owner)
	now := time.Now().UnixMilli()
	if err != nil || job.SourceDigest != trial.item.Case.Fixtures[0].SHA256 ||
		job.CreatedAt < trial.started.UnixMilli() || job.CreatedAt > now || job.ExpiresAt <= now {
		return document.FormJobRecord{}, ErrPDFTrial
	}
	return job, nil
}

func (trial *pdfTrial) formStoreOptions() document.FormJobStoreOptions {
	return document.FormJobStoreOptions{
		StateRoot: filepath.Join(trial.home, "state", "document-form-jobs"),
		KeyRoot:   filepath.Join(trial.home, "keys", "document-form-jobs"),
	}
}

// Only read the locator in an opaque native envelope. This is not decryption,
// authentication of the binding, or a duplicate protected-input validator.
// The owner-bound native store authenticates the public job; native ingress
// independently opens and authorizes the sealed binding when accepting input.
func protectedJobLocator(token string) (string, error) {
	if len(token) == 0 || len(token) > interactions.MaxProtectedBinding {
		return "", ErrPDFTrial
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ErrPDFTrial
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return "", ErrPDFTrial
	}
	fields, ok := value.(map[string]any)
	jobID, jobOK := fields["job_id"].(string)
	kind, kindOK := fields["kind"].(string)
	version, versionOK := fields["version"].(string)
	if _, err = decoder.Token(); !ok || !jobOK || !validID(jobID) || !kindOK ||
		!slices.Contains([]string{"answer_binding", "intake_binding"}, kind) ||
		!versionOK || version != document.FormJobEnvelopeVersion || !errors.Is(err, io.EOF) {
		return "", ErrPDFTrial
	}
	return jobID, nil
}

// protectedRequest resolves a handle only after fresh native observation. The
// returned request is ephemeral: it must not enter reports or role context.
// Explicit /answer ingress consumes stale/wrong routes before ordinary history;
// the native sink, not this harness, validates the sealed field/intake binding.
func (trial *pdfTrial) protectedRequest(prompt Event, handle string) (protocol.MintClawMessage, error) {
	value, found := trial.item.Case.Driver.Facts[handle]
	if !found || value == "" || value == generatePrivateFact || prompt.Kind != "question" {
		return protocol.MintClawMessage{}, ErrPDFTrial
	}
	record, err := trial.observePrompt(prompt)
	if err != nil {
		return protocol.MintClawMessage{}, err
	}
	if _, _, err = trial.observeProtectedJob(record); err != nil {
		return protocol.MintClawMessage{}, err
	}
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: trial.sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{
			protocol.PayloadKeyContent: "/answer " + record.ShortID + " " + value,
		},
	}, nil
}

func readPDFNativeFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	data, err := readNativeEvidenceFile(root, name, maximum)
	if err != nil {
		return nil, ErrPDFTrial
	}
	return data, nil
}

func (trial *pdfTrial) scanNativeSurface(data []byte) error {
	return inspectPDFWire(data, trial.item.Case.Driver.Facts, trial.filter)
}
