package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type FinalDeliveryEvidence struct {
	Version        int                        `json:"version"`
	Code           string                     `json:"code"`
	Settled        []SteeringOutboxEvidence   `json:"settled"`
	Metadata       string                     `json:"metadata"`
	MetadataSHA256 string                     `json:"metadata_sha256"`
	History        []string                   `json:"history"`
	HistorySHA256  string                     `json:"history_sha256"`
	Outbox         []SteeringOutboxEvidence   `json:"outbox"`
	OutboxNames    []string                   `json:"outbox_names"`
	Wire           *FinalDeliveryWireEvidence `json:"wire,omitempty"`
}

type finalDeliveryConversation struct {
	item      Case
	target    admittedTarget
	sessionID string
	finals    []Event
	settled   []SteeringOutboxEvidence
	wire      *finalDeliveryWire
}

func (domain *finalDeliveryConversation) initialTask() string { return domain.item.Task }

func (domain *finalDeliveryConversation) request(
	_ context.Context,
	action Action,
	_ Event,
) (protocol.MintClawMessage, bool, error) {
	if domain.wire == nil || action.Kind != "message" {
		return protocol.MintClawMessage{}, true, errNativeEvidence
	}
	request := protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{protocol.PayloadKeyContent: action.Text},
	}
	return request, true, domain.wire.register(request)
}

func (domain *finalDeliveryConversation) freezeWire(trial *Trial, complete bool) {
	if trial.FinalDelivery != nil && domain.wire != nil {
		trial.FinalDelivery.Wire = domain.wire.snapshot(complete && trial.TransportClosed)
	}
}

func (domain *finalDeliveryConversation) observe(ctx context.Context, event Event) error {
	if event.Kind != "final" || len(domain.finals) >= 2 {
		return errNativeEvidence
	}
	domain.finals = append(domain.finals, event)
	trial := Trial{
		SessionID: domain.sessionID, SessionKey: event.SessionKey,
		AgentID: domain.target.preset.ExpectedAgent, Events: domain.finals,
	}
	row, err := waitFinalDeliverySettlement(ctx, domain.target, trial, event, len(domain.finals))
	if err == nil {
		domain.settled = append(domain.settled, row)
	}
	return err
}

func waitFinalDeliverySettlement(
	ctx context.Context, target admittedTarget, trial Trial, event Event, expected int,
) (SteeringOutboxEvidence, error) {
	// A wire final precedes the durable receipt. Observe native settlement,
	// rather than sleeping or treating the broadcast as its own delivery proof.
	settlement, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, names, err := readSteeringOutbox(settlement, target)
		if errors.Is(err, ErrPrivateEvidence) {
			return SteeringOutboxEvidence{}, err
		}
		if err == nil && validFinalDeliveryCensus(
			rows, names, trial, target.preset.ExpectedWorkspace, expected,
		) {
			for _, row := range rows {
				if finalDeliveryReceipt(row, event, trial, target.preset.ExpectedWorkspace) {
					return row, nil
				}
			}
		}
		select {
		case <-settlement.Done():
			return SteeringOutboxEvidence{}, errNativeEvidence
		case <-ticker.C:
		}
	}
}

func (domain *finalDeliveryConversation) conclude(_ context.Context, trial *Trial) {
	code := "not_completed"
	if trial.ExecutionVerdict == Pass && len(domain.settled) == 2 {
		code = "awaiting_stop"
	}
	settled := append([]SteeringOutboxEvidence{}, domain.settled...)
	trial.FinalDelivery = &FinalDeliveryEvidence{
		Version: Version, Code: code, Settled: settled, History: []string{},
		Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
	}
}

func refreshFinalDeliveryScope(
	ctx context.Context, target admittedTarget, trial *Trial, facts map[string]string, filter *strings.Replacer,
) {
	proof := trial.FinalDelivery
	proof.Code = "post_stop_evidence_unavailable"
	history, err := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
	if err != nil {
		return
	}
	rows, names, err := readSteeringOutbox(ctx, target)
	if err != nil {
		return
	}
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Outbox, proof.OutboxNames, proof.Code = rows, names, "observed"
}
