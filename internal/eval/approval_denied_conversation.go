package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type ApprovalDeniedEvidence struct {
	Version         int                      `json:"version"`
	Code            string                   `json:"code"`
	Workspace       string                   `json:"workspace"`
	Before          ReadFixtureSnapshot      `json:"before"`
	After           ReadFixtureSnapshot      `json:"after"`
	Waiting         string                   `json:"waiting"`
	KeySHA256       string                   `json:"key_sha256"`
	Resolved        string                   `json:"resolved"`
	DenialRequestID string                   `json:"denial_request_id"`
	DenialAt        int64                    `json:"denial_at"`
	ObservedAt      int64                    `json:"observed_at"`
	Metadata        string                   `json:"metadata"`
	MetadataSHA256  string                   `json:"metadata_sha256"`
	History         []string                 `json:"history"`
	HistorySHA256   string                   `json:"history_sha256"`
	Log             []string                 `json:"log"`
	LogSHA256       string                   `json:"log_sha256"`
	Outbox          []SteeringOutboxEvidence `json:"outbox"`
	OutboxNames     []string                 `json:"outbox_names"`
}

type approvalDeniedConversation struct {
	item     Case
	question *questionConversation
	proof    *ApprovalDeniedEvidence
}

func newApprovalDeniedConversation(
	ctx context.Context, item Case, target admittedTarget, sessionID string,
) (*approvalDeniedConversation, error) {
	if item.ApprovalDenied == nil || item.Validate() != nil ||
		validateApprovalDeniedBinding(target.preset, item) != nil || target.ownedLog == "" {
		return nil, errNativeEvidence
	}
	before := observeApprovalFixture(ctx, target.preset.ExpectedWorkspace)
	if !approvalFixtureMatches(before) {
		return nil, errNativeEvidence
	}
	return &approvalDeniedConversation{
		item: item, question: &questionConversation{
			home: target.preset.IsolationRoot, workspace: target.preset.ExpectedWorkspace,
			sessionID: sessionID, agent: target.preset.ExpectedAgent, started: time.Now(),
			filter: target.config.SensitiveDataReplacer(),
		},
		proof: &ApprovalDeniedEvidence{
			Version: Version, Code: "not_completed", Workspace: target.preset.ExpectedWorkspace,
			Before: before, After: ReadFixtureSnapshot{Code: "unavailable"}, History: []string{},
			Log: []string{}, Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
		},
	}, nil
}

func (domain *approvalDeniedConversation) initialTask() string { return domain.item.Task }

func (domain *approvalDeniedConversation) request(
	_ context.Context, action Action, prompt Event,
) (protocol.MintClawMessage, bool, error) {
	if prompt.Kind == "" && domain.proof.Waiting == "" && domain.proof.DenialRequestID == "" &&
		action == (Action{Kind: "message", Text: domain.item.Task}) {
		return protocol.MintClawMessage{}, false, nil
	}
	if action.Kind != "deny" || prompt.Kind != "approval" || domain.proof.DenialRequestID != "" ||
		prompt.ID != domain.question.prompt.ID || prompt.MessageID != domain.question.prompt.MessageID ||
		prompt.InteractionID != domain.question.waiting.ID || prompt.InteractionShortID != domain.question.waiting.ShortID ||
		domain.question.waiting.Kind != interactions.KindApproval {
		return protocol.MintClawMessage{}, false, errNativeEvidence
	}
	domain.proof.DenialRequestID, domain.proof.DenialAt = uuid.NewString(), time.Now().UnixMilli()
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: domain.proof.DenialRequestID, SessionID: domain.question.sessionID,
		Timestamp: domain.proof.DenialAt, Payload: map[string]any{
			protocol.PayloadKeyContent:            "Deny",
			protocol.PayloadKeyInteractionChoice:  string(bus.InboundInteractionChoiceDeny),
			protocol.PayloadKeyInteractionShortID: prompt.InteractionShortID,
			protocol.PayloadKeyInteractionPrompt:  prompt.MessageID,
		},
	}, true, nil
}

func (domain *approvalDeniedConversation) observe(ctx context.Context, event Event) error {
	if event.Kind == "final" && domain.proof.DenialRequestID != "" {
		return domain.waitTerminal(ctx)
	}
	if event.Kind != "approval" || domain.proof.Waiting != "" {
		return errNativeEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.question.record(event.InteractionID)
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		if err == nil {
			matched, matchErr := domain.question.matchesPromptKind(
				record,
				event,
				interactions.KindApproval,
				"write_file",
			)
			if errors.Is(matchErr, ErrPrivateEvidence) {
				return matchErr
			}
			if matched {
				keySHA, keyErr := observeApprovalArgumentBinding(domain.question.workspace, record.Origin.ArgumentHash)
				if keyErr != nil {
					return keyErr
				}
				raw, marshalErr := json.Marshal(record)
				if marshalErr != nil || len(raw) > MaxManifestBytes {
					return errNativeEvidence
				}
				domain.question.prompt, domain.question.waiting = event, record
				domain.proof.Waiting = string(raw)
				domain.proof.KeySHA256 = keySHA
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errNativeEvidence
		case <-ticker.C:
		}
	}
}

func (domain *approvalDeniedConversation) waitTerminal(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.question.record(domain.question.waiting.ID)
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		if err == nil && record.Status == interactions.StatusResolved {
			if record.Kind != interactions.KindApproval || record.Outcome != interactions.OutcomeDenied ||
				record.ApprovalConsumedAt != 0 {
				return errNativeEvidence
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errNativeEvidence
		case <-ticker.C:
		}
	}
}

func (domain *approvalDeniedConversation) conclude(_ context.Context, trial *Trial) {
	trial.ApprovalDenied = domain.proof
	if trial.ExecutionVerdict == Pass && domain.proof.Waiting != "" && domain.proof.DenialRequestID != "" {
		domain.proof.Code = "awaiting_stop"
	}
}

func observeApprovalArgumentBinding(workspace, expected string) (string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	key, err := readNativeEvidenceFile(root, "state/interaction_hmac.key", 32)
	if err != nil {
		return "", errNativeEvidence
	}
	defer clear(key)
	matched, err := interactions.VerifyArgumentHash(key, map[string]any{
		"path": evalfixture.ApprovalPath, "content": evalfixture.ApprovalAfter, "overwrite": true,
	}, expected)
	if err != nil || !matched {
		return "", errNativeEvidence
	}
	return digest(key), nil
}

func refreshApprovalDeniedScope(
	ctx context.Context, target admittedTarget, trial *Trial, facts map[string]string, filter *strings.Replacer,
) {
	proof := trial.ApprovalDenied
	if proof == nil || proof.Code != "awaiting_stop" || trial.ExecutionVerdict != Pass {
		return
	}
	proof.Code = "post_stop_fixture_unavailable"
	proof.After = observeApprovalFixture(ctx, target.preset.ExpectedWorkspace)
	before, valid := frozenQuestion(proof.Waiting)
	if !valid || proof.After.Code != "observed" {
		return
	}
	observer := &questionConversation{
		home:      target.preset.IsolationRoot,
		workspace: target.preset.ExpectedWorkspace, facts: facts, filter: filter,
	}
	proof.Code = "post_stop_record_unavailable"
	current, err := observer.record(before.ID)
	if err != nil {
		return
	}
	raw, err := json.Marshal(current)
	if err != nil || len(raw) > MaxManifestBytes {
		return
	}
	proof.Resolved = string(raw)
	proof.Code = "post_stop_history_unavailable"
	history, err := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
	if err != nil {
		return
	}
	proof.Metadata, proof.History = history.metadata, history.history
	proof.Code = "post_stop_log_unavailable"
	records, err := readOwnedToolLog(ctx, target, facts, filter)
	if err != nil {
		return
	}
	proof.Code = "post_stop_delivery_unavailable"
	rows, names, err := readSteeringOutbox(ctx, target)
	if err != nil {
		return
	}
	proof.Resolved = string(raw)
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Log, proof.Outbox, proof.OutboxNames = stopLogLines(records), rows, names
	proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
	proof.Code, proof.ObservedAt = "observed", time.Now().UnixMilli()
}
